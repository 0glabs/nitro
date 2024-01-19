// Copyright 2022-2024, Offchain Labs, Inc.
// For license information, see https://github.com/OffchainLabs/nitro/blob/master/LICENSE

use crate::{
    program::Program,
};
use arbutil::{
    evm::{user::UserOutcomeKind, EvmData},
    format::DebugBytes,
    heapify, wavm, Bytes32,
};
use prover::{
    machine::Module,
    programs::config::{PricingParams, StylusConfig},
};

type Uptr = usize;

// these hostio methods allow the replay machine to modify itself
#[link(wasm_import_module = "hostio")]
extern "C" {
    fn wavm_link_module(hash: *const MemoryLeaf) -> u32;
    fn wavm_unlink_module();
}

// these dynamic hostio methods allow introspection into user modules
#[link(wasm_import_module = "hostio")]
extern "C" {
    fn program_set_ink(module: u32, ink: u64);
    fn program_set_stack(module: u32, stack: u32);
    fn program_ink_left(module: u32) -> u64;
    fn program_ink_status(module: u32) -> u32;
    fn program_stack_left(module: u32) -> u32;
}

#[repr(C, align(256))]
struct MemoryLeaf([u8; 32]);

// Instruments and "activates" a user wasm, producing a unique module hash.
//
// Note that this operation costs gas and is limited by the amount supplied via the `gas` pointer.
// The amount left is written back at the end of the call.
//
// pages_ptr: starts pointing to max allowed pages, returns number of pages used
#[no_mangle]
pub unsafe extern "C" fn programs__activate(
    wasm_ptr: Uptr,
    wasm_size: usize,
    pages_ptr: Uptr,
    asm_estimate_ptr: Uptr,
    init_gas_ptr: Uptr,
    version: u16,
    debug: u32,
    module_hash_ptr: Uptr,
    gas_ptr: Uptr,
    err_buf: Uptr,
    err_buf_len: usize,
) -> usize {
    let wasm = wavm::read_slice_usize(wasm_ptr, wasm_size);
    let debug = debug != 0;

    let page_limit = wavm::caller_load16(pages_ptr);
    let gas_left = &mut wavm::caller_load64(gas_ptr);
    match Module::activate(&wasm, version, page_limit, debug, gas_left) {
        Ok((module, data)) => {
            wavm::caller_store64(gas_ptr, *gas_left);
            wavm::caller_store16(pages_ptr, data.footprint);
            wavm::caller_store32(asm_estimate_ptr, data.asm_estimate);
            wavm::caller_store32(init_gas_ptr, data.init_gas);
            wavm::write_bytes32_usize(module_hash_ptr, module.hash());
            0
        },
        Err(error) => {
            let mut err_bytes = error.wrap_err("failed to activate").debug_bytes();
            err_bytes.truncate(err_buf_len);
            wavm::write_slice_usize(&err_bytes, err_buf);
            wavm::caller_store64(gas_ptr, 0);
            wavm::caller_store16(pages_ptr, 0);
            wavm::caller_store32(asm_estimate_ptr, 0);
            wavm::caller_store32(init_gas_ptr, 0);
            wavm::write_bytes32_usize(module_hash_ptr, Bytes32::default());
            err_bytes.len()
        },
    }
}


/// Links and creates user program
/// consumes both evm_data_handler and config_handler
/// returns module number
/// see program-exec for starting the user program
#[no_mangle]
pub unsafe extern "C" fn programs__new_program(
    compiled_hash_ptr: Uptr,
    calldata_ptr: Uptr,
    calldata_size: usize,
    config_box: u64,
    evm_data_box: u64,
    gas: u64,
) -> u32 {
    let compiled_hash = wavm::read_bytes32_usize(compiled_hash_ptr);
    let calldata = wavm::read_slice_usize(calldata_ptr, calldata_size);
    let config: StylusConfig = *Box::from_raw(config_box as *mut StylusConfig);
    let evm_data: EvmData = *Box::from_raw(evm_data_box as *mut EvmData);

    // buy ink
    let pricing = config.pricing;
    let ink = pricing.gas_to_ink(gas);

    // link the program and ready its instrumentation
    let module = wavm_link_module(&MemoryLeaf(*compiled_hash));
    program_set_ink(module, ink);
    program_set_stack(module, config.max_depth);

    // provide arguments
    Program::push_new(calldata, evm_data, module, config);

    module
}

