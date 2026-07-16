//! Integration-style contract tests (Soroban `Env`).

#![cfg(test)]

extern crate alloc;

use alloc::vec::Vec;

use k256::ecdsa::SigningKey;
use sha3::{Digest, Keccak256};
use soroban_sdk::testutils::Address as _;
use soroban_sdk::testutils::Ledger;
use soroban_sdk::xdr::ToXdr;
use soroban_sdk::{
    contract, contractimpl, Address, Bytes, BytesN, Env, IntoVal, Symbol, Val, Vec as SorobanVec,
};

use crate::abi_encoding::{
    eth_signed_message_hash_32, hash_root_metadata, hash_set_root_inner, hash_stellar_op,
};
use crate::constants::domain_meta;
use crate::crypto::{cmp_bytes32, efficient_hash_pair};
use crate::error::McmsError;
use crate::types::{
    MerkleProof, Signature, SignatureVec, SignerAddresses, SignerGroups, StellarOp,
    StellarRootMetadata,
};
use crate::{McmsContract, McmsContractClient};
use ccip_ramp_registry::RampRegistryContract;
use common_interfaces::ramp_registry::RampRegistryClient;
use timelock::{Calls, TimelockContract, TimelockContractClient};

const NUM_GROUP_BYTES: usize = 32;

/// Deterministic 32-byte “padded EVM” signer (strictly-increasing single-signer tests).
fn signer_a(env: &Env) -> BytesN<32> {
    let mut a = [0u8; 32];
    a[12..32].copy_from_slice(&[0x1u8; 20]);
    BytesN::from_array(env, &a)
}

fn zero_chain_id(env: &Env) -> BytesN<32> {
    BytesN::from_array(env, &[0u8; 32])
}

fn one_of_one_quorum(env: &Env) -> BytesN<32> {
    let mut gq = [0u8; NUM_GROUP_BYTES];
    gq[0] = 1; // group 0 quorum 1
    BytesN::from_array(env, &gq)
}

fn two_of_two_group0_quorum(env: &Env) -> BytesN<32> {
    let mut gq = [0u8; NUM_GROUP_BYTES];
    gq[0] = 2; // group 0 quorum 2
    BytesN::from_array(env, &gq)
}

fn all_zero_parents(env: &Env) -> BytesN<32> {
    BytesN::from_array(env, &[0u8; NUM_GROUP_BYTES])
}

fn register_client(env: &Env) -> McmsContractClient<'_> {
    let id = env.register(McmsContract, ());
    McmsContractClient::new(env, &id)
}

fn register_timelock(env: &Env) -> TimelockContractClient<'_> {
    let id = env.register(TimelockContract, ());
    TimelockContractClient::new(env, &id)
}

/// Minimal callee for successful MCMS `execute` tests (no self re-entry).
#[contract]
pub struct ExecPingMock;

#[contractimpl]
impl ExecPingMock {
    pub fn ping(_env: Env) {}
}

/// Minimal callee whose `revert` entrypoint always traps, for MCMS `execute` `CallReverted`
/// tests. `try_invoke_contract` catches the host trap into its `Err(_)` arm, which MCMS maps to
/// `CallReverted`.
#[contract]
pub struct ExecRevertMock;

#[contractimpl]
impl ExecRevertMock {
    pub fn revert(_env: Env) {
        panic!("ExecRevertMock: deliberate revert");
    }
}

mod test_support {
    use soroban_sdk::{address_payload::AddressPayload, Address, BytesN, Env};

    pub fn addr_to_contract_id(addr: &Address, env: &Env) -> BytesN<32> {
        match addr.to_payload() {
            Some(AddressPayload::ContractIdHash(id)) => id,
            _ => BytesN::from_array(env, &[0u8; 32]),
        }
    }
}

// --- lifecycle ---

#[test]
fn test_initialize_and_chain_id() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);

    client.initialize(&owner, &chain);
    assert_eq!(client.chain_network_id(), chain);
    assert_eq!(client.get_op_count(), 0);
    let (root, valid_until) = client.get_root();
    assert_eq!(root, BytesN::from_array(&env, &[0u8; 32]));
    assert_eq!(valid_until, 0u32);
}

/// Second `initialize` returns contract error; non-`try` entrypoint may panic in tests.
#[test]
#[should_panic]
fn test_double_initialize_panics() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);
    client.initialize(&owner, &chain);
}

#[test]
fn test_set_config_1_of_1_and_get_config() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let s = signer_a(&env);
    let addrs = SignerAddresses {
        inner: SorobanVec::from_array(&env, [s.clone()]),
    };
    let groups = SignerGroups {
        inner: SorobanVec::from_array(&env, [0u32]),
    };

    client.set_config(
        &addrs,
        &groups,
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    let cfg = client.get_config();
    assert_eq!(cfg.signers.len(), 1);
    assert_eq!(cfg.signers.get(0).unwrap().addr, s);
    assert_eq!(cfg.signers.get(0).unwrap().index, 0);
    assert_eq!(cfg.signers.get(0).unwrap().group, 0u32);
}

