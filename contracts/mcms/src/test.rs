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
    contract, contracterror, contractimpl, symbol_short, Address, Bytes, BytesN, Env, IntoVal,
    Symbol, Val, Vec as SorobanVec,
};

use crate::constants::ENCODING_VERSION;
use crate::crypto::{cmp_bytes32, efficient_hash_pair};
use crate::encoding::{
    eth_signed_message_hash_32, hash_root_metadata, hash_set_root_inner, hash_stellar_op,
};
use crate::error::McmsError;
use crate::types::{
    MerkleProof, Signature, SignatureVec, SignerAddresses, SignerGroups, StellarOp,
    StellarRootMetadata,
};
use crate::{McmsContract, McmsContractClient};
use ccip_ramp_registry::RampRegistryContract;
use common_interfaces::ramp_registry::RampRegistryClient;
use timelock::{Call, Calls, TimelockContract, TimelockContractClient};

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

fn test_label(env: &Env) -> Symbol {
    Symbol::new(env, "PROPOSER")
}

/// Atomic `initialize` with the `signer_a` 1-of-1 config and the test instance label.
fn initialize_default(env: &Env, client: &McmsContractClient, owner: &Address, chain: &BytesN<32>) {
    client.initialize(
        owner,
        chain,
        &SignerAddresses {
            inner: SorobanVec::from_array(env, [signer_a(env)]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(env, [0u32]),
        },
        &one_of_one_quorum(env),
        &all_zero_parents(env),
        &test_label(env),
    );
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

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq)]
#[repr(u32)]
pub enum ExecMockError {
    Rejected = 1,
}

/// Callee that can return a typed contract error or abort before returning.
#[contract]
pub struct ExecFailureMock;

#[contractimpl]
impl ExecFailureMock {
    pub fn set_reject(env: Env, reject: bool) {
        env.storage()
            .instance()
            .set(&symbol_short!("REJECT"), &reject);
    }

    pub fn maybe_fail(env: Env) -> Result<(), ExecMockError> {
        if env
            .storage()
            .instance()
            .get::<_, bool>(&symbol_short!("REJECT"))
            .unwrap_or(false)
        {
            return Err(ExecMockError::Rejected);
        }
        Ok(())
    }

    pub fn abort(_env: Env) {
        panic!("intentional abort");
    }
}

/// Observable target for the nested-execution regression test.
#[contract]
pub struct ExecCounterMock;

#[contractimpl]
impl ExecCounterMock {
    pub fn increment(env: Env) {
        let value = env
            .storage()
            .instance()
            .get::<_, u32>(&symbol_short!("COUNT"))
            .unwrap_or(0);
        env.storage()
            .instance()
            .set(&symbol_short!("COUNT"), &(value + 1));
    }

    pub fn count(env: Env) -> u32 {
        env.storage()
            .instance()
            .get::<_, u32>(&symbol_short!("COUNT"))
            .unwrap_or(0)
    }
}

/// Stores a second operation/proof out of band, then attempts it during the outer operation.
/// The nested error is intentionally ignored so the outer operation can complete.
#[contract]
pub struct ExecReentrantMock;

#[contractimpl]
impl ExecReentrantMock {
    pub fn configure(env: Env, mcms: Address, op: StellarOp, proof: MerkleProof) {
        env.storage().instance().set(&symbol_short!("MCMS"), &mcms);
        env.storage().instance().set(&symbol_short!("OP"), &op);
        env.storage()
            .instance()
            .set(&symbol_short!("PROOF"), &proof);
    }

