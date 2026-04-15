#![cfg(test)]

use super::*;
use soroban_sdk::{testutils::Address as _, token, vec, Address, Bytes, Env, Vec};

fn create_test_static_config(env: &Env) -> StaticConfig {
    StaticConfig {
        chain_selector: 12345,
        token_admin_registry: Address::generate(env),
        rmn_proxy: Address::generate(env),
        max_usd_cents_per_message: 100_000, // $1000 max
    }
}

fn create_test_dynamic_config(env: &Env) -> DynamicConfig {
    DynamicConfig {
        fee_quoter: Address::generate(env),
        fee_aggregator: Address::generate(env),
    }
}

fn create_test_dest_chain_config_args(env: &Env, dest_selector: u64) -> DestChainConfigArgs {
    DestChainConfigArgs {
        dest_chain_selector: dest_selector,
        router: Address::generate(env),
        address_bytes_length: 20, // EVM-style address
        token_receiver_allowed: true,
        message_network_fee_usd_cents: 50, // $0.50
        token_network_fee_usd_cents: 100,  // $1.00
        base_execution_gas_cost: 200_000,
        execution_fee_usd_cents: 25, // $0.25
        default_executor: Address::generate(env),
        lane_mandated_ccvs: Vec::new(env),
        default_ccvs: vec![env, Address::generate(env)],
        off_ramp: Bytes::from_array(env, &[0u8; 20]),
    }
}

#[test]
fn test_initialize() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = create_test_dynamic_config(&env);

    // Initialize should succeed
    client.initialize(&owner, &static_config, &dynamic_config);

    // Verify configs are stored correctly
    let stored_static = client.get_static_config();
    assert_eq!(stored_static.chain_selector, static_config.chain_selector);

    let stored_dynamic = client.get_dynamic_config();
    assert_eq!(stored_dynamic.fee_quoter, dynamic_config.fee_quoter);

    // Verify owner
    let stored_owner = client.owner();
    assert_eq!(stored_owner, Some(owner));
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")] // AlreadyInitialized
fn test_double_initialize_fails() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = create_test_dynamic_config(&env);

    // First init succeeds
    client.initialize(&owner, &static_config, &dynamic_config);

    // Second init should fail
    client.initialize(&owner, &static_config, &dynamic_config);
}

#[test]
fn test_apply_dest_chain_config() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = create_test_dynamic_config(&env);

    client.initialize(&owner, &static_config, &dynamic_config);

    // Add a destination chain config
    let dest_selector: u64 = 67890;
    let dest_config = create_test_dest_chain_config_args(&env, dest_selector);

    client.apply_dest_chain_config_updates(&vec![&env, dest_config.clone()]);

    // Verify config is stored
    let stored_config = client.get_dest_chain_config(&dest_selector);
    assert_eq!(stored_config.router, dest_config.router);
    assert_eq!(
        stored_config.address_bytes_length,
        dest_config.address_bytes_length
    );
    assert_eq!(
        stored_config.base_execution_gas_cost,
        dest_config.base_execution_gas_cost
    );
}

#[test]
fn test_get_expected_next_message_number() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = create_test_dynamic_config(&env);

    client.initialize(&owner, &static_config, &dynamic_config);

    let dest_selector: u64 = 67890;
    let dest_config = create_test_dest_chain_config_args(&env, dest_selector);
    client.apply_dest_chain_config_updates(&vec![&env, dest_config]);

    // Initial message number should be 1 (0 + 1)
    let next_msg_num = client.get_expected_next_message_number(&dest_selector);
    assert_eq!(next_msg_num, 1);
}

#[test]
fn test_set_dynamic_config() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = create_test_dynamic_config(&env);

    client.initialize(&owner, &static_config, &dynamic_config);

    // Update dynamic config
    let new_fee_quoter = Address::generate(&env);
    let new_dynamic_config = DynamicConfig {
        fee_quoter: new_fee_quoter.clone(),
        fee_aggregator: dynamic_config.fee_aggregator.clone(),
    };

    client.set_dynamic_config(&new_dynamic_config);

    // Verify update
    let stored_config = client.get_dynamic_config();
    assert_eq!(stored_config.fee_quoter, new_fee_quoter);
}

#[test]
fn test_transfer_ownership() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = create_test_dynamic_config(&env);

    client.initialize(&owner, &static_config, &dynamic_config);

    // Transfer ownership
    let new_owner = Address::generate(&env);
    client.transfer_ownership(&new_owner);

    // Accept ownership (this mocks authorization from the new owner)
    client.accept_ownership();

    // Verify new owner
    let stored_owner = client.owner();
    assert_eq!(stored_owner, Some(new_owner));
}