/// No `set_config` yet → `get_config` fails with `MissingConfig`
#[test]
fn test_get_config_fails_before_set() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    // Generated `try_*` type: `Result<Result<Config, ConversionError>, Result<McmsError, InvokeError>>`
    // — contract `Err(_)` is `Err(Ok(McmsError::...))` on the outer `Result`.
    assert!(matches!(
        client.try_get_config(),
        Err(Ok(McmsError::MissingConfig))
    ));
}

/// Without a prior `set_root`, stored root metadata is absent: `execute` must fail
#[test]
fn test_execute_fails_missing_root_metadata() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);
    let self_addr = client.address.clone();
    let multisig = test_support::addr_to_contract_id(&self_addr, &env);
    let op = StellarOp {
        chain_id: chain.clone(),
        multisig: multisig.clone(),
        nonce: 0,
        to: multisig, // self-invoke
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: Bytes::new(&env),
    };
    let proof = MerkleProof {
        inner: SorobanVec::new(&env),
    };
    assert!(matches!(
        client.try_execute(&op, &proof),
        Err(Ok(McmsError::MissingRootMetadata))
    ));
}

/// `set_root` with no prior `set_config` must fail before reaching signature verification.
#[test]
fn test_set_root_fails_without_config() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let root = BytesN::from_array(&env, &[0u8; 32]);
    let metadata = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: BytesN::from_array(&env, &[0u8; 32]),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: false,
    };
    let metadata_proof = MerkleProof {
        inner: SorobanVec::new(&env),
    };
    let signatures = SignatureVec {
        inner: SorobanVec::new(&env),
    };

    assert!(matches!(
        client.try_set_root(&root, &0u32, &metadata, &metadata_proof, &signatures),
        Err(Ok(McmsError::MissingConfig))
    ));
}

/// `set_config(clear_root=true)` must zero the expiring root, preserve op_count, and
/// write root metadata — exercising a security path (invalidating pending ops) and
/// the otherwise-untested `get_root_metadata` getter.
#[test]
fn test_set_config_clear_root_resets_state() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let s = signer_a(&env);
    let addrs = SignerAddresses {
        inner: SorobanVec::from_array(&env, [s]),
    };
    let groups = SignerGroups {
        inner: SorobanVec::from_array(&env, [0u32]),
    };

    client.set_config(
        &addrs,
        &groups,
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &true,
    );

    let (root, valid_until) = client.get_root();
    assert_eq!(root, BytesN::from_array(&env, &[0u8; 32]));
    assert_eq!(valid_until, 0u32);
    assert_eq!(client.get_op_count(), 0u64);

    let meta = client.get_root_metadata();
    assert_eq!(meta.pre_op_count, 0);
    assert_eq!(meta.post_op_count, 0);
    assert!(meta.override_previous_root);
}

// --- set_config error paths (still as owner) ---

#[test]
fn test_set_config_mismatched_vec_lengths() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let s = signer_a(&env);
    let addrs = SignerAddresses {
        inner: SorobanVec::from_array(&env, [s]),
    };
    let groups = SignerGroups {
        inner: SorobanVec::new(&env), // 0 — mismatch
    };

    assert!(matches!(
        client.try_set_config(
            &addrs,
            &groups,
            &one_of_one_quorum(&env),
            &all_zero_parents(&env),
            &false,
        ),
        Err(Ok(McmsError::SignerGroupsLengthMismatch))
    ));
}

// ---------------------------------------------------------------------------
// Merkle + signing helpers (align with ccip-owner MerkleHelper + Anvil key)
// ---------------------------------------------------------------------------

/// Anvil / Foundry default account #0 secret (public; test-only).
const ANVIL_SK_0: [u8; 32] = [
    0xac, 0x09, 0x74, 0xbe, 0xc3, 0x9a, 0x17, 0xe3, 0x6b, 0xa4, 0xa6, 0xb4, 0xd2, 0x38, 0xff, 0x94,
    0x4b, 0xac, 0xb4, 0x78, 0xcb, 0xed, 0x5e, 0xfc, 0xae, 0x78, 0x4d, 0x7b, 0xf4, 0xf2, 0xff, 0x80,
];

/// Anvil / Foundry default account #1 secret (public; test-only).
const ANVIL_SK_1: [u8; 32] = [
    0x59, 0xc6, 0x99, 0x5e, 0x99, 0x8f, 0x97, 0xa5, 0xa0, 0x04, 0x49, 0x66, 0xf0, 0x94, 0x53, 0x89,
    0xdc, 0x9e, 0x86, 0xda, 0xe8, 0x8c, 0x7a, 0x84, 0x12, 0xf4, 0x60, 0x3b, 0x6b, 0x78, 0x69, 0x0d,
];

fn padded_eth_address(env: &Env, sk: &SigningKey) -> BytesN<32> {
    let vk = sk.verifying_key();
    let encoded = vk.to_encoded_point(false);
    let mut hasher = Keccak256::new();
    hasher.update(&encoded.as_bytes()[1..]);
    let out = hasher.finalize();
    let mut padded = [0u8; 32];
    padded[12..32].copy_from_slice(&out[12..]);
    BytesN::from_array(env, &padded)
}

