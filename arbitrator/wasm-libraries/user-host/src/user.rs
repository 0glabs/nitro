// Copyright 2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

use crate::Program;
use arbutil::{
    evm::{self, api::EvmApi},
    wavm,
};
use prover::programs::meter::{GasMeteredMachine, MeteredMachine};

#[no_mangle]
pub unsafe extern "C" fn user_host__read_args(ptr: usize) {
    let program = Program::start();
    program.pay_for_evm_copy(program.args.len() as u64).unwrap();
    wavm::write_slice_usize(&program.args, ptr);
}

#[no_mangle]
pub unsafe extern "C" fn user_host__return_data(ptr: usize, len: usize) {
    let program = Program::start();
    program.pay_for_evm_copy(len as u64).unwrap();
    program.outs = wavm::read_slice_usize(ptr, len);
}

#[no_mangle]
pub unsafe extern "C" fn user_host__account_load_bytes32(key: usize, ptr: usize) {
    let program = Program::start();
    let key = wavm::read_bytes32(key);

    let (value, gas_cost) = program.evm_api.get_bytes32(key.into());
    program.buy_gas(gas_cost).unwrap();
    wavm::write_slice_usize(&value.0, ptr);
}

#[no_mangle]
pub unsafe extern "C" fn user_host__account_store_bytes32(key: usize, value: usize) {
    let program = Program::start();
    program.require_gas(evm::SSTORE_SENTRY_GAS).unwrap();

    let api = &mut program.evm_api;
    let key = wavm::read_bytes32(key);
    let value = wavm::read_bytes32(value);

    let gas_cost = api.set_bytes32(key.into(), value.into()).unwrap();
    program.buy_gas(gas_cost).unwrap();
}

#[no_mangle]
pub unsafe extern "C" fn user_host__call_contract(
    contract: usize,
    calldata: usize,
    calldata_len: usize,
    value: usize,
    mut ink: u64,
    return_data_len: usize,
) -> u8 {
    let program = Program::start();
    program.pay_for_evm_copy(calldata_len as u64).unwrap();
    ink = ink.min(program.ink_ready().unwrap());

    let gas = program.pricing().ink_to_gas(ink);
    let contract = wavm::read_bytes20(contract).into();
    let input = wavm::read_slice_usize(calldata, calldata_len);
    let value = wavm::read_bytes32(value).into();
    let api = &mut program.evm_api;

    let (outs_len, gas_cost, status) = api.contract_call(contract, input, gas, value);
    program.evm_data.return_data_len = outs_len;
    wavm::caller_store32(return_data_len, outs_len);
    program.buy_gas(gas_cost).unwrap();
    status as u8
}

#[no_mangle]
pub unsafe extern "C" fn user_host__delegate_call_contract(
    contract: usize,
    calldata: usize,
    calldata_len: usize,
    mut ink: u64,
    return_data_len: usize,
) -> u8 {
    let program = Program::start();
    program.pay_for_evm_copy(calldata_len as u64).unwrap();
    ink = ink.min(program.ink_ready().unwrap());

    let gas = program.pricing().ink_to_gas(ink);
    let contract = wavm::read_bytes20(contract).into();
    let input = wavm::read_slice_usize(calldata, calldata_len);
    let api = &mut program.evm_api;

    let (outs_len, gas_cost, status) = api.delegate_call(contract, input, gas);
    program.evm_data.return_data_len = outs_len;
    wavm::caller_store32(return_data_len, outs_len);
    program.buy_gas(gas_cost).unwrap();
    status as u8
}

#[no_mangle]
pub unsafe extern "C" fn user_host__static_call_contract(
    contract: usize,
    calldata: usize,
    calldata_len: usize,
    mut ink: u64,
    return_data_len: usize,
) -> u8 {
    let program = Program::start();
    program.pay_for_evm_copy(calldata_len as u64).unwrap();
    ink = ink.min(program.ink_ready().unwrap());

    let gas = program.pricing().ink_to_gas(ink);
    let contract = wavm::read_bytes20(contract).into();
    let input = wavm::read_slice_usize(calldata, calldata_len);
    let api = &mut program.evm_api;

    let (outs_len, gas_cost, status) = api.static_call(contract, input, gas);
    program.evm_data.return_data_len = outs_len;
    wavm::caller_store32(return_data_len, outs_len);
    program.buy_gas(gas_cost).unwrap();
    status as u8
}

