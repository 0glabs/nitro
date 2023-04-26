// Copyright 2023, Offchain Labs, Inc.
// For license information, see https://github.com/OffchainLabs/nitro/blob/master/LICENSE

#![allow(clippy::too_many_arguments)]

use arbutil::Color;
use eyre::{bail, eyre, Result};
use prover::{
    programs::{config::EvmData, prelude::*, run::UserOutcomeKind},
    utils::{Bytes20, Bytes32},
};
use std::{
    fmt::Debug,
    sync::mpsc::{self, SyncSender},
    thread,
    time::Duration,
};
use stylus::{native::NativeInstance, run::RunProgram, EvmApi, EvmApiMethod, EvmApiStatus};

use crate::{
    gostack::GoStack,
    machine::WasmEnvMut,
    syscall::{DynamicObject, GoValue, JsValue, STYLUS_ID},
};

struct JitApi {
    object_ids: Vec<u32>,
    parent: SyncSender<EvmMsg>,
}

enum EvmMsg {
    Call(u32, Vec<ApiValue>, SyncSender<Vec<ApiValue>>),
    Panic(String),
    Done,
}

#[derive(Clone)]
struct ApiValue(Vec<u8>);

type Bytes = Vec<u8>;

#[derive(Debug)]
enum ApiValueKind {
    U32(u32),
    U64(u64),
    Bytes(Bytes),
    Bytes20(Bytes20),
    Bytes32(Bytes32),
    String(String),
    Nil,
}

impl Debug for ApiValue {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let data = &self.0;
        f.write_fmt(format_args!("{}_", data[0]))?;
        f.write_str(&hex::encode(&data[1..]))
    }
}

impl ApiValueKind {
    fn discriminant(&self) -> u8 {
        match self {
            ApiValueKind::U32(_) => 0,
            ApiValueKind::U64(_) => 1,
            ApiValueKind::Bytes(_) => 2,
            ApiValueKind::Bytes20(_) => 3,
            ApiValueKind::Bytes32(_) => 4,
            ApiValueKind::String(_) => 5,
            ApiValueKind::Nil => 6,
        }
    }
}

impl From<ApiValue> for ApiValueKind {
    fn from(value: ApiValue) -> Self {
        let kind = value.0[0];
        let data = &value.0[1..];
        match kind {
            0 => ApiValueKind::U32(u32::from_be_bytes(data.try_into().unwrap())),
            1 => ApiValueKind::U64(u64::from_be_bytes(data.try_into().unwrap())),
            2 => ApiValueKind::Bytes(data.to_vec()),
            3 => ApiValueKind::Bytes20(data.try_into().unwrap()),
            4 => ApiValueKind::Bytes32(data.try_into().unwrap()),
            5 => ApiValueKind::String(String::from_utf8(data.to_vec()).unwrap()),
            6 => ApiValueKind::Nil,
            _ => unreachable!(),
        }
    }
}

impl From<ApiValueKind> for ApiValue {
    fn from(value: ApiValueKind) -> Self {
        use ApiValueKind::*;
        let mut data = vec![value.discriminant()];
        data.extend(match value {
            U32(x) => x.to_be_bytes().to_vec(),
            U64(x) => x.to_be_bytes().to_vec(),
            Bytes(x) => x,
            Bytes20(x) => x.0.as_ref().to_vec(),
            Bytes32(x) => x.0.as_ref().to_vec(),
            String(x) => x.as_bytes().to_vec(),
            Nil => vec![],
        });
        Self(data)
    }
}

impl From<u32> for ApiValue {
    fn from(value: u32) -> Self {
        ApiValueKind::U32(value).into()
    }
}

impl From<u64> for ApiValue {
    fn from(value: u64) -> Self {
        ApiValueKind::U64(value).into()
    }
}

impl From<Bytes> for ApiValue {
    fn from(value: Bytes) -> Self {
        ApiValueKind::Bytes(value).into()
    }
}

impl From<Bytes20> for ApiValue {
    fn from(value: Bytes20) -> Self {
        ApiValueKind::Bytes20(value).into()
    }
}

impl From<Bytes32> for ApiValue {
    fn from(value: Bytes32) -> Self {
        ApiValueKind::Bytes32(value).into()
    }
}

