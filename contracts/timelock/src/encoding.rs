use common_helpers::soroban_invoke::decode_invoke_args;
use soroban_sdk::xdr::ToXdr;
use soroban_sdk::{address_payload::AddressPayload, Bytes, BytesN, Env};

use crate::error::TimelockError;
use crate::types::{Call, Calls};

/// `keccak256("RBAC_TIMELOCK_DOMAIN_SEPARATOR_BATCH_STELLAR")`.
const DOMAIN_TIMELOCK_BATCH_STELLAR: [u8; 32] = [
    0xe0, 0xf6, 0x6b, 0x80, 0x81, 0xe2, 0x9e, 0xf8, 0x2e, 0x0f, 0x2a, 0x28, 0xd6, 0x8a, 0xf0, 0x18,
    0x40, 0x2d, 0xd0, 0x3f, 0x4a, 0x71, 0xf5, 0x43, 0x18, 0xb8, 0x2d, 0x42, 0x7a, 0x7e, 0xd6, 0xdc,
];

fn append_bytes(buf: &mut Bytes, value: &Bytes) {
    let mut i = 0u32;
    while i < value.len() {
        buf.push_back(value.get(i).unwrap());
        i += 1;
    }
}

pub fn validate_call(env: &Env, call: &Call) -> Result<(), TimelockError> {
    match call.target.to_payload() {
        Some(AddressPayload::ContractIdHash(_)) => {}
        _ => return Err(TimelockError::InvalidTarget),
    }
    decode_invoke_args(env, &call.args_xdr).map_err(|_| TimelockError::InvalidArgsXdr)?;
    Ok(())
}

pub fn hash_args(env: &Env, call: &Call) -> BytesN<32> {
    env.crypto().keccak256(&call.args_xdr).into()
}

pub fn hash_single_call(env: &Env, call: &Call) -> Result<BytesN<32>, TimelockError> {
    validate_call(env, call)?;
    let target = match call.target.to_payload() {
        Some(AddressPayload::ContractIdHash(id)) => id,
        _ => return Err(TimelockError::InvalidTarget),
    };
    let function_xdr = call.function.clone().to_xdr(env);
    let mut buf = Bytes::new(env);
    buf.extend_from_slice(&target.to_array());
    buf.extend_from_slice(&function_xdr.len().to_be_bytes());
    append_bytes(&mut buf, &function_xdr);
    buf.extend_from_slice(&call.args_xdr.len().to_be_bytes());
    append_bytes(&mut buf, &call.args_xdr);
    Ok(env.crypto().keccak256(&buf).into())
}

pub fn hash_operation_batch(
    env: &Env,
    calls: &Calls,
    predecessor: &BytesN<32>,
    salt: &BytesN<32>,
) -> Result<BytesN<32>, TimelockError> {
    if calls.inner.is_empty() {
        return Err(TimelockError::EmptyBatch);
    }
    let mut buf = Bytes::new(env);
    buf.extend_from_slice(&DOMAIN_TIMELOCK_BATCH_STELLAR);
    buf.extend_from_slice(&calls.inner.len().to_be_bytes());
    let mut i = 0u32;
    while i < calls.inner.len() {
        buf.extend_from_slice(&hash_single_call(env, &calls.inner.get(i).unwrap())?.to_array());
        i += 1;
    }
    buf.extend_from_slice(&predecessor.to_array());
    buf.extend_from_slice(&salt.to_array());
    Ok(env.crypto().keccak256(&buf).into())
}

#[cfg(test)]
mod tests {
    extern crate alloc;

    use alloc::string::ToString;
    use soroban_sdk::xdr::ToXdr;
    use soroban_sdk::{Address, Bytes, BytesN, Env, IntoVal, Symbol, Val, Vec};
    use stellar_strkey::Contract as StrkeyContract;

    use super::*;

    fn sequential_bytes(start: u8) -> [u8; 32] {
        let mut out = [0u8; 32];
        let mut i = 0usize;
        while i < 32 {
            out[i] = start + i as u8;
            i += 1;
        }
        out
    }

    fn contract_address(env: &Env, id: [u8; 32]) -> Address {
        let encoded = StrkeyContract(id).to_string();
        Address::from_str(env, encoded.as_str())
    }

    fn golden_call(env: &Env) -> Call {
        Call {
            target: contract_address(env, sequential_bytes(64)),
            function: Symbol::new(env, "schedule_batch"),
            args_xdr: Bytes::from_array(
                env,
                &[
                    0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00,
                ],
            ),
        }
    }

    fn single_call_batch(env: &Env, call: Call) -> Calls {
        Calls {
            inner: Vec::from_array(env, [call]),
        }
    }

    fn zero_bytes32(env: &Env) -> BytesN<32> {
        BytesN::from_array(env, &[0u8; 32])
    }