#[no_mangle]
pub unsafe extern "C" fn user_host__create1(
    code: usize,
    code_len: usize,
    endowment: usize,
    contract: usize,
    revert_data_len: usize,
) {
    let program = Program::start();
    program.pay_for_evm_copy(code_len as u64).unwrap();

    let code = wavm::read_slice_usize(code, code_len);
    let endowment = wavm::read_bytes32(endowment).into();
    let gas = program.gas_left().unwrap();
    let api = &mut program.evm_api;

    let (result, ret_len, gas_cost) = api.create1(code, endowment, gas);
    program.evm_data.return_data_len = ret_len;
    wavm::caller_store32(revert_data_len, ret_len);
    program.buy_gas(gas_cost).unwrap();
    wavm::write_slice_usize(&result.unwrap().0, contract);
}

#[no_mangle]
pub unsafe extern "C" fn user_host__create2(
    code: usize,
    code_len: usize,
    endowment: usize,
    salt: usize,
    contract: usize,
    revert_data_len: usize,
) {
    let program = Program::start();
    program.pay_for_evm_copy(code_len as u64).unwrap();

    let code = wavm::read_slice_usize(code, code_len);
    let endowment = wavm::read_bytes32(endowment).into();
    let salt = wavm::read_bytes32(salt).into();
    let gas = program.gas_left().unwrap();
    let api = &mut program.evm_api;

    let (result, ret_len, gas_cost) = api.create2(code, endowment, salt, gas);
    program.evm_data.return_data_len = ret_len;
    wavm::caller_store32(revert_data_len, ret_len);
    program.buy_gas(gas_cost).unwrap();
    wavm::write_slice_usize(&result.unwrap().0, contract);
}

#[no_mangle]
pub unsafe extern "C" fn user_host__read_return_data(ptr: usize) {
    let program = Program::start();
    let len = program.evm_data.return_data_len;
    program.pay_for_evm_copy(len.into()).unwrap();

    let data = program.evm_api.get_return_data();
    assert_eq!(data.len(), len as usize);
    wavm::write_slice_usize(&data, ptr);
}

#[no_mangle]
pub unsafe extern "C" fn user_host__return_data_size() -> u32 {
    let program = Program::start();
    program.evm_data.return_data_len
}

#[no_mangle]
pub unsafe extern "C" fn user_host__emit_log(data: usize, len: u32, topics: u32) {
    let program = Program::start();
    if topics > 4 || len < topics * 32 {
        panic!("bad topic data");
    }
    program.pay_for_evm_log(topics, len - topics * 32).unwrap();

    let data = wavm::read_slice_usize(data, len as usize);
    program.evm_api.emit_log(data, topics).unwrap();
}

#[no_mangle]
pub unsafe extern "C" fn user_host__address_balance(address: usize, ptr: usize) {
    let program = Program::start();
    let address = wavm::read_bytes20(address);

    let (value, gas_cost) = program.evm_api.address_balance(address.into());
    program.buy_gas(gas_cost).unwrap();
    wavm::write_slice_usize(&value.0, ptr);
}

#[no_mangle]
pub unsafe extern "C" fn user_host__address_codehash(address: usize, ptr: usize) {
    let program = Program::start();
    let address = wavm::read_bytes20(address);

    let (value, gas_cost) = program.evm_api.address_codehash(address.into());
    program.buy_gas(gas_cost).unwrap();
    wavm::write_slice_usize(&value.0, ptr);
}

#[no_mangle]
pub unsafe extern "C" fn user_host__evm_blockhash(block: usize, ptr: usize) {
    let program = Program::start();
    let block = wavm::read_bytes32(block);

    let (value, gas_cost) = program.evm_api.evm_blockhash(block.into());
    program.buy_gas(gas_cost).unwrap();
    wavm::write_slice_usize(&value.0, ptr);
}

#[no_mangle]
pub unsafe extern "C" fn user_host__evm_gas_left() -> u64 {
    let program = Program::start();
    program.buy_gas(evm::GASLEFT_GAS).unwrap();
    program.gas_left().unwrap()
}

