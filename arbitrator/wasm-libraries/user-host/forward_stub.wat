;; Copyright 2022-2023, Offchain Labs, Inc.
;; For license information, see https://github.com/OffchainLabs/nitro/blob/master/LICENSE

(module
    (func (export "forward__read_args")              (param i32) unreachable)
    (func (export "forward__return_data")            (param i32 i32) unreachable)
    (func (export "forward__address_balance")        (param i32 i32) unreachable)
    (func (export "forward__address_codehash")       (param i32 i32) unreachable)
    (func (export "forward__evm_blockhash")          (param i32 i32) unreachable)
    (func (export "forward__account_load_bytes32")   (param i32 i32) unreachable)
    (func (export "forward__account_store_bytes32")  (param i32 i32) unreachable)
    (func (export "forward__call_contract")          (param i32 i32 i32 i32 i64 i32) (result i32) unreachable)
    (func (export "forward__delegate_call_contract") (param i32 i32 i32 i64 i32) (result i32) unreachable)
    (func (export "forward__static_call_contract")   (param i32 i32 i32 i64 i32) (result i32) unreachable)
    (func (export "forward__create1")                (param i32 i32 i32 i32 i32) unreachable)
    (func (export "forward__create2")                (param i32 i32 i32 i32 i32 i32) unreachable)
    (func (export "forward__read_return_data")       (param i32) unreachable)
    (func (export "forward__return_data_size")       (result i32) unreachable)
    (func (export "forward__emit_log")               (param i32 i32 i32) unreachable)
    (func (export "forward__tx_origin")              (param i32) unreachable))
