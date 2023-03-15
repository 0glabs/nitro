// Copyright 2022-2023, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

use crate::env::{Escape, MaybeEscape, WasmEnv, WasmEnvMut};
use arbutil::Color;
use prover::programs::prelude::*;

pub(crate) fn read_args(mut env: WasmEnvMut, ptr: u32) -> MaybeEscape {
    WasmEnv::begin(&mut env)?;

    let (env, memory) = WasmEnv::data(&mut env);
    memory.write_slice(ptr, &env.args)?;
    Ok(())
}

pub(crate) fn return_data(mut env: WasmEnvMut, ptr: u32, len: u32) -> MaybeEscape {
    let mut meter = WasmEnv::begin(&mut env)?;
    meter.pay_for_evm_copy(len as usize)?;

    let (env, memory) = WasmEnv::data(&mut env);
    env.outs = memory.read_slice(ptr, len)?;
    Ok(())
}

pub(crate) fn account_load_bytes32(mut env: WasmEnvMut, key: u32, dest: u32) -> MaybeEscape {
    WasmEnv::begin(&mut env)?;

    let (data, memory) = WasmEnv::data(&mut env);
    let key = memory.read_bytes32(key)?;
    let (value, cost) = data.evm()?.load_bytes32(key);
    memory.write_slice(dest, &value.0)?;

    let mut meter = WasmEnv::meter(&mut env);
    meter.buy_evm_gas(cost)
}

pub(crate) fn account_store_bytes32(mut env: WasmEnvMut, key: u32, value: u32) -> MaybeEscape {
    let mut meter = WasmEnv::begin(&mut env)?;
    meter.require_evm_gas(2300)?; // params.SstoreSentryGasEIP2200 (see operations_acl_arbitrum.go)

    let (data, memory) = WasmEnv::data(&mut env);
    let key = memory.read_bytes32(key)?;
    let value = memory.read_bytes32(value)?;
    let cost = data.evm()?.store_bytes32(key, value)?;

    let mut meter = WasmEnv::meter(&mut env);
    meter.buy_evm_gas(cost)
}

pub(crate) fn call_contract(
    mut env: WasmEnvMut,
    contract: u32,
    calldata: u32,
    calldata_len: u32,
    value: u32,
    return_data_len: u32,
) -> Result<u8, Escape> {
    let mut env = WasmEnv::start(&mut env)?;
    let gas: u64 = env.gas_left().into();

    let contract = env.read_bytes20(contract)?;
    let input = env.read_slice(calldata, calldata_len)?;
    let value = env.read_bytes32(value)?;

    let (outs, cost, status) = env.evm()?.call_contract(contract, input, gas, value);
    env.write_u32(return_data_len, outs.len() as u32);
    env.evm()?.return_data = Some(outs);

    env.buy_gas(cost)?;
    Ok(status as u8)
}

pub(crate) fn read_return_data(mut env: WasmEnvMut, dest: u32) -> MaybeEscape {
    let mut env = WasmEnv::start(&mut env)?;
    let data = env.return_data()?;
    env.pay_for_evm_copy(data.len())?;
    env.write_slice(dest, &env.return_data()?)?;
    Ok(())
}

pub(crate) fn debug_println(mut env: WasmEnvMut, ptr: u32, len: u32) -> MaybeEscape {
    let memory = WasmEnv::memory(&mut env);
    let text = memory.read_slice(ptr, len)?;
    println!(
        "{} {}",
        "Stylus says:".yellow(),
        String::from_utf8_lossy(&text)
    );
    Ok(())
}