#[no_mangle]
pub unsafe extern "C" fn user_host__evm_ink_left() -> u64 {
    let program = Program::start();
    program.buy_gas(evm::GASLEFT_GAS).unwrap();
    program.ink_ready().unwrap()
}

#[no_mangle]
pub unsafe extern "C" fn user_host__block_basefee(ptr: usize) {
    let program = Program::start();
    program.buy_gas(evm::BASEFEE_GAS).unwrap();
    let block_basefee = program.evm_data.block_basefee;
    wavm::write_slice_usize(&block_basefee.0, ptr)
}

#[no_mangle]
pub unsafe extern "C" fn user_host__block_chainid(ptr: usize) {
    let program = Program::start();
    program.buy_gas(evm::CHAINID_GAS).unwrap();
    let block_chainid = program.evm_data.block_chainid;
    wavm::write_slice_usize(&block_chainid.0, ptr)
}

#[no_mangle]
pub unsafe extern "C" fn user_host__block_coinbase(ptr: usize) {
    let program = Program::start();
    program.buy_gas(evm::COINBASE_GAS).unwrap();
    let block_coinbase = program.evm_data.block_coinbase;
    wavm::write_slice_usize(&block_coinbase.0, ptr)
}

#[no_mangle]
pub unsafe extern "C" fn user_host__block_difficulty(ptr: usize) {
    let program = Program::start();
    program.buy_gas(evm::DIFFICULTY_GAS).unwrap();
    let difficulty = program.evm_data.block_difficulty;
    wavm::write_slice_usize(&difficulty.0, ptr)
}

#[no_mangle]
pub unsafe extern "C" fn user_host__block_gas_limit() -> u64 {
    let program = Program::start();
    program.buy_gas(evm::GASLIMIT_GAS).unwrap();
    program.evm_data.block_gas_limit
}

#[no_mangle]
pub unsafe extern "C" fn user_host__block_number(ptr: usize) {
    let program = Program::start();
    program.buy_gas(evm::NUMBER_GAS).unwrap();
    let block_number = program.evm_data.block_number;
    wavm::write_slice_usize(&block_number.0, ptr)
}

#[no_mangle]
pub unsafe extern "C" fn user_host__block_timestamp(ptr: usize) {
    let program = Program::start();
    program.buy_gas(evm::TIMESTAMP_GAS).unwrap();
    let block_timestamp = program.evm_data.block_timestamp;
    wavm::write_slice_usize(&block_timestamp.0, ptr)
}

#[no_mangle]
pub unsafe extern "C" fn user_host__contract_address(ptr: usize) {
    let program = Program::start();
    program.buy_gas(evm::ADDRESS_GAS).unwrap();
    let contract_address = program.evm_data.contract_address;
    wavm::write_slice_usize(&contract_address.0, ptr)
}

#[no_mangle]
pub unsafe extern "C" fn user_host__msg_sender(ptr: usize) {
    let program = Program::start();
    program.buy_gas(evm::CALLER_GAS).unwrap();
    let msg_sender = program.evm_data.msg_sender;
    wavm::write_slice_usize(&msg_sender.0, ptr)
}

#[no_mangle]
pub unsafe extern "C" fn user_host__msg_value(ptr: usize) {
    let program = Program::start();
    program.buy_gas(evm::CALLVALUE_GAS).unwrap();
    let msg_value = program.evm_data.msg_value;
    wavm::write_slice_usize(&msg_value.0, ptr)
}

#[no_mangle]
pub unsafe extern "C" fn user_host__tx_gas_price(ptr: usize) {
    let program = Program::start();
    program.buy_gas(evm::GASPRICE_GAS).unwrap();
    let tx_gas_price = program.evm_data.tx_gas_price;
    wavm::write_slice_usize(&tx_gas_price.0, ptr)
}

#[no_mangle]
pub unsafe extern "C" fn user_host__tx_ink_price() -> u64 {
    let program = Program::start();
    program.buy_gas(evm::GASPRICE_GAS).unwrap();
    program.pricing().ink_price
}

#[no_mangle]
pub unsafe extern "C" fn user_host__tx_origin(ptr: usize) {
    let program = Program::start();
    program.buy_gas(evm::ORIGIN_GAS).unwrap();
    let tx_origin = program.evm_data.tx_origin;
    wavm::write_slice_usize(&tx_origin.0, ptr)
}