fn proof_len_ceiling(leaf_count: usize) -> usize {
    let mut power = 1usize;
    let mut exp = 0usize;
    while power < leaf_count {
        power *= 2;
        exp += 1;
    }
    exp
}

fn hash_level_native(env: &Env, data: &[BytesN<32>]) -> Vec<BytesN<32>> {
    assert_eq!(data.len() % 2, 0);
    let mut out = Vec::new();
    let mut i = 0usize;
    while i < data.len() {
        out.push(efficient_hash_pair(env, &data[i], &data[i + 1]));
        i += 2;
    }
    out
}

fn merkle_root_native(env: &Env, leaves: &[BytesN<32>]) -> BytesN<32> {
    assert_eq!(leaves.len() % 2, 0);
    let mut data: Vec<BytesN<32>> = leaves.to_vec();
    while data.len() > 1 {
        data = hash_level_native(env, &data);
    }
    data[0].clone()
}

/// Proof for leaf at `index` (same sibling walk as `MerkleHelper.computeProofForLeaf`).
fn compute_proof_for_leaf(
    env: &Env,
    mut leaves: Vec<BytesN<32>>,
    mut index: usize,
) -> SorobanVec<BytesN<32>> {
    assert_eq!(leaves.len() % 2, 0);
    let plen = proof_len_ceiling(leaves.len());
    let mut proof = SorobanVec::new(env);
    while leaves.len() > 1 {
        let sibling_idx = if index & 1 == 1 { index - 1 } else { index + 1 };
        proof.push_back(leaves[sibling_idx].clone());
        index /= 2;
        leaves = hash_level_native(env, &leaves);
    }
    assert_eq!(proof.len() as usize, plen);
    proof
}

fn encode_ping(env: &Env) -> Bytes {
    let mut v: SorobanVec<Val> = SorobanVec::new(env);
    v.push_back(Symbol::new(env, "ping").into_val(env));
    v.to_xdr(env)
}

fn encode_revert(env: &Env) -> Bytes {
    let mut v: SorobanVec<Val> = SorobanVec::new(env);
    v.push_back(Symbol::new(env, "revert").into_val(env));
    v.to_xdr(env)
}

fn encode_extend_all_ttls(env: &Env) -> Bytes {
    let mut v: SorobanVec<Val> = SorobanVec::new(env);
    v.push_back(Symbol::new(env, "extend_all_ttls").into_val(env));
    v.to_xdr(env)
}

fn signature_vec_single(env: &Env, sk: &SigningKey, signed_digest: &BytesN<32>) -> SignatureVec {
    let (sig64, recid) = sk
        .sign_prehash_recoverable(signed_digest.to_array().as_slice())
        .expect("secp256k1 sign");
    let b = sig64.to_bytes();
    let r = BytesN::from_array(env, b[..32].try_into().unwrap());
    let s = BytesN::from_array(env, b[32..].try_into().unwrap());
    let v = 27u32 + recid.to_byte() as u32;
    let sig = Signature { v, r, s };
    SignatureVec {
        inner: SorobanVec::from_array(env, [sig]),
    }
}

fn encode_accept_ownership_ramp(env: &Env) -> Bytes {
    let mut v: SorobanVec<Val> = SorobanVec::new(env);
    v.push_back(Symbol::new(env, "accept_ownership").into_val(env));
    v.to_xdr(env)
}

fn encode_transfer_ownership_ramp(env: &Env, new_owner: Address) -> Bytes {
    let mut v: SorobanVec<Val> = SorobanVec::new(env);
    v.push_back(Symbol::new(env, "transfer_ownership").into_val(env));
    v.push_back(new_owner.into_val(env));
    v.to_xdr(env)
}

fn zero_bytes32(env: &Env) -> BytesN<32> {
    BytesN::from_array(env, &[0u8; 32])
}

fn salt_byte(env: &Env, v: u8) -> BytesN<32> {
    let mut s = [0u8; 32];
    s[31] = v;
    BytesN::from_array(env, &s)
}

fn encode_schedule_batch(
    env: &Env,
    caller: Address,
    calls: Calls,
    predecessor: BytesN<32>,
    salt: BytesN<32>,
    delay: u64,
) -> Bytes {
    let mut v: SorobanVec<Val> = SorobanVec::new(env);
    v.push_back(Symbol::new(env, "schedule_batch").into_val(env));
    v.push_back(caller.into_val(env));
    v.push_back(calls.into_val(env));
    v.push_back(predecessor.into_val(env));
    v.push_back(salt.into_val(env));
    v.push_back(delay.into_val(env));
    v.to_xdr(env)
}

// --- additional coverage (EVM-aligned negative paths + happy execution) ---