#[test]
fn test_get_all_dest_chain_configs() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = create_test_dynamic_config(&env);

    client.initialize(&owner, &static_config, &dynamic_config);

    // Add multiple destination chain configs
    let dest1 = create_test_dest_chain_config_args(&env, 100);
    let dest2 = create_test_dest_chain_config_args(&env, 200);

    client.apply_dest_chain_config_updates(&vec![&env, dest1.clone(), dest2.clone()]);

    // Get all configs
    let (selectors, _configs) = client.get_all_dest_chain_configs();
    assert_eq!(selectors.len(), 2);
}

#[test]
#[should_panic(expected = "Error(Contract, #52)")] // InvalidConfig - invalid chain selector
fn test_invalid_dest_chain_config_zero_selector() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = create_test_dynamic_config(&env);

    client.initialize(&owner, &static_config, &dynamic_config);

    // Try to add config with zero selector
    let mut dest_config = create_test_dest_chain_config_args(&env, 0);
    dest_config.dest_chain_selector = 0;

    client.apply_dest_chain_config_updates(&vec![&env, dest_config]);
}

#[test]
#[should_panic(expected = "Error(Contract, #52)")] // InvalidConfig - same as local chain
fn test_invalid_dest_chain_config_same_as_local() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = create_test_dynamic_config(&env);

    client.initialize(&owner, &static_config, &dynamic_config);

    // Try to add config with same selector as local chain
    let dest_config = create_test_dest_chain_config_args(&env, static_config.chain_selector);

    client.apply_dest_chain_config_updates(&vec![&env, dest_config]);
}

// ============================================================
// Helper for fully-initialized OnRamp with a dest chain
// ============================================================

fn init_onramp_with_dest(
    env: &Env,
) -> (
    OnRampContractClient<'_>,
    u64,
    DestChainConfigArgs,
    StaticConfig,
) {
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(env, &contract_id);

    let owner = Address::generate(env);
    let static_config = create_test_static_config(env);
    let dynamic_config = create_test_dynamic_config(env);

    client.initialize(&owner, &static_config, &dynamic_config);

    let dest_selector: u64 = 67890;
    let dest_config = create_test_dest_chain_config_args(env, dest_selector);
    client.apply_dest_chain_config_updates(&vec![env, dest_config.clone()]);

    (client, dest_selector, dest_config, static_config)
}

// ============================================================
// Test cases for forward_from_router validation & config
// ============================================================

#[test]
#[should_panic(expected = "Error(Contract, #37)")] // DestinationChainNotSupported
fn test_dest_chain_not_configured_fails() {
    let env = Env::default();
    let (client, _, _, _) = init_onramp_with_dest(&env);

    // Calling get_dest_chain_config for an unconfigured chain should fail
    let unconfigured_selector: u64 = 999999;
    client.get_dest_chain_config(&unconfigured_selector);
}

#[test]
#[should_panic(expected = "Error(Contract, #1)")] // NotInitialized
fn test_not_initialized_fails() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let msg = common_message::StellarToAnyMessage {
        receiver: Bytes::from_array(&env, &[0u8; 20]),
        data: Bytes::new(&env),
        token_amounts: Vec::new(&env),
        fee_token: Address::generate(&env),
        extra_args: Bytes::new(&env),
    };

    client.forward_from_router(&67890, &msg, &0_i128, &Address::generate(&env));
}

#[test]
fn test_multiple_dest_chain_configs() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = create_test_dynamic_config(&env);
    client.initialize(&owner, &static_config, &dynamic_config);

    let dest1 = create_test_dest_chain_config_args(&env, 100);
    let dest2 = create_test_dest_chain_config_args(&env, 200);
    let dest3 = create_test_dest_chain_config_args(&env, 300);

    client.apply_dest_chain_config_updates(&vec![
        &env,
        dest1.clone(),
        dest2.clone(),
        dest3.clone(),
    ]);

    let (selectors, configs) = client.get_all_dest_chain_configs();
    assert_eq!(selectors.len(), 3);
    assert_eq!(configs.len(), 3);

    // Verify each chain starts at message_number 0 (next = 1)
    assert_eq!(client.get_expected_next_message_number(&100), 1);
    assert_eq!(client.get_expected_next_message_number(&200), 1);
    assert_eq!(client.get_expected_next_message_number(&300), 1);
}

