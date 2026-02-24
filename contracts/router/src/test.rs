#![cfg(test)]

use super::*;
use soroban_sdk::{
    testutils::Address as _, testutils::Events as _, vec, Address, Bytes, Env, TryIntoVal, Vec,
};

// ============================================================
// Unit Test Helpers
// ============================================================

fn setup_env() -> (Env, Address, Address, Address, Address) {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);

    // Deploy and initialize RMN Remote so proxy can delegate is_cursed() to it
    let rmn_remote_id = env.register(rmn_remote::RmnRemoteContract, ());
    let rmn_remote_client = rmn_remote::RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &1u64);

    let rmn_proxy_addr = env.register(rmn_proxy::RmnProxyContract, ());
    let rmn_proxy_client = rmn_proxy::RmnProxyContractClient::new(&env, &rmn_proxy_addr);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let router_id = env.register(RouterContract, ());

    (env, router_id, owner, rmn_proxy_addr, rmn_remote_id)
}

// ============================================================
// Unit Tests
// ============================================================

#[test]
fn test_initialize() {
    let (env, contract_id, owner, rmn_proxy, _) = setup_env();
    let client = RouterContractClient::new(&env, &contract_id);

    // Initialize
    client.initialize(&owner, &rmn_proxy);

    // Verify owner
    env.as_contract(&client.address, || {
        assert_eq!(RouterContract::owner(&env).unwrap(), owner);
    });

    // Verify config
    let config = client.get_config();
    assert_eq!(config.rmn_proxy, rmn_proxy);
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")]
fn test_initialize_already_initialized() {
    let (env, contract_id, owner, rmn_proxy, _) = setup_env();
    let client = RouterContractClient::new(&env, &contract_id);

    // Initialize twice should fail
    client.initialize(&owner, &rmn_proxy);
    client.initialize(&owner, &rmn_proxy);
}

#[test]
fn test_set_onramp() {
    let (env, contract_id, owner, rmn_proxy, _) = setup_env();
    let client = RouterContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn_proxy);

    let onramp = Address::generate(&env);
    let dest_chain_selector: u64 = 123;

    // Set OnRamp
    client.set_onramp(&dest_chain_selector, &onramp);

    // Verify
    assert_eq!(client.get_onramp(&dest_chain_selector), onramp);
    assert!(client.is_chain_supported(&dest_chain_selector));
}

#[test]
fn test_add_remove_offramp() {
    let (env, contract_id, owner, rmn_proxy, _) = setup_env();
    let client = RouterContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn_proxy);

    let offramp = Address::generate(&env);
    let source_chain_selector: u64 = 456;

    // Add OffRamp
    client.add_offramp(&source_chain_selector, &offramp);

    // Verify added
    assert!(client.is_offramp(&source_chain_selector, &offramp));

    let offramps = client.get_offramps();
    assert_eq!(offramps.len(), 1);
    assert_eq!(
        offramps.get(0).unwrap().source_chain_selector,
        source_chain_selector
    );
    assert_eq!(offramps.get(0).unwrap().offramp, offramp);

    // Remove OffRamp
    client.remove_offramp(&source_chain_selector, &offramp);

    // Verify removed
    assert!(!client.is_offramp(&source_chain_selector, &offramp));
    assert_eq!(client.get_offramps().len(), 0);
}

#[test]
#[should_panic(expected = "Error(Contract, #60)")]
fn test_add_duplicate_offramp() {
    let (env, contract_id, owner, rmn_proxy, _) = setup_env();
    let client = RouterContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn_proxy);

    let offramp = Address::generate(&env);
    let source_chain_selector: u64 = 456;

    // Add same OffRamp twice should fail
    client.add_offramp(&source_chain_selector, &offramp);
    client.add_offramp(&source_chain_selector, &offramp);
}

#[test]
#[should_panic(expected = "Error(Contract, #61)")]
fn test_remove_nonexistent_offramp() {
    let (env, contract_id, owner, rmn_proxy, _) = setup_env();
    let client = RouterContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn_proxy);

    let offramp = Address::generate(&env);
    let source_chain_selector: u64 = 456;

    // Remove non-existent OffRamp should fail
    client.remove_offramp(&source_chain_selector, &offramp);
}