impl From<String> for ApiValue {
    fn from(value: String) -> Self {
        ApiValueKind::String(value).into()
    }
}

impl ApiValueKind {
    fn assert_u32(self) -> u32 {
        match self {
            ApiValueKind::U32(value) => value,
            x => panic!("wrong type {x:?}"),
        }
    }

    fn assert_u64(self) -> u64 {
        match self {
            ApiValueKind::U64(value) => value,
            x => panic!("wrong type {x:?}"),
        }
    }

    fn assert_bytes(self) -> Bytes {
        match self {
            ApiValueKind::Bytes(value) => value,
            x => panic!("wrong type {x:?}"),
        }
    }

    fn assert_bytes32(self) -> Bytes32 {
        match self {
            ApiValueKind::Bytes32(value) => value,
            x => panic!("wrong type {x:?}"),
        }
    }

    fn assert_status(self) -> UserOutcomeKind {
        match self {
            ApiValueKind::Nil => EvmApiStatus::Success.into(),
            ApiValueKind::String(_) => EvmApiStatus::Failure.into(),
            x => panic!("wrong type {x:?}"),
        }
    }
}

impl JitApi {
    fn new(ids: Vec<u8>, parent: SyncSender<EvmMsg>) -> Self {
        let mut object_ids = vec![];
        for i in 0..(ids.len() / 4) {
            let slice = &ids[(i * 4)..(i * 4 + 4)];
            let value = u32::from_be_bytes(slice.try_into().unwrap());
            object_ids.push(value);
        }
        Self { object_ids, parent }
    }

    fn call(&mut self, func: EvmApiMethod, args: Vec<ApiValue>) -> Vec<ApiValue> {
        let (tx, rx) = mpsc::sync_channel(0);
        let func = self.object_ids[func as usize];
        let msg = EvmMsg::Call(func, args, tx);
        self.parent.send(msg).unwrap();
        rx.recv().unwrap()
    }
}

macro_rules! call {
    ($self:expr, $num:expr, $func:ident $(,$args:expr)*) => {{
        let outs = $self.call(EvmApiMethod::$func, vec![$($args.into()),*]);
        let x: [ApiValue; $num] = outs.try_into().unwrap();
        let x: [ApiValueKind; $num] = x.map(Into::into);
        x
    }};
}

impl EvmApi for JitApi {
    fn get_bytes32(&mut self, key: Bytes32) -> (Bytes32, u64) {
        let [value, cost] = call!(self, 2, GetBytes32, key);
        (value.assert_bytes32(), cost.assert_u64())
    }

    fn set_bytes32(&mut self, key: Bytes32, value: Bytes32) -> Result<u64> {
        let [out] = call!(self, 1, SetBytes32, key, value);
        match out {
            ApiValueKind::U64(value) => Ok(value),
            ApiValueKind::String(err) => bail!(err),
            _ => unreachable!(),
        }
    }

    fn contract_call(
        &mut self,
        contract: Bytes20,
        input: Bytes,
        gas: u64,
        value: Bytes32,
    ) -> (u32, u64, UserOutcomeKind) {
        let [len, cost, status] = call!(self, 3, ContractCall, contract, input, gas, value);
        (len.assert_u32(), cost.assert_u64(), status.assert_status())
    }

    fn delegate_call(
        &mut self,
        contract: Bytes20,
        input: Bytes,
        gas: u64,
    ) -> (u32, u64, UserOutcomeKind) {
        let [len, cost, status] = call!(self, 3, DelegateCall, contract, input, gas);
        (len.assert_u32(), cost.assert_u64(), status.assert_status())
    }

    fn static_call(
        &mut self,
        contract: Bytes20,
        input: Bytes,
        gas: u64,
    ) -> (u32, u64, UserOutcomeKind) {
        let [len, cost, status] = call!(self, 3, StaticCall, contract, input, gas);
        (len.assert_u32(), cost.assert_u64(), status.assert_status())
    }

    fn create1(
        &mut self,
        code: Bytes,
        endowment: Bytes32,
        gas: u64,
    ) -> (Result<Bytes20>, u32, u64) {
        let [result, len, cost] = call!(self, 3, Create1, code, endowment, gas);
        let result = match result {
            ApiValueKind::Bytes20(account) => Ok(account),
            ApiValueKind::String(err) => Err(eyre!(err)),
            _ => unreachable!(),
        };
        (result, len.assert_u32(), cost.assert_u64())
    }