    pub fn reenter(env: Env) {
        let mcms: Address = env
            .storage()
            .instance()
            .get(&symbol_short!("MCMS"))
            .unwrap();
        let op: StellarOp = env.storage().instance().get(&symbol_short!("OP")).unwrap();
        let proof: MerkleProof = env
            .storage()
            .instance()
            .get(&symbol_short!("PROOF"))
            .unwrap();
        let _ = McmsContractClient::new(&env, &mcms).try_execute(&op, &proof);
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

    initialize_default(&env, &client, &owner, &chain);
    assert_eq!(client.chain_network_id(), chain);
    assert_eq!(client.get_config_version(), 1);
    assert_eq!(client.get_instance_label(), test_label(&env));
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
    initialize_default(&env, &client, &owner, &chain);
    initialize_default(&env, &client, &owner, &chain);
}

#[test]
fn test_set_config_1_of_1_and_get_config() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    initialize_default(&env, &client, &owner, &chain);

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
    assert_eq!(client.get_config_version(), 2);
    assert_eq!(cfg.signers.len(), 1);
    assert_eq!(cfg.signers.get(0).unwrap().addr, s);
    assert_eq!(cfg.signers.get(0).unwrap().index, 0);
    assert_eq!(cfg.signers.get(0).unwrap().group, 0u32);
}

