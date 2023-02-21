// Copyright 2022-2023, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

use crate::env::{MaybeEscape, WasmEnv, WasmEnvMut};
use prover::programs::prelude::*;

pub(crate) fn read_args(mut env: WasmEnvMut, ptr: u32) -> MaybeEscape {
    WasmEnv::begin(&mut env)?;

    let (env, memory) = WasmEnv::data(&mut env);
    memory.write_slice(ptr, &env.args)?;
    Ok(())
}

pub(crate) fn return_data(mut env: WasmEnvMut, ptr: u32, len: u32) -> MaybeEscape {
    let mut meter = WasmEnv::begin(&mut env)?;

    let evm_words = |count: u64| count.saturating_mul(31) / 32;
    let evm_gas = evm_words(len.into()).saturating_mul(3); // 3 evm gas per word
    meter.buy_evm_gas(evm_gas)?;

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
) -> MaybeEscape {
    let mut meter = WasmEnv::begin(&mut env)?;
    let gas = meter.gas_left().into();

    let (data, memory) = WasmEnv::data(&mut env);
    let contract = memory.read_bytes20(contract)?;
    let input = memory.read_slice(calldata, calldata_len)?;
    let value = memory.read_bytes32(value)?;

    let (output, cost, status) = data.evm()?.call_contract(contract, input, gas, value);

    let mut meter = WasmEnv::meter(&mut env);
    meter.buy_gas(cost)?;

    Ok(())
}
