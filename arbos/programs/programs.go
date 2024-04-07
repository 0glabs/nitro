// Copyright 2022-2024, Offchain Labs, Inc.
// For license information, see https://github.com/OffchainLabs/nitro/blob/master/LICENSE

package programs

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/offchainlabs/nitro/arbcompress"
	"github.com/offchainlabs/nitro/arbos/addressSet"
	"github.com/offchainlabs/nitro/arbos/storage"
	"github.com/offchainlabs/nitro/arbos/util"
	"github.com/offchainlabs/nitro/arbutil"
	"github.com/offchainlabs/nitro/util/arbmath"
)

type Programs struct {
	backingStorage *storage.Storage
	programs       *storage.Storage
	moduleHashes   *storage.Storage
	dataPricer     *DataPricer
	cacheManagers  *addressSet.AddressSet
}

type Program struct {
	version       uint16
	initGas       uint16
	cachedInitGas uint16
	footprint     uint16
	asmEstimateKb uint24 // Predicted size of the asm
	activatedAt   uint24 // Hours since Arbitrum began
	secondsLeft   uint64 // Not stored in state
	cached        bool
}

type uint24 = arbmath.Uint24

var paramsKey = []byte{0}
var programDataKey = []byte{1}
var moduleHashesKey = []byte{2}
var dataPricerKey = []byte{3}
var cacheManagersKey = []byte{4}

var ErrProgramActivation = errors.New("program activation failed")

var ProgramNotActivatedError func() error
var ProgramNeedsUpgradeError func(version, stylusVersion uint16) error
var ProgramExpiredError func(age uint64) error
var ProgramUpToDateError func() error
var ProgramKeepaliveTooSoon func(age uint64) error

func Initialize(sto *storage.Storage) {
	initStylusParams(sto.OpenSubStorage(paramsKey))
	initDataPricer(sto.OpenSubStorage(dataPricerKey))
	_ = addressSet.Initialize(sto.OpenCachedSubStorage(cacheManagersKey))
}

func Open(sto *storage.Storage) *Programs {
	return &Programs{
		backingStorage: sto,
		programs:       sto.OpenSubStorage(programDataKey),
		moduleHashes:   sto.OpenSubStorage(moduleHashesKey),
		dataPricer:     openDataPricer(sto.OpenCachedSubStorage(dataPricerKey)),
		cacheManagers:  addressSet.OpenAddressSet(sto.OpenCachedSubStorage(cacheManagersKey)),
	}
}

func (p Programs) DataPricer() *DataPricer {
	return p.dataPricer
}

func (p Programs) CacheManagers() *addressSet.AddressSet {
	return p.cacheManagers
}

func (p Programs) ActivateProgram(evm *vm.EVM, address common.Address, debugMode bool) (
	uint16, common.Hash, common.Hash, *big.Int, bool, error,
) {
	statedb := evm.StateDB
	codeHash := statedb.GetCodeHash(address)
	burner := p.programs.Burner()
	time := evm.Context.Time

	if statedb.HasSelfDestructed(address) {
		return 0, codeHash, common.Hash{}, nil, false, errors.New("self destructed")
	}

	params, err := p.Params()
	if err != nil {
		return 0, codeHash, common.Hash{}, nil, false, err
	}

	stylusVersion := params.Version
	currentVersion, expired, cached, err := p.programExists(codeHash, time, params)
	if err != nil {
		return 0, codeHash, common.Hash{}, nil, false, err
	}
	if currentVersion == stylusVersion && !expired {
		// already activated and up to date
		return 0, codeHash, common.Hash{}, nil, false, ProgramUpToDateError()
	}
	wasm, err := getWasm(statedb, address)
	if err != nil {
		return 0, codeHash, common.Hash{}, nil, false, err
	}

	// require the program's footprint not exceed the remaining memory budget
	pageLimit := arbmath.SaturatingUSub(params.PageLimit, statedb.GetStylusPagesOpen())

	info, err := activateProgram(statedb, address, wasm, pageLimit, stylusVersion, debugMode, burner)
	if err != nil {
		return 0, codeHash, common.Hash{}, nil, true, err
	}
	if err := p.moduleHashes.Set(codeHash, info.moduleHash); err != nil {
		return 0, codeHash, common.Hash{}, nil, true, err
	}

	estimateKb, err := arbmath.IntToUint24(arbmath.DivCeil(info.asmEstimate, 1024)) // stored in kilobytes
	if err != nil {
		return 0, codeHash, common.Hash{}, nil, true, err
	}

	dataFee, err := p.dataPricer.UpdateModel(info.asmEstimate, time)
	if err != nil {
		return 0, codeHash, common.Hash{}, nil, true, err
	}

	programData := Program{
		version:       stylusVersion,
		initGas:       info.initGas,
		cachedInitGas: info.cachedInitGas,
		footprint:     info.footprint,
		asmEstimateKb: estimateKb,
		activatedAt:   hoursSinceArbitrum(time),
		cached:        cached, // TODO: propagate to Rust
	}
	return stylusVersion, codeHash, info.moduleHash, dataFee, false, p.setProgram(codeHash, programData)
}