// gets information about request according to id
// request_id MUST be last request id returned from start_program or send_response
#[no_mangle]
pub unsafe extern "C" fn programs__get_request(id: u32, len_ptr: usize) -> u32 {
    let (req_type, data) = Program::current().evm_api.request_handler().get_request(id);
    if len_ptr != 0 {
        wavm::caller_store32(len_ptr, data.len() as u32);
    }
    req_type
}

// gets data associated with last request.
// request_id MUST be last request receieved
// data_ptr MUST point to a buffer of at least the length returned by get_request
#[no_mangle]
pub unsafe extern "C" fn programs__get_request_data(id: u32, data_ptr: usize) {
    let (_, data) = Program::current().evm_api.request_handler().get_request(id);
    wavm::write_slice_usize(&data, data_ptr);
}

// sets response for the next request made
// id MUST be the id of last request made
// see program-exec send_response for sending this response to the program
#[no_mangle]
pub unsafe extern "C" fn programs__set_response(
    id: u32,
    gas: u64,
    reponse_ptr: Uptr,
    response_len: usize,
) {
    let program = Program::current();
    program.evm_api.request_handler().set_response(id, wavm::read_slice_usize(reponse_ptr, response_len), gas);
}

// removes the last created program
#[no_mangle]
pub unsafe extern "C" fn programs__pop() {
    Program::pop();
    wavm_unlink_module();
}

// used by program-exec
// returns arguments_len
// module MUST be the last one returned from new_program
#[no_mangle]
pub unsafe extern "C" fn program_internal__args_len(module: u32) -> usize {
    let program = Program::current();
    if program.module != module {
        panic!("args_len requested for wrong module");
    }
    program.args_len()
}

// used by program-exec
// sets status of the last program and sends a program_done request
#[no_mangle]
pub unsafe extern "C" fn program_internal__set_done(mut status: u8) -> u32 {
    let program = Program::current();
    let module = program.module;
    let mut outs = &program.outs;

    let mut ink_left = program_ink_left(module);

    let empty_vec = vec![];
    // check if instrumentation stopped the program
    use UserOutcomeKind::*;
    if program_ink_status(module) != 0 {
        status = OutOfInk.into();
        outs = &empty_vec;
        ink_left = 0;
    }
    if program_stack_left(module) == 0 {
        status = OutOfStack.into();
        outs = &empty_vec;
        ink_left = 0;
    }
    let gas_left = program.config.pricing.ink_to_gas(ink_left);
    let mut output = gas_left.to_be_bytes().to_vec();
    output.extend(outs.iter());
    program.evm_api.request_handler().set_request(status as u32, &output)
}

/// Creates a `StylusConfig` from its component parts.
#[no_mangle]
pub unsafe extern "C" fn programs__create_stylus_config(
    version: u16,
    max_depth: u32,
    ink_price: u32,
    _debug: u32,
) -> u64 {
    let config = StylusConfig {
        version,
        max_depth,
        pricing: PricingParams {
            ink_price,
        },
    };
    heapify(config) as u64
}

/// Creates an `EvmData` handler from its component parts.
///
#[no_mangle]
pub unsafe extern "C" fn programs__create_evm_data(
    block_basefee_ptr: Uptr,
    chainid: u64,
    block_coinbase_ptr: Uptr,
    block_gas_limit: u64,
    block_number: u64,
    block_timestamp: u64,
    contract_address_ptr: Uptr,
    msg_sender_ptr: Uptr,
    msg_value_ptr: Uptr,
    tx_gas_price_ptr: Uptr,
    tx_origin_ptr: Uptr,
    reentrant: u32,
) -> u64 {
    let evm_data = EvmData {
        block_basefee: wavm::read_bytes32_usize(block_basefee_ptr),
        chainid,
        block_coinbase: wavm::read_bytes20_usize(block_coinbase_ptr),
        block_gas_limit,
        block_number,
        block_timestamp,
        contract_address: wavm::read_bytes20_usize(contract_address_ptr),
        msg_sender: wavm::read_bytes20_usize(msg_sender_ptr),
        msg_value: wavm::read_bytes32_usize(msg_value_ptr),
        tx_gas_price:  wavm::read_bytes32_usize(tx_gas_price_ptr),
        tx_origin: wavm::read_bytes20_usize(tx_origin_ptr),
        reentrant,
        return_data_len: 0,
        tracing: false,
    };
    heapify(evm_data) as u64
}