#[test]
fn test_set_config_rejects_without_owner_auth() {
    let env = Env::default();
    env.mock_auths(&[]);
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let addr = padded_eth_address(&env, &sk);
    let addrs = SignerAddresses {
        inner: SorobanVec::from_array(&env, [addr]),
    };
    let groups = SignerGroups {
        inner: SorobanVec::from_array(&env, [0u32]),
    };

    assert!(client
        .try_set_config(
            &addrs,
            &groups,
            &one_of_one_quorum(&env),
            &all_zero_parents(&env),
            &false,
        )
        .is_err());
}

#[test]
fn test_extend_all_ttls_without_auth_succeeds() {
    let env = Env::default();
    env.mock_auths(&[]);
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let r = client.try_extend_all_ttls();
    assert!(r.is_ok());
}

#[test]
fn test_set_config_rejects_zero_signers() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let addrs = SignerAddresses {
        inner: SorobanVec::new(&env),
    };
    let groups = SignerGroups {
        inner: SorobanVec::new(&env),
    };

    assert!(matches!(
        client.try_set_config(
            &addrs,
            &groups,
            &one_of_one_quorum(&env),
            &all_zero_parents(&env),
            &false,
        ),
        Err(Ok(McmsError::OutOfBoundsNumOfSigners))
    ));
}

/// `set_root` → `execute(ping)` on an external mock (single op, 1-of-1), then second `execute` hits post-op count.
#[test]
fn test_set_root_execute_and_post_op_count_reached() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| {
        li.timestamp = 1_000;
    });

    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    let addrs = SignerAddresses {
        inner: SorobanVec::from_array(&env, [signer_addr.clone()]),
    };
    let groups = SignerGroups {
        inner: SorobanVec::from_array(&env, [0u32]),
    };
    client.set_config(
        &addrs,
        &groups,
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    let self_cid = test_support::addr_to_contract_id(&client.address, &env);
    let ping_addr = env.register(ExecPingMock, ());
    let ping_cid = test_support::addr_to_contract_id(&ping_addr, &env);
    let valid_until: u32 = 2_000_000;
    let metadata = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: false,
    };
    let meta_leaf = hash_root_metadata(&env, &domain_meta(&env), &metadata).unwrap();
    let op = StellarOp {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        nonce: 0,
        to: ping_cid,
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: encode_ping(&env),
    };
    let op_leaf = hash_stellar_op(&env, &crate::constants::domain_op(&env), &op).unwrap();
    let leaves = Vec::from([meta_leaf.clone(), op_leaf.clone()]);
    let root = merkle_root_native(&env, &leaves);
    let metadata_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves.clone(), 0),
    };
    let inner = hash_set_root_inner(&env, &root, valid_until);
    let signed = eth_signed_message_hash_32(&env, &inner);
    let sigs = signature_vec_single(&env, &sk, &signed);

    client.set_root(&root, &valid_until, &metadata, &metadata_proof, &sigs);

    let op_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves, 1),
    };
    assert_eq!(client.get_op_count(), 0);
    client.execute(&op, &op_proof);
    assert_eq!(client.get_op_count(), 1);

    assert!(matches!(
        client.try_execute(&op, &op_proof),
        Err(Ok(McmsError::PostOpCountReached))
    ));
}

/// A failed downstream invoke surfaces as `CallReverted` and does NOT consume the nonce.
///
/// `execute` persists the incremented op_count before the downstream invoke (reentrancy guard,
/// matching ManyChainMultiSig's "increase the counter *before* execution"). The failed invocation
/// rolls that write back, so the op remains executable. Regression for the op_count persist
/// ordering change.
#[test]
fn test_execute_reverted_call_does_not_consume_nonce() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| {
        li.timestamp = 1_000;
    });

    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    client.set_config(
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    let self_cid = test_support::addr_to_contract_id(&client.address, &env);
    let revert_addr = env.register(ExecRevertMock, ());
    let revert_cid = test_support::addr_to_contract_id(&revert_addr, &env);
    let valid_until: u32 = 2_000_000;
    let metadata = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: false,
    };
    let meta_leaf = hash_root_metadata(&env, &domain_meta(&env), &metadata).unwrap();
    let op = StellarOp {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        nonce: 0,
        to: revert_cid,
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: encode_revert(&env),
    };
    let op_leaf = hash_stellar_op(&env, &crate::constants::domain_op(&env), &op).unwrap();
    let leaves = Vec::from([meta_leaf, op_leaf]);
    let root = merkle_root_native(&env, &leaves);
    let metadata_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves.clone(), 0),
    };
    let inner = hash_set_root_inner(&env, &root, valid_until);
    let signed = eth_signed_message_hash_32(&env, &inner);
    let sigs = signature_vec_single(&env, &sk, &signed);

    client.set_root(&root, &valid_until, &metadata, &metadata_proof, &sigs);

    let op_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves, 1),
    };
    assert_eq!(client.get_op_count(), 0);
    // Failed downstream invoke → CallReverted (caught via try_execute).
    assert!(matches!(
        client.try_execute(&op, &op_proof),
        Err(Ok(McmsError::CallReverted))
    ));
    // Nonce not consumed: the write was rolled back, so the op is still executable.
    assert_eq!(client.get_op_count(), 0);
    // And re-executing against a succeeding target now advances the nonce from 0. The previous
    // root still has a pending (unexecuted) op, so the new root must override it.
    let ping_addr = env.register(ExecPingMock, ());
    let ping_cid = test_support::addr_to_contract_id(&ping_addr, &env);
    let ok_op = StellarOp {
        chain_id: chain,
        multisig: self_cid,
        nonce: 0,
        to: ping_cid,
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: encode_ping(&env),
    };
    let ok_metadata = StellarRootMetadata {
        chain_id: zero_chain_id(&env),
        multisig: test_support::addr_to_contract_id(&client.address, &env),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: true,
    };
    let ok_leaf = hash_stellar_op(&env, &crate::constants::domain_op(&env), &ok_op).unwrap();
    let ok_leaves = Vec::from([
        hash_root_metadata(&env, &domain_meta(&env), &ok_metadata).unwrap(),
        ok_leaf,
    ]);
    let ok_root = merkle_root_native(&env, &ok_leaves);
    let ok_meta_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, ok_leaves.clone(), 0),
    };
    let ok_inner = hash_set_root_inner(&env, &ok_root, valid_until);
    let ok_signed = eth_signed_message_hash_32(&env, &ok_inner);
    let ok_sigs = signature_vec_single(&env, &sk, &ok_signed);
    client.set_root(
        &ok_root,
        &valid_until,
        &ok_metadata,
        &ok_meta_proof,
        &ok_sigs,
    );
    let ok_op_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, ok_leaves, 1),
    };
    client.execute(&ok_op, &ok_op_proof);
    assert_eq!(client.get_op_count(), 1);
}