#[test]
fn test_update_dest_chain_config() {
    let env = Env::default();
    let (client, dest_selector, _, _) = init_onramp_with_dest(&env);

    // Update the config with new values (off_ramp length must match address_bytes_length)
    let mut updated = create_test_dest_chain_config_args(&env, dest_selector);
    updated.base_execution_gas_cost = 500_000;

    client.apply_dest_chain_config_updates(&vec![&env, updated.clone()]);

    let stored = client.get_dest_chain_config(&dest_selector);
    assert_eq!(stored.base_execution_gas_cost, 500_000);
}

#[test]
fn test_dest_chain_config_stores_execution_fee() {
    let env = Env::default();
    let (client, dest_selector, dest_config, _) = init_onramp_with_dest(&env);

    let stored = client.get_dest_chain_config(&dest_selector);
    assert_eq!(
        stored.execution_fee_usd_cents,
        dest_config.execution_fee_usd_cents
    );
    assert_eq!(stored.execution_fee_usd_cents, 25);
}

#[test]
fn test_dest_chain_config_stores_network_fees() {
    let env = Env::default();
    let (client, dest_selector, dest_config, _) = init_onramp_with_dest(&env);

    let stored = client.get_dest_chain_config(&dest_selector);
    assert_eq!(
        stored.message_network_fee_usd_cents,
        dest_config.message_network_fee_usd_cents
    );
    assert_eq!(
        stored.token_network_fee_usd_cents,
        dest_config.token_network_fee_usd_cents
    );
    assert_eq!(stored.message_network_fee_usd_cents, 50);
    assert_eq!(stored.token_network_fee_usd_cents, 100);
}

#[test]
fn test_dynamic_config_update() {
    let env = Env::default();
    let (client, _, _, _) = init_onramp_with_dest(&env);

    let new_fee_quoter = Address::generate(&env);
    let new_fee_aggregator = Address::generate(&env);
    let new_config = DynamicConfig {
        fee_quoter: new_fee_quoter.clone(),
        fee_aggregator: new_fee_aggregator.clone(),
    };

    client.set_dynamic_config(&new_config);

    let stored = client.get_dynamic_config();
    assert_eq!(stored.fee_quoter, new_fee_quoter);
    assert_eq!(stored.fee_aggregator, new_fee_aggregator);
}

#[test]
fn test_validate_message_too_many_tokens() {
    let env = Env::default();
    let ta1 = common_message::TokenAmount {
        token: Address::generate(&env),
        amount: 10,
    };
    let ta2 = common_message::TokenAmount {
        token: Address::generate(&env),
        amount: 20,
    };

    let msg = common_message::StellarToAnyMessage {
        receiver: Bytes::from_array(&env, &[0u8; 20]),
        data: Bytes::new(&env),
        token_amounts: vec![&env, ta1, ta2],
        fee_token: Address::generate(&env),
        extra_args: Bytes::new(&env),
    };

    assert_eq!(
        msg.validate(),
        Err(common_error::CCIPError::CanOnlySendOneTokenPerMessage)
    );
}

// ============================================================
// CCV Merge Logic Tests
// ============================================================

#[test]
fn test_merge_ccv_lists_user_only() {
    let env = Env::default();
    let a = Address::generate(&env);
    let b = Address::generate(&env);

    let user = vec![&env, a.clone(), b.clone()];
    let lane = Vec::new(&env);
    let defaults = vec![&env, Address::generate(&env)];

    let merged = OnRampContract::merge_ccv_lists(&env, &user, &lane, &defaults);
    assert_eq!(merged.len(), 2);
    assert_eq!(merged.get(0), Some(a));
    assert_eq!(merged.get(1), Some(b));
}

#[test]
fn test_merge_ccv_lists_falls_back_to_defaults() {
    let env = Env::default();
    let default_ccv = Address::generate(&env);

    let user: Vec<Address> = Vec::new(&env);
    let lane: Vec<Address> = Vec::new(&env);
    let defaults = vec![&env, default_ccv.clone()];

    let merged = OnRampContract::merge_ccv_lists(&env, &user, &lane, &defaults);
    assert_eq!(merged.len(), 1);
    assert_eq!(merged.get(0), Some(default_ccv));
}

