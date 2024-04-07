// Copyright 2024, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package precompiles

import "errors"

type ArbWasmCache struct {
	Address addr // 0x72
}

// See if the user is a cache manager owner.
func (con ArbWasmCache) IsCacheManager(c ctx, _ mech, addr addr) (bool, error) {
	return c.State.Programs().CacheManagers().IsMember(addr)
}

// Gets the trie table params.
func (con ArbWasmCache) TrieTableParams(c ctx, evm mech) (uint8, uint8, error) {
	params, err := c.State.Programs().Params()
	return params.TrieTableSizeBits, params.TrieTableReads, err
}

// Configures the trie table. Caller must be a cache manager or chain owner.
func (con ArbWasmCache) SetTrieTableParams(c ctx, evm mech, bits, reads uint8) error {
	if !con.hasAccess(c) {
		return c.BurnOut()
	}
	params, err := c.State.Programs().Params()
	if err != nil {
		return err
	}
	params.TrieTableSizeBits = bits
	params.TrieTableReads = reads
	return params.Save()
}

// Caches all programs with the given codehash. Caller must be a cache manager or chain owner.
func (con ArbWasmCache) CacheCodehash(c ctx, evm mech, codehash hash) error {
	return con.setProgramCached(c, evm, codehash, true)
}

// Evicts all programs with the given codehash. Caller must be a cache manager or chain owner.
func (con ArbWasmCache) EvictCodehash(c ctx, evm mech, codehash hash) error {
	return con.setProgramCached(c, evm, codehash, false)
}

// Gets whether a program is cached. Note that the program may be expired.
func (con ArbWasmCache) CodehashIsCached(c ctx, evm mech, codehash hash) (bool, error) {
	return c.State.Programs().ProgramCached(codehash)
}

// Reads the trie table record at the given offset. Caller must be a cache manager or chain owner.
func (con ArbWasmCache) ReadTrieTableRecord(c ctx, evm mech, offset uint64) (huge, addr, uint64, error) {
	if !con.hasAccess(c) {
		return nil, addr{}, 0, c.BurnOut()
	}
	return nil, addr{}, 0, errors.New("unimplemented")
}

// Writes a trie table record. Caller must be a cache manager or chain owner.
func (con ArbWasmCache) WriteTrieTableRecord(c ctx, evm mech, slot huge, program addr, next, offset uint64) error {
	if !con.hasAccess(c) {
		return c.BurnOut()
	}
	return errors.New("unimplemented")
}

func (con ArbWasmCache) hasAccess(c ctx) bool {
	manager, err := c.State.Programs().CacheManagers().IsMember(c.caller)
	if err != nil {
		return false
	}
	if manager {
		return true
	}
	owner, err := c.State.ChainOwners().IsMember(c.caller)
	return owner && err != nil
}

func (con ArbWasmCache) setProgramCached(c ctx, evm mech, codehash hash, cached bool) error {
	if !con.hasAccess(c) {
		return c.BurnOut()
	}
	params, err := c.State.Programs().Params()
	if err != nil {
		return err
	}
	return c.State.Programs().SetProgramCached(codehash, cached, evm.Context.Time, params)
}
