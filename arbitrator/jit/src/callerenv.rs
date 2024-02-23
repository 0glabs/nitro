// Copyright 2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

#![allow(clippy::useless_transmute)]

use crate::machine::{WasmEnv, WasmEnvMut};
use callerenv::CallerEnv;
use arbutil::{Bytes20, Bytes32};
use rand_pcg::Pcg32;
use rand::RngCore;
use std::{
    collections::{BTreeSet, BinaryHeap},
    fmt::Debug,
};
use wasmer::{Memory, MemoryView, StoreMut, WasmPtr};

pub struct JitCallerEnv<'s> {
    pub memory: Memory,
    pub store: StoreMut<'s>,
    pub wenv: &'s mut WasmEnv,
}

#[allow(dead_code)]
impl<'s> JitCallerEnv<'s> {
    /// Returns the memory size, in bytes.
    /// note: wasmer measures memory in 65536-byte pages.
    fn memory_size(&self) -> u64 {
        self.view().size().0 as u64 * 65536
    }

    pub fn new(env: &'s mut WasmEnvMut) -> Self {
        let memory = env.data().memory.clone().unwrap();
        let (data, store) = env.data_and_store_mut();
        Self {
            memory,
            store,
            wenv: data,
        }
    }

    fn view(&self) -> MemoryView {
        self.memory.view(&self.store)
    }

    pub fn caller_write_bytes20(&mut self, ptr: u32, val: Bytes20) {
        self.caller_write_slice(ptr, val.as_slice())
    }

    pub fn caller_write_bytes32(&mut self, ptr: u32, val: Bytes32) {
        self.caller_write_slice(ptr, val.as_slice())
    }

    pub fn caller_read_bytes20(&mut self, ptr: u32) -> Bytes20 {
        self.caller_read_slice(ptr, 20).try_into().unwrap()
    }

    pub fn caller_read_bytes32(&mut self, ptr: u32) -> Bytes32 {
        self.caller_read_slice(ptr, 32).try_into().unwrap()
    }

    pub fn caller_read_string(&mut self, ptr: u32, len: u32) -> String {
        let bytes = self.caller_read_slice(ptr, len);
        match String::from_utf8(bytes) {
            Ok(s) => s,
            Err(e) => {
                let bytes = e.as_bytes();
                eprintln!("Go string {} is not valid utf8: {e:?}", hex::encode(bytes));
                String::from_utf8_lossy(bytes).into_owned()
            }
        }
    }

    pub fn caller_read_slice(&self, ptr: u32, len: u32) -> Vec<u8> {
        u32::try_from(ptr).expect("Go pointer not a u32"); // kept for consistency
        let len = u32::try_from(len).expect("length isn't a u32") as usize;
        let mut data = vec![0; len];
        self.view()
            .read(ptr.into(), &mut data)
            .expect("failed to read");
        data
    }

    pub fn caller_write_slice<T: TryInto<u32>>(&self, ptr: T, src: &[u8])
    where
        T::Error: Debug,
    {
        let ptr: u32 = ptr.try_into().expect("Go pointer not a u32");
        self.view().write(ptr.into(), src).unwrap();
    }
}

impl CallerEnv<'_> for JitCallerEnv<'_> {
    fn caller_read_u8(&self, ptr: u32) -> u8 {
        let ptr: WasmPtr<u8> = WasmPtr::new(ptr);
        ptr.deref(&self.view()).read().unwrap()
    }

    fn caller_read_u16(&self, ptr: u32) -> u16 {
        let ptr: WasmPtr<u16> = WasmPtr::new(ptr);
        ptr.deref(&self.view()).read().unwrap()
    }

    fn caller_read_u32(&self, ptr: u32) -> u32 {
        let ptr: WasmPtr<u32> = WasmPtr::new(ptr);
        ptr.deref(&self.view()).read().unwrap()
    }

    fn caller_read_u64(&self, ptr: u32) -> u64 {
        let ptr: WasmPtr<u64> = WasmPtr::new(ptr);
        ptr.deref(&self.view()).read().unwrap()
    }

    fn caller_write_u8(&mut self, ptr: u32, x: u8) -> &mut Self {
        let ptr: WasmPtr<u8> = WasmPtr::new(ptr);
        ptr.deref(&self.view()).write(x).unwrap();
        self
    }

    fn caller_write_u16(&mut self, ptr: u32, x: u16) -> &mut Self {
        let ptr: WasmPtr<u16> = WasmPtr::new(ptr);
        ptr.deref(&self.view()).write(x).unwrap();
        self
    }

    fn caller_write_u32(&mut self, ptr: u32, x: u32) -> &mut Self {
        let ptr: WasmPtr<u32> = WasmPtr::new(ptr);
        ptr.deref(&self.view()).write(x).unwrap();
        self
    }

    fn caller_write_u64(&mut self, ptr: u32, x: u64) -> &mut Self {
        let ptr: WasmPtr<u64> = WasmPtr::new(ptr);
        ptr.deref(&self.view()).write(x).unwrap();
        self
    }

    fn caller_print_string(&mut self, ptr: u32, len: u32) {
        let data = self.caller_read_string(ptr, len);
        eprintln!("JIT: WASM says: {data}");
    }

    fn caller_get_time(&self) -> u64 {
        self.wenv.go_state.time
    }

    fn caller_advance_time(&mut self, delta: u64) {
        self.wenv.go_state.time += delta
    }

    fn next_rand_u32(&mut self) -> u32 {
        self.wenv.go_state.rng.next_u32()
    }
}

pub struct GoRuntimeState {
    /// An increasing clock used when Go asks for time, measured in nanoseconds
    pub time: u64,
    /// The amount of time advanced each check. Currently 10 milliseconds
    pub time_interval: u64,
    /// Deterministic source of random data
    pub rng: Pcg32,
}

impl Default for GoRuntimeState {
    fn default() -> Self {
        Self {
            time: 0,
            time_interval: 10_000_000,
            rng: Pcg32::new(callerenv::PCG_INIT_STATE, callerenv::PCG_INIT_STREAM),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TimeoutInfo {
    pub time: u64,
    pub id: u32,
}

impl Ord for TimeoutInfo {
    fn cmp(&self, other: &Self) -> std::cmp::Ordering {
        other
            .time
            .cmp(&self.time)
            .then_with(|| other.id.cmp(&self.id))
    }
}

impl PartialOrd for TimeoutInfo {
    fn partial_cmp(&self, other: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(other))
    }
}

#[derive(Default, Debug)]
pub struct TimeoutState {
    /// Contains tuples of (time, id)
    pub times: BinaryHeap<TimeoutInfo>,
    pub pending_ids: BTreeSet<u32>,
    pub next_id: u32,
}
