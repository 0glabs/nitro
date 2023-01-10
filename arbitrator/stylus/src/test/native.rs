// Copyright 2022-2023, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

use crate::{env::WasmEnv, stylus};
use arbutil::{crypto, Color};
use eyre::{bail, Result};
use prover::{
    binary,
    programs::{
        config::StylusConfig,
        counter::CountedMachine,
        depth::DepthCheckedMachine,
        meter::{MachineMeter, MeteredMachine},
        native::{GlobalMod, NativeInstance},
        run::{RunProgram, UserOutcome},
        start::StartlessMachine,
        ModuleMod, STYLUS_ENTRY_POINT,
    },
    Machine,
};
use std::path::Path;
use wasmer::{
    imports, CompilerConfig, ExportIndex, Function, Imports, Instance, MemoryType, Module, Pages,
    Store,
};
use wasmer_compiler_singlepass::Singlepass;

fn new_test_instance(path: &str, config: StylusConfig) -> Result<NativeInstance> {
    let mut store = config.store();
    let wat = std::fs::read(path)?;
    let module = Module::new(&store, &wat)?;
    let imports = imports! {
        "test" => {
            "noop" => Function::new_typed(&mut store, || {}),
        },
    };
    let instance = Instance::new(&mut store, &module, &imports)?;
    Ok(NativeInstance::new(instance, store))
}

fn new_vanilla_instance(path: &str) -> Result<NativeInstance> {
    let mut compiler = Singlepass::new();
    compiler.canonicalize_nans(true);
    compiler.enable_verifier();

    let mut store = Store::new(compiler);
    let wat = std::fs::read(path)?;
    let module = Module::new(&mut store, &wat)?;
    let instance = Instance::new(&mut store, &module, &Imports::new())?;
    Ok(NativeInstance::new(instance, store))
}

fn uniform_cost_config() -> StylusConfig {
    let mut config = StylusConfig::default();
    config.start_gas = 1_000_000;
    config.pricing.wasm_gas_price = 100_00;
    config.pricing.hostio_cost = 100;
    config.costs = |_| 1;
    config
}

#[test]
fn test_gas() -> Result<()> {
    let config = StylusConfig {
        costs: super::expensive_add,
        ..Default::default()
    };

    let mut instance = new_test_instance("tests/add.wat", config)?;
    let exports = &instance.exports;
    let add_one = exports.get_typed_function::<i32, i32>(&instance.store, "add_one")?;

    assert_eq!(instance.gas_left(), MachineMeter::Ready(0));

    macro_rules! exhaust {
        ($gas:expr) => {
            instance.set_gas($gas);
            assert_eq!(instance.gas_left(), MachineMeter::Ready($gas));
            assert!(add_one.call(&mut instance.store, 32).is_err());
            assert_eq!(instance.gas_left(), MachineMeter::Exhausted);
        };
    }

    exhaust!(0);
    exhaust!(50);
    exhaust!(99);

    let mut gas_left = 500;
    instance.set_gas(gas_left);
    while gas_left > 0 {
        assert_eq!(instance.gas_left(), MachineMeter::Ready(gas_left));
        assert_eq!(add_one.call(&mut instance.store, 64)?, 65);
        gas_left -= 100;
    }
    assert!(add_one.call(&mut instance.store, 32).is_err());
    assert_eq!(instance.gas_left(), MachineMeter::Exhausted);
    Ok(())
}

#[test]
fn test_depth() -> Result<()> {
    // in depth.wat
    //    the `depth` global equals the number of times `recurse` is called
    //    the `recurse` function calls itself
    //    the `recurse` function has 1 parameter and 2 locals
    //    comments show that the max depth is 3 words

    let config = StylusConfig {
        max_depth: 64,
        ..Default::default()
    };

    let mut instance = new_test_instance("tests/depth.wat", config)?;
    let exports = &instance.exports;
    let recurse = exports.get_typed_function::<i64, ()>(&instance.store, "recurse")?;

    let program_depth: u32 = instance.get_global("depth")?;
    assert_eq!(program_depth, 0);
    assert_eq!(instance.stack_left(), 64);

    let mut check = |space: u32, expected: u32| -> Result<()> {
        instance.set_global("depth", 0)?;
        instance.set_stack(space);
        assert_eq!(instance.stack_left(), space);

        assert!(recurse.call(&mut instance.store, 0).is_err());
        assert_eq!(instance.stack_left(), 0);

        let program_depth: u32 = instance.get_global("depth")?;
        assert_eq!(program_depth, expected);
        Ok(())
    };

    let locals = 2;
    let depth = 3;
    let fixed = 4;

    let frame_size = locals + depth + fixed;

    check(frame_size, 0)?; // should immediately exhaust (space left <= frame)
    check(frame_size + 1, 1)?;
    check(2 * frame_size, 1)?;
    check(2 * frame_size + 1, 2)?;
    check(4 * frame_size, 3)?;
    check(4 * frame_size + frame_size / 2, 4)
}

