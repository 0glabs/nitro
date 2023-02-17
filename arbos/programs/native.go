// Copyright 2022-2023, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

//go:build !js
// +build !js

package programs

/*
#cgo CFLAGS: -g -Wall -I../../target/include/
#cgo LDFLAGS: ${SRCDIR}/../../target/lib/libstylus.a -ldl -lm
#include "arbitrator.h"

Bytes32 getBytes32WrapperC(size_t api, Bytes32 key);
void    setBytes32WrapperC(size_t api, Bytes32 key, Bytes32 value);
*/
import "C"
import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/offchainlabs/nitro/arbutil"
)

type u8 = C.uint8_t
type u32 = C.uint32_t
type u64 = C.uint64_t
type usize = C.size_t
type bytes32 = C.Bytes32

func compileUserWasm(db vm.StateDB, program common.Address, wasm []byte, version uint32) error {
	output := rustVec()
	status := userStatus(C.stylus_compile(
		goSlice(wasm),
		u32(version),
		output,
	))
	result, err := status.output(output.read())
	if err == nil {
		db.SetCompiledWasmCode(program, result, version)
	}
	return err
}

func callUserWasm(db vm.StateDB, program common.Address, calldata []byte, gas *uint64, params *goParams) ([]byte, error) {
	if db, ok := db.(*state.StateDB); ok {
		db.RecordProgram(program, params.version)
	}

	module := db.GetCompiledWasmCode(program, params.version)

	getBytes32 := func(key common.Hash) common.Hash {
		return db.GetState(program, key)
	}
	setBytes32 := func(key, value common.Hash) {
		db.SetState(program, key, value)
	}

	output := rustVec()
	status := userStatus(C.stylus_call(
		goSlice(module),
		goSlice(calldata),
		params.encode(),
		newAPI(getBytes32, setBytes32),
		output,
		(*u64)(gas),
	))
	data, err := status.output(output.read())
	if status == userFailure {
		log.Debug("program failure", "err", string(data), "program", program)
	}
	return data, err
}

//export getBytes32API
func getBytes32API(api usize, key bytes32) bytes32 {
	closure, err := getAPI(api)
	if err != nil {
		log.Error(err.Error())
		return bytes32{}
	}
	return hashToBytes32(closure.getBytes32(key.toHash()))
}

//export setBytes32API
func setBytes32API(api usize, key, value bytes32) {
	closure, err := getAPI(api)
	if err != nil {
		log.Error(err.Error())
		return
	}
	closure.setBytes32(key.toHash(), value.toHash())
}

func (value bytes32) toHash() common.Hash {
	hash := common.Hash{}
	for index, b := range value.bytes {
		hash[index] = byte(b)
	}
	return hash
}

func hashToBytes32(hash common.Hash) bytes32 {
	value := bytes32{}
	for index, b := range hash.Bytes() {
		value.bytes[index] = u8(b)
	}
	return value
}

func rustVec() C.RustVec {
	var ptr *u8
	var len usize
	var cap usize
	return C.RustVec{
		ptr: (**u8)(&ptr),
		len: (*usize)(&len),
		cap: (*usize)(&cap),
	}
}

func (vec C.RustVec) read() []byte {
	slice := arbutil.PointerToSlice((*byte)(*vec.ptr), int(*vec.len))
	C.stylus_free(vec)
	return slice
}

func goSlice(slice []byte) C.GoSliceData {
	return C.GoSliceData{
		ptr: (*u8)(arbutil.SliceToPointer(slice)),
		len: usize(len(slice)),
	}
}

func (params *goParams) encode() C.GoParams {
	return C.GoParams{
		version:        u32(params.version),
		max_depth:      u32(params.maxDepth),
		wasm_gas_price: u64(params.wasmGasPrice),
		hostio_cost:    u64(params.hostioCost),
	}
}