#[test]
fn test_apply_ramp_updates() {
    let (env, contract_id, owner, rmn_proxy, _) = setup_env();
    let client = RouterContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn_proxy);

    let onramp1 = Address::generate(&env);
    let onramp2 = Address::generate(&env);
    let offramp1 = Address::generate(&env);
    let offramp2 = Address::generate(&env);

    // Create update vectors
    let mut onramp_updates: Vec<OnRampEntry> = Vec::new(&env);
    onramp_updates.push_back(OnRampEntry {
        dest_chain_selector: 100,
        onramp: onramp1.clone(),
    });
    onramp_updates.push_back(OnRampEntry {
        dest_chain_selector: 200,
        onramp: onramp2.clone(),
    });

    let offramp_removes: Vec<OffRampEntry> = Vec::new(&env);

    let mut offramp_adds: Vec<OffRampEntry> = Vec::new(&env);
    offramp_adds.push_back(OffRampEntry {
        source_chain_selector: 300,
        offramp: offramp1.clone(),
    });
    offramp_adds.push_back(OffRampEntry {
        source_chain_selector: 400,
        offramp: offramp2.clone(),
    });

    // Apply updates
    client.apply_ramp_updates(&onramp_updates, &offramp_removes, &offramp_adds);

    // Verify OnRamps
    assert_eq!(client.get_onramp(&100), onramp1);
    assert_eq!(client.get_onramp(&200), onramp2);

    // Verify OffRamps
    assert!(client.is_offramp(&300, &offramp1));
    assert!(client.is_offramp(&400, &offramp2));
}

#[test]
fn test_transfer_ownership_two_step() {
    let (env, contract_id, owner, rmn_proxy, _) = setup_env();
    let client = RouterContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn_proxy);

    let new_owner = Address::generate(&env);

    env.as_contract(&client.address, || {
        // Step 1: Initiate transfer
        let _ = RouterContract::transfer_ownership(&env, &new_owner);
        // Owner should still be the original owner until accepted
        assert_eq!(RouterContract::owner(&env).unwrap(), owner);
        // Step 2: Accept transfer
        let _ = RouterContract::accept_ownership(&env);
        // Now the new owner should be set
        assert_eq!(RouterContract::owner(&env).unwrap(), new_owner);
    });
}

#[test]
fn test_get_onramps() {
    let (env, contract_id, owner, rmn_proxy, _) = setup_env();
    let client = RouterContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn_proxy);

    let onramp1 = Address::generate(&env);
    let onramp2 = Address::generate(&env);

    client.set_onramp(&100, &onramp1);
    client.set_onramp(&200, &onramp2);

    let onramps = client.get_onramps();
    assert_eq!(onramps.len(), 2);
}

#[test]
fn test_is_chain_supported_false() {
    let (env, contract_id, owner, rmn_proxy, _) = setup_env();
    let client = RouterContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn_proxy);

    // Non-configured chain should not be supported
    assert!(!client.is_chain_supported(&999));
}

// ============================================================
// Integration Test: Full ccip_send flow
// Router -> RMN Proxy (curse check) -> OnRamp (forward_from_router)
// ============================================================

#[test]
fn test_ccip_send_full_flow() {
    use onramp::types::{DestChainConfigArgs, DynamicConfig, StaticConfig};

    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let sender = Address::generate(&env);

    // ---- Deploy RMN Remote and Proxy ----
    let rmn_remote_id = env.register(rmn_remote::RmnRemoteContract, ());
    let rmn_remote_client = rmn_remote::RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &1u64);

    let rmn_proxy_id = env.register(rmn_proxy::RmnProxyContract, ());
    let rmn_proxy_client = rmn_proxy::RmnProxyContractClient::new(&env, &rmn_proxy_id);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let router_id = env.register(RouterContract, ());
    let onramp_id = env.register(onramp::OnRampContract, ());

    // ---- Initialize Router ----
    let router_client = RouterContractClient::new(&env, &router_id);
    router_client.initialize(&owner, &rmn_proxy_id);

    // ---- Initialize OnRamp ----
    let stellar_chain_selector: u64 = 12345;
    let evm_chain_selector: u64 = 67890;

    let static_config = StaticConfig {
        chain_selector: stellar_chain_selector,
        token_admin_registry: Address::generate(&env),
        rmn_proxy: rmn_proxy_id.clone(),
        max_usd_cents_per_message: 100_000,
    };

    let dynamic_config = DynamicConfig {
        fee_quoter: Address::generate(&env),
        fee_aggregator: Address::generate(&env),
    };

    let onramp_client = onramp::OnRampContractClient::new(&env, &onramp_id);
    onramp_client.initialize(&owner, &static_config, &dynamic_config);

    // ---- Configure OnRamp's dest chain config with Router as the authorized caller ----
    let dest_chain_config = DestChainConfigArgs {
        dest_chain_selector: evm_chain_selector,
        router: router_id.clone(), // Router must be the caller for forward_from_router
        address_bytes_length: 20,  // EVM-style addresses
        token_receiver_allowed: true,
        message_network_fee_usd_cents: 50,
        token_network_fee_usd_cents: 100,
        base_execution_gas_cost: 200_000,
        default_executor: Address::generate(&env),
        lane_mandated_ccvs: Vec::new(&env),
        default_ccvs: vec![&env, Address::generate(&env)],
        off_ramp: Bytes::from_array(&env, &[0u8; 20]),
    };

    onramp_client.apply_dest_chain_config_updates(&vec![&env, dest_chain_config]);

    // ---- Register OnRamp in Router for the EVM destination chain ----
    router_client.set_onramp(&evm_chain_selector, &onramp_id);

    // ---- Build a CCIP message ----
    let message = common_message::StellarToAnyMessage {
        receiver: Bytes::from_array(&env, &[1u8; 20]),
        data: Bytes::from_slice(&env, b"hello from stellar"),
        token_amounts: Vec::new(&env),
        fee_token: Address::generate(&env),
        extra_args: Bytes::new(&env),
    };

    // ---- Send the message via Router ----
    let message_id = router_client.ccip_send(&sender, &evm_chain_selector, &message, &0i128);

    // ---- Verify message ID is non-zero (32 bytes) ----
    let zero_hash = soroban_sdk::BytesN::from_array(&env, &[0u8; 32]);
    assert_ne!(message_id, zero_hash, "Message ID should not be all zeros");

    // TODO: Event verification temporarily disabled pending soroban-sdk v25
    // ContractEvents API migration (iter() removed). Re-enable once the
    // events iteration API is updated for SDK v25.
}