#[test]
fn test_start() -> Result<()> {
    // in start.wat
    //     the `status` global equals 10 at initialization
    //     the `start` function increments `status`
    //     by the spec, `start` must run at initialization

    fn check(instance: &mut NativeInstance, value: i32) -> Result<()> {
        let status: i32 = instance.get_global("status")?;
        assert_eq!(status, value);
        Ok(())
    }

    let mut instance = new_vanilla_instance("tests/start.wat")?;
    check(&mut instance, 11)?;

    let config = StylusConfig::default();
    let mut instance = new_test_instance("tests/start.wat", config)?;
    check(&mut instance, 10)?;

    let exports = &instance.exports;
    let move_me = exports.get_typed_function::<(), ()>(&instance.store, "move_me")?;
    let starter = instance.get_start(&instance.store)?;

    move_me.call(&mut instance.store)?;
    starter.call(&mut instance.store)?;
    check(&mut instance, 12)?;
    Ok(())
}

#[test]
fn test_import_export_safety() -> Result<()> {
    // test wasms
    //     bad-export.wat   there's a global named `stylus_gas_left`
    //     bad-export2.wat  there's a func named `stylus_global_with_random_name`
    //     bad-import.wat   there's an import named `stylus_global_with_random_name`

    fn check(path: &str, both: bool) -> Result<()> {
        if both {
            let config = StylusConfig::default();
            assert!(new_test_instance(path, config).is_err());
        }
        let path = &Path::new(path);
        let wat = std::fs::read(path)?;
        let wasm = wasmer::wat2wasm(&wat)?;
        assert!(binary::parse(&wasm, path).is_err());
        Ok(())
    }

    // TODO: perform all the same checks in instances
    check("tests/bad-export.wat", true)?;
    check("tests/bad-export2.wat", false)?;
    check("tests/bad-import.wat", false)
}

#[test]
fn test_module_mod() -> Result<()> {
    // in module-mod.wat
    //     the func `void` has the signature λ()
    //     the func `more` has the signature λ(i32, i64) -> f32
    //     the func `noop` is imported

    let file = "tests/module-mod.wat";
    let wat = std::fs::read(file)?;
    let wasm = wasmer::wat2wasm(&wat)?;
    let binary = binary::parse(&wasm, &Path::new(file))?;

    let config = StylusConfig::default();
    let instance = new_test_instance(file, config)?;
    let module = instance.module().info();

    assert_eq!(module.all_functions()?, binary.all_functions()?);
    assert_eq!(module.all_signatures()?, binary.all_signatures()?);

    let check = |name: &str| {
        let Some(ExportIndex::Function(func)) = module.exports.get(name) else {
            bail!("no func named {}", name.red())
        };
        let wasmer_ty = module.get_function(*func)?;
        let binary_ty = binary.get_function(*func)?;
        assert_eq!(wasmer_ty, binary_ty);
        println!("{} {}", func.as_u32(), binary_ty.blue());
        Ok(())
    };

    check("void")?;
    check("more")
}

#[test]
fn test_heap() -> Result<()> {
    // test wasms
    //     memory.wat   there's a 2-page memory with an upper limit of 4
    //     memory2.wat  there's a 2-page memory with no upper limit

    let config = StylusConfig {
        heap_bound: Pages(1).into(),
        ..Default::default()
    };
    assert!(new_test_instance("tests/memory.wat", config.clone()).is_err());
    assert!(new_test_instance("tests/memory2.wat", config.clone()).is_err());

    let check = |start: u32, bound: u32, expected: u32, file: &str| -> Result<()> {
        let config = StylusConfig {
            heap_bound: Pages(bound).into(),
            ..Default::default()
        };

        let instance = new_test_instance(file, config.clone())?;
        let machine = super::wavm::new_test_machine(file, config.clone())?;

        let ty = MemoryType::new(start, Some(expected), false);
        let memory = instance.exports.get_memory("mem")?;
        assert_eq!(ty, memory.ty(&instance.store));

        let memory = machine.main_module_memory();
        assert_eq!(expected as u64, memory.max_size);
        Ok(())
    };

    check(2, 2, 2, "tests/memory.wat")?;
    check(2, 2, 2, "tests/memory2.wat")?;
    check(2, 3, 3, "tests/memory.wat")?;
    check(2, 3, 3, "tests/memory2.wat")?;
    check(2, 5, 4, "tests/memory.wat")?; // the upper limit of 4 is stricter
    check(2, 5, 5, "tests/memory2.wat")
}

