#![cfg(test)]

use super::*;
use soroban_sdk::{testutils::Address as _, vec, Address, Bytes, BytesN, Env};

use crate::types::{DynamicConfig, RemoteChainConfig};
use crate::{CommitteeVerifierContract, CommitteeVerifierContractClient};
use rmn_proxy::{RmnProxyContract, RmnProxyContractClient};
use rmn_remote::{RmnRemoteContract, RmnRemoteContractClient};

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

fn setup() -> (
    Env,
    CommitteeVerifierContractClient<'static>,
    Address,
    Address,
    Vec<Bytes>,
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
    rmn_remote_client.initialize(&owner, &1u64);

    let rmn_proxy = env.register(RmnProxyContract, ());
    let rmn_proxy_client = RmnProxyContractClient::new(&env, &rmn_proxy);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let dynamic_config = default_dynamic_config(&env);
    client.initialize(&owner, &dynamic_config, &storage_locations, &rmn_proxy);

    (env, client, owner, rmn_proxy, storage_locations)
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
    let (env, client, _owner, _rmn_proxy, storage_locations) = setup();
    let owner2 = Address::generate(&env);
    let dynamic_config = default_dynamic_config(&env);

    client.initialize(
        &owner2,
        &dynamic_config,
        &storage_locations,
        &Address::generate(&env),
    );
}

// ============================================================
// Version Tag Tests
// ============================================================

#[test]
fn test_version_tag() {
    let (env, client, ..) = setup();

    let expected = BytesN::from_array(&env, &[0x49, 0xff, 0x34, 0xed]);
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

    let (fee_usd_cents, gas_for_verification, payload_size_bytes) =
        client.get_fee(&dest_chain, &Bytes::new(&env), &Bytes::new(&env), &0u32);
    assert_eq!(fee_usd_cents, 10);
    assert_eq!(gas_for_verification, 100_000);
    assert_eq!(payload_size_bytes, 256);
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
#[should_panic(expected = "Error(Contract, #10)")] // FeatureNotEnabled
fn test_forward_to_verifier_fails_when_allowlist_not_enabled_for_chain() {
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

// ============================================================
// Verify Message Tests
// ============================================================

#[test]
#[should_panic(expected = "Error(Contract, #20)")] // InvalidVerifierResults
fn test_verify_message_fails_when_verifier_results_too_short() {
    let (env, client, ..) = setup();

    let source_chain: u64 = 1;
    let message_hash = BytesN::from_array(&env, &[0u8; 32]);
    let short_results = Bytes::from_slice(&env, &[0x49, 0xff]); // Only 2 bytes, need at least 6

    client.verify_message(&source_chain, &message_hash, &short_results);
}

#[test]
#[should_panic(expected = "Error(Contract, #59)")] // InvalidCCVVersion
fn test_verify_message_fails_when_wrong_version() {
    let (env, client, ..) = setup();

    let source_chain: u64 = 1;
    let message_hash = BytesN::from_array(&env, &[0u8; 32]);
    // Wrong version tag (0x00,0x00,0x00,0x00) + 2 bytes sig len (0x00, 0x00) + 0 sig bytes
    let wrong_version_results = Bytes::from_slice(&env, &[0x00, 0x00, 0x00, 0x00, 0x00, 0x00]);

    client.verify_message(&source_chain, &message_hash, &wrong_version_results);
}

#[test]
#[should_panic(expected = "Error(Contract, #19)")] // SourceNotConfigured
fn test_verify_message_fails_when_source_chain_not_configured() {
    let (env, client, ..) = setup();

    let source_chain: u64 = 99999; // Not configured
    let message_hash = BytesN::from_array(&env, &[0u8; 32]);
    // Correct version + sig len (0, 0) + no signatures - will fail at validate_signatures
    let verifier_results = Bytes::from_slice(&env, &[0x49, 0xff, 0x34, 0xed, 0x00, 0x00]);

    client.verify_message(&source_chain, &message_hash, &verifier_results);
}