    fn create2(
        &mut self,
        code: Bytes,
        endowment: Bytes32,
        salt: Bytes32,
        gas: u64,
    ) -> (Result<Bytes20>, u32, u64) {
        let [result, len, cost] = call!(self, 3, Create2, code, endowment, salt, gas);
        let result = match result {
            ApiValueKind::Bytes20(account) => Ok(account),
            ApiValueKind::String(err) => Err(eyre!(err)),
            _ => unreachable!(),
        };
        (result, len.assert_u32(), cost.assert_u64())
    }

    fn get_return_data(&mut self) -> Bytes {
        let [data] = call!(self, 1, GetReturnData);
        data.assert_bytes()
    }

    fn emit_log(&mut self, data: Bytes, topics: u32) -> Result<()> {
        let [out] = call!(self, 1, EmitLog, data, topics);
        match out {
            ApiValueKind::Nil => Ok(()),
            ApiValueKind::String(err) => bail!(err),
            _ => unreachable!(),
        }
    }
}

/// Executes a wasm on a new thread
pub(super) fn exec_wasm(
    sp: &mut GoStack,
    mut env: WasmEnvMut,
    module: Vec<u8>,
    calldata: Vec<u8>,
    compile: CompileConfig,
    config: StylusConfig,
    evm: Vec<u8>,
    evm_data: EvmData,
    ink: u64,
) -> Result<(Result<UserOutcome>, u64)> {
    use EvmMsg::*;
    use UserOutcomeKind::*;

    let (tx, rx) = mpsc::sync_channel(0);
    let evm = JitApi::new(evm, tx.clone());

    let handle = thread::spawn(move || unsafe {
        // Safety: module came from compile_user_wasm
        let instance = NativeInstance::deserialize(&module, compile.clone(), evm, evm_data);
        let mut instance = match instance {
            Ok(instance) => instance,
            Err(error) => {
                let message = format!("failed to instantiate program {error:?}");
                tx.send(Panic(message.clone())).unwrap();
                panic!("{message}");
            }
        };

        let outcome = instance.run_main(&calldata, config, ink);
        tx.send(Done).unwrap();

        let ink_left = match outcome.as_ref().map(|e| e.into()) {
            Ok(OutOfStack) => 0, // take all ink when out of stack
            _ => instance.ink_left().into(),
        };
        (outcome, ink_left)
    });

    loop {
        let msg = match rx.recv_timeout(Duration::from_secs(15)) {
            Ok(msg) => msg,
            Err(err) => bail!("{}", err.red()),
        };
        match msg {
            Call(func, args, respond) => {
                let (env, mut store) = env.data_and_store_mut();
                let js = &mut env.js_state;

                let mut objects = vec![];
                let mut object_ids = vec![];
                for arg in args {
                    let id = js.pool.insert(DynamicObject::Uint8Array(arg.0));
                    objects.push(GoValue::Object(id));
                    object_ids.push(id);
                }

                let Some(DynamicObject::FunctionWrapper(func)) = js.pool.get(func).cloned() else {
                    bail!("missing func {}", func.red())
                };

                js.set_pending_event(func, JsValue::Ref(STYLUS_ID), objects);
                unsafe { sp.resume(env, &mut store)? };

                let js = &mut env.js_state;
                let Some(JsValue::Ref(output)) = js.stylus_result.take() else {
                    bail!("no return value for func {}", func.red())
                };
                let Some(DynamicObject::ValueArray(output)) = js.pool.remove(output) else {
                    bail!("bad return value for func {}", func.red())
                };

                let mut outs = vec![];
                for out in output {
                    let id = out.assume_id()?;
                    let Some(DynamicObject::Uint8Array(x)) = js.pool.remove(id) else {
                        bail!("bad inner return value for func {}", func.red())
                    };
                    outs.push(ApiValue(x));
                }

                for id in object_ids {
                    env.js_state.pool.remove(id);
                }
                respond.send(outs).unwrap();
            }
            Panic(error) => bail!(error),
            Done => break,
        }
    }

    Ok(handle.join().unwrap())
}