#[test]
fn test_rust() -> Result<()> {
    // in keccak.rs
    //     the input is the # of hashings followed by a preimage
    //     the output is the iterated hash of the preimage

    let filename = "tests/keccak/target/wasm32-unknown-unknown/release/keccak.wasm";
    let preimage = "°º¤ø,¸,ø¤°º¤ø,¸,ø¤°º¤ø,¸ nyan nyan ~=[,,_,,]:3 nyan nyan";
    let preimage = preimage.as_bytes().to_vec();
    let hash = hex::encode(crypto::keccak(&preimage));

    let mut args = vec![0x01];
    args.extend(preimage);
    let args_len = args.len() as u32;

    let config = uniform_cost_config();
    let env = WasmEnv::new(config.clone(), args.clone());
    let (mut native, env) = stylus::instance(filename, env)?;
    let exports = &native.instance.exports;
    let store = &mut native.store;

    let main = exports.get_typed_function::<u32, i32>(store, STYLUS_ENTRY_POINT)?;
    let status = main.call(store, args_len)?;
    assert_eq!(status, 0);

    let env = env.as_ref(&store);
    assert_eq!(hex::encode(&env.outs), hash);

    let mut machine = Machine::from_user_path(Path::new(filename), &config)?;
    let output = match machine.run_main(args, &config)? {
        UserOutcome::Success(output) => hex::encode(output),
        err => bail!("user program failure: {}", err.red()),
    };

    assert_eq!(output, hash);
    assert_eq!(native.gas_left(), machine.gas_left());
    assert_eq!(native.stack_left(), machine.stack_left());
    Ok(())
}

#[test]
fn test_c() -> Result<()> {
    // in siphash.c
    //     the inputs are a hash, key, and plaintext
    //     the output is whether the hash was valid

    let filename = "tests/siphash/siphash.wasm";

    let text: Vec<u8> = (0..63).collect();
    let key: Vec<u8> = (0..16).collect();
    let key: [u8; 16] = key.try_into().unwrap();
    let hash = crypto::siphash(&text, &key);

    let mut args = hash.to_le_bytes().to_vec();
    args.extend(key);
    args.extend(text);
    let args_len = args.len() as i32;

    let config = uniform_cost_config();
    let env = WasmEnv::new(config.clone(), args.clone());
    let (mut native, env) = stylus::instance(filename, env)?;
    let exports = &native.instance.exports;
    let store = &mut native.store;

    let main = exports.get_typed_function::<i32, i32>(store, STYLUS_ENTRY_POINT)?;
    let status = main.call(store, args_len)?;
    assert_eq!(status, 0);

    let env = env.as_ref(&store);
    assert_eq!(hex::encode(&env.outs), hex::encode(&env.args));

    let mut machine = Machine::from_user_path(Path::new(filename), &config)?;
    let output = match machine.run_main(args, &config)? {
        UserOutcome::Success(output) => hex::encode(output),
        err => bail!("user program failure: {}", err.red()),
    };

    assert_eq!(output, hex::encode(&env.outs));
    assert_eq!(native.gas_left(), machine.gas_left());
    assert_eq!(native.stack_left(), machine.stack_left());
    Ok(())
}

#[test]
fn test_counter_rust_keccak() -> Result<()> {
    let max_unique_operator_count = 255;
    let config = StylusConfig {
        max_unique_operator_count,
        ..Default::default()
    };
    let opcode_indexes = config.opcode_indexes.clone();

    // in keccak.rs
    //     the input is the # of hashings followed by a preimage
    //     the output is the iterated hash of the preimage

    let preimage = "°º¤ø,¸,ø¤°º¤ø,¸,ø¤°º¤ø,¸ nyan nyan ~=[,,_,,]:3 nyan nyan";
    let preimage = preimage.as_bytes().to_vec();
    let hash = hex::encode(crypto::keccak(&preimage));

    let mut args = vec![0x01];
    args.extend(preimage);
    let args_len = args.len() as i32;

    let env = WasmEnv::new(config, args);
    let filename = "tests/keccak/target/wasm32-unknown-unknown/release/keccak.wasm";
    let (mut native, env) = stylus::instance(filename, env)?;

    let main = native
        .exports
        .get_typed_function::<i32, i32>(&native.store, "arbitrum_main")?;
    let status = main.call(&mut native.store, args_len)?;
    assert_eq!(status, 0);

    let counts = native.opcode_counts(max_unique_operator_count);
    for (opcode, index) in opcode_indexes.lock().iter() {
        if *index < counts.len() && counts[*index] > 0 {
            eprintln!("{} executed {} times", opcode, counts[*index]);
        }
    }
    let env = env.as_ref(&native.store);
    assert_eq!(hex::encode(&env.outs), hash);
    Ok(())
}