#[test]
fn test_execute_reverts_bad_proof() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| {
        li.timestamp = 1_000;
    });

    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    client.set_config(
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    let self_cid = test_support::addr_to_contract_id(&client.address, &env);
    let valid_until: u32 = 2_000_000;
    let metadata = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: false,
    };
    let meta_leaf = hash_root_metadata(&env, &domain_meta(&env), &metadata).unwrap();
    let op = StellarOp {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        nonce: 0,
        to: self_cid.clone(),
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: encode_extend_all_ttls(&env),
    };
    let op_leaf = hash_stellar_op(&env, &crate::constants::domain_op(&env), &op).unwrap();
    let leaves = Vec::from([meta_leaf, op_leaf]);
    let root = merkle_root_native(&env, &leaves);
    let metadata_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves.clone(), 0),
    };
    let inner = hash_set_root_inner(&env, &root, valid_until);
    let signed = eth_signed_message_hash_32(&env, &inner);
    let sigs = signature_vec_single(&env, &sk, &signed);
    client.set_root(&root, &valid_until, &metadata, &metadata_proof, &sigs);

    let empty_proof = MerkleProof {
        inner: SorobanVec::new(&env),
    };
    assert!(matches!(
        client.try_execute(&op, &empty_proof),
        Err(Ok(McmsError::ProofCannotBeVerified))
    ));
}

#[test]
fn test_execute_reverts_wrong_chain_id_op() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| {
        li.timestamp = 1_000;
    });

    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    client.set_config(
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    let self_cid = test_support::addr_to_contract_id(&client.address, &env);
    let valid_until: u32 = 2_000_000;
    let metadata = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: false,
    };
    let meta_leaf = hash_root_metadata(&env, &domain_meta(&env), &metadata).unwrap();
    let op = StellarOp {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        nonce: 0,
        to: self_cid.clone(),
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: encode_extend_all_ttls(&env),
    };
    let op_leaf = hash_stellar_op(&env, &crate::constants::domain_op(&env), &op).unwrap();
    let leaves = Vec::from([meta_leaf, op_leaf]);
    let root = merkle_root_native(&env, &leaves);
    let metadata_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves.clone(), 0),
    };
    let inner = hash_set_root_inner(&env, &root, valid_until);
    let signed = eth_signed_message_hash_32(&env, &inner);
    let sigs = signature_vec_single(&env, &sk, &signed);
    client.set_root(&root, &valid_until, &metadata, &metadata_proof, &sigs);

    let mut bad_op = op.clone();
    bad_op.chain_id = BytesN::from_array(&env, &[0x01; 32]);
    let op_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves, 1),
    };
    assert!(matches!(
        client.try_execute(&bad_op, &op_proof),
        Err(Ok(McmsError::WrongChainIdOp))
    ));
}

#[test]
fn test_set_root_reverts_valid_until_expired() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| {
        li.timestamp = 10_000;
    });

    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    client.set_config(
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    let self_cid = test_support::addr_to_contract_id(&client.address, &env);
    let valid_until: u32 = 100;
    let metadata = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: false,
    };
    let meta_leaf = hash_root_metadata(&env, &domain_meta(&env), &metadata).unwrap();
    let op = StellarOp {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        nonce: 0,
        to: self_cid.clone(),
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: encode_extend_all_ttls(&env),
    };
    let op_leaf = hash_stellar_op(&env, &crate::constants::domain_op(&env), &op).unwrap();
    let leaves = Vec::from([meta_leaf, op_leaf]);
    let root = merkle_root_native(&env, &leaves);
    let metadata_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves, 0),
    };
    let inner = hash_set_root_inner(&env, &root, valid_until);
    let signed = eth_signed_message_hash_32(&env, &inner);
    let sigs = signature_vec_single(&env, &sk, &signed);

    assert!(matches!(
        client.try_set_root(&root, &valid_until, &metadata, &metadata_proof, &sigs,),
        Err(Ok(McmsError::ValidUntilHasAlreadyPassed))
    ));
}

