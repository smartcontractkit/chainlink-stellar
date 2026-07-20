#![cfg(test)]

extern crate alloc;

use super::*;
use alloc::vec::Vec as HostVec;
use k256::ecdsa::{signature::hazmat::PrehashSigner, SigningKey};
use sha3::{Digest, Keccak256};
use soroban_sdk::{
    testutils::Address as _, token, vec, Address, Bytes, BytesN, Env, Vec as SorobanVec,
};

use crate::types::{DynamicConfig, RemoteChainConfig, SignatureQuorumConfig};
use crate::{
    CommitteeVerifierContract, CommitteeVerifierContractClient, DEFAULT_VERIFIER_VERSION_TAG,
};
use rmn_proxy::{RmnProxyContract, RmnProxyContractClient};
use rmn_remote::{RmnRemoteContract, RmnRemoteContractClient};

fn make_signing_key(seed: u8) -> SigningKey {
    let mut bytes = [0u8; 32];
    bytes[0] = seed;
    SigningKey::from_slice(&bytes).expect("valid secp256k1 secret key")
}

/// Derive the left-zero-padded 32-byte Ethereum address from a secp256k1 signing key.
fn eth_address_padded(sk: &SigningKey) -> [u8; 32] {
    let vk = sk.verifying_key();
    let uncompressed = vk.to_encoded_point(false);
    let hash = Keccak256::digest(&uncompressed.as_bytes()[1..]);
    let mut padded = [0u8; 32];
    padded[12..].copy_from_slice(&hash[12..]);
    padded
}

/// Deterministic secp256k1 keys from seeds, sorted by Ethereum address (ascending).
fn sorted_signers_from_seeds(seeds: &[u8]) -> HostVec<(SigningKey, [u8; 32])> {
    let mut v: HostVec<(SigningKey, [u8; 32])> = seeds
        .iter()
        .copied()
        .map(|s| {
            let sk = make_signing_key(s);
            let addr = eth_address_padded(&sk);
            (sk, addr)
        })
        .collect();
    v.sort_by(|a, b| a.1.cmp(&b.1));
    v
}

fn signers_to_soroban_vec(
    env: &Env,
    pairs: &HostVec<(SigningKey, [u8; 32])>,
) -> SorobanVec<BytesN<32>> {
    let mut out = SorobanVec::new(env);
    for (_, addr) in pairs {
        out.push_back(BytesN::from_array(env, addr));
    }
    out
}

/// `keccak256(version_tag || message_hash)` — must match `verify_message` signed payload hashing.
fn keccak_signed_hash(env: &Env, message_hash: &BytesN<32>) -> BytesN<32> {
    keccak_signed_hash_with_tag(env, &DEFAULT_VERIFIER_VERSION_TAG, message_hash)
}

fn keccak_signed_hash_with_tag(env: &Env, tag: &[u8; 4], message_hash: &BytesN<32>) -> BytesN<32> {
    let mut signed_payload = Bytes::new(env);
    signed_payload.append(&Bytes::from_array(env, tag));
    signed_payload.append(&Bytes::from_array(env, &message_hash.to_array()));
    env.crypto().keccak256(&signed_payload).into()
}

fn build_verifier_results(env: &Env, sig_payload: &[u8]) -> Bytes {
    build_verifier_results_with_tag(env, &DEFAULT_VERIFIER_VERSION_TAG, sig_payload)
}

fn build_verifier_results_with_tag(env: &Env, tag: &[u8; 4], sig_payload: &[u8]) -> Bytes {
    let len = sig_payload.len();
    assert!(len <= u16::MAX as usize);
    let b0 = ((len >> 8) & 0xff) as u8;
    let b1 = (len & 0xff) as u8;
    let mut raw: HostVec<u8> = HostVec::with_capacity(6 + len);
    raw.extend_from_slice(tag);
    raw.push(b0);
    raw.push(b1);
    raw.extend_from_slice(sig_payload);
    Bytes::from_slice(env, &raw)
}