    fn golden_salt(env: &Env) -> BytesN<32> {
        let mut salt = [0u8; 32];
        salt[31] = 1;
        BytesN::from_array(env, &salt)
    }

    #[test]
    fn batch_domain_is_keccak_of_ascii_label() {
        let env = Env::default();
        let ascii = Bytes::from_slice(&env, b"RBAC_TIMELOCK_DOMAIN_SEPARATOR_BATCH_STELLAR");
        let expected: BytesN<32> = env.crypto().keccak256(&ascii).into();
        assert_eq!(expected.to_array(), DOMAIN_TIMELOCK_BATCH_STELLAR);
    }

    /// Fixture: `contracts/mcms/testdata/stellar_golden_vectors.json` (`timelock` section).
    #[test]
    fn golden_vector_matches_normative_fixture() {
        let env = Env::default();
        let call = golden_call(&env);

        // call_hash has no domain prefix, so it is independent of the batch domain separator.
        let call_hash = hash_single_call(&env, &call).unwrap();
        assert_eq!(
            call_hash,
            BytesN::from_array(
                &env,
                &[
                    0x18, 0xd3, 0xe9, 0x17, 0x26, 0x88, 0x4e, 0xcc, 0x89, 0x69, 0x74, 0x45, 0x8f,
                    0x55, 0xe1, 0x65, 0x73, 0x18, 0x83, 0xb2, 0x7c, 0x43, 0xc3, 0xcc, 0x7b, 0xb0,
                    0xf8, 0x9f, 0x04, 0x47, 0xdd, 0x66,
                ],
            )
        );

        let operation_id = hash_operation_batch(
            &env,
            &single_call_batch(&env, call),
            &zero_bytes32(&env),
            &golden_salt(&env),
        )
        .unwrap();
        assert_eq!(
            operation_id,
            BytesN::from_array(
                &env,
                &[
                    0x1c, 0xe6, 0x7c, 0x58, 0x8a, 0xd5, 0xf0, 0x0c, 0x9a, 0xdd, 0x1f, 0x33, 0x4b,
                    0x27, 0x2d, 0x23, 0xf4, 0x0a, 0x8f, 0x91, 0x55, 0xab, 0x6f, 0x9c, 0xff, 0x2d,
                    0x8c, 0x35, 0xee, 0x16, 0xe7, 0x9e,
                ],
            )
        );
    }

    #[test]
    fn operation_id_changes_on_any_field_mutation() {
        let env = Env::default();
        let base_call = golden_call(&env);
        let predecessor = zero_bytes32(&env);
        let salt = golden_salt(&env);
        let base_id = hash_operation_batch(
            &env,
            &single_call_batch(&env, base_call.clone()),
            &predecessor,
            &salt,
        )
        .unwrap();

        let mut other_target = base_call.clone();
        other_target.target = contract_address(&env, sequential_bytes(65));
        let mut other_function = base_call.clone();
        other_function.function = Symbol::new(&env, "cancel");
        let mut other_args = base_call.clone();
        let mut args: Vec<Val> = Vec::new(&env);
        args.push_back(7u32.into_val(&env));
        other_args.args_xdr = args.to_xdr(&env);

        for mutated in [other_target.clone(), other_function, other_args] {
            let id =
                hash_operation_batch(&env, &single_call_batch(&env, mutated), &predecessor, &salt)
                    .unwrap();
            assert_ne!(id, base_id);
        }

        let mut other_predecessor = [0u8; 32];
        other_predecessor[0] = 1;
        let id = hash_operation_batch(
            &env,
            &single_call_batch(&env, base_call.clone()),
            &BytesN::from_array(&env, &other_predecessor),
            &salt,
        )
        .unwrap();
        assert_ne!(id, base_id);

        let mut other_salt = [0u8; 32];
        other_salt[31] = 2;
        let id = hash_operation_batch(
            &env,
            &single_call_batch(&env, base_call.clone()),
            &predecessor,
            &BytesN::from_array(&env, &other_salt),
        )
        .unwrap();
        assert_ne!(id, base_id);

        let forward = Calls {
            inner: Vec::from_array(&env, [base_call.clone(), other_target.clone()]),
        };
        let reversed = Calls {
            inner: Vec::from_array(&env, [other_target, base_call]),
        };
        let forward_id = hash_operation_batch(&env, &forward, &predecessor, &salt).unwrap();
        let reversed_id = hash_operation_batch(&env, &reversed, &predecessor, &salt).unwrap();
        assert_ne!(forward_id, reversed_id);
    }

    #[test]
    fn empty_batch_is_rejected() {
        let env = Env::default();
        let calls = Calls {
            inner: Vec::new(&env),
        };
        assert_eq!(
            hash_operation_batch(&env, &calls, &zero_bytes32(&env), &golden_salt(&env)),
            Err(TimelockError::EmptyBatch)
        );
    }
}
