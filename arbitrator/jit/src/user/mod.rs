// Copyright 2022-2023, Offchain Labs, Inc.
// For license information, see https://github.com/OffchainLabs/nitro/blob/master/LICENSE

use crate::{
    gostack::GoStack,
    machine::{Escape, MaybeEscape, WasmEnvMut},
    user::evm_api::exec_wasm,
};
use arbutil::{
    evm::{
        user::{UserOutcome, UserOutcomeKind},
        EvmData,
    },
    heapify,
};
use eyre::eyre;
use prover::{
    binary::WasmBinary,
    programs::{
        config::{MemoryModel, PricingParams},
        prelude::*,
    },
};
use std::mem;
use stylus::native;

mod evm_api;

/// Compiles and instruments user wasm.
/// go side: λ(wasm []byte, version, debug u32, pageLimit u16) (machine *Machine, footprint u32, err *Vec<u8>)
pub fn compile_user_wasm(env: WasmEnvMut, sp: u32) {
    let mut sp = GoStack::simple(sp, &env);
    let wasm = sp.read_go_slice_owned();
    let compile = CompileConfig::version(sp.read_u32(), sp.read_u32() != 0);
    let page_limit = sp.read_u16();
    sp.skip_space();

    macro_rules! error {
        ($text:expr, $error:expr) => {
            error!($error.wrap_err($text))
        };
        ($error:expr) => {{
            let error = format!("{:?}", $error).as_bytes().to_vec();
            sp.write_nullptr();
            sp.skip_space();
            sp.write_ptr(heapify(error));
            return;
        }};
    }

    // ensure the wasm compiles during proving
    let footprint = match WasmBinary::parse_user(&wasm, page_limit, &compile) {
        Ok((.., pages)) => pages,
        Err(error) => error!(error),
    };
    let module = match native::module(&wasm, compile) {
        Ok(module) => module,
        Err(error) => error!("failed to compile", error),
    };
    sp.write_ptr(heapify(module));
    sp.write_u16(footprint).skip_space();
    sp.write_nullptr();
}

/// Links and executes a user wasm.
/// λ(mach *Machine, calldata []byte, params *Configs, evmApi []byte, evmData: *EvmData, gas *u64, root *[32]byte)
///     -> (status byte, out *Vec<u8>)
pub fn call_user_wasm(env: WasmEnvMut, sp: u32) -> MaybeEscape {
    let sp = &mut GoStack::simple(sp, &env);
    macro_rules! unbox {
        () => {
            unsafe { *Box::from_raw(sp.read_ptr_mut()) }
        };
    }
    use UserOutcomeKind::*;

    // move inputs
    let module: Vec<u8> = unbox!();
    let calldata = sp.read_go_slice_owned();
    let (compile, config): (CompileConfig, StylusConfig) = unbox!();
    let evm_api = sp.read_go_slice_owned();
    let evm_data: EvmData = unbox!();

    // buy ink
    let pricing = config.pricing;
    let gas = sp.read_go_ptr();
    let ink = pricing.gas_to_ink(sp.read_u64_raw(gas));

    // skip the root since we don't use these
    sp.skip_u64();

    let result = exec_wasm(
        sp, env, module, calldata, compile, config, evm_api, evm_data, ink,
    );
    let (outcome, ink_left) = result.map_err(Escape::Child)?;

    match outcome {
        Err(err) | Ok(UserOutcome::Failure(err)) => {
            let outs = format!("{:?}", err.wrap_err(eyre!("failed to execute program")));
            sp.write_u8(Failure.into()).skip_space();
            sp.write_ptr(heapify(outs.into_bytes()));
        }
        Ok(outcome) => {
            let (status, outs) = outcome.into_data();
            sp.write_u8(status.into()).skip_space();
            sp.write_ptr(heapify(outs));
        }
    }
    sp.write_u64_raw(gas, pricing.ink_to_gas(ink_left));
    Ok(())
}

/// Reads the length of a rust `Vec`
/// go side: λ(vec *Vec<u8>) (len u32)
pub fn read_rust_vec_len(env: WasmEnvMut, sp: u32) {
    let mut sp = GoStack::simple(sp, &env);
    let vec: &Vec<u8> = unsafe { &*sp.read_ptr() };
    sp.write_u32(vec.len() as u32);
}

/// Copies the contents of a rust `Vec` into a go slice, dropping it in the process
/// go side: λ(vec *Vec<u8>, dest []byte)
pub fn rust_vec_into_slice(env: WasmEnvMut, sp: u32) {
    let mut sp = GoStack::simple(sp, &env);
    let vec: Vec<u8> = unsafe { *Box::from_raw(sp.read_ptr_mut()) };
    let ptr: *mut u8 = sp.read_ptr_mut();
    sp.write_slice(ptr as u64, &vec);
    mem::drop(vec)
}

/// Creates a `StylusConfig` from its component parts.
/// go side: λ(version, maxDepth u32, inkPrice, hostioInk u64, debugMode: u32) *(CompileConfig, StylusConfig)
pub fn rust_config_impl(env: WasmEnvMut, sp: u32) {
    let mut sp = GoStack::simple(sp, &env);

    let config = StylusConfig {
        version: sp.read_u32(),
        max_depth: sp.read_u32(),
        pricing: PricingParams {
            ink_price: sp.read_u64(),
            hostio_ink: sp.read_u64(),
            memory_model: MemoryModel::default(),
        },
    };
    let compile = CompileConfig::version(config.version, sp.read_u32() != 0);
    sp.skip_space().write_ptr(heapify((compile, config)));
}

/// Creates an `EvmData` from its component parts.
/// go side: λ(
///     block_basefee, block_chainid *[32]byte, block_coinbase *[20]byte, block_difficulty *[32]byte,
///     block_gas_limit u64, block_number *[32]byte, block_timestamp u64, contract_address, msg_sender *[20]byte,
///     msg_value, tx_gas_price *[32]byte, tx_origin *[20]byte, footprint u16
///) *EvmData
pub fn evm_data_impl(env: WasmEnvMut, sp: u32) {
    let mut sp = GoStack::simple(sp, &env);
    let evm_data = EvmData {
        block_basefee: sp.read_bytes32().into(),
        block_chainid: sp.read_bytes32().into(),
        block_coinbase: sp.read_bytes20().into(),
        block_difficulty: sp.read_bytes32().into(),
        block_gas_limit: sp.read_u64(),
        block_number: sp.read_bytes32().into(),
        block_timestamp: sp.read_u64(),
        contract_address: sp.read_bytes20().into(),
        msg_sender: sp.read_bytes20().into(),
        msg_value: sp.read_bytes32().into(),
        tx_gas_price: sp.read_bytes32().into(),
        tx_origin: sp.read_bytes20().into(),
        footprint: sp.read_u16(),
        return_data_len: 0,
    };
    sp.skip_space();
    sp.write_ptr(heapify(evm_data));
}