/// Produce an EIP-2098 compact ECDSA signature (64 bytes) for a prehashed message.
fn sign_compact(sk: &SigningKey, prehash: &[u8; 32]) -> [u8; 64] {
    let (sig, recid) = sk.sign_prehash(prehash).expect("signing must succeed");
    let sig_bytes = sig.to_bytes();
    let mut compact = [0u8; 64];
    compact[..32].copy_from_slice(&sig_bytes[..32]); // r
    compact[32..].copy_from_slice(&sig_bytes[32..]); // s
                                                     // Pack recovery ID into bit 255 of yParityAndS
    compact[32] |= (recid.to_byte() & 1) << 7;
    compact
}

/// Build EIP-2098 compact ECDSA signature payload, ordered by the caller.
fn signature_payload_valid(
    pairs_in_wire_order: &[(SigningKey, [u8; 32])],
    signed_hash: &[u8; 32],
) -> HostVec<u8> {
    let mut out = HostVec::with_capacity(pairs_in_wire_order.len() * 64);
    for (sk, _addr) in pairs_in_wire_order {
        let compact = sign_compact(sk, signed_hash);
        out.extend_from_slice(&compact);
    }
    out
}

fn default_dynamic_config(env: &Env) -> DynamicConfig {
    DynamicConfig {
        fee_aggregator: Some(Address::generate(env)),
        allowlist_admin: None,
    }
}

fn default_remote_chain_config(env: &Env, remote_chain_selector: u64) -> RemoteChainConfig {
    RemoteChainConfig {
        remote_chain_selector,
        router: Some(Address::generate(env)),
        allowlist_enabled: false,
        fee_usd_cents: 10,
        gas_for_verification: 100_000,
        payload_size_bytes: 256,
    }
}

fn setup_with_version_tag(
    version_tag: &[u8; 4],
) -> (
    Env,
    CommitteeVerifierContractClient<'static>,
    Address,
    Address,
    SorobanVec<Bytes>,
) {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(CommitteeVerifierContract, ());
    let client = CommitteeVerifierContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let storage_locations = vec![&env];

    // Initialize RMN Remote and Proxy (required for require_not_cursed / is_cursed)
    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy = env.register(RmnProxyContract, ());
    let rmn_proxy_client = RmnProxyContractClient::new(&env, &rmn_proxy);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let dynamic_config = default_dynamic_config(&env);
    let tag = BytesN::from_array(&env, version_tag);
    client.initialize(
        &owner,
        &dynamic_config,
        &storage_locations,
        &rmn_proxy,
        &tag,
    );

    (env, client, owner, rmn_proxy, storage_locations)
}

fn setup() -> (
    Env,
    CommitteeVerifierContractClient<'static>,
    Address,
    Address,
    SorobanVec<Bytes>,
) {
    setup_with_version_tag(&DEFAULT_VERIFIER_VERSION_TAG)
}

// ============================================================
// Initialization Tests
// ============================================================

