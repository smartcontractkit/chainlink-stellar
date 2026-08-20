use soroban_sdk::xdr::ToXdr;
use soroban_sdk::{address_payload::AddressPayload, Address, Bytes, BytesN, Env};

use crate::constants::{domain_meta, domain_op, ENCODING_VERSION};
use crate::error::McmsError;
use crate::types::{StellarOp, StellarRootMetadata};

pub const UINT40_LIMIT: u64 = 1u64 << 40;

fn append_u32(buf: &mut Bytes, value: u32) {
    buf.extend_from_slice(&value.to_be_bytes());
}

fn append_u64(buf: &mut Bytes, value: u64) {
    buf.extend_from_slice(&value.to_be_bytes());
}

fn append_bytes(buf: &mut Bytes, value: &Bytes) {
    let mut i = 0u32;
    while i < value.len() {
        buf.push_back(value.get(i).unwrap());
        i += 1;
    }
}

fn validate_uint40(value: u64) -> Result<(), McmsError> {
    if value >= UINT40_LIMIT {
        return Err(McmsError::InvalidUint40);
    }
    Ok(())
}

pub fn contract_id(address: &Address, error: McmsError) -> Result<BytesN<32>, McmsError> {
    match address.to_payload() {
        Some(AddressPayload::ContractIdHash(id)) => Ok(id),
        _ => Err(error),
    }
}

pub fn encode_root_metadata(env: &Env, metadata: &StellarRootMetadata) -> Result<Bytes, McmsError> {
    if metadata.encoding_version != ENCODING_VERSION {
        return Err(McmsError::UnsupportedEncodingVersion);
    }
    validate_uint40(metadata.pre_op_count)?;
    validate_uint40(metadata.post_op_count)?;
    let multisig = contract_id(&metadata.multisig, McmsError::InvalidMultisig)?;

    let mut preimage = Bytes::new(env);
    preimage.extend_from_slice(&domain_meta(env).to_array());
    append_u32(&mut preimage, metadata.encoding_version);
    preimage.extend_from_slice(&metadata.network_id.to_array());
    preimage.extend_from_slice(&multisig.to_array());
    append_u64(&mut preimage, metadata.pre_op_count);
    append_u64(&mut preimage, metadata.post_op_count);
    preimage.push_back(if metadata.override_previous_root {
        1
    } else {
        0
    });
    append_u64(&mut preimage, metadata.config_version);
    Ok(preimage)
}

pub fn hash_root_metadata(
    env: &Env,
    metadata: &StellarRootMetadata,
) -> Result<BytesN<32>, McmsError> {
    Ok(env
        .crypto()
        .keccak256(&encode_root_metadata(env, metadata)?)
        .into())
}

pub fn encode_stellar_op(env: &Env, op: &StellarOp) -> Result<Bytes, McmsError> {
    if op.encoding_version != ENCODING_VERSION {
        return Err(McmsError::UnsupportedEncodingVersion);
    }
    validate_uint40(op.nonce)?;
    let multisig = contract_id(&op.multisig, McmsError::InvalidMultisig)?;
    let target = contract_id(&op.target, McmsError::InvalidTarget)?;
    let function_xdr = op.function.clone().to_xdr(env);

    let mut preimage = Bytes::new(env);
    preimage.extend_from_slice(&domain_op(env).to_array());
    append_u32(&mut preimage, op.encoding_version);
    preimage.extend_from_slice(&op.network_id.to_array());
    preimage.extend_from_slice(&multisig.to_array());
    append_u64(&mut preimage, op.nonce);
    preimage.extend_from_slice(&target.to_array());
    append_u32(&mut preimage, function_xdr.len());
    append_bytes(&mut preimage, &function_xdr);
    append_u32(&mut preimage, op.args_xdr.len());
    append_bytes(&mut preimage, &op.args_xdr);
    Ok(preimage)
}

pub fn hash_stellar_op(env: &Env, op: &StellarOp) -> Result<BytesN<32>, McmsError> {
    Ok(env.crypto().keccak256(&encode_stellar_op(env, op)?).into())
}

/// Inner hash for ECDSA: `keccak256(abi.encode(bytes32 root, uint32 validUntil))`.
pub fn hash_set_root_inner(env: &Env, root: &BytesN<32>, valid_until: u32) -> BytesN<32> {
    let mut preimage = Bytes::new(env);
    preimage.extend_from_slice(&root.to_array());
    let mut valid_until_word = [0u8; 32];
    valid_until_word[28..32].copy_from_slice(&valid_until.to_be_bytes());
    preimage.extend_from_slice(&valid_until_word);
    env.crypto().keccak256(&preimage).into()
}

/// EIP-191 Ethereum Signed Message prefix for a 32-byte digest payload.
pub fn eth_signed_message_hash_32(env: &Env, digest: &BytesN<32>) -> BytesN<32> {
    const PREFIX: &[u8] = b"\x19Ethereum Signed Message:\n32";
    let mut preimage = Bytes::new(env);
    preimage.extend_from_slice(PREFIX);
    preimage.extend_from_slice(&digest.to_array());
    env.crypto().keccak256(&preimage).into()
}