#[test]
fn test_set_root_reverts_valid_until_exceeds_90_day_cap() {
    let env = Env::default();
    env.mock_all_auths();
    const NOW: u64 = 1_000_000;
    env.ledger().with_mut(|li| {
        li.timestamp = NOW;
    });

    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    client.set_config(
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    let self_cid = test_support::addr_to_contract_id(&client.address, &env);
    let max_valid = NOW.saturating_add(crate::constants::MAX_ROOT_VALIDITY_SECS);
    let valid_until: u32 = (max_valid + 1) as u32;
    let metadata = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: false,
    };
    let meta_leaf = hash_root_metadata(&env, &domain_meta(&env), &metadata).unwrap();
    let op = StellarOp {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        nonce: 0,
        to: self_cid.clone(),
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: encode_extend_all_ttls(&env),
    };
    let op_leaf = hash_stellar_op(&env, &crate::constants::domain_op(&env), &op).unwrap();
    let leaves = Vec::from([meta_leaf, op_leaf]);
    let root = merkle_root_native(&env, &leaves);
    let metadata_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves, 0),
    };
    let inner = hash_set_root_inner(&env, &root, valid_until);
    let signed = eth_signed_message_hash_32(&env, &inner);
    let sigs = signature_vec_single(&env, &sk, &signed);

    assert!(matches!(
        client.try_set_root(&root, &valid_until, &metadata, &metadata_proof, &sigs),
        Err(Ok(McmsError::ValidUntilExceedsMaximum))
    ));
}

/// Group 0 quorum is **2** with **two** registered signers, but only **one** signature is submitted
/// for the `set_root` digest → [`McmsError::InsufficientSigners`]. Without a successful `set_root`,
/// no Merkle root is stored and [`McmsContractClient::execute`] cannot run batched ops (quorum gates the root update, not `execute` itself).
#[test]
fn test_set_root_reverts_when_quorum_not_met_insufficient_signatures() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| {
        li.timestamp = 1_000;
    });

    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    let sk0 = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let sk1 = SigningKey::from_slice(&ANVIL_SK_1).unwrap();
    let a0 = padded_eth_address(&env, &sk0);
    let a1 = padded_eth_address(&env, &sk1);
    let (addr_lo, addr_hi) = if cmp_bytes32(&a0, &a1) < 0 {
        (a0, a1)
    } else {
        (a1, a0)
    };

    client.set_config(
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [addr_lo, addr_hi]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32, 0u32]),
        },
        &two_of_two_group0_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    let self_cid = test_support::addr_to_contract_id(&client.address, &env);
    let valid_until: u32 = 2_000_000;
    let metadata = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: false,
    };
    let meta_leaf = hash_root_metadata(&env, &domain_meta(&env), &metadata).unwrap();
    let op = StellarOp {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        nonce: 0,
        to: self_cid.clone(),
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: encode_extend_all_ttls(&env),
    };
    let op_leaf = hash_stellar_op(&env, &crate::constants::domain_op(&env), &op).unwrap();
    let leaves = Vec::from([meta_leaf, op_leaf]);
    let root = merkle_root_native(&env, &leaves);
    let metadata_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves.clone(), 0),
    };
    let inner = hash_set_root_inner(&env, &root, valid_until);
    let signed = eth_signed_message_hash_32(&env, &inner);
    let sigs = signature_vec_single(&env, &sk0, &signed);

    assert!(matches!(
        client.try_set_root(&root, &valid_until, &metadata, &metadata_proof, &sigs),
        Err(Ok(McmsError::InsufficientSigners))
    ));
}