func (p Programs) CallProgram(
	scope *vm.ScopeContext,
	statedb vm.StateDB,
	interpreter *vm.EVMInterpreter,
	tracingInfo *util.TracingInfo,
	calldata []byte,
	reentrant bool,
) ([]byte, error) {
	evm := interpreter.Evm()
	contract := scope.Contract
	debugMode := evm.ChainConfig().DebugMode()

	params, err := p.Params()
	if err != nil {
		return nil, err
	}

	program, err := p.getProgram(contract.CodeHash, evm.Context.Time, params)
	if err != nil {
		return nil, err
	}
	moduleHash, err := p.moduleHashes.Get(contract.CodeHash)
	if err != nil {
		return nil, err
	}
	goParams := p.goParams(program.version, debugMode, params)
	l1BlockNumber, err := evm.ProcessingHook.L1BlockNumber(evm.Context)
	if err != nil {
		return nil, err
	}

	// pay for memory init
	open, ever := statedb.GetStylusPages()
	model := NewMemoryModel(params.FreePages, params.PageGas)
	callCost := model.GasCost(program.footprint, open, ever)

	// pay for program init
	if program.cached {
		callCost = arbmath.SaturatingUAdd(callCost, 64*uint64(params.MinCachedInitGas))
		callCost = arbmath.SaturatingUAdd(callCost, uint64(program.cachedInitGas))
	} else {
		callCost = arbmath.SaturatingUAdd(callCost, 256*uint64(params.MinInitGas))
		callCost = arbmath.SaturatingUAdd(callCost, uint64(program.initGas))
	}
	if err := contract.BurnGas(callCost); err != nil {
		return nil, err
	}
	statedb.AddStylusPages(program.footprint)
	defer statedb.SetStylusPagesOpen(open)

	evmData := &evmData{
		blockBasefee:    common.BigToHash(evm.Context.BaseFee),
		chainId:         evm.ChainConfig().ChainID.Uint64(),
		blockCoinbase:   evm.Context.Coinbase,
		blockGasLimit:   evm.Context.GasLimit,
		blockNumber:     l1BlockNumber,
		blockTimestamp:  evm.Context.Time,
		contractAddress: scope.Contract.Address(),
		msgSender:       scope.Contract.Caller(),
		msgValue:        common.BigToHash(scope.Contract.Value()),
		txGasPrice:      common.BigToHash(evm.TxContext.GasPrice),
		txOrigin:        evm.TxContext.Origin,
		reentrant:       arbmath.BoolToUint32(reentrant),
		tracing:         tracingInfo != nil,
	}

	address := contract.Address()
	if contract.CodeAddr != nil {
		address = *contract.CodeAddr
	}
	return callProgram(
		address, moduleHash, scope, statedb, interpreter,
		tracingInfo, calldata, evmData, goParams, model,
	)
}