#[test]
fn test_initialize() {
    let (_env, client, owner, _rmn_proxy, _storage_locations) = setup();
    assert_eq!(client.owner(), Some(owner));
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")] // AlreadyInitialized
fn test_double_initialize_fails() {
    let (env, client, _owner, rmn_proxy, storage_locations) = setup();
    let owner2 = Address::generate(&env);
    let dynamic_config = default_dynamic_config(&env);
    let tag = BytesN::from_array(&env, &DEFAULT_VERIFIER_VERSION_TAG);

    client.initialize(
        &owner2,
        &dynamic_config,
        &storage_locations,
        &rmn_proxy,
        &tag,
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #13)")] // InvalidVersionTag
fn test_initialize_rejects_zero_version_tag() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(CommitteeVerifierContract, ());
    let client = CommitteeVerifierContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let storage_locations = vec![&env];

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy = env.register(RmnProxyContract, ());
    let rmn_proxy_client = RmnProxyContractClient::new(&env, &rmn_proxy);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let dynamic_config = default_dynamic_config(&env);
    let zero = BytesN::from_array(&env, &[0u8; 4]);
    client.initialize(
        &owner,
        &dynamic_config,
        &storage_locations,
        &rmn_proxy,
        &zero,
    );
}

// ============================================================
// Version Tag Tests
// ============================================================

#[test]
fn test_version_tag() {
    let (env, client, ..) = setup();

    let expected = BytesN::from_array(&env, &DEFAULT_VERIFIER_VERSION_TAG);
    assert_eq!(client.version_tag(), expected);
}

// ============================================================
// Dynamic Config Tests
// ============================================================

#[test]
fn test_get_and_set_dynamic_config() {
    let (env, client, _owner, ..) = setup();

    let new_fee_aggregator = Address::generate(&env);
    let new_config = DynamicConfig {
        fee_aggregator: Some(new_fee_aggregator.clone()),
        allowlist_admin: Some(Address::generate(&env)),
    };

    client.set_dynamic_config(&new_config);

    let stored = client.get_dynamic_config();
    assert_eq!(stored.fee_aggregator, Some(new_fee_aggregator));
}

// ============================================================
// Remote Chain Config Tests
// ============================================================

#[test]
fn test_apply_remote_chain_config_and_get_fee() {
    let (env, client, _owner, ..) = setup();

    let dest_chain: u64 = 12345;
    let remote_config = default_remote_chain_config(&env, dest_chain);

    client.apply_remote_chain_cfg_updates(&vec![&env, remote_config.clone()]);

    let retrieved = client.get_remote_chain_config(&dest_chain);
    assert_eq!(retrieved.remote_chain_selector, dest_chain);
    assert_eq!(retrieved.fee_usd_cents, 10);
    assert_eq!(retrieved.gas_for_verification, 100_000);

    let FeeResponse {
        fee,
        dest_gas_limit,
        dest_bytes_overhead,
    } = client.get_fee(&dest_chain, &Bytes::new(&env), &Bytes::new(&env), &0u32);
    assert_eq!(fee, 10);
    assert_eq!(dest_gas_limit, 100_000);
    assert_eq!(dest_bytes_overhead, 256);
}

#[test]
#[should_panic(expected = "Error(Contract, #48)")] // RemoteChainNotSupported
fn test_get_remote_chain_config_fails_when_not_configured() {
    let (_env, client, ..) = setup();

    client.get_remote_chain_config(&99999);
}

#[test]
#[should_panic(expected = "Error(Contract, #48)")] // RemoteChainNotSupported
fn test_get_fee_fails_when_chain_not_configured() {
    let (env, client, ..) = setup();

    client.get_fee(&99999, &Bytes::new(&env), &Bytes::new(&env), &0u32);
}

// ============================================================
// Storage Locations Tests
// ============================================================

#[test]
fn test_update_storage_locations() {
    let (env, client, _owner, ..) = setup();

    let new_locations = vec![
        &env,
        Bytes::from_slice(&env, b"location1"),
        Bytes::from_slice(&env, b"location2"),
    ];

    client.update_storage_locations(&new_locations);

    let stored = client.get_storage_locations();
    assert_eq!(stored.len(), 2);
}

#[test]
fn test_storage_locations_admin_two_step_transfer() {
    let (env, client, owner, ..) = setup();
    let new_admin = Address::generate(&env);

    assert_eq!(client.get_storage_locations_admin(), owner);

    client.transfer_storage_locations_admin(&new_admin);
    assert_eq!(
        client.get_pending_storage_loc_admin(),
        Some(new_admin.clone())
    );

    client.accept_storage_locations_admin();
    assert_eq!(client.get_storage_locations_admin(), new_admin);
    assert_eq!(client.get_pending_storage_loc_admin(), None);
}

// ============================================================
// Ownership Tests
// ============================================================

#[test]
#[ignore]
fn test_transfer_ownership() {
    let (env, client, owner, ..) = setup();

    let new_owner = Address::generate(&env);
    let _ = <CommitteeVerifierContract as common_authorization::Ownable>::transfer_ownership(
        &env, &new_owner,
    );

    env.as_contract(&client.address, || {
        assert_eq!(CommitteeVerifierContract::owner(&env).unwrap(), owner); // Pending, not yet accepted
        let _ =
            <CommitteeVerifierContract as common_authorization::Ownable>::accept_ownership(&env);
        assert_eq!(CommitteeVerifierContract::owner(&env).unwrap(), new_owner);
    });
}

// ============================================================
// Forward to Verifier Tests
// ============================================================

#[test]
fn test_forward_to_verifier_passes_when_allowlist_not_enabled_for_chain() {
    let (env, client, _owner, ..) = setup();

    let dest_chain: u64 = 12345;
    let sender = Address::generate(&env);
    let message_id = BytesN::from_array(&env, &[0u8; 32]);
    let fee_token = Address::generate(&env);
    let verifier_args = Bytes::new(&env);

    client.forward_to_verifier(
        &dest_chain,
        &sender,
        &message_id,
        &fee_token,
        &0,
        &verifier_args,
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #6)")] // CallerNotAuthorized
fn test_forward_to_verifier_fails_when_sender_not_in_allowlist() {
    let (env, client, _owner, ..) = setup();

    let dest_chain: u64 = 12345;
    let sender = Address::generate(&env);
    let message_id = BytesN::from_array(&env, &[0u8; 32]);
    let fee_token = Address::generate(&env);
    let verifier_args = Bytes::new(&env);

    client.apply_allowlist_updates(&vec![
        &env,
        AllowListUpdate {
            dest_chain_selector: dest_chain,
            allowlist_enabled: true,
            added_allowlisted_senders: vec![&env],
            removed_allowlisted_senders: vec![&env],
        },
    ]);

    client.forward_to_verifier(
        &dest_chain,
        &sender,
        &message_id,
        &fee_token,
        &0,
        &verifier_args,
    );
}

// ============================================================
// Verify Message Tests
// ============================================================

#[test]
#[should_panic(expected = "Error(Contract, #20)")] // InvalidVerifierResults
fn test_verify_message_fails_when_verifier_results_too_short() {
    let (env, client, ..) = setup();

    let source_chain: u64 = 1;
    let message_hash = BytesN::from_array(&env, &[0u8; 32]);
    let short_results = Bytes::from_slice(&env, &[0xe9, 0xa0]); // Only 2 bytes, need at least 6

    client.verify_message(&source_chain, &message_hash, &short_results);
}

/// Legacy CommitteeVerifier 1.7.x tag — used to test init + blob tag mismatch vs default 2.0.
const VERSION_TAG_LEGACY_V1_7: [u8; 4] = [0x49, 0xff, 0x34, 0xed];

#[test]
fn test_verify_message_accepts_alternate_initialized_version_tag() {
    let (env, client, _owner, ..) = setup_with_version_tag(&VERSION_TAG_LEGACY_V1_7);

    let source_chain: u64 = 400;
    let pairs = sorted_signers_from_seeds(&[29, 31, 37]);
    let signers = signers_to_soroban_vec(&env, &pairs);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: source_chain,
        threshold: 2,
        signers,
    };
    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);

    let message_hash = BytesN::from_array(&env, &[0xabu8; 32]);
    let signed_hash = keccak_signed_hash_with_tag(&env, &VERSION_TAG_LEGACY_V1_7, &message_hash);
    let signed_bytes: [u8; 32] = signed_hash.to_array();

    let wire_subset = [pairs[0].clone(), pairs[1].clone()];
    let sig_payload = signature_payload_valid(&wire_subset, &signed_bytes);
    let verifier_results =
        build_verifier_results_with_tag(&env, &VERSION_TAG_LEGACY_V1_7, &sig_payload);

    client.verify_message(&source_chain, &message_hash, &verifier_results);
}

#[test]
#[should_panic(expected = "Error(Contract, #19)")] // SourceNotConfigured
fn test_verify_message_fails_when_source_chain_not_configured() {
    let (env, client, ..) = setup();

    let source_chain: u64 = 99999; // Not configured
    let message_hash = BytesN::from_array(&env, &[0u8; 32]);
    // Correct version + sig len (0, 0) + no signatures - will fail at validate_signatures
    let verifier_results = Bytes::from_slice(
        &env,
        &[
            DEFAULT_VERIFIER_VERSION_TAG[0],
            DEFAULT_VERIFIER_VERSION_TAG[1],
            DEFAULT_VERIFIER_VERSION_TAG[2],
            DEFAULT_VERIFIER_VERSION_TAG[3],
            0x00,
            0x00,
        ],
    );

    client.verify_message(&source_chain, &message_hash, &verifier_results);
}

// ============================================================
// SignatureQuorum — config management
// ============================================================

#[test]
fn test_apply_signature_configs_and_get() {
    let (env, client, _owner, ..) = setup();

    let source_chain: u64 = 100;
    let pairs = sorted_signers_from_seeds(&[3, 7, 11]);
    let signers = signers_to_soroban_vec(&env, &pairs);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: source_chain,
        threshold: 2,
        signers: signers.clone(),
    };

    client.apply_signature_configs(&vec![&env], &vec![&env, cfg.clone()]);

    let got = client.get_signature_config(&source_chain);
    assert_eq!(got.source_chain_selector, source_chain);
    assert_eq!(got.threshold, 2);
    assert_eq!(got.signers.len(), 3);
    for i in 0..3 {
        assert_eq!(got.signers.get(i).unwrap(), signers.get(i).unwrap());
    }
}

#[test]
#[should_panic(expected = "Error(Contract, #19)")] // SourceSignersNotConfigured
fn test_apply_signature_configs_remove() {
    let (env, client, _owner, ..) = setup();

    let source_chain: u64 = 101;
    let pairs = sorted_signers_from_seeds(&[5, 9, 13]);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: source_chain,
        threshold: 2,
        signers: signers_to_soroban_vec(&env, &pairs),
    };

    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);
    client.apply_signature_configs(&vec![&env, source_chain], &vec![&env]);

    client.get_signature_config(&source_chain);
}

