// Copyright 2023, Offchain Labs, Inc.
// For license information, see https://github.com/OffchainLabs/nitro/blob/master/LICENSE

#![no_main]

use arbitrum::{contract, debug, Bytes20, Bytes32};

arbitrum::arbitrum_main!(user_main);

fn user_main(input: Vec<u8>) -> Result<Vec<u8>, Vec<u8>> {
    let mut input = input.as_slice();
    let should_revert_all = input[0];
    let count = input[1];
    input = &input[2..];

    // combined output of all calls
    let mut output = vec![];

    debug::println(format!("Calling {count} contract(s), and reverting all? {}", should_revert_all));
    for i in 0..count {
        let length = u32::from_be_bytes(input[..4].try_into().unwrap()) as usize;
        input = &input[4..];

        let next = &input[length..];
        let mut curr = &input[..length];

        let kind = curr[0];
        curr = &curr[1..];

        let mut value = None;
        if kind == 0 {
            value = Some(Bytes32::from_slice(&curr[..32]).unwrap());
            curr = &curr[32..];
        }

        let addr = Bytes20::from_slice(&curr[..20]).unwrap();
        let data = &curr[20..];
        debug::println(match value {
            Some(value) if value != Bytes32::default() => format!(
                "{i} Calling {addr} with {} bytes and value {} {kind}",
                hex::encode(data),
                hex::encode(&value)
            ),
            _ => format!("{i} Calling {addr} with {} bytes {kind}", hex::encode(data)),
        });
        let return_data = match kind {
            0 => contract::call(addr, data, value, None),
            1 => contract::delegate_call(addr, data, None),
            2 => contract::static_call(addr, data, None),
            x => panic!("unknown call kind {x}"),
        };
        let results: Vec<u8> = match return_data {
            Ok(data) => {
                debug::println(format!("SUCCESS Call {}", i));
                Ok::<Vec<u8>, Vec<u8>>(data)
            },
            Err(data) => {
                debug::println(format!("FAILED Call {}", i));
                if should_revert_all == 1 {
                    return Err(data);
                }
                Ok(data)
            }
        }?;
        if !results.is_empty() {
            debug::println(format!(
                "{i} Contract {addr} returned {} bytes",
                results.len(),
            ));
        }
        output.extend(results);
        input = next;
    }
    debug::println("finito");

    Ok(output)
}