/// `initialize` applies the config atomically, so the only config-less state is
/// pre-initialization: `get_config` fails with `NotInitialized`.
#[test]
fn test_get_config_fails_before_initialize() {
    let env = Env::default();
    env.mock_all_auths();
    let client = register_client(&env);

    // Generated `try_*` type: `Result<Result<Config, ConversionError>, Result<McmsError, InvokeError>>`
    // — contract `Err(_)` is `Err(Ok(McmsError::...))` on the outer `Result`.
    assert!(matches!(
        client.try_get_config(),
        Err(Ok(McmsError::NotInitialized))
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
    initialize_default(&env, &client, &owner, &chain);
    let op = build_operation(
        &env,
        chain.clone(),
        client.address.clone(),
        0,
        client.address.clone(),
        encode_extend_all_ttls(&env),
    );
    let proof = MerkleProof {
        inner: SorobanVec::new(&env),
    };
    assert!(matches!(
        client.try_execute(&op, &proof),
        Err(Ok(McmsError::MissingRootMetadata))
    ));
}

/// `initialize` applies the config atomically, so a config-less `set_root` can only happen
/// pre-initialization and must fail before reaching signature verification.
#[test]
fn test_set_root_fails_before_initialize() {
    let env = Env::default();
    env.mock_all_auths();
    let chain = zero_chain_id(&env);
    let client = register_client(&env);

    let root = BytesN::from_array(&env, &[0u8; 32]);
    let metadata = build_metadata(chain, client.address.clone(), 0, 1, false, 1);
    let metadata_proof = MerkleProof {
        inner: SorobanVec::new(&env),
    };
    let signatures = SignatureVec {
        inner: SorobanVec::new(&env),
    };

    assert!(matches!(
        client.try_set_root(&root, &0u32, &metadata, &metadata_proof, &signatures),
        Err(Ok(McmsError::NotInitialized))
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
    initialize_default(&env, &client, &owner, &chain);

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
    initialize_default(&env, &client, &owner, &chain);

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

fn encode_no_args(env: &Env, function: &str) -> Bytes {
    let mut v: SorobanVec<Val> = SorobanVec::new(env);
    v.push_back(Symbol::new(env, function).into_val(env));
    v.to_xdr(env)
}

fn build_metadata(
    network_id: BytesN<32>,
    multisig: Address,
    pre_op_count: u64,
    post_op_count: u64,
    override_previous_root: bool,
    config_version: u64,
) -> StellarRootMetadata {
    StellarRootMetadata {
        network_id,
        multisig,
        pre_op_count,
        post_op_count,
        override_previous_root,
        config_version,
        encoding_version: ENCODING_VERSION,
    }
}

fn build_operation(
    env: &Env,
    network_id: BytesN<32>,
    multisig: Address,
    nonce: u64,
    target: Address,
    payload: Bytes,
) -> StellarOp {
    let (function, args) =
        common_helpers::soroban_invoke::decode_invoke_payload(env, &payload).unwrap();
    StellarOp {
        network_id,
        multisig,
        nonce,
        target,
        function,
        args_xdr: args.to_xdr(env),
        encoding_version: ENCODING_VERSION,
    }
}

fn setup_single_operation<'a>(
    env: &'a Env,
    target: Address,
    data: Bytes,
) -> (McmsContractClient<'a>, StellarOp, MerkleProof) {
    env.mock_all_auths();
    env.ledger().with_mut(|li| li.timestamp = 1_000);

    let owner = Address::generate(env);
    let chain = zero_chain_id(env);
    let client = register_client(env);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    client.initialize(
        &owner,
        &chain,
        &SignerAddresses {
            inner: SorobanVec::from_array(env, [padded_eth_address(env, &sk)]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(env, [0u32]),
        },
        &one_of_one_quorum(env),
        &all_zero_parents(env),
        &test_label(env),
    );

    let metadata = build_metadata(chain.clone(), client.address.clone(), 0, 1, false, 1);
    let op = build_operation(env, chain, client.address.clone(), 0, target, data);
    let meta_leaf = hash_root_metadata(env, &metadata).unwrap();
    let op_leaf = hash_stellar_op(env, &op).unwrap();
    let leaves = Vec::from([meta_leaf, op_leaf]);
    let root = merkle_root_native(env, &leaves);
    let metadata_proof = MerkleProof {
        inner: compute_proof_for_leaf(env, leaves.clone(), 0),
    };
    let valid_until = 2_000_000u32;
    let signed = eth_signed_message_hash_32(env, &hash_set_root_inner(env, &root, valid_until));
    client.set_root(
        &root,
        &valid_until,
        &metadata,
        &metadata_proof,
        &signature_vec_single(env, &sk, &signed),
    );

    let op_proof = MerkleProof {
        inner: compute_proof_for_leaf(env, leaves, 1),
    };
    (client, op, op_proof)
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
    initialize_default(&env, &client, &owner, &chain);

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
    initialize_default(&env, &client, &owner, &chain);

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
    initialize_default(&env, &client, &owner, &chain);

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

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    let addrs = SignerAddresses {
        inner: SorobanVec::from_array(&env, [signer_addr.clone()]),
    };
    let groups = SignerGroups {
        inner: SorobanVec::from_array(&env, [0u32]),
    };
    client.initialize(
        &owner,
        &chain,
        &addrs,
        &groups,
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &test_label(&env),
    );

    let ping_addr = env.register(ExecPingMock, ());
    let valid_until: u32 = 2_000_000;
    let metadata = build_metadata(chain.clone(), client.address.clone(), 0, 1, false, 1);
    let meta_leaf = hash_root_metadata(&env, &metadata).unwrap();
    let op = build_operation(
        &env,
        chain.clone(),
        client.address.clone(),
        0,
        ping_addr,
        encode_ping(&env),
    );
    let op_leaf = hash_stellar_op(&env, &op).unwrap();
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


#[test]
fn test_execute_reverted_call_does_not_consume_nonce_and_can_retry() {
    let env = Env::default();
    let target = env.register(ExecFailureMock, ());
    let target_client = ExecFailureMockClient::new(&env, &target);
    target_client.set_reject(&true);

    let (client, op, proof) =
        setup_single_operation(&env, target, encode_no_args(&env, "maybe_fail"));

    assert!(matches!(
        client.try_execute(&op, &proof),
        Err(Ok(McmsError::CallReverted))
    ));
    assert_eq!(client.get_op_count(), 0);

    target_client.set_reject(&false);
    client.execute(&op, &proof);
    assert_eq!(client.get_op_count(), 1);
}

#[test]
fn test_execute_aborted_call_is_distinct_and_does_not_consume_nonce() {
    let env = Env::default();
    let target = env.register(ExecFailureMock, ());
    let (client, op, proof) = setup_single_operation(&env, target, encode_no_args(&env, "abort"));

    assert!(matches!(
        client.try_execute(&op, &proof),
        Err(Ok(McmsError::CallAborted))
    ));
    assert_eq!(client.get_op_count(), 0);
}

#[test]
fn test_config_change_invalidates_active_root() {
    let env = Env::default();
    let target = env.register(ExecPingMock, ());
    let (client, op, proof) = setup_single_operation(&env, target, encode_ping(&env));

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    client.set_config(
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [padded_eth_address(&env, &sk)]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &false,
    );

    assert_eq!(client.get_config_version(), 2);
    assert!(matches!(
        client.try_execute(&op, &proof),
        Err(Ok(McmsError::ConfigVersionMismatch))
    ));
    assert_eq!(client.get_op_count(), 0);
}

#[test]
fn test_execute_rejects_unsupported_version_before_proof() {
    let env = Env::default();
    let target = env.register(ExecPingMock, ());
    let (client, mut op, proof) = setup_single_operation(&env, target, encode_ping(&env));
    op.encoding_version = ENCODING_VERSION + 1;

    assert!(matches!(
        client.try_execute(&op, &proof),
        Err(Ok(McmsError::UnsupportedEncodingVersion))
    ));
}

#[test]
fn test_execute_rejects_noncanonical_empty_argument_bytes() {
    let env = Env::default();
    let target = env.register(ExecPingMock, ());
    let (client, mut op, proof) = setup_single_operation(&env, target, encode_ping(&env));
    op.args_xdr = Bytes::new(&env);

    assert!(matches!(
        client.try_execute(&op, &proof),
        Err(Ok(McmsError::InvalidArgsXdr))
    ));
    assert_eq!(client.get_op_count(), 0);
}

#[test]
fn test_execute_rejects_account_target_before_proof() {
    let env = Env::default();
    let target = env.register(ExecPingMock, ());
    let (client, mut op, proof) = setup_single_operation(&env, target, encode_ping(&env));
    // `Address::generate` produces contract addresses; an account address must be explicit.
    op.target = Address::from_str(
        &env,
        "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
    );

    assert!(matches!(
        client.try_execute(&op, &proof),
        Err(Ok(McmsError::InvalidTarget))
    ));
    assert_eq!(client.get_op_count(), 0);
}

#[test]
fn test_execute_persists_nonce_before_reentrant_nested_execute() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| li.timestamp = 1_000);

    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    client.initialize(
        &owner,
        &chain,
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [padded_eth_address(&env, &sk)]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &test_label(&env),
    );

    let counter_addr = env.register(ExecCounterMock, ());
    let counter_client = ExecCounterMockClient::new(&env, &counter_addr);
    let reentrant_addr = env.register(ExecReentrantMock, ());
    let reentrant_client = ExecReentrantMockClient::new(&env, &reentrant_addr);

    let metadata = build_metadata(chain.clone(), client.address.clone(), 0, 1, false, 1);
    let outer_op = build_operation(
        &env,
        chain.clone(),
        client.address.clone(),
        0,
        reentrant_addr,
        encode_no_args(&env, "reenter"),
    );
    let nested_op = build_operation(
        &env,
        chain,
        client.address.clone(),
        0,
        counter_addr,
        encode_no_args(&env, "increment"),
    );

    let leaves = Vec::from([
        hash_root_metadata(&env, &metadata).unwrap(),
        hash_stellar_op(&env, &outer_op).unwrap(),
        hash_stellar_op(&env, &nested_op).unwrap(),
        BytesN::from_array(&env, &[0x55; 32]),
    ]);
    let root = merkle_root_native(&env, &leaves);
    let valid_until = 2_000_000u32;
    let signed = eth_signed_message_hash_32(&env, &hash_set_root_inner(&env, &root, valid_until));
    client.set_root(
        &root,
        &valid_until,
        &metadata,
        &MerkleProof {
            inner: compute_proof_for_leaf(&env, leaves.clone(), 0),
        },
        &signature_vec_single(&env, &sk, &signed),
    );

    reentrant_client.configure(
        &client.address,
        &nested_op,
        &MerkleProof {
            inner: compute_proof_for_leaf(&env, leaves.clone(), 2),
        },
    );
    client.execute(
        &outer_op,
        &MerkleProof {
            inner: compute_proof_for_leaf(&env, leaves, 1),
        },
    );

    assert_eq!(client.get_op_count(), 1);
    assert_eq!(counter_client.count(), 0);
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

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    client.initialize(
        &owner,
        &chain,
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &test_label(&env),
    );

    let valid_until: u32 = 2_000_000;
    let metadata = build_metadata(chain.clone(), client.address.clone(), 0, 1, false, 1);
    let meta_leaf = hash_root_metadata(&env, &metadata).unwrap();
    let op = build_operation(
        &env,
        chain.clone(),
        client.address.clone(),
        0,
        client.address.clone(),
        encode_extend_all_ttls(&env),
    );
    let op_leaf = hash_stellar_op(&env, &op).unwrap();
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

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    client.initialize(
        &owner,
        &chain,
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &test_label(&env),
    );

    let valid_until: u32 = 2_000_000;
    let metadata = build_metadata(chain.clone(), client.address.clone(), 0, 1, false, 1);
    let meta_leaf = hash_root_metadata(&env, &metadata).unwrap();
    let op = build_operation(
        &env,
        chain.clone(),
        client.address.clone(),
        0,
        client.address.clone(),
        encode_extend_all_ttls(&env),
    );
    let op_leaf = hash_stellar_op(&env, &op).unwrap();
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
    bad_op.network_id = BytesN::from_array(&env, &[0x01; 32]);
    let op_proof = MerkleProof {
        inner: compute_proof_for_leaf(&env, leaves, 1),
    };
    assert!(matches!(
        client.try_execute(&bad_op, &op_proof),
        Err(Ok(McmsError::WrongChainIdOp))
    ));
}

#[test]
fn test_execute_reverts_wrong_multisig_op() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| {
        li.timestamp = 1_000;
    });

    let owner = Address::generate(&env);
    let chain = zero_chain_id(&env);
    let client = register_client(&env);
    let other_mcms = register_client(&env);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    client.initialize(
        &owner,
        &chain,
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &test_label(&env),
    );

    let valid_until: u32 = 2_000_000;
    let metadata = build_metadata(chain.clone(), client.address.clone(), 0, 1, false, 1);
    let meta_leaf = hash_root_metadata(&env, &metadata).unwrap();
    let mut op = build_operation(
        &env,
        chain.clone(),
        client.address.clone(),
        0,
        client.address.clone(),
        encode_extend_all_ttls(&env),
    );
    op.multisig = other_mcms.address.clone();
    let op_leaf = hash_stellar_op(&env, &op).unwrap();
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
    assert!(matches!(
        client.try_execute(&op, &op_proof),
        Err(Ok(McmsError::WrongMultiSigOp))
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

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    client.initialize(
        &owner,
        &chain,
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &test_label(&env),
    );

    let valid_until: u32 = 100;
    let metadata = build_metadata(chain.clone(), client.address.clone(), 0, 1, false, 1);
    let meta_leaf = hash_root_metadata(&env, &metadata).unwrap();
    let op = build_operation(
        &env,
        chain.clone(),
        client.address.clone(),
        0,
        client.address.clone(),
        encode_extend_all_ttls(&env),
    );
    let op_leaf = hash_stellar_op(&env, &op).unwrap();
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

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    client.initialize(
        &owner,
        &chain,
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &test_label(&env),
    );

    let max_valid = NOW.saturating_add(crate::constants::MAX_ROOT_VALIDITY_SECS);
    let valid_until: u32 = (max_valid + 1) as u32;
    let metadata = build_metadata(chain.clone(), client.address.clone(), 0, 1, false, 1);
    let meta_leaf = hash_root_metadata(&env, &metadata).unwrap();
    let op = build_operation(
        &env,
        chain.clone(),
        client.address.clone(),
        0,
        client.address.clone(),
        encode_extend_all_ttls(&env),
    );
    let op_leaf = hash_stellar_op(&env, &op).unwrap();
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

    let sk0 = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let sk1 = SigningKey::from_slice(&ANVIL_SK_1).unwrap();
    let a0 = padded_eth_address(&env, &sk0);
    let a1 = padded_eth_address(&env, &sk1);
    let (addr_lo, addr_hi) = if cmp_bytes32(&a0, &a1) < 0 {
        (a0, a1)
    } else {
        (a1, a0)
    };

    client.initialize(
        &owner,
        &chain,
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [addr_lo, addr_hi]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32, 0u32]),
        },
        &two_of_two_group0_quorum(&env),
        &all_zero_parents(&env),
        &test_label(&env),
    );

    let valid_until: u32 = 2_000_000;
    let metadata = build_metadata(chain.clone(), client.address.clone(), 0, 1, false, 1);
    let meta_leaf = hash_root_metadata(&env, &metadata).unwrap();
    let op = build_operation(
        &env,
        chain.clone(),
        client.address.clone(),
        0,
        client.address.clone(),
        encode_extend_all_ttls(&env),
    );
    let op_leaf = hash_stellar_op(&env, &op).unwrap();
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

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    mcms_client.initialize(
        &mcms_owner,
        &chain,
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &test_label(&env),
    );

    let ramp_id = env.register(RampRegistryContract, ());
    let ramp = RampRegistryClient::new(&env, &ramp_id);
    ramp.initialize(&alice);

    let mcms_addr = mcms_client.address.clone();
    ramp.transfer_ownership(&mcms_addr);

    let valid_until: u32 = 2_000_000;

    let metadata1 = build_metadata(chain.clone(), mcms_addr.clone(), 0, 1, false, 1);
    let meta_leaf1 = hash_root_metadata(&env, &metadata1).unwrap();
    let op_accept = build_operation(
        &env,
        chain.clone(),
        mcms_addr.clone(),
        0,
        ramp_id.clone(),
        encode_accept_ownership_ramp(&env),
    );
    let op_leaf1 = hash_stellar_op(&env, &op_accept).unwrap();
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

    let metadata2 = build_metadata(chain.clone(), mcms_addr.clone(), 1, 2, false, 1);
    let meta_leaf2 = hash_root_metadata(&env, &metadata2).unwrap();
    let op_transfer_back = build_operation(
        &env,
        chain.clone(),
        mcms_addr.clone(),
        1,
        ramp_id.clone(),
        encode_transfer_ownership_ramp(&env, alice.clone()),
    );
    let op_leaf2 = hash_stellar_op(&env, &op_transfer_back).unwrap();
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

    let owner = Address::generate(&env);
    let canceller = Address::generate(&env);
    let bypasser = Address::generate(&env);
    let chain = zero_chain_id(&env);

    let mcms_client = register_client(&env);

    let sk = SigningKey::from_slice(&ANVIL_SK_0).unwrap();
    let signer_addr = padded_eth_address(&env, &sk);
    mcms_client.initialize(
        &owner,
        &chain,
        &SignerAddresses {
            inner: SorobanVec::from_array(&env, [signer_addr]),
        },
        &SignerGroups {
            inner: SorobanVec::from_array(&env, [0u32]),
        },
        &one_of_one_quorum(&env),
        &all_zero_parents(&env),
        &test_label(&env),
    );

    let mcms_addr = mcms_client.address.clone();
    let tl_client = register_timelock(&env);
    tl_client.initialize(
        &100u64,
        &SorobanVec::from_array(&env, [mcms_addr.clone()]),
        &SorobanVec::from_array(&env, [canceller]),
        &SorobanVec::from_array(&env, [bypasser]),
    );

    let ping_addr = env.register(ExecPingMock, ());
    let calls = Calls {
        inner: SorobanVec::from_array(
            &env,
            [Call {
                target: ping_addr,
                function: Symbol::new(&env, "ping"),
                args_xdr: SorobanVec::<Val>::new(&env).to_xdr(&env),
            }],
        ),
    };
    let pred = zero_bytes32(&env);
    let salt_b = salt_byte(&env, 42);
    let schedule_data = encode_schedule_batch(
        &env,
        mcms_addr.clone(),
        calls.clone(),
        pred.clone(),
        salt_b.clone(),
        100u64,
    );

    let valid_until: u32 = 3_000_000;
    let metadata = build_metadata(chain.clone(), mcms_addr.clone(), 0, 1, false, 1);
    let meta_leaf = hash_root_metadata(&env, &metadata).unwrap();
    let op = build_operation(
        &env,
        chain.clone(),
        mcms_addr.clone(),
        0,
        tl_client.address.clone(),
        schedule_data,
    );
    let op_leaf = hash_stellar_op(&env, &op).unwrap();
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

    let id = tl_client.hash_operation_batch(&calls, &pred, &salt_b);
    assert!(tl_client.is_operation(&id));
    assert!(tl_client.is_operation_pending(&id));
    assert_eq!(tl_client.get_timestamp(&id), 2_100u64);
}