#[test]
#[should_panic(expected = "Error(Contract, #17)")] // InvalidSignatureThreshold
fn test_apply_signature_configs_rejects_zero_threshold() {
    let (env, client, _owner, ..) = setup();

    let pairs = sorted_signers_from_seeds(&[17, 19]);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: 300,
        threshold: 0,
        signers: signers_to_soroban_vec(&env, &pairs),
    };

    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);
}

#[test]
#[should_panic(expected = "Error(Contract, #17)")] // InvalidSignatureThreshold
fn test_apply_signature_configs_rejects_threshold_exceeding_signers() {
    let (env, client, _owner, ..) = setup();

    let pairs = sorted_signers_from_seeds(&[21, 23]);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: 301,
        threshold: 3,
        signers: signers_to_soroban_vec(&env, &pairs),
    };

    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);
}

#[test]
#[should_panic(expected = "Error(Contract, #66)")] // DuplicateOnchainPublicKey
fn test_apply_signature_configs_rejects_duplicate_signers() {
    let (env, client, _owner, ..) = setup();

    let pk = BytesN::from_array(&env, &[7u8; 32]);
    let mut signers = SorobanVec::new(&env);
    signers.push_back(pk.clone());
    signers.push_back(pk);

    let cfg = SignatureQuorumConfig {
        source_chain_selector: 302,
        threshold: 1,
        signers,
    };

    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);
}