#[test]
fn test_ccip_send_unsupported_chain() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let sender = Address::generate(&env);

    let rmn_remote_id = env.register(rmn_remote::RmnRemoteContract, ());
    let rmn_remote_client = rmn_remote::RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &1u64);

    let rmn_proxy_id = env.register(rmn_proxy::RmnProxyContract, ());
    let rmn_proxy_client = rmn_proxy::RmnProxyContractClient::new(&env, &rmn_proxy_id);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let router_id = env.register(RouterContract, ());

    let router_client = RouterContractClient::new(&env, &router_id);
    router_client.initialize(&owner, &rmn_proxy_id);

    // No OnRamp configured for chain 999
    let message = common_message::StellarToAnyMessage {
        receiver: Bytes::from_array(&env, &[1u8; 20]),
        data: Bytes::new(&env),
        token_amounts: Vec::new(&env),
        fee_token: Address::generate(&env),
        extra_args: Bytes::new(&env),
    };

    // Should panic with UnsupportedDestinationChain (error #4)
    let result = router_client.try_ccip_send(&sender, &999u64, &message, &0i128);
    assert!(result.is_err(), "Should fail for unsupported chain");
}

#[test]
fn test_get_fee_via_onramp() {
    use onramp::types::{DestChainConfigArgs, DynamicConfig, StaticConfig};

    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);

    let rmn_remote_id = env.register(rmn_remote::RmnRemoteContract, ());
    let rmn_remote_client = rmn_remote::RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &1u64);

    let rmn_proxy_id = env.register(rmn_proxy::RmnProxyContract, ());
    let rmn_proxy_client = rmn_proxy::RmnProxyContractClient::new(&env, &rmn_proxy_id);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let router_id = env.register(RouterContract, ());
    let onramp_id = env.register(onramp::OnRampContract, ());

    let router_client = RouterContractClient::new(&env, &router_id);
    router_client.initialize(&owner, &rmn_proxy_id);

    let onramp_client = onramp::OnRampContractClient::new(&env, &onramp_id);
    onramp_client.initialize(
        &owner,
        &StaticConfig {
            chain_selector: 12345,
            token_admin_registry: Address::generate(&env),
            rmn_proxy: rmn_proxy_id.clone(),
            max_usd_cents_per_message: 100_000,
        },
        &DynamicConfig {
            fee_quoter: Address::generate(&env),
            fee_aggregator: Address::generate(&env),
        },
    );

    let dest_chain: u64 = 67890;
    onramp_client.apply_dest_chain_config_updates(&vec![
        &env,
        DestChainConfigArgs {
            dest_chain_selector: dest_chain,
            router: router_id.clone(),
            address_bytes_length: 20,
            token_receiver_allowed: true,
            message_network_fee_usd_cents: 50,
            token_network_fee_usd_cents: 100,
            base_execution_gas_cost: 200_000,
            default_executor: Address::generate(&env),
            lane_mandated_ccvs: Vec::new(&env),
            default_ccvs: vec![&env, Address::generate(&env)],
            off_ramp: Bytes::from_array(&env, &[0u8; 20]),
        },
    ]);

    router_client.set_onramp(&dest_chain, &onramp_id);

    let message = common_message::StellarToAnyMessage {
        receiver: Bytes::from_array(&env, &[1u8; 20]),
        data: Bytes::new(&env),
        token_amounts: Vec::new(&env),
        fee_token: Address::generate(&env),
        extra_args: Bytes::new(&env),
    };

    // get_fee should return 0 (OnRamp's placeholder)
    let fee = router_client.get_fee(&dest_chain, &message);
    assert_eq!(fee, 0);
}
