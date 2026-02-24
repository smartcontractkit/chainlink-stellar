#![cfg(test)]

use super::*;
use soroban_sdk::{testutils::Address as _, vec, Address, Bytes, Env, Vec};

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