#[test]
#[should_panic(expected = "Error(Contract, #67)")] // InvalidSignerOrder
fn test_apply_signature_configs_rejects_unordered_signers() {
    let (env, client, _owner, ..) = setup();

    let hi = BytesN::from_array(&env, &[0xFFu8; 32]);
    let lo = BytesN::from_array(&env, &[0x00u8; 32]);
    let mut signers = SorobanVec::new(&env);
    signers.push_back(hi);
    signers.push_back(lo);

    let cfg = SignatureQuorumConfig {
        source_chain_selector: 303,
        threshold: 1,
        signers,
    };

    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);
}

// ============================================================
// SignatureQuorum — verify_message (Ed25519 quorum)
// ============================================================

#[test]
fn test_verify_message_with_valid_signatures() {
    let (env, client, _owner, ..) = setup();

    let source_chain: u64 = 400;
    let pairs = sorted_signers_from_seeds(&[29, 31, 37]);
    let signers = signers_to_soroban_vec(&env, &pairs);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: source_chain,
        threshold: 2,
        signers,
    };
    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);

    let message_hash = BytesN::from_array(&env, &[0xabu8; 32]);
    let signed_hash = keccak_signed_hash(&env, &message_hash);
    let signed_bytes: [u8; 32] = signed_hash.to_array();

    let wire_subset = [pairs[0].clone(), pairs[1].clone()];
    let sig_payload = signature_payload_valid(&wire_subset, &signed_bytes);
    let verifier_results = build_verifier_results(&env, &sig_payload);

    client.verify_message(&source_chain, &message_hash, &verifier_results);
}

