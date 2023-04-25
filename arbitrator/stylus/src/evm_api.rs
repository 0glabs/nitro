// Copyright 2022-2023, Offchain Labs, Inc.
// For license information, see https://github.com/OffchainLabs/nitro/blob/master/LICENSE

use eyre::{ErrReport, Result};
use prover::{
    programs::run::UserOutcomeKind,
    utils::{Bytes20, Bytes32},
};

use crate::RustVec;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u8)]
pub enum EvmApiStatus {
    Success,
    Failure,
}

impl From<EvmApiStatus> for UserOutcomeKind {
    fn from(value: EvmApiStatus) -> Self {
        match value {
            EvmApiStatus::Success => UserOutcomeKind::Success,
            EvmApiStatus::Failure => UserOutcomeKind::Revert,
        }
    }
}

impl From<u8> for EvmApiStatus {
    fn from(value: u8) -> Self {
        match value {
            0 => Self::Success,
            _ => Self::Failure,
        }
    }
}

#[repr(C)]
pub struct GoApi {
    pub get_bytes32: unsafe extern "C" fn(id: usize, key: Bytes32, evm_cost: *mut u64) -> Bytes32, // value
    pub set_bytes32: unsafe extern "C" fn(
        id: usize,
        key: Bytes32,
        value: Bytes32,
        evm_cost: *mut u64,
        error: *mut RustVec,
    ) -> EvmApiStatus,
    pub contract_call: unsafe extern "C" fn(
        id: usize,
        contract: Bytes20,
        calldata: *mut RustVec,
        gas: *mut u64,
        value: Bytes32,
        return_data_len: *mut u32,
    ) -> EvmApiStatus,
    pub delegate_call: unsafe extern "C" fn(
        id: usize,
        contract: Bytes20,
        calldata: *mut RustVec,
        gas: *mut u64,
        return_data_len: *mut u32,
    ) -> EvmApiStatus,
    pub static_call: unsafe extern "C" fn(
        id: usize,
        contract: Bytes20,
        calldata: *mut RustVec,
        gas: *mut u64,
        return_data_len: *mut u32,
    ) -> EvmApiStatus,
    pub create1: unsafe extern "C" fn(
        id: usize,
        code: *mut RustVec,
        endowment: Bytes32,
        gas: *mut u64,
        return_data_len: *mut u32,
    ) -> EvmApiStatus,
    pub create2: unsafe extern "C" fn(
        id: usize,
        code: *mut RustVec,
        endowment: Bytes32,
        salt: Bytes32,
        gas: *mut u64,
        return_data_len: *mut u32,
    ) -> EvmApiStatus,
    pub get_return_data: unsafe extern "C" fn(id: usize, output: *mut RustVec),
    pub emit_log: unsafe extern "C" fn(id: usize, data: *mut RustVec, topics: u32) -> EvmApiStatus,
    pub id: usize,
}

#[repr(usize)]
pub enum EvmApiMethod {
    GetBytes32,
    SetBytes32,
    ContractCall,
    DelegateCall,
    StaticCall,
    Create1,
    Create2,
    GetReturnData,
    EmitLog,
}

pub trait EvmApi: Send + 'static {
    fn get_bytes32(&mut self, key: Bytes32) -> (Bytes32, u64);
    fn set_bytes32(&mut self, key: Bytes32, value: Bytes32) -> Result<u64>;
    fn contract_call(
        &mut self,
        contract: Bytes20,
        input: Vec<u8>,
        gas: u64,
        value: Bytes32,
    ) -> (u32, u64, UserOutcomeKind);
    fn delegate_call(
        &mut self,
        contract: Bytes20,
        input: Vec<u8>,
        gas: u64,
    ) -> (u32, u64, UserOutcomeKind);
    fn static_call(
        &mut self,
        contract: Bytes20,
        input: Vec<u8>,
        gas: u64,
    ) -> (u32, u64, UserOutcomeKind);
    fn create1(
        &mut self,
        code: Vec<u8>,
        endowment: Bytes32,
        gas: u64,
    ) -> (eyre::Result<Bytes20>, u32, u64);
    fn create2(
        &mut self,
        code: Vec<u8>,
        endowment: Bytes32,
        salt: Bytes32,
        gas: u64,
    ) -> (eyre::Result<Bytes20>, u32, u64);
    fn get_return_data(&mut self) -> Vec<u8>;
    fn emit_log(&mut self, data: Vec<u8>, topics: u32) -> Result<()>;
}