#[test]
fn test_merge_ccv_lists_lane_mandated_appended() {
    let env = Env::default();
    let user_ccv = Address::generate(&env);
    let lane_ccv = Address::generate(&env);

    let user = vec![&env, user_ccv.clone()];
    let lane = vec![&env, lane_ccv.clone()];
    let defaults = vec![&env, Address::generate(&env)];

    let merged = OnRampContract::merge_ccv_lists(&env, &user, &lane, &defaults);
    assert_eq!(merged.len(), 2);
    assert_eq!(merged.get(0), Some(user_ccv));
    assert_eq!(merged.get(1), Some(lane_ccv));
}

#[test]
fn test_merge_ccv_lists_deduplication() {
    let env = Env::default();
    let shared = Address::generate(&env);

    let user = vec![&env, shared.clone()];
    let lane = vec![&env, shared.clone()];
    let defaults = vec![&env, Address::generate(&env)];

    let merged = OnRampContract::merge_ccv_lists(&env, &user, &lane, &defaults);
    assert_eq!(merged.len(), 1);
    assert_eq!(merged.get(0), Some(shared));
}

#[test]
fn test_merge_ccv_lists_lane_only_no_fallback() {
    let env = Env::default();
    let lane_ccv = Address::generate(&env);

    let user: Vec<Address> = Vec::new(&env);
    let lane = vec![&env, lane_ccv.clone()];
    let defaults = vec![&env, Address::generate(&env)];

    let merged = OnRampContract::merge_ccv_lists(&env, &user, &lane, &defaults);
    assert_eq!(merged.len(), 1);
    assert_eq!(merged.get(0), Some(lane_ccv));
}

// ============================================================
// Withdraw Fee Tokens Tests
// ============================================================

#[test]
fn test_withdraw_fee_tokens_transfers_balance() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let fee_aggregator = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = DynamicConfig {
        fee_quoter: Address::generate(&env),
        fee_aggregator: fee_aggregator.clone(),
    };

    client.initialize(&owner, &static_config, &dynamic_config);

    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    let token_client = token::Client::new(&env, &token_address);

    sac_client.mint(&contract_id, &1000);
    assert_eq!(token_client.balance(&contract_id), 1000);

    client.withdraw_fee_tokens(&vec![&env, token_address.clone()]);

    assert_eq!(token_client.balance(&contract_id), 0);
    assert_eq!(token_client.balance(&fee_aggregator), 1000);
}

#[test]
fn test_withdraw_fee_tokens_skips_zero_balance() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let fee_aggregator = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = DynamicConfig {
        fee_quoter: Address::generate(&env),
        fee_aggregator: fee_aggregator.clone(),
    };

    client.initialize(&owner, &static_config, &dynamic_config);

    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_client = token::Client::new(&env, &token_address);

    // No balance minted -- should not panic
    client.withdraw_fee_tokens(&vec![&env, token_address.clone()]);

    assert_eq!(token_client.balance(&contract_id), 0);
    assert_eq!(token_client.balance(&fee_aggregator), 0);
}

#[test]
fn test_withdraw_fee_tokens_multiple_tokens() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let fee_aggregator = Address::generate(&env);
    let static_config = create_test_static_config(&env);
    let dynamic_config = DynamicConfig {
        fee_quoter: Address::generate(&env),
        fee_aggregator: fee_aggregator.clone(),
    };

    client.initialize(&owner, &static_config, &dynamic_config);

    let admin1 = Address::generate(&env);
    let tc1 = env.register_stellar_asset_contract_v2(admin1);
    let addr1 = tc1.address();
    let sac1 = token::StellarAssetClient::new(&env, &addr1);
    let tok1 = token::Client::new(&env, &addr1);
    sac1.mint(&contract_id, &500);

    let admin2 = Address::generate(&env);
    let tc2 = env.register_stellar_asset_contract_v2(admin2);
    let addr2 = tc2.address();
    let sac2 = token::StellarAssetClient::new(&env, &addr2);
    let tok2 = token::Client::new(&env, &addr2);
    sac2.mint(&contract_id, &300);

    client.withdraw_fee_tokens(&vec![&env, addr1.clone(), addr2.clone()]);

    assert_eq!(tok1.balance(&contract_id), 0);
    assert_eq!(tok1.balance(&fee_aggregator), 500);
    assert_eq!(tok2.balance(&contract_id), 0);
    assert_eq!(tok2.balance(&fee_aggregator), 300);
}

#[test]
#[should_panic(expected = "Error(Contract, #1)")] // NotInitialized
fn test_withdraw_fee_tokens_not_initialized() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);

    client.withdraw_fee_tokens(&Vec::new(&env));
}