#[test]
#[should_panic(expected = "Error(Contract, #71)")] // ThresholdNotMet
fn test_verify_message_fails_below_threshold() {
    let (env, client, _owner, ..) = setup();

    let source_chain: u64 = 401;
    let pairs = sorted_signers_from_seeds(&[41, 43, 47]);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: source_chain,
        threshold: 2,
        signers: signers_to_soroban_vec(&env, &pairs),
    };
    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);

    let message_hash = BytesN::from_array(&env, &[0xcd_u8; 32]);
    let signed_hash = keccak_signed_hash(&env, &message_hash);
    let signed_bytes: [u8; 32] = signed_hash.to_array();

    let wire_subset = [pairs[0].clone()];
    let sig_payload = signature_payload_valid(&wire_subset, &signed_bytes);
    let verifier_results = build_verifier_results(&env, &sig_payload);

    client.verify_message(&source_chain, &message_hash, &verifier_results);
}

#[test]
#[should_panic(expected = "Error(Contract, #70)")] // OutOfOrderSignatures
fn test_verify_message_fails_with_out_of_order_signatures() {
    let (env, client, _owner, ..) = setup();

    let source_chain: u64 = 402;
    let pairs = sorted_signers_from_seeds(&[53, 59, 61]);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: source_chain,
        threshold: 2,
        signers: signers_to_soroban_vec(&env, &pairs),
    };
    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);

    let message_hash = BytesN::from_array(&env, &[0x11u8; 32]);
    let signed_hash = keccak_signed_hash(&env, &message_hash);
    let signed_bytes: [u8; 32] = signed_hash.to_array();

    // Wire order: larger pubkey first (descending) — violates strictly ascending requirement.
    let wire_desc = [pairs[2].clone(), pairs[0].clone()];
    let sig_payload = signature_payload_valid(&wire_desc, &signed_bytes);
    let verifier_results = build_verifier_results(&env, &sig_payload);

    client.verify_message(&source_chain, &message_hash, &verifier_results);
}

#[test]
#[should_panic(expected = "Error(Contract, #70)")] // OutOfOrderSignatures
fn test_verify_message_fails_with_duplicate_signatures() {
    let (env, client, _owner, ..) = setup();

    let source_chain: u64 = 403;
    let pairs = sorted_signers_from_seeds(&[67, 71, 73]);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: source_chain,
        threshold: 2,
        signers: signers_to_soroban_vec(&env, &pairs),
    };
    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);

    let message_hash = BytesN::from_array(&env, &[0x22u8; 32]);
    let signed_hash = keccak_signed_hash(&env, &message_hash);
    let signed_bytes: [u8; 32] = signed_hash.to_array();

    let p0 = pairs[0].clone();
    let wire_dup = [p0.clone(), p0];
    let sig_payload = signature_payload_valid(&wire_dup, &signed_bytes);
    let verifier_results = build_verifier_results(&env, &sig_payload);

    client.verify_message(&source_chain, &message_hash, &verifier_results);
}