func getWasm(statedb vm.StateDB, program common.Address) ([]byte, error) {
	prefixedWasm := statedb.GetCode(program)
	if prefixedWasm == nil {
		return nil, fmt.Errorf("missing wasm at address %v", program)
	}
	wasm, dictByte, err := state.StripStylusPrefix(prefixedWasm)
	if err != nil {
		return nil, err
	}

	var dict arbcompress.Dictionary
	switch dictByte {
	case 0:
		dict = arbcompress.EmptyDictionary
	case 1:
		dict = arbcompress.StylusProgramDictionary
	default:
		return nil, fmt.Errorf("unsupported dictionary %v", dictByte)
	}
	return arbcompress.DecompressWithDictionary(wasm, MaxWasmSize, dict)
}

func (p Programs) getProgram(codeHash common.Hash, time uint64, params *StylusParams) (Program, error) {
	data, err := p.programs.Get(codeHash)
	if err != nil {
		return Program{}, err
	}
	program := Program{
		version:       arbmath.BytesToUint16(data[:2]),
		initGas:       arbmath.BytesToUint16(data[2:4]),
		cachedInitGas: arbmath.BytesToUint16(data[4:6]),
		footprint:     arbmath.BytesToUint16(data[6:8]),
		activatedAt:   arbmath.BytesToUint24(data[8:11]),
		asmEstimateKb: arbmath.BytesToUint24(data[11:14]),
		cached:        arbmath.BytesToBool(data[14:15]),
	}
	if program.version == 0 {
		return program, ProgramNotActivatedError()
	}

	// check that the program is up to date
	stylusVersion := params.Version
	if program.version != stylusVersion {
		return program, ProgramNeedsUpgradeError(program.version, stylusVersion)
	}

	// ensure the program hasn't expired
	expiryDays := params.ExpiryDays
	age := hoursToAge(time, program.activatedAt)
	expirySeconds := arbmath.DaysToSeconds(expiryDays)
	if age > expirySeconds {
		return program, ProgramExpiredError(age)
	}
	program.secondsLeft = arbmath.SaturatingUSub(expirySeconds, age)
	return program, nil
}

func (p Programs) setProgram(codehash common.Hash, program Program) error {
	data := common.Hash{}
	copy(data[0:], arbmath.Uint16ToBytes(program.version))
	copy(data[2:], arbmath.Uint16ToBytes(program.initGas))
	copy(data[4:], arbmath.Uint16ToBytes(program.cachedInitGas))
	copy(data[6:], arbmath.Uint16ToBytes(program.footprint))
	copy(data[8:], arbmath.Uint24ToBytes(program.activatedAt))
	copy(data[11:], arbmath.Uint24ToBytes(program.asmEstimateKb))
	copy(data[14:], arbmath.BoolToBytes(program.cached))
	return p.programs.Set(codehash, data)
}

func (p Programs) programExists(codeHash common.Hash, time uint64, params *StylusParams) (uint16, bool, bool, error) {
	data, err := p.programs.Get(codeHash)
	if err != nil {
		return 0, false, false, err
	}

	version := arbmath.BytesToUint16(data[:2])
	activatedAt := arbmath.BytesToUint24(data[9:12])
	cached := arbmath.BytesToBool(data[14:15])
	expired := hoursToAge(time, activatedAt) > arbmath.DaysToSeconds(params.ExpiryDays)
	return version, expired, cached, err
}

func (p Programs) ProgramKeepalive(codeHash common.Hash, time uint64, params *StylusParams) (*big.Int, error) {
	program, err := p.getProgram(codeHash, time, params)
	if err != nil {
		return nil, err
	}
	keepaliveDays := params.KeepaliveDays
	if program.secondsLeft < arbmath.DaysToSeconds(keepaliveDays) {
		return nil, ProgramKeepaliveTooSoon(hoursToAge(time, program.activatedAt))
	}

	stylusVersion := params.Version
	if program.version != stylusVersion {
		return nil, ProgramNeedsUpgradeError(program.version, stylusVersion)
	}

	bytes := arbmath.SaturatingUMul(program.asmEstimateKb.ToUint32(), 1024)
	dataFee, err := p.dataPricer.UpdateModel(bytes, time)
	if err != nil {
		return nil, err
	}
	program.activatedAt = hoursSinceArbitrum(time)
	return dataFee, p.setProgram(codeHash, program)

}