macro_rules! ptr {
    ($expr:expr) => {
        &mut $expr as *mut _
    };
}
macro_rules! error {
    ($data:expr) => {
        ErrReport::msg(String::from_utf8_lossy(&$data).to_string())
    };
}
macro_rules! call {
    ($self:expr, $func:ident $(,$message:expr)*) => {
        unsafe { ($self.$func)($self.id $(,$message)*) }
    };
}
macro_rules! into_vec {
    ($expr:expr) => {
        unsafe { $expr.into_vec() }
    };
}

impl EvmApi for GoApi {
    fn get_bytes32(&mut self, key: Bytes32) -> (Bytes32, u64) {
        let mut cost = 0;
        let value = call!(self, get_bytes32, key, ptr!(cost));
        (value, cost)
    }

    fn set_bytes32(&mut self, key: Bytes32, value: Bytes32) -> Result<u64> {
        let mut error = RustVec::new(vec![]);
        let mut cost = 0;
        let api_status = call!(self, set_bytes32, key, value, ptr!(cost), ptr!(error));
        let error = into_vec!(error); // done here to always drop
        match api_status {
            EvmApiStatus::Success => Ok(cost),
            EvmApiStatus::Failure => Err(error!(error)),
        }
    }

    fn contract_call(
        &mut self,
        contract: Bytes20,
        calldata: Vec<u8>,
        gas: u64,
        value: Bytes32,
    ) -> (u32, u64, UserOutcomeKind) {
        let mut call_gas = gas; // becomes the call's cost
        let mut return_data_len = 0;
        let api_status = call!(
            self,
            contract_call,
            contract,
            ptr!(RustVec::new(calldata)),
            ptr!(call_gas),
            value,
            ptr!(return_data_len)
        );
        (return_data_len, call_gas, api_status.into())
    }

    fn delegate_call(
        &mut self,
        contract: Bytes20,
        calldata: Vec<u8>,
        gas: u64,
    ) -> (u32, u64, UserOutcomeKind) {
        let mut call_gas = gas; // becomes the call's cost
        let mut return_data_len = 0;
        let api_status = call!(
            self,
            delegate_call,
            contract,
            ptr!(RustVec::new(calldata)),
            ptr!(call_gas),
            ptr!(return_data_len)
        );
        (return_data_len, call_gas, api_status.into())
    }

    fn static_call(
        &mut self,
        contract: Bytes20,
        calldata: Vec<u8>,
        gas: u64,
    ) -> (u32, u64, UserOutcomeKind) {
        let mut call_gas = gas; // becomes the call's cost
        let mut return_data_len = 0;
        let api_status = call!(
            self,
            static_call,
            contract,
            ptr!(RustVec::new(calldata)),
            ptr!(call_gas),
            ptr!(return_data_len)
        );
        (return_data_len, call_gas, api_status.into())
    }

    fn create1(
        &mut self,
        code: Vec<u8>,
        endowment: Bytes32,
        gas: u64,
    ) -> (Result<Bytes20>, u32, u64) {
        let mut call_gas = gas; // becomes the call's cost
        let mut return_data_len = 0;
        let mut code = RustVec::new(code);
        let api_status = call!(
            self,
            create1,
            ptr!(code),
            endowment,
            ptr!(call_gas),
            ptr!(return_data_len)
        );
        let output = into_vec!(code);
        let result = match api_status {
            EvmApiStatus::Success => Ok(Bytes20::try_from(output).unwrap()),
            EvmApiStatus::Failure => Err(error!(output)),
        };
        (result, return_data_len, call_gas)
    }

    fn create2(
        &mut self,
        code: Vec<u8>,
        endowment: Bytes32,
        salt: Bytes32,
        gas: u64,
    ) -> (Result<Bytes20>, u32, u64) {
        let mut call_gas = gas; // becomes the call's cost
        let mut return_data_len = 0;
        let mut code = RustVec::new(code);
        let api_status = call!(
            self,
            create2,
            ptr!(code),
            endowment,
            salt,
            ptr!(call_gas),
            ptr!(return_data_len)
        );
        let output = into_vec!(code);
        let result = match api_status {
            EvmApiStatus::Success => Ok(Bytes20::try_from(output).unwrap()),
            EvmApiStatus::Failure => Err(error!(output)),
        };
        (result, return_data_len, call_gas)
    }

    fn get_return_data(&mut self) -> Vec<u8> {
        let mut data = RustVec::new(vec![]);
        call!(self, get_return_data, ptr!(data));
        into_vec!(data)
    }

    fn emit_log(&mut self, data: Vec<u8>, topics: u32) -> Result<()> {
        let mut data = RustVec::new(data);
        let api_status = call!(self, emit_log, ptr!(data), topics);
        let error = into_vec!(data); // done here to always drop
        match api_status {
            EvmApiStatus::Success => Ok(()),
            EvmApiStatus::Failure => Err(error!(error)),
        }
    }
}