#[test]
#[should_panic(expected = "Error(Contract, #72)")] // UnexpectedSigner
fn test_verify_message_fails_with_unknown_signer() {
    let (env, client, _owner, ..) = setup();

    let source_chain: u64 = 404;
    let pairs = sorted_signers_from_seeds(&[79, 83, 89]);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: source_chain,
        threshold: 1,
        signers: signers_to_soroban_vec(&env, &pairs),
    };
    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);

    let outsider = sorted_signers_from_seeds(&[97]);
    let message_hash = BytesN::from_array(&env, &[0x33u8; 32]);
    let signed_hash = keccak_signed_hash(&env, &message_hash);
    let signed_bytes: [u8; 32] = signed_hash.to_array();

    let wire = [outsider[0].clone()];
    let sig_payload = signature_payload_valid(&wire, &signed_bytes);
    let verifier_results = build_verifier_results(&env, &sig_payload);

    client.verify_message(&source_chain, &message_hash, &verifier_results);
}

#[test]
#[should_panic]
fn test_verify_message_fails_with_invalid_signature() {
    let (env, client, _owner, ..) = setup();

    let source_chain: u64 = 405;
    let pairs = sorted_signers_from_seeds(&[101, 103, 107]);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: source_chain,
        threshold: 1,
        signers: signers_to_soroban_vec(&env, &pairs),
    };
    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);

    let message_hash = BytesN::from_array(&env, &[0x44u8; 32]);
    // Bogus 64-byte compact signature — will recover a wrong/invalid address
    let mut sig_payload = HostVec::with_capacity(64);
    sig_payload.extend_from_slice(&[0xEEu8; 64]);

    let verifier_results = build_verifier_results(&env, &sig_payload);
    client.verify_message(&source_chain, &message_hash, &verifier_results);
}

#[test]
#[should_panic(expected = "Error(Contract, #14)")] // InvalidSignatureLength
fn test_verify_message_fails_with_malformed_payload() {
    let (env, client, _owner, ..) = setup();

    let source_chain: u64 = 406;
    let pairs = sorted_signers_from_seeds(&[109, 113]);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: source_chain,
        threshold: 1,
        signers: signers_to_soroban_vec(&env, &pairs),
    };
    client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);

    let message_hash = BytesN::from_array(&env, &[0x55u8; 32]);
    let signed_hash = keccak_signed_hash(&env, &message_hash);
    let signed_bytes: [u8; 32] = signed_hash.to_array();

    let mut sig_payload = signature_payload_valid(&[pairs[0].clone()], &signed_bytes);
    sig_payload.truncate(63); // not a multiple of 64

    let verifier_results = build_verifier_results(&env, &sig_payload);
    client.verify_message(&source_chain, &message_hash, &verifier_results);
}

// ============================================================
// Withdraw Fee Tokens (EVM FeeTokenHandler parity)
// ============================================================

#[test]
fn test_withdraw_fee_tokens_transfers_balance() {
    let (env, client, ..) = setup();

    let fee_aggregator = client
        .get_dynamic_config()
        .fee_aggregator
        .expect("fee aggregator");

    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    let token_client = token::Client::new(&env, &token_address);

    sac_client.mint(&client.address, &1000);
    assert_eq!(token_client.balance(&client.address), 1000);

    client.withdraw_fee_tokens(&vec![&env, token_address.clone()]);

    assert_eq!(token_client.balance(&client.address), 0);
    assert_eq!(token_client.balance(&fee_aggregator), 1000);
}

#[test]
#[should_panic(expected = "Error(Contract, #803)")]
fn test_withdraw_fee_tokens_reverts_without_fee_aggregator() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(CommitteeVerifierContract, ());
    let client = CommitteeVerifierContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let storage_locations = vec![&env];

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy = env.register(RmnProxyContract, ());
    let rmn_proxy_client = RmnProxyContractClient::new(&env, &rmn_proxy);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let dynamic_config = DynamicConfig {
        fee_aggregator: None,
        allowlist_admin: None,
    };
    let tag = BytesN::from_array(&env, &DEFAULT_VERIFIER_VERSION_TAG);
    client.initialize(
        &owner,
        &dynamic_config,
        &storage_locations,
        &rmn_proxy,
        &tag,
    );

    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    sac_client.mint(&client.address, &100);

    client.withdraw_fee_tokens(&vec![&env, token_address]);
}