/// MCMS takes ownership of [`RampRegistryContract`] via `execute(accept_ownership)`, then transfers
/// ownership back to the original owner; the original owner completes the two-step transfer with `accept_ownership`.
#[test]
fn test_mcms_ownership_round_trip_via_ramp_registry() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| {
        li.timestamp = 1_000;
    });

    let alice = Address::generate(&env);
    let mcms_owner = Address::generate(&env);
    let chain = zero_chain_id(&env);

    let mcms_client = register_client(&env);
    mcms_client.initialize(&mcms_owner, &chain);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    mcms_client.set_config(
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    let ramp_id = env.register(RampRegistryContract, ());
    let ramp = RampRegistryClient::new(&env, &ramp_id);
    ramp.initialize(&alice);

    let mcms_addr = mcms_client.address.clone();
    let mcms_cid = test_support::addr_to_contract_id(&mcms_addr, &env);
    let ramp_cid = test_support::addr_to_contract_id(&ramp_id, &env);

    ramp.transfer_ownership(&mcms_addr);

    let valid_until: u32 = 2_000_000;

    let metadata1 = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: mcms_cid.clone(),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: false,
    };
    let meta_leaf1 = hash_root_metadata(&env, &domain_meta(&env), &metadata1).unwrap();
    let op_accept = StellarOp {
        chain_id: chain.clone(),
        multisig: mcms_cid.clone(),
        nonce: 0,
        to: ramp_cid.clone(),
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: encode_accept_ownership_ramp(&env),
    };
    let op_leaf1 = hash_stellar_op(&env, &crate::constants::domain_op(&env), &op_accept).unwrap();
    let leaves1 = Vec::from([meta_leaf1, op_leaf1]);
    let root1 = merkle_root_native(&env, &leaves1);
    let metadata_proof1 = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves1.clone(), 0),
    };
    let inner1 = hash_set_root_inner(&env, &root1, valid_until);
    let signed1 = eth_signed_message_hash_32(&env, &inner1);
    let sigs1 = signature_vec_single(&env, &sk, &signed1);
    mcms_client.set_root(&root1, &valid_until, &metadata1, &metadata_proof1, &sigs1);

    let op_proof1 = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves1, 1),
    };
    mcms_client.execute(&op_accept, &op_proof1);

    assert_eq!(ramp.owner(), Some(mcms_addr.clone()));

    let metadata2 = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: mcms_cid.clone(),
        pre_op_count: 1,
        post_op_count: 2,
        override_previous_root: false,
    };
    let meta_leaf2 = hash_root_metadata(&env, &domain_meta(&env), &metadata2).unwrap();
    let op_transfer_back = StellarOp {
        chain_id: chain.clone(),
        multisig: mcms_cid.clone(),
        nonce: 1,
        to: ramp_cid.clone(),
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: encode_transfer_ownership_ramp(&env, alice.clone()),
    };
    let op_leaf2 =
        hash_stellar_op(&env, &crate::constants::domain_op(&env), &op_transfer_back).unwrap();
    let leaves2 = Vec::from([meta_leaf2, op_leaf2]);
    let root2 = merkle_root_native(&env, &leaves2);
    let metadata_proof2 = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves2.clone(), 0),
    };
    let inner2 = hash_set_root_inner(&env, &root2, valid_until);
    let signed2 = eth_signed_message_hash_32(&env, &inner2);
    let sigs2 = signature_vec_single(&env, &sk, &signed2);
    mcms_client.set_root(&root2, &valid_until, &metadata2, &metadata_proof2, &sigs2);

    let op_proof2 = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves2, 1),
    };
    mcms_client.execute(&op_transfer_back, &op_proof2);

    ramp.accept_ownership();

    assert_eq!(ramp.owner(), Some(alice.clone()));
}

/// MCMS `execute` invokes timelock `schedule_batch` with `caller` = MCMS contract; MCMS must be PROPOSER.
#[test]
fn test_mcms_execute_timelock_schedule_batch_proposer_self_auth() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| {
        li.timestamp = 2_000;
    });

    let admin = Address::generate(&env);
    let executor = Address::generate(&env);
    let canceller = Address::generate(&env);
    let bypasser = Address::generate(&env);
    let chain = zero_chain_id(&env);

    let mcms_client = register_client(&env);
    mcms_client.initialize(&admin, &chain);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    mcms_client.set_config(
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    let mcms_addr = mcms_client.address.clone();
    let tl_client = register_timelock(&env);
    tl_client.initialize(
        &100u64,
        &admin,
        &SorobanVec::from_array(&env, [mcms_addr.clone()]),
        &SorobanVec::from_array(&env, [executor.clone()]),
        &SorobanVec::from_array(&env, [canceller.clone()]),
        &SorobanVec::from_array(&env, [bypasser.clone()]),
    );

    let tl_cid = test_support::addr_to_contract_id(&tl_client.address, &env);
    let mcms_cid = test_support::addr_to_contract_id(&mcms_addr, &env);

    let calls_empty = Calls {
        inner: SorobanVec::new(&env),
    };
    let pred = zero_bytes32(&env);
    let salt_b = salt_byte(&env, 42);
    let schedule_data = encode_schedule_batch(
        &env,
        mcms_addr.clone(),
        calls_empty.clone(),
        pred.clone(),
        salt_b.clone(),
        100u64,
    );

    let valid_until: u32 = 3_000_000;
    let metadata = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: mcms_cid.clone(),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: false,
    };
    let meta_leaf = hash_root_metadata(&env, &domain_meta(&env), &metadata).unwrap();
    let op = StellarOp {
        chain_id: chain.clone(),
        multisig: mcms_cid.clone(),
        nonce: 0,
        to: tl_cid,
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: schedule_data,
    };
    let op_leaf = hash_stellar_op(&env, &crate::constants::domain_op(&env), &op).unwrap();
    let leaves = Vec::from([meta_leaf, op_leaf]);
    let root = merkle_root_native(&env, &leaves);
    let metadata_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves.clone(), 0),
    };
    let inner_h = hash_set_root_inner(&env, &root, valid_until);
    let signed = eth_signed_message_hash_32(&env, &inner_h);
    let sigs = signature_vec_single(&env, &sk, &signed);
    mcms_client.set_root(&root, &valid_until, &metadata, &metadata_proof, &sigs);

    let op_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves, 1),
    };
    mcms_client.execute(&op, &op_proof);

    let id = tl_client.hash_operation_batch(&calls_empty, &pred, &salt_b);
    assert!(tl_client.is_operation(&id));
    assert!(tl_client.is_operation_pending(&id));
    assert_eq!(tl_client.get_timestamp(&id), 2_100u64);
}