func (p Programs) SetProgramCached(codeHash common.Hash, cached bool, time uint64, params *StylusParams) error {
	program, err := p.getProgram(codeHash, time, params)
	if err != nil {
		return err
	}
	program.cached = cached // TODO: propagate to Rust
	return p.setProgram(codeHash, program)
}

func (p Programs) CodehashVersion(codeHash common.Hash, time uint64, params *StylusParams) (uint16, error) {
	program, err := p.getProgram(codeHash, time, params)
	if err != nil {
		return 0, err
	}
	return program.version, nil
}

func (p Programs) ProgramTimeLeft(codeHash common.Hash, time uint64, params *StylusParams) (uint64, error) {
	program, err := p.getProgram(codeHash, time, params)
	if err != nil {
		return 0, err
	}
	return program.secondsLeft, nil
}

func (p Programs) ProgramInitGas(codeHash common.Hash, time uint64, params *StylusParams) (uint16, uint16, error) {
	program, err := p.getProgram(codeHash, time, params)
	return program.initGas, program.cachedInitGas, err
}

func (p Programs) ProgramMemoryFootprint(codeHash common.Hash, time uint64, params *StylusParams) (uint16, error) {
	program, err := p.getProgram(codeHash, time, params)
	return program.footprint, err
}

type goParams struct {
	version   uint16
	maxDepth  uint32
	inkPrice  uint24
	debugMode uint32
}

func (p Programs) goParams(version uint16, debug bool, params *StylusParams) *goParams {
	config := &goParams{
		version:  version,
		maxDepth: params.MaxStackDepth,
		inkPrice: params.InkPrice,
	}
	if debug {
		config.debugMode = 1
	}
	return config
}

type evmData struct {
	blockBasefee    common.Hash
	chainId         uint64
	blockCoinbase   common.Address
	blockGasLimit   uint64
	blockNumber     uint64
	blockTimestamp  uint64
	contractAddress common.Address
	msgSender       common.Address
	msgValue        common.Hash
	txGasPrice      common.Hash
	txOrigin        common.Address
	reentrant       uint32
	tracing         bool
}

type activationInfo struct {
	moduleHash    common.Hash
	initGas       uint16
	cachedInitGas uint16
	asmEstimate   uint32
	footprint     uint16
}

type userStatus uint8

const (
	userSuccess userStatus = iota
	userRevert
	userFailure
	userOutOfInk
	userOutOfStack
)

func (status userStatus) toResult(data []byte, debug bool) ([]byte, string, error) {
	msg := arbutil.ToStringOrHex(data)
	switch status {
	case userSuccess:
		return data, "", nil
	case userRevert:
		return data, msg, vm.ErrExecutionReverted
	case userFailure:
		return nil, msg, vm.ErrExecutionReverted
	case userOutOfInk:
		return nil, "", vm.ErrOutOfGas
	case userOutOfStack:
		return nil, "", vm.ErrDepth
	default:
		log.Error("program errored with unknown status", "status", status, "data", msg)
		return nil, msg, vm.ErrExecutionReverted
	}
}

// Hours since Arbitrum began, rounded down.
func hoursSinceArbitrum(time uint64) uint24 {
	return uint24((time - lastUpdateTimeOffset) / 3600)
}

// Computes program age in seconds from the hours passed since Arbitrum began.
func hoursToAge(time uint64, hours uint24) uint64 {
	seconds := arbmath.SaturatingUMul(uint64(hours), 3600)
	activatedAt := arbmath.SaturatingUAdd(lastUpdateTimeOffset, seconds)
	return arbmath.SaturatingUSub(time, activatedAt)
}