#[cfg(test)]
mod tests {
    extern crate alloc;

    use alloc::string::ToString;
    use soroban_sdk::xdr::ToXdr;
    use soroban_sdk::{Address, Bytes, BytesN, Env, Symbol};
    use stellar_strkey::Contract as StrkeyContract;

    use super::*;
    use crate::crypto::efficient_hash_pair;

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

    #[test]
    fn golden_vector_matches_normative_fixture() {
        let env = Env::default();
        let network_id = BytesN::from_array(&env, &sequential_bytes(0));
        let multisig = contract_address(&env, sequential_bytes(32));
        let target = contract_address(&env, sequential_bytes(64));
        let metadata = StellarRootMetadata {
            network_id: network_id.clone(),
            multisig: multisig.clone(),
            pre_op_count: 0,
            post_op_count: 1,
            override_previous_root: false,
            config_version: 1,
            encoding_version: ENCODING_VERSION,
        };
        let operation = StellarOp {
            network_id,
            multisig,
            nonce: 0,
            target,
            function: Symbol::new(&env, "schedule_batch"),
            args_xdr: Bytes::from_array(
                &env,
                &[
                    0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00,
                ],
            ),
            encoding_version: ENCODING_VERSION,
        };

        let metadata_preimage = encode_root_metadata(&env, &metadata).unwrap();
        let operation_preimage = encode_stellar_op(&env, &operation).unwrap();
        assert_eq!(metadata_preimage.len(), 125);
        assert_eq!(operation_preimage.len(), 184);

        let metadata_leaf = hash_root_metadata(&env, &metadata).unwrap();
        let operation_leaf = hash_stellar_op(&env, &operation).unwrap();
        assert_eq!(
            metadata_leaf,
            BytesN::from_array(
                &env,
                &[
                    0x66, 0x85, 0x3c, 0x15, 0xb7, 0x5d, 0x9d, 0x40, 0x83, 0xef, 0xb5, 0xd1, 0x86,
                    0x0d, 0x20, 0x66, 0xd3, 0x3b, 0x34, 0xe8, 0x24, 0x83, 0xb7, 0x55, 0x05, 0x93,
                    0xe8, 0x76, 0xb6, 0x71, 0xc4, 0x78,
                ],
            )
        );
        assert_eq!(
            operation_leaf,
            BytesN::from_array(
                &env,
                &[
                    0x07, 0x0f, 0x68, 0xc7, 0x2d, 0xb9, 0x75, 0xfd, 0xa1, 0xc4, 0x7c, 0x4a, 0x1d,
                    0x5c, 0xf2, 0xc7, 0xb4, 0x9e, 0x92, 0xda, 0x40, 0x61, 0x40, 0xf5, 0x3c, 0x52,
                    0x76, 0xde, 0xf4, 0x09, 0x4a, 0xd3,
                ],
            )
        );

        let root = efficient_hash_pair(&env, &metadata_leaf, &operation_leaf);
        assert_eq!(
            root,
            BytesN::from_array(
                &env,
                &[
                    0x68, 0xe1, 0x8d, 0x95, 0x09, 0xc2, 0xad, 0x56, 0x18, 0x46, 0x2a, 0xe3, 0xf2,
                    0xbb, 0x0c, 0x38, 0x01, 0x9b, 0xdb, 0xd7, 0x5b, 0x45, 0xbd, 0x16, 0x8f, 0xf3,
                    0x81, 0x72, 0xa2, 0xf6, 0x64, 0x2d,
                ],
            )
        );
        let signed_hash =
            eth_signed_message_hash_32(&env, &hash_set_root_inner(&env, &root, 2_000_000));
        assert_eq!(
            signed_hash,
            BytesN::from_array(
                &env,
                &[
                    0xfa, 0xde, 0x6e, 0x36, 0xe4, 0x4e, 0x1d, 0xc6, 0x93, 0x79, 0x2b, 0xb1, 0x53,
                    0x54, 0x93, 0xdc, 0x3d, 0x2b, 0xa3, 0xec, 0xc1, 0xc5, 0x04, 0x31, 0x59, 0x84,
                    0x0c, 0x0c, 0xc3, 0xb9, 0x8a, 0x45,
                ],
            )
        );
    }

    #[test]
    fn rejects_account_addresses_and_out_of_range_counts() {
        let env = Env::default();
        let account = Address::from_str(
            &env,
            "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
        );
        let contract = contract_address(&env, [7u8; 32]);
        let mut operation = StellarOp {
            network_id: BytesN::from_array(&env, &[0u8; 32]),
            multisig: contract.clone(),
            nonce: 0,
            target: account,
            function: Symbol::new(&env, "ping"),
            args_xdr: soroban_sdk::Vec::<soroban_sdk::Val>::new(&env).to_xdr(&env),
            encoding_version: ENCODING_VERSION,
        };
        assert_eq!(
            hash_stellar_op(&env, &operation),
            Err(McmsError::InvalidTarget)
        );

        operation.target = contract;
        operation.nonce = UINT40_LIMIT;
        assert_eq!(
            hash_stellar_op(&env, &operation),
            Err(McmsError::InvalidUint40)
        );
    }
}