// --- min_secs_per_ledger ---

/// Default getter returns `MIN_SECS_PER_LEDGER_DEFAULT` before any setter call.
#[test]
fn test_min_secs_per_ledger_default() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    assert_eq!(
        client.get_min_secs_per_ledger(),
        crate::constants::MIN_SECS_PER_LEDGER_DEFAULT
    );
}

/// Owner can update `min_secs_per_ledger`; getter reflects the new value.
#[test]
fn test_set_min_secs_per_ledger_round_trip() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    client.set_min_secs_per_ledger(&7u64);
    assert_eq!(client.get_min_secs_per_ledger(), 7u64);
}

/// Out-of-range values (0 and `> MIN_SECS_PER_LEDGER_UPPER_BOUND`) are rejected.
#[test]
fn test_set_min_secs_per_ledger_rejects_out_of_range() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    assert!(matches!(
        client.try_set_min_secs_per_ledger(&0u64),
        Err(Ok(McmsError::InvalidMinSecsPerLedger))
    ));
    assert!(matches!(
        client
            .try_set_min_secs_per_ledger(&(crate::constants::MIN_SECS_PER_LEDGER_UPPER_BOUND + 1)),
        Err(Ok(McmsError::InvalidMinSecsPerLedger))
    ));
}

/// `set_min_secs_per_ledger` is owner-gated. Without owner auth the call is rejected.
/// Mirrors the auth-test pattern used by `set_config`.
#[test]
#[should_panic]
fn test_set_min_secs_per_ledger_panics_without_owner_auth() {
    let env = Env::default();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    env.mock_all_auths();
    client.initialize(&owner, &chain);

    // Drop mocked auths so the require_owner check fails on the next call.
    let env2 = client.env.clone();
    env2.set_auths(&[]);
    client.set_min_secs_per_ledger(&7u64);
}

/// When `min_secs_per_ledger` is shrunk such that the dynamic cap drops below the static
/// 90-day cap, `set_root` enforces the smaller dynamic cap.
#[test]
fn test_set_root_dynamic_cap_shrinks_with_lower_min_secs_per_ledger() {
    let env = Env::default();
    env.mock_all_auths();
    const NOW: u64 = 1_000_000;
    env.ledger().with_mut(|li| {
        li.timestamp = NOW;
    });

    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    client.initialize(&owner, &chain);

    // Lower the assumed seconds-per-ledger floor to 1; this collapses the dynamic cap to
    // LEDGER_BUMP * 1 - SAFETY_MARGIN, which is ~66 days < the static 90-day cap.
    client.set_min_secs_per_ledger(&1u64);

    let dynamic_cap = (crate::constants::LEDGER_BUMP as u64)
        .saturating_mul(1)
        .saturating_sub(crate::constants::SEEN_TTL_SAFETY_MARGIN_SECS);
    assert!(dynamic_cap < crate::constants::MAX_ROOT_VALIDITY_SECS);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    client.set_config(
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    let self_cid = test_support::addr_to_contract_id(&client.address, &env);
    // Pick `valid_until` strictly between the dynamic cap and the static cap. With these
    // values the runtime should reject because the **dynamic** cap is the binding one.
    let valid_until: u32 = (NOW + dynamic_cap + 1) as u32;
    let metadata = StellarRootMetadata {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        pre_op_count: 0,
        post_op_count: 1,
        override_previous_root: false,
    };
    let meta_leaf = hash_root_metadata(&env, &domain_meta(&env), &metadata).unwrap();
    let op = StellarOp {
        chain_id: chain.clone(),
        multisig: self_cid.clone(),
        nonce: 0,
        to: self_cid.clone(),
        value: BytesN::from_array(&env, &[0u8; 32]),
        data: encode_extend_all_ttls(&env),
    };
    let op_leaf = hash_stellar_op(&env, &crate::constants::domain_op(&env), &op).unwrap();
    let leaves = Vec::from([meta_leaf, op_leaf]);
    let root = merkle_root_native(&env, &leaves);
    let metadata_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves, 0),
    };
    let inner = hash_set_root_inner(&env, &root, valid_until);
    let signed = eth_signed_message_hash_32(&env, &inner);
    let sigs = signature_vec_single(&env, &sk, &signed);

    assert!(matches!(
        client.try_set_root(&root, &valid_until, &metadata, &metadata_proof, &sigs),
        Err(Ok(McmsError::ValidUntilExceedsMaximum))
    ));
}
