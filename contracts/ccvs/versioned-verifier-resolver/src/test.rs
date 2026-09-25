#![cfg(test)]

use common_authorization::Ownable;
use soroban_sdk::{testutils::Address as _, token, vec, Address, Bytes, BytesN, Env};

use crate::{
    InboundImplementationArgs, InboundImplementationUpdate, OutboundImplementationArgs,
    OutboundImplementationUpdate, VersionedVerifierResolverContract,
    VersionedVerifierResolverContractClient,
};

fn setup() -> (
    Env,
    VersionedVerifierResolverContractClient<'static>,
    Address,
) {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(VersionedVerifierResolverContract, ());
    let client = VersionedVerifierResolverContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let fee_aggregator = Address::generate(&env);
    client.initialize(&owner, &fee_aggregator);

    (env, client, owner)
}

// ============================================================
// Initialization Tests
// ============================================================

#[test]
fn test_initialize() {
    let (env, client, owner) = setup();

    env.as_contract(&client.address, || {
        assert_eq!(
            VersionedVerifierResolverContract::owner(&env).unwrap(),
            owner
        );
    });

    assert_eq!(client.get_all_inbound_implementations().len(), 0);
    assert_eq!(client.get_all_outbound_implementations().len(), 0);
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")] // AlreadyInitialized
fn test_double_initialize_fails() {
    let (env, client, _owner) = setup();
    let owner2 = Address::generate(&env);
    let fee_agg2 = Address::generate(&env);
    client.initialize(&owner2, &fee_agg2);
}

// ============================================================
// Inbound Implementation Tests
// ============================================================

#[test]
fn test_apply_inbound_set_single() {
    let (env, client, _owner) = setup();

    let version = BytesN::from_array(&env, &[0x01, 0x07, 0x00, 0x00]);
    let verifier = Address::generate(&env);

    client.apply_inbound_impl_updates(&vec![
        &env,
        InboundImplementationUpdate {
            version: version.clone(),
            verifier: Some(verifier.clone()),
        },
    ]);

    // Query using verifier_results with the same 4-byte prefix + extra data
    let mut verifier_results = Bytes::new(&env);
    verifier_results.push_back(0x01);
    verifier_results.push_back(0x07);
    verifier_results.push_back(0x00);
    verifier_results.push_back(0x00);
    verifier_results.push_back(0xAA);
    verifier_results.push_back(0xBB);

    let result = client.get_inbound_implementation(&verifier_results);
    assert_eq!(result, verifier);
}

#[test]
fn test_apply_inbound_set_multiple() {
    let (env, client, _owner) = setup();

    let version1 = BytesN::from_array(&env, &[0x01, 0x07, 0x00, 0x00]);
    let verifier1 = Address::generate(&env);
    let version2 = BytesN::from_array(&env, &[0x01, 0x08, 0x00, 0x00]);
    let verifier2 = Address::generate(&env);

    // Batch set both at once
    client.apply_inbound_impl_updates(&vec![
        &env,
        InboundImplementationUpdate {
            version: version1.clone(),
            verifier: Some(verifier1.clone()),
        },
        InboundImplementationUpdate {
            version: version2.clone(),
            verifier: Some(verifier2.clone()),
        },
    ]);

    let all = client.get_all_inbound_implementations();
    assert_eq!(all.len(), 2);

    let entry0: InboundImplementationArgs = all.get(0).unwrap();
    let entry1: InboundImplementationArgs = all.get(1).unwrap();

    let has_v1 = (entry0.version == version1 && entry0.verifier == verifier1)
        || (entry1.version == version1 && entry1.verifier == verifier1);
    let has_v2 = (entry0.version == version2 && entry0.verifier == verifier2)
        || (entry1.version == version2 && entry1.verifier == verifier2);
    assert!(has_v1);
    assert!(has_v2);
}

#[test]
fn test_apply_inbound_remove_via_none() {
    let (env, client, _owner) = setup();

    let version = BytesN::from_array(&env, &[0x01, 0x07, 0x00, 0x00]);
    let verifier = Address::generate(&env);

    // First, set the implementation
    client.apply_inbound_impl_updates(&vec![
        &env,
        InboundImplementationUpdate {
            version: version.clone(),
            verifier: Some(verifier.clone()),
        },
    ]);
    assert_eq!(client.get_all_inbound_implementations().len(), 1);

    // Remove by passing None as verifier
    client.apply_inbound_impl_updates(&vec![
        &env,
        InboundImplementationUpdate {
            version: version.clone(),
            verifier: None,
        },
    ]);
    assert_eq!(client.get_all_inbound_implementations().len(), 0);
}

#[test]
fn test_apply_inbound_mixed_set_and_remove() {
    let (env, client, _owner) = setup();

    let version1 = BytesN::from_array(&env, &[0x01, 0x07, 0x00, 0x00]);
    let verifier1 = Address::generate(&env);
    let version2 = BytesN::from_array(&env, &[0x01, 0x08, 0x00, 0x00]);
    let verifier2 = Address::generate(&env);

    // Set both versions
    client.apply_inbound_impl_updates(&vec![
        &env,
        InboundImplementationUpdate {
            version: version1.clone(),
            verifier: Some(verifier1.clone()),
        },
        InboundImplementationUpdate {
            version: version2.clone(),
            verifier: Some(verifier2.clone()),
        },
    ]);
    assert_eq!(client.get_all_inbound_implementations().len(), 2);

    // In one batch: remove version1, add version3
    let version3 = BytesN::from_array(&env, &[0x02, 0x00, 0x00, 0x00]);
    let verifier3 = Address::generate(&env);

    client.apply_inbound_impl_updates(&vec![
        &env,
        InboundImplementationUpdate {
            version: version1.clone(),
            verifier: None, // remove
        },
        InboundImplementationUpdate {
            version: version3.clone(),
            verifier: Some(verifier3.clone()), // add
        },
    ]);

    let all = client.get_all_inbound_implementations();
    assert_eq!(all.len(), 2);

    // Should have version2 and version3, NOT version1
    let entry0: InboundImplementationArgs = all.get(0).unwrap();
    let entry1: InboundImplementationArgs = all.get(1).unwrap();

    let has_v2 = (entry0.version == version2 && entry0.verifier == verifier2)
        || (entry1.version == version2 && entry1.verifier == verifier2);
    let has_v3 = (entry0.version == version3 && entry0.verifier == verifier3)
        || (entry1.version == version3 && entry1.verifier == verifier3);
    assert!(has_v2);
    assert!(has_v3);
}

#[test]
fn test_apply_inbound_update_existing() {
    let (env, client, _owner) = setup();

    let version = BytesN::from_array(&env, &[0x01, 0x07, 0x00, 0x00]);
    let verifier1 = Address::generate(&env);
    let verifier2 = Address::generate(&env);

    client.apply_inbound_impl_updates(&vec![
        &env,
        InboundImplementationUpdate {
            version: version.clone(),
            verifier: Some(verifier1.clone()),
        },
    ]);

    // Update with new verifier
    client.apply_inbound_impl_updates(&vec![
        &env,
        InboundImplementationUpdate {
            version: version.clone(),
            verifier: Some(verifier2.clone()),
        },
    ]);

    // Should still have only 1 entry
    assert_eq!(client.get_all_inbound_implementations().len(), 1);

    let mut verifier_results = Bytes::new(&env);
    verifier_results.push_back(0x01);
    verifier_results.push_back(0x07);
    verifier_results.push_back(0x00);
    verifier_results.push_back(0x00);
    assert_eq!(
        client.get_inbound_implementation(&verifier_results),
        verifier2
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #58)")] // InvalidVersion
fn test_apply_inbound_zero_version_fails() {
    let (env, client, _owner) = setup();

    let zero_version = BytesN::from_array(&env, &[0x00, 0x00, 0x00, 0x00]);
    let verifier = Address::generate(&env);

    client.apply_inbound_impl_updates(&vec![
        &env,
        InboundImplementationUpdate {
            version: zero_version,
            verifier: Some(verifier),
        },
    ]);
}

#[test]
#[should_panic(expected = "Error(Contract, #53)")] // InvalidVerifierResultsLength
fn test_get_inbound_implementation_too_short() {
    let (env, client, _owner) = setup();

    let mut short_data = Bytes::new(&env);
    short_data.push_back(0x01);
    short_data.push_back(0x02);
    // Only 2 bytes, need at least 4

    client.get_inbound_implementation(&short_data);
}

// REQ (Claim 3): the literal "add a CCV to the destination's reachable set"
// op is `apply_inbound_impl_updates` (maps a verifier version-tag → CCV
// address), owner-gated at lib.rs:278. A non-owner must be rejected. Args are
// empty so the only gate exercised is `require_owner`.
#[test]
fn test_apply_inbound_impl_updates_is_owner_only() {
    let (env, client, _owner) = setup();
    // Turn off mock_all_auths so the owner's require_auth() is not satisfied.
    env.mock_auths(&[]);
    let r = client.try_apply_inbound_impl_updates(&vec![&env]);
    assert!(
        r.is_err(),
        "non-owner must be rejected from adding a CCV to the inbound reachable set"
    );
}

// ============================================================
// Outbound Implementation Tests
// ============================================================

#[test]
fn test_apply_outbound_set_single() {
    let (env, client, _owner) = setup();

    let dest_chain: u64 = 12345;
    let verifier = Address::generate(&env);

    client.apply_outbound_impl_updates(&vec![
        &env,
        OutboundImplementationUpdate {
            dest_chain_selector: dest_chain,
            verifier: Some(verifier.clone()),
        },
    ]);

    let extra_args = Bytes::new(&env);
    let result = client.get_outbound_implementation(&dest_chain, &extra_args);
    assert_eq!(result, verifier);
}

#[test]
fn test_apply_outbound_set_multiple() {
    let (env, client, _owner) = setup();

    let dest1: u64 = 100;
    let verifier1 = Address::generate(&env);
    let dest2: u64 = 200;
    let verifier2 = Address::generate(&env);

    client.apply_outbound_impl_updates(&vec![
        &env,
        OutboundImplementationUpdate {
            dest_chain_selector: dest1,
            verifier: Some(verifier1.clone()),
        },
        OutboundImplementationUpdate {
            dest_chain_selector: dest2,
            verifier: Some(verifier2.clone()),
        },
    ]);

    let all = client.get_all_outbound_implementations();
    assert_eq!(all.len(), 2);

    let entry0: OutboundImplementationArgs = all.get(0).unwrap();
    let entry1: OutboundImplementationArgs = all.get(1).unwrap();

    let has_d1 = (entry0.dest_chain_selector == dest1 && entry0.verifier == verifier1)
        || (entry1.dest_chain_selector == dest1 && entry1.verifier == verifier1);
    let has_d2 = (entry0.dest_chain_selector == dest2 && entry0.verifier == verifier2)
        || (entry1.dest_chain_selector == dest2 && entry1.verifier == verifier2);
    assert!(has_d1);
    assert!(has_d2);
}

#[test]
fn test_apply_outbound_remove_via_none() {
    let (env, client, _owner) = setup();

    let dest_chain: u64 = 42;
    let verifier = Address::generate(&env);

    client.apply_outbound_impl_updates(&vec![
        &env,
        OutboundImplementationUpdate {
            dest_chain_selector: dest_chain,
            verifier: Some(verifier.clone()),
        },
    ]);
    assert_eq!(client.get_all_outbound_implementations().len(), 1);

    // Remove by passing None
    client.apply_outbound_impl_updates(&vec![
        &env,
        OutboundImplementationUpdate {
            dest_chain_selector: dest_chain,
            verifier: None,
        },
    ]);
    assert_eq!(client.get_all_outbound_implementations().len(), 0);
}

#[test]
fn test_apply_outbound_mixed_set_and_remove() {
    let (env, client, _owner) = setup();

    let dest1: u64 = 100;
    let verifier1 = Address::generate(&env);
    let dest2: u64 = 200;
    let verifier2 = Address::generate(&env);

    // Set both
    client.apply_outbound_impl_updates(&vec![
        &env,
        OutboundImplementationUpdate {
            dest_chain_selector: dest1,
            verifier: Some(verifier1.clone()),
        },
        OutboundImplementationUpdate {
            dest_chain_selector: dest2,
            verifier: Some(verifier2.clone()),
        },
    ]);
    assert_eq!(client.get_all_outbound_implementations().len(), 2);

    // Remove dest1, add dest3
    let dest3: u64 = 300;
    let verifier3 = Address::generate(&env);

    client.apply_outbound_impl_updates(&vec![
        &env,
        OutboundImplementationUpdate {
            dest_chain_selector: dest1,
            verifier: None, // remove
        },
        OutboundImplementationUpdate {
            dest_chain_selector: dest3,
            verifier: Some(verifier3.clone()), // add
        },
    ]);

    let all = client.get_all_outbound_implementations();
    assert_eq!(all.len(), 2);
}

#[test]
fn test_apply_outbound_update_existing() {
    let (env, client, _owner) = setup();

    let dest_chain: u64 = 999;
    let verifier1 = Address::generate(&env);
    let verifier2 = Address::generate(&env);

    client.apply_outbound_impl_updates(&vec![
        &env,
        OutboundImplementationUpdate {
            dest_chain_selector: dest_chain,
            verifier: Some(verifier1.clone()),
        },
    ]);
    client.apply_outbound_impl_updates(&vec![
        &env,
        OutboundImplementationUpdate {
            dest_chain_selector: dest_chain,
            verifier: Some(verifier2.clone()),
        },
    ]);

    assert_eq!(client.get_all_outbound_implementations().len(), 1);

    let extra_args = Bytes::new(&env);
    assert_eq!(
        client.get_outbound_implementation(&dest_chain, &extra_args),
        verifier2
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #57)")] // InvalidChainSelector
fn test_apply_outbound_zero_chain_selector_fails() {
    let (env, client, _owner) = setup();
    let verifier = Address::generate(&env);

    client.apply_outbound_impl_updates(&vec![
        &env,
        OutboundImplementationUpdate {
            dest_chain_selector: 0,
            verifier: Some(verifier),
        },
    ]);
}

// REQ (Claim 3): `apply_outbound_impl_updates` maps a dest chain → CCV
// address (outbound routing), owner-gated at lib.rs:311. A non-owner must be
// rejected. Args are empty so the only gate exercised is `require_owner`.
#[test]
fn test_apply_outbound_impl_updates_is_owner_only() {
    let (env, client, _owner) = setup();
    // Turn off mock_all_auths so the owner's require_auth() is not satisfied.
    env.mock_auths(&[]);
    let r = client.try_apply_outbound_impl_updates(&vec![&env]);
    assert!(
        r.is_err(),
        "non-owner must be rejected from configuring outbound CCV routing"
    );
}

// ============================================================
// Fee Aggregator Tests
// ============================================================

#[test]
fn test_set_fee_aggregator() {
    let (env, client, _owner) = setup();

    let new_fee_agg = Address::generate(&env);
    client.set_fee_aggregator(&new_fee_agg);

    assert_eq!(client.get_fee_aggregator(), new_fee_agg);
}

#[test]
fn test_withdraw_fee_tokens_transfers_balance() {
    let (env, client, _) = setup();
    let fee_aggregator = client.get_fee_aggregator();

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
fn test_withdraw_fee_tokens_reverts_for_zero_fee_aggregator() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(VersionedVerifierResolverContract, ());
    let client = VersionedVerifierResolverContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let zero_fee_agg = Address::from_str(
        &env,
        "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
    );
    client.initialize(&owner, &zero_fee_agg);

    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    sac_client.mint(&client.address, &50);

    client.withdraw_fee_tokens(&vec![&env, token_address]);
}
