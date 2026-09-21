#![cfg(test)]

use super::*;
use soroban_sdk::{
    contract, contractimpl, symbol_short,
    testutils::Address as _,
    testutils::{Address as _, Events as _, Ledger},
    token, vec, Address, Bytes, BytesN, Env, Map, Symbol, TryFromVal, TryIntoVal, Val, Vec,
};

use crate::types::Receipt;
use crate::{OnRampContract, OnRampContractClient};
use ccip_ramp_registry::{OnRampUpdate, RampRegistryContract, RampRegistryContractClient};
use ccvs_versioned_verifier_resolver::{
    OutboundImplementationUpdate, VersionedVerifierResolverContract,
    VersionedVerifierResolverContractClient,
};
use common_error::CCIPError;
use common_interfaces::committee_verifier::FeeResponse;
use common_message::{
    CcipMessageV1, CcipTokenTransferV1, FromBytes, GenericExtraArgsV3, StellarToAnyMessage,
    TokenAmount,
};
use common_pool::{ChainUpdate, LockBoxEntry, RateLimitConfig};
use executor::{
    types::{
        DynamicConfig as ExecDynamicConfig, RemoteChainConfig as ExecRemoteChainConfig,
        RemoteChainConfigArgs as ExecRemoteChainConfigArgs,
    },
    ExecutorContract, ExecutorContractClient,
};
use fee_quoter::{
    types::{
        DestChainConfig, DestChainConfigArgs as FqDestChainConfigArgs, GasPriceUpdate,
        PriceUpdates, StaticConfig as FqStaticConfig, TokenFeeConfigArgs, TokenPriceUpdate,
        TokenTransferFeeConfig,
    },
    FeeQuoterContract, FeeQuoterContractClient,
};
use pools_lock_release_pool::{LockReleaseTokenPoolContract, LockReleaseTokenPoolContractClient};
use pools_token_lock_box::{TokenLockBox, TokenLockBoxClient};
use rmn_proxy::{RmnProxyContract, RmnProxyContractClient};
use rmn_remote::{RmnRemoteContract, RmnRemoteContractClient};
use router::{RouterContract, RouterContractClient};
use token_admin_registry::{TokenAdminRegistryContract, TokenAdminRegistryContractClient};

use crate::types::{DestChainConfigArgs as OnrampDestChainConfigArgs, DynamicConfig, StaticConfig};

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
    let user_args = vec![&env, Bytes::new(&env), Bytes::new(&env)];
    let lane = Vec::new(&env);
    let defaults = vec![&env, Address::generate(&env)];

    let (merged, args) =
        OnRampContract::merge_ccv_lists_with_ccv_args(&env, &user, &user_args, &lane, &defaults)
            .unwrap();
    assert_eq!(merged.len(), 2);
    assert_eq!(args.len(), 2);
    assert_eq!(merged.get(0), Some(a));
    assert_eq!(merged.get(1), Some(b));
}

#[test]
fn test_merge_ccv_lists_falls_back_to_defaults() {
    let env = Env::default();
    let default_ccv = Address::generate(&env);

    let user: Vec<Address> = Vec::new(&env);
    let user_args: Vec<Bytes> = Vec::new(&env);
    let lane: Vec<Address> = Vec::new(&env);
    let defaults = vec![&env, default_ccv.clone()];

    let (merged, args) =
        OnRampContract::merge_ccv_lists_with_ccv_args(&env, &user, &user_args, &lane, &defaults)
            .unwrap();
    assert_eq!(merged.len(), 1);
    assert_eq!(args.len(), 1);
    assert_eq!(merged.get(0), Some(default_ccv));
}

#[test]
fn test_merge_ccv_lists_lane_mandated_appended() {
    let env = Env::default();
    let user_ccv = Address::generate(&env);
    let lane_ccv = Address::generate(&env);

    let user = vec![&env, user_ccv.clone()];
    let user_args = vec![&env, Bytes::new(&env)];
    let lane = vec![&env, lane_ccv.clone()];
    let defaults = vec![&env, Address::generate(&env)];

    let (merged, args) =
        OnRampContract::merge_ccv_lists_with_ccv_args(&env, &user, &user_args, &lane, &defaults)
            .unwrap();
    assert_eq!(merged.len(), 2);
    assert_eq!(args.len(), 2);
    assert_eq!(merged.get(0), Some(user_ccv));
    assert_eq!(merged.get(1), Some(lane_ccv));
}

#[test]
fn test_merge_ccv_lists_deduplication() {
    let env = Env::default();
    let shared = Address::generate(&env);

    let user = vec![&env, shared.clone()];
    let user_args = vec![&env, Bytes::new(&env)];
    let lane = vec![&env, shared.clone()];
    let defaults = vec![&env, Address::generate(&env)];

    let (merged, args) =
        OnRampContract::merge_ccv_lists_with_ccv_args(&env, &user, &user_args, &lane, &defaults)
            .unwrap();
    assert_eq!(merged.len(), 1);
    assert_eq!(args.len(), 1);
    assert_eq!(merged.get(0), Some(shared));
}

#[test]
fn test_merge_ccv_lists_lane_only_no_fallback() {
    let env = Env::default();
    let lane_ccv = Address::generate(&env);

    let user: Vec<Address> = Vec::new(&env);
    let user_args: Vec<Bytes> = Vec::new(&env);
    let lane = vec![&env, lane_ccv.clone()];
    let defaults = vec![&env, Address::generate(&env)];

    let (merged, args) =
        OnRampContract::merge_ccv_lists_with_ccv_args(&env, &user, &user_args, &lane, &defaults)
            .unwrap();
    assert_eq!(merged.len(), 1);
    assert_eq!(args.len(), 1);
    assert_eq!(merged.get(0), Some(lane_ccv));
}

#[test]
fn test_merge_ccv_lists_rejects_duplicate_user_ccvs() {
    // M-16 / INV-SRC-1/17: user-supplied CCVs (from ExtraArgsV3) must not contain
    // duplicates, mirroring EVM `CCVConfigValidation._assertNoDuplicates(userCCVs)`
    // (OnRamp.sol:812). A duplicate would emit duplicate fee receipts and produce a
    // `ccv_and_executor_hash` that won't match offchain expectations. Lane-mandated and
    // pool-required CCVs are deduped against the running list inside the merge; only the
    // user list (cloned verbatim) needs this explicit rejection.
    let env = Env::default();
    let dup = Address::generate(&env);

    let user = vec![&env, dup.clone(), dup.clone()];
    let user_args = vec![&env, Bytes::new(&env), Bytes::new(&env)];
    let lane: Vec<Address> = Vec::new(&env);
    let defaults = vec![&env, Address::generate(&env)];

    let err =
        OnRampContract::merge_ccv_lists_with_ccv_args(&env, &user, &user_args, &lane, &defaults)
            .unwrap_err();
    assert_eq!(err, CCIPError::DuplicateCCVNotAllowed);
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

///! Router → OnRamp outbound flow with a lock-release token pool, asserting CCIPMessageSent
///! receipt ordering: `[CCV…, TokenPool, Executor, NetworkFee]` (EVM / chainlink-ccv parity).

#[contract]
pub struct MockOutboundCcvVerifier;

#[contractimpl]
impl MockOutboundCcvVerifier {
    pub fn get_fee(
        _env: Env,
        _dest_chain_selector: u64,
        _message: Bytes,
        _extra_args: Bytes,
        _block_confirmations: u32,
    ) -> Result<FeeResponse, CCIPError> {
        Ok(FeeResponse {
            dest_bytes_overhead: 0,
            dest_gas_limit: 0,
            fee: 0,
        })
    }

    pub fn forward_to_verifier(
        env: Env,
        _dest_chain_selector: u64,
        _sender: Address,
        _message_id: BytesN<32>,
        _fee_token: Address,
        _fee_token_amount: i128,
        _verifier_args: Bytes,
    ) -> Result<Bytes, CCIPError> {
        Ok(Bytes::new(&env))
    }
}

fn deploy_default_ccv_resolver(env: &Env, owner: &Address, dest_chain_selector: u64) -> Address {
    let verifier_id = env.register(MockOutboundCcvVerifier, ());
    let vvr_id = env.register(VersionedVerifierResolverContract, ());
    let vvr = VersionedVerifierResolverContractClient::new(env, &vvr_id);
    vvr.initialize(owner, &Address::generate(env));
    vvr.apply_outbound_impl_updates(&vec![
        env,
        OutboundImplementationUpdate {
            dest_chain_selector,
            verifier: Some(verifier_id),
        },
    ]);
    vvr_id
}

fn setup_fee_quoter(
    env: &Env,
    owner: &Address,
    dest_chain_selector: u64,
    fee_token: &Address,
    transfer_token: &Address,
) -> Address {
    env.ledger().with_mut(|li| {
        li.timestamp = 1000;
    });

    let fee_quoter_id = env.register(FeeQuoterContract, ());
    let fee_quoter_client = FeeQuoterContractClient::new(env, &fee_quoter_id);

    let link_token = Address::generate(env);
    let static_config = FqStaticConfig {
        max_fee_juels_per_msg: 1_000_000_000_000_000_000_000, // 1e21 (1000 LINK) — sane cap that exceeds realistic per-message fees
        link_token: link_token.clone(),
    };

    let mut authorized_callers: Vec<Address> = Vec::new(env);
    authorized_callers.push_back(owner.clone());

    fee_quoter_client.initialize(owner, &static_config, &authorized_callers);

    let dest_config = DestChainConfig {
        is_enabled: true,
        max_data_bytes: 50000,
        max_per_msg_gas_limit: 4_000_000,
        dest_gas_overhead: 350_000,
        dest_gas_per_payload_byte: 16,
        default_token_fee_usd: 50,
        default_token_dest_gas: 50_000,
        default_tx_gas_limit: 200_000,
        network_fee_usd_cents: 100,
        link_premium_percent: 90,
    };

    let mut config_args: Vec<FqDestChainConfigArgs> = Vec::new(env);
    config_args.push_back(FqDestChainConfigArgs {
        dest_chain_selector,
        config: dest_config,
    });
    fee_quoter_client.apply_dest_chain_configs(&config_args);

    let mut token_updates: Vec<TokenPriceUpdate> = Vec::new(env);
    token_updates.push_back(TokenPriceUpdate {
        token: link_token.clone(),
        usd_per_token: 15_000_000_000_000_000_000,
    });
    token_updates.push_back(TokenPriceUpdate {
        token: fee_token.clone(),
        usd_per_token: 15_000_000_000_000_000_000,
    });
    token_updates.push_back(TokenPriceUpdate {
        token: transfer_token.clone(),
        usd_per_token: 1_000_000_000_000_000_000,
    });

    let mut gas_updates: Vec<GasPriceUpdate> = Vec::new(env);
    gas_updates.push_back(GasPriceUpdate {
        dest_chain_selector,
        usd_per_unit_gas: 100_000_000_000_000,
    });

    fee_quoter_client.update_prices(
        owner,
        &PriceUpdates {
            token_price_updates: token_updates,
            gas_price_updates: gas_updates,
        },
    );

    let token_fee_args = vec![
        env,
        TokenFeeConfigArgs {
            dest_chain_selector,
            token: transfer_token.clone(),
            config: TokenTransferFeeConfig {
                fee_usd_cents: 5000,
                dest_gas_overhead: 75_000,
                dest_bytes_overhead: 64,
                is_enabled: true,
            },
        },
    ];
    fee_quoter_client.apply_token_fee_configs(&token_fee_args, &Vec::new(env));

    fee_quoter_id
}

/// Deploys a real `ExecutorContract`, initializes it (CCV allowlist off, with
/// the given `allowed_finality_config`), and enables `dest_chain_selector` with
/// `usd_cents_fee`. Returns the executor contract address, to use as a lane's
/// `default_executor`. Mirrors EVM `Executor` wiring: the OnRamp cross-contract
/// `get_fee` call hits this real contract, so the test exercises the actual
/// executor interface.
fn setup_executor(
    env: &Env,
    owner: &Address,
    dest_chain_selector: u64,
    usd_cents_fee: u32,
    allowed_finality_config: u32,
) -> Address {
    let executor_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(env, &executor_id);
    let dynamic_config = ExecDynamicConfig {
        fee_aggregator: Some(Address::generate(env)),
        // `allowed_finality_config` is the executor layer of the 5-layer FTF
        // opt-in matrix (H-8). 0 = WAIT_FOR_FINALITY only.
        allowed_finality_config,
        ccv_allowlist_enabled: false,
    };
    client.initialize(owner, &2, &dynamic_config);
    let to_add = vec![
        env,
        ExecRemoteChainConfigArgs {
            dest_chain_selector,
            config: ExecRemoteChainConfig {
                usd_cents_fee,
                enabled: true,
            },
        },
    ];
    client.apply_dest_chain_updates(&Vec::new(env), &to_add);
    executor_id
}

fn receipts_from_last_onramp_ccip_event(env: &Env, onramp: &Address) -> Vec<Receipt> {
    let evs = env.events().all().filter_by_contract(onramp);
    for e in evs.events().iter().rev() {
        let soroban_sdk::xdr::ContractEventBody::V0(ref v0) = e.body;
        let val: Val = v0
            .data
            .clone()
            .try_into_val(env)
            .expect("event data ScVal to Val");
        let Ok(map) = Map::<Symbol, Val>::try_from_val(env, &val) else {
            continue;
        };
        let Some(rval) = map.get(symbol_short!("receipts")) else {
            continue;
        };
        if let Ok(rvec) = Vec::<Receipt>::try_from_val(env, &rval) {
            return rvec;
        }
    }
    panic!("expected CCIPMessageSent event with receipts from onramp");
}

/// Extract the `encoded_message` field from the last `CCIPMessageSent` event emitted by
/// `onramp`. `encoded_message` is >9 chars so it is encoded as a long `Symbol`.
fn encoded_message_from_last_onramp_event(env: &Env, onramp: &Address) -> Bytes {
    let evs = env.events().all().filter_by_contract(onramp);
    for e in evs.events().iter().rev() {
        let soroban_sdk::xdr::ContractEventBody::V0(ref v0) = e.body;
        let val: Val = v0
            .data
            .clone()
            .try_into_val(env)
            .expect("event data ScVal to Val");
        let Ok(map) = Map::<Symbol, Val>::try_from_val(env, &val) else {
            continue;
        };
        let Some(mval) = map.get(Symbol::new(env, "encoded_message")) else {
            continue;
        };
        if let Ok(encoded) = Bytes::try_from_val(env, &mval) {
            return encoded;
        }
    }
    panic!("expected CCIPMessageSent event with encoded_message from onramp");
}

#[test]
fn test_ccip_send_emits_token_pool_receipt_before_executor_and_network_fee() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let sender = Address::generate(&env);

    let stellar_chain_selector: u64 = 12345;
    let evm_chain_selector: u64 = 67890;

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    let rmn_proxy_client = RmnProxyContractClient::new(&env, &rmn_proxy_id);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let router_id = env.register(RouterContract, ());
    let router_client = RouterContractClient::new(&env, &router_id);
    router_client.initialize(&owner, &rmn_proxy_id);

    let onramp_id = env.register(OnRampContract, ());
    let onramp_client = OnRampContractClient::new(&env, &onramp_id);

    let fee_token_admin = Address::generate(&env);
    let fee_token_contract = env.register_stellar_asset_contract_v2(fee_token_admin.clone());
    let fee_token = fee_token_contract.address();
    let fee_token_sac = token::StellarAssetClient::new(&env, &fee_token);

    let transfer_token_admin = Address::generate(&env);
    let transfer_token_contract =
        env.register_stellar_asset_contract_v2(transfer_token_admin.clone());
    let transfer_token = transfer_token_contract.address();
    let transfer_token_sac = token::StellarAssetClient::new(&env, &transfer_token);

    let ramp_registry_id = env.register(RampRegistryContract, ());
    let ramp_registry_client = RampRegistryContractClient::new(&env, &ramp_registry_id);
    ramp_registry_client.initialize(&owner);

    let pool_id = env.register(LockReleaseTokenPoolContract, ());
    let pool_client = LockReleaseTokenPoolContractClient::new(&env, &pool_id);
    pool_client.initialize(
        &owner,
        &transfer_token,
        &7u32,
        &router_id,
        &ramp_registry_client.address,
        &rmn_proxy_id,
    );
    // M-6: the pool's `lock_or_burn` curse check reads the RMN proxy from pool
    // storage (set at `initialize`, mirroring EVM's immutable constructor arg), not
    // via `Router.get_config()` (which would re-enter the Router mid-`ccip_send`).
    // `rmn_proxy_id` is the same RMN proxy the Router was initialized with above.

    let remote_pool = Bytes::from_slice(&env, &[0x11u8; 20]);
    let remote_token = Bytes::from_slice(&env, &[0x22u8; 20]);
    pool_client.apply_chain_updates(
        &vec![
            &env,
            ChainUpdate {
                remote_chain_selector: evm_chain_selector,
                remote_pool_addresses: remote_pool,
                remote_token_address: remote_token,
                outbound_rate_limiter_config: RateLimitConfig::disabled(),
                inbound_rate_limiter_config: RateLimitConfig::disabled(),
            },
        ],
        &Vec::new(&env),
    );

    // L-4: the canonical lock-release pool escrows locked tokens in a TokenLockBox
    // (parity with its siloed sibling). `lock_or_burn` resolves the lockbox for the
    // destination chain via `configure_lock_boxes`; without it, `resolve_lock_box`
    // reverts InvalidConfig (#52). The lockbox holds the transfer token.
    let lockbox_id = env.register(TokenLockBox, ());
    let lockbox_client = TokenLockBoxClient::new(&env, &lockbox_id);
    lockbox_client.initialize(&owner, &transfer_token);
    lockbox_client.add_allowed_callers(&vec![&env, pool_client.address.clone()]);
    pool_client.configure_lock_boxes(&vec![
        &env,
        LockBoxEntry {
            remote_chain_selector: evm_chain_selector,
            lock_box: lockbox_client.address.clone(),
        },
    ]);

    ramp_registry_client.apply_onramp_updates(&vec![
        &env,
        OnRampUpdate {
            dest_chain_selector: evm_chain_selector,
            onramp: Some(onramp_id.clone()),
        },
    ]);

    let tar_id = env.register(TokenAdminRegistryContract, ());
    let tar_client = TokenAdminRegistryContractClient::new(&env, &tar_id);
    tar_client.initialize(&owner);

    let token_registry_admin = Address::generate(&env);
    tar_client.propose_administrator(&owner, &transfer_token, &token_registry_admin);
    tar_client.accept_admin_role(&transfer_token);
    tar_client.set_pool(&transfer_token, &Some(pool_id.clone()));

    let fee_quoter_id = setup_fee_quoter(
        &env,
        &owner,
        evm_chain_selector,
        &fee_token,
        &transfer_token,
    );

    let static_config = StaticConfig {
        chain_selector: stellar_chain_selector,
        token_admin_registry: tar_id.clone(),
        rmn_proxy: rmn_proxy_id.clone(),
        max_usd_cents_per_message: 100_000,
    };

    let fee_aggregator = Address::generate(&env);
    let dynamic_config = DynamicConfig {
        fee_quoter: fee_quoter_id,
        fee_aggregator: fee_aggregator.clone(),
    };

    onramp_client.initialize(&owner, &static_config, &dynamic_config);

    let default_ccv = deploy_default_ccv_resolver(&env, &owner, evm_chain_selector);
    // Real Executor contract (EVM parity): the OnRamp cross-contract `get_fee`
    // call hits this contract. Configured with a 25-cent flat fee, matching the
    // legacy `execution_fee_usd_cents` so existing fee-magnitude expectations hold.
    let default_executor = setup_executor(&env, &owner, evm_chain_selector, 25, 0);

    let dest_chain_config = OnrampDestChainConfigArgs {
        dest_chain_selector: evm_chain_selector,
        router: router_id.clone(),
        address_bytes_length: 20,
        token_receiver_allowed: true,
        message_network_fee_usd_cents: 50,
        token_network_fee_usd_cents: 100,
        base_execution_gas_cost: 200_000,
        execution_fee_usd_cents: 25,
        default_executor: default_executor.clone(),
        lane_mandated_ccvs: Vec::new(&env),
        default_ccvs: vec![&env, default_ccv.clone()],
        off_ramp: Bytes::from_array(&env, &[0u8; 20]),
    };

    onramp_client.apply_dest_chain_config_updates(&vec![&env, dest_chain_config]);

    router_client.set_onramp(&evm_chain_selector, &onramp_id);

    let mut token_amounts: Vec<TokenAmount> = Vec::new(&env);
    token_amounts.push_back(TokenAmount {
        token: transfer_token.clone(),
        amount: 1_000_000,
    });

    let message = StellarToAnyMessage {
        receiver: Bytes::from_array(&env, &[0x33u8; 20]),
        data: Bytes::from_slice(&env, b"token send with data"),
        token_amounts,
        fee_token: fee_token.clone(),
        extra_args: Bytes::new(&env),
    };

    let required_fee = router_client.get_fee(&evm_chain_selector, &message);
    assert!(required_fee > 0, "quoted fee must be positive");

    fee_token_sac.mint(&sender, &(required_fee * 2));
    transfer_token_sac.mint(&sender, &1_000_000);

    let message_id = router_client.ccip_send(&sender, &evm_chain_selector, &message, &required_fee);
    assert_ne!(
        message_id,
        BytesN::from_array(&env, &[0u8; 32]),
        "message id must be non-zero"
    );

    let receipts = receipts_from_last_onramp_ccip_event(&env, &onramp_id);
    assert_eq!(
        receipts.len(),
        4,
        "expected 1 CCV + pool + executor + network"
    );

    assert_eq!(receipts.get(0).unwrap().issuer, default_ccv);
    assert_eq!(receipts.get(1).unwrap().issuer, pool_id);
    // H-5 / INV-SRC-5: the pool receipt no longer hardcodes dest_gas_limit /
    // dest_bytes_overhead to 0. The pool's own `get_fee` is disabled here, so the
    // OnRamp falls back to the FeeQuoter's per-token `TokenTransferFeeConfig`
    // (set in `setup_fee_quoter`: dest_gas_overhead=75_000, dest_bytes_overhead=64).
    assert_eq!(receipts.get(1).unwrap().dest_gas_limit, 75_000);
    assert_eq!(receipts.get(1).unwrap().dest_bytes_overhead, 64);

    assert_eq!(receipts.get(2).unwrap().issuer, default_executor);
    assert_eq!(receipts.get(3).unwrap().issuer, router_id);

    // H-3 / INV-FEE-19: the executor fee (flat 25 cents + priced exec gas) is
    // transferred to the Executor contract at send time. The executor receipt
    // carries the same amount (USD cents) and must be positive.
    let executor_receipt_fee = receipts.get(2).unwrap().fee_token_amount;
    assert!(
        executor_receipt_fee > 0,
        "executor receipt fee must be positive"
    );

    // H-2 / INV-TR-3: with empty `extra_args` (no `token_receiver`), the encoded token
    // transfer's `token_receiver` must default to `message.receiver` (EVM `OnRamp.sol:311`).
    // NOTE: this event extraction MUST run before any further contract invocation below —
    // `env.events()` in the test env only reflects events from the most-recent contract
    // call, so a `token::Client::balance` query would clear the CCIPMessageSent event.
    let encoded = encoded_message_from_last_onramp_event(&env, &onramp_id);
    let decoded = CcipMessageV1::from_bytes(&env, &encoded).expect("decode encoded message");
    let token_transfer = CcipTokenTransferV1::from_bytes(&env, &decoded.token_transfer)
        .expect("decode token transfer");
    assert_eq!(
        token_transfer.token_receiver, message.receiver,
        "empty tokenReceiver must default to the message receiver"
    );

    // H-3 balance check (contract invocation) — deliberately last, after all event
    // extraction, for the reason noted above.
    let fee_token_client = token::Client::new(&env, &fee_token);
    assert!(
        fee_token_client.balance(&default_executor) > 0,
        "executor fee must be transferred to the executor contract (H-3)"
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #44)")] // FeeExceedsMaxAllowed
fn test_get_fee_reverts_when_fee_exceeds_max_usd_cents_per_message() {
    // H-4: the OnRamp per-message fee cap (`max_usd_cents_per_message`) must be
    // enforced on the TOTAL user-paid fee, mirroring EVM `OnRamp.sol:1104`
    // (`FeeExceedsMaxAllowed`). Here the cap is set to 1 cent ($0.01) while the
    // quoted fee (network fee alone is 50 cents) far exceeds it, so `get_fee`
    // must revert. A data-only message with empty `default_ccvs` avoids the
    // pool / token-admin-registry / router / ramp-registry setup, exercising the
    // cap in isolation. The positive case (fee under cap) is already covered by
    // `test_ccip_send_emits_token_pool_receipt_before_executor_and_network_fee`,
    // which uses a $1000 cap and sends successfully.
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let stellar_chain_selector: u64 = 12345;
    let evm_chain_selector: u64 = 67890;

    // RMN (required by the OnRamp curse check in `get_fee`).
    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    let rmn_proxy_client = RmnProxyContractClient::new(&env, &rmn_proxy_id);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    // `get_fee` only prices the fee token (no transfer), so a bare address that
    // `setup_fee_quoter` registers a price for is sufficient.
    let fee_token = Address::generate(&env);
    let transfer_token = Address::generate(&env);

    let fee_quoter_id = setup_fee_quoter(
        &env,
        &owner,
        evm_chain_selector,
        &fee_token,
        &transfer_token,
    );

    let onramp_id = env.register(OnRampContract, ());
    let onramp_client = OnRampContractClient::new(&env, &onramp_id);

    let static_config = StaticConfig {
        chain_selector: stellar_chain_selector,
        token_admin_registry: Address::generate(&env),
        rmn_proxy: rmn_proxy_id.clone(),
        // Deliberately tiny: $0.01. Any realistic quote (network fee = 50 cents)
        // exceeds it, so the cap must trip.
        max_usd_cents_per_message: 1,
    };
    let dynamic_config = DynamicConfig {
        fee_quoter: fee_quoter_id,
        fee_aggregator: Address::generate(&env),
    };
    onramp_client.initialize(&owner, &static_config, &dynamic_config);

    let default_ccv = deploy_default_ccv_resolver(&env, &owner, evm_chain_selector);
    // Real Executor (25-cent flat fee) so the OnRamp `get_fee` cross-contract call
    // succeeds; the cap then trips on the total (network 50 cents alone already
    // exceeds the 1-cent cap).
    let default_executor = setup_executor(&env, &owner, evm_chain_selector, 25, 0);

    let dest_chain_config = OnrampDestChainConfigArgs {
        dest_chain_selector: evm_chain_selector,
        router: Address::generate(&env),
        address_bytes_length: 20,
        token_receiver_allowed: true,
        message_network_fee_usd_cents: 50,
        token_network_fee_usd_cents: 100,
        base_execution_gas_cost: 200_000,
        execution_fee_usd_cents: 25,
        default_executor: default_executor.clone(),
        lane_mandated_ccvs: Vec::new(&env),
        default_ccvs: vec![&env, default_ccv.clone()],
        off_ramp: Bytes::from_array(&env, &[0u8; 20]),
    };
    onramp_client.apply_dest_chain_config_updates(&vec![&env, dest_chain_config]);

    let message = StellarToAnyMessage {
        receiver: Bytes::from_array(&env, &[0x33u8; 20]),
        data: Bytes::from_slice(&env, b"trips the per-message fee cap"),
        token_amounts: Vec::new(&env),
        fee_token: fee_token.clone(),
        extra_args: Bytes::new(&env),
    };

    // Must revert with FeeExceedsMaxAllowed (#44), not quote a fee.
    onramp_client.get_fee(&evm_chain_selector, &message);
}

#[test]
#[should_panic(expected = "Error(Contract, #43)")] // InvalidDestChainAddress
fn test_get_fee_reverts_when_receiver_length_mismatch() {
    // M-2 / INV-MSG-8: the destination `receiver` must be exactly
    // `dest_config.address_bytes_length` bytes, mirroring EVM `OnRamp._validateDestChainAddress`
    // (`if (len != addressBytesLength) revert InvalidDestChainAddress`, OnRamp.sol:471-503). Here
    // the lane is configured with `address_bytes_length: 20` but the message carries a 19-byte
    // receiver, so `get_fee` must revert before quoting. The per-message fee cap is set high so
    // the address-length check is the only thing that can trip.
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let stellar_chain_selector: u64 = 12345;
    let evm_chain_selector: u64 = 67890;

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    let rmn_proxy_client = RmnProxyContractClient::new(&env, &rmn_proxy_id);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let fee_token = Address::generate(&env);
    let transfer_token = Address::generate(&env);

    let fee_quoter_id = setup_fee_quoter(
        &env,
        &owner,
        evm_chain_selector,
        &fee_token,
        &transfer_token,
    );

    let onramp_id = env.register(OnRampContract, ());
    let onramp_client = OnRampContractClient::new(&env, &onramp_id);

    let static_config = StaticConfig {
        chain_selector: stellar_chain_selector,
        token_admin_registry: Address::generate(&env),
        rmn_proxy: rmn_proxy_id.clone(),
        // High cap so the address-length check is the sole revert reason.
        max_usd_cents_per_message: 100_000,
    };
    let dynamic_config = DynamicConfig {
        fee_quoter: fee_quoter_id,
        fee_aggregator: Address::generate(&env),
    };
    onramp_client.initialize(&owner, &static_config, &dynamic_config);

    let default_ccv = deploy_default_ccv_resolver(&env, &owner, evm_chain_selector);

    let dest_chain_config = OnrampDestChainConfigArgs {
        dest_chain_selector: evm_chain_selector,
        router: Address::generate(&env),
        address_bytes_length: 20,
        token_receiver_allowed: true,
        message_network_fee_usd_cents: 50,
        token_network_fee_usd_cents: 100,
        base_execution_gas_cost: 200_000,
        execution_fee_usd_cents: 25,
        default_executor: Address::generate(&env),
        lane_mandated_ccvs: Vec::new(&env),
        default_ccvs: vec![&env, default_ccv.clone()],
        off_ramp: Bytes::from_array(&env, &[0u8; 20]),
    };
    onramp_client.apply_dest_chain_config_updates(&vec![&env, dest_chain_config]);

    let message = StellarToAnyMessage {
        // 19 bytes ≠ address_bytes_length (20) ⇒ InvalidDestChainAddress.
        receiver: Bytes::from_array(&env, &[0x33u8; 19]),
        data: Bytes::from_slice(&env, b"receiver length mismatch"),
        token_amounts: Vec::new(&env),
        fee_token: fee_token.clone(),
        extra_args: Bytes::new(&env),
    };

    onramp_client.get_fee(&evm_chain_selector, &message);
}

#[test]
#[should_panic(expected = "Error(Contract, #316)")] // RequestedFinalityCanOnlyHaveOneMode
fn test_get_fee_reverts_when_requested_finality_malformed() {
    // M-8 / INV-FIN-SRC-1/3: a requested finality combining a flag with a block depth
    // (here WAIT_FOR_SAFE_FLAG | 5 = 0x1_0005) must be rejected before it is committed
    // verbatim into the message ID for data-only messages. Mirrors EVM
    // `FinalityCodec._validateRequestedFinality`, invoked on the parsed extraArgs. The
    // receiver is a valid 20-byte address and the per-message fee cap is high, so the
    // finality-shape check is the sole revert reason.
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let stellar_chain_selector: u64 = 12345;
    let evm_chain_selector: u64 = 67890;

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    let rmn_proxy_client = RmnProxyContractClient::new(&env, &rmn_proxy_id);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let fee_token = Address::generate(&env);
    let transfer_token = Address::generate(&env);

    let fee_quoter_id = setup_fee_quoter(
        &env,
        &owner,
        evm_chain_selector,
        &fee_token,
        &transfer_token,
    );

    let onramp_id = env.register(OnRampContract, ());
    let onramp_client = OnRampContractClient::new(&env, &onramp_id);

    let default_executor = Address::generate(&env);
    let static_config = StaticConfig {
        chain_selector: stellar_chain_selector,
        token_admin_registry: Address::generate(&env),
        rmn_proxy: rmn_proxy_id.clone(),
        // High cap so the finality-shape check is the sole revert reason.
        max_usd_cents_per_message: 100_000,
    };
    let dynamic_config = DynamicConfig {
        fee_quoter: fee_quoter_id,
        fee_aggregator: Address::generate(&env),
    };
    onramp_client.initialize(&owner, &static_config, &dynamic_config);

    let default_ccv = deploy_default_ccv_resolver(&env, &owner, evm_chain_selector);

    let dest_chain_config = OnrampDestChainConfigArgs {
        dest_chain_selector: evm_chain_selector,
        router: Address::generate(&env),
        address_bytes_length: 20,
        token_receiver_allowed: true,
        message_network_fee_usd_cents: 50,
        token_network_fee_usd_cents: 100,
        base_execution_gas_cost: 200_000,
        execution_fee_usd_cents: 25,
        default_executor: default_executor.clone(),
        lane_mandated_ccvs: Vec::new(&env),
        default_ccvs: vec![&env, default_ccv.clone()],
        off_ramp: Bytes::from_array(&env, &[0u8; 20]),
    };
    onramp_client.apply_dest_chain_config_updates(&vec![&env, dest_chain_config]);

    // WAIT_FOR_SAFE_FLAG (1 << 16) | 5 ⇒ a flag combined with a block depth ⇒ malformed.
    let extra_args = GenericExtraArgsV3 {
        gas_limit: 0,
        block_confirmations: 0x1_0005,
        ccvs: Vec::new(&env),
        ccv_args: Vec::new(&env),
        executor: default_executor.clone(),
        executor_args: Bytes::new(&env),
        token_receiver: Bytes::new(&env),
        token_args: Bytes::new(&env),
    };

    let message = StellarToAnyMessage {
        // Valid 20-byte receiver (== address_bytes_length) so validate_dest_address passes.
        receiver: Bytes::from_array(&env, &[0x33u8; 20]),
        data: Bytes::from_slice(&env, b"malformed requested finality"),
        token_amounts: Vec::new(&env),
        fee_token: fee_token.clone(),
        extra_args: extra_args.to_xdr(&env),
    };

    onramp_client.get_fee(&evm_chain_selector, &message);
}

// ============================================================
// Executor sentinel + executor-layer finality tests (H-3/H-5/H-8/M-5/M-7)
// ============================================================

// WAIT_FOR_SAFE flag (single mode, valid shape) — requesting this against an
// executor that only allows WAIT_FOR_FINALITY must trip the executor-layer FTF
// opt-in check (#315).
const EXEC_TEST_WAIT_FOR_SAFE: u32 = 1 << 16;

/// A data-only (no token transfer) Stellar→EVM lane with a real `Executor`
/// contract wired as `default_executor`. Avoids the pool / ramp-registry /
/// token-admin-registry setup so the executor slice can be exercised in
/// isolation. Fields are held by value (soroban clients own their `Env`).
struct DataOnlyLane {
    env: Env,
    sender: Address,
    evm_chain_selector: u64,
    onramp_id: Address,
    onramp_client: OnRampContractClient<'static>,
    router_client: RouterContractClient<'static>,
    fee_token: Address,
    fee_token_sac: token::StellarAssetClient<'static>,
    default_executor: Address,
}

impl DataOnlyLane {
    /// Send a data-only message with the given extra args; return the receipts
    /// emitted in the `CCIPMessageSent` event. Receipt extraction MUST run
    /// immediately after `ccip_send` — `env.events()` reflects only the most
    /// recent top-level contract invocation, so any intervening contract call
    /// (e.g. a `balance` query) would wipe the event view.
    fn send_data_only(&self, extra_args: GenericExtraArgsV3) -> Vec<Receipt> {
        self.send_data_only_full(extra_args).0
    }

    /// Like `send_data_only` but also returns the `encoded_message` (canonical
    /// `CcipMessageV1` bytes) from the same `CCIPMessageSent` event, so callers
    /// can inspect fields committed to the message ID (e.g. the
    /// `ccv_and_executor_hash`) without a second event extraction that would
    /// race the event view. Both extractions run back-to-back right after
    /// `ccip_send`, before any other contract call.
    fn send_data_only_full(&self, extra_args: GenericExtraArgsV3) -> (Vec<Receipt>, Bytes) {
        let env = &self.env;
        let message = StellarToAnyMessage {
            receiver: Bytes::from_array(env, &[0x33u8; 20]),
            data: Bytes::from_slice(env, b"data-only send"),
            token_amounts: Vec::new(env),
            fee_token: self.fee_token.clone(),
            extra_args: extra_args.to_xdr(env),
        };
        let required_fee = self
            .router_client
            .get_fee(&self.evm_chain_selector, &message);
        assert!(required_fee > 0, "quoted fee must be positive");
        self.fee_token_sac.mint(&self.sender, &(required_fee * 2));
        self.router_client.ccip_send(
            &self.sender,
            &self.evm_chain_selector,
            &message,
            &required_fee,
        );
        let receipts = receipts_from_last_onramp_ccip_event(env, &self.onramp_id);
        let encoded = encoded_message_from_last_onramp_event(env, &self.onramp_id);
        (receipts, encoded)
    }
}

fn setup_data_only_lane(
    executor_usd_cents_fee: u32,
    executor_allowed_finality: u32,
) -> DataOnlyLane {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let sender = Address::generate(&env);
    let stellar_chain_selector: u64 = 12345;
    let evm_chain_selector: u64 = 67890;

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    let rmn_proxy_client = RmnProxyContractClient::new(&env, &rmn_proxy_id);
    rmn_proxy_client.initialize(&owner, &rmn_remote_id);

    let router_id = env.register(RouterContract, ());
    let router_client = RouterContractClient::new(&env, &router_id);
    router_client.initialize(&owner, &rmn_proxy_id);

    let onramp_id = env.register(OnRampContract, ());
    let onramp_client = OnRampContractClient::new(&env, &onramp_id);

    let fee_token_admin = Address::generate(&env);
    let fee_token_contract = env.register_stellar_asset_contract_v2(fee_token_admin.clone());
    let fee_token = fee_token_contract.address();
    let fee_token_sac = token::StellarAssetClient::new(&env, &fee_token);

    // `transfer_token` is unused for a data-only send, but `setup_fee_quoter`
    // registers a price for it.
    let transfer_token = Address::generate(&env);
    let fee_quoter_id = setup_fee_quoter(
        &env,
        &owner,
        evm_chain_selector,
        &fee_token,
        &transfer_token,
    );

    let static_config = StaticConfig {
        chain_selector: stellar_chain_selector,
        token_admin_registry: Address::generate(&env),
        rmn_proxy: rmn_proxy_id.clone(),
        max_usd_cents_per_message: 100_000, // $1000 cap
    };
    let dynamic_config = DynamicConfig {
        fee_quoter: fee_quoter_id,
        fee_aggregator: Address::generate(&env),
    };
    onramp_client.initialize(&owner, &static_config, &dynamic_config);

    let default_ccv = deploy_default_ccv_resolver(&env, &owner, evm_chain_selector);
    let default_executor = setup_executor(
        &env,
        &owner,
        evm_chain_selector,
        executor_usd_cents_fee,
        executor_allowed_finality,
    );

    let dest_chain_config = OnrampDestChainConfigArgs {
        dest_chain_selector: evm_chain_selector,
        router: router_id.clone(),
        address_bytes_length: 20,
        token_receiver_allowed: true,
        message_network_fee_usd_cents: 50,
        token_network_fee_usd_cents: 100,
        base_execution_gas_cost: 200_000,
        execution_fee_usd_cents: 25,
        default_executor: default_executor.clone(),
        lane_mandated_ccvs: Vec::new(&env),
        default_ccvs: vec![&env, default_ccv.clone()],
        off_ramp: Bytes::from_array(&env, &[0u8; 20]),
    };
    onramp_client.apply_dest_chain_config_updates(&vec![&env, dest_chain_config]);
    router_client.set_onramp(&evm_chain_selector, &onramp_id);

    DataOnlyLane {
        env,
        sender,
        evm_chain_selector,
        onramp_id,
        onramp_client,
        router_client,
        fee_token,
        fee_token_sac,
        default_executor,
    }
}

/// M-7 / INV-NOEXEC-1/2, INV-FEE-9: a message whose `executor` field is the
/// no-execution sentinel must (a) leave the sentinel in place as the executor
/// receipt issuer, (b) zero the executor flat fee AND the execution-gas cost,
/// and (c) NOT transfer any fee token to the sentinel (no auto-execution). The
/// OnRamp must not call `Executor::get_fee` on the sentinel.
#[test]
fn test_no_execution_sentinel_zero_executor_fee_and_no_transfer() {
    let lane = setup_data_only_lane(25, 0);
    let env = &lane.env;

    let no_exec = GenericExtraArgsV3::no_execution_address(env);
    let extra_args = GenericExtraArgsV3 {
        gas_limit: 0,
        block_confirmations: 0,
        ccvs: Vec::new(env),
        ccv_args: Vec::new(env),
        executor: no_exec.clone(),
        executor_args: Bytes::new(env),
        token_receiver: Bytes::new(env),
        token_args: Bytes::new(env),
    };

    let receipts = lane.send_data_only(extra_args);
    // Data-only: [CCV, Executor, NetworkFee] (no pool row).
    assert_eq!(receipts.len(), 3, "expected 1 CCV + executor + network");
    assert_eq!(
        receipts.get(1).unwrap().issuer,
        no_exec,
        "executor receipt issuer must be the no-execution sentinel"
    );
    assert_eq!(
        receipts.get(1).unwrap().fee_token_amount,
        0,
        "no-execution sentinel must yield zero executor fee (flat + exec gas)"
    );

    // No H-3 transfer to the sentinel. (Contract call — run last, after event
    // extraction, since it clears the event view.)
    let fee_token_client = token::Client::new(env, &lane.fee_token);
    assert_eq!(
        fee_token_client.balance(&no_exec),
        0,
        "no-execution sentinel must receive no fee token transfer (M-7)"
    );
}

/// M-5 / INV-ENC-5: a non-empty `extra_args` whose `executor` field is the
/// "use default" sentinel must resolve to the lane's concrete `default_executor`
/// BEFORE hashing and before `Executor::get_fee`. Proved by the send succeeding
/// (the sentinel address itself has no `get_fee` contract, so an unresolved
/// sentinel would panic) and the executor receipt issuer being the real
/// default executor with a positive fee.
#[test]
fn test_use_default_sentinel_resolves_to_default_executor() {
    let lane = setup_data_only_lane(25, 0);
    let env = &lane.env;

    let use_default = GenericExtraArgsV3::use_default_executor_address(env);
    // Non-empty extra_args customizing gas_limit while requesting the default
    // executor via the sentinel (the ergonomic gap closed by M-5).
    let extra_args = GenericExtraArgsV3 {
        gas_limit: 100_000,
        block_confirmations: 0,
        ccvs: Vec::new(env),
        ccv_args: Vec::new(env),
        executor: use_default,
        executor_args: Bytes::new(env),
        token_receiver: Bytes::new(env),
        token_args: Bytes::new(env),
    };

    let receipts = lane.send_data_only(extra_args);
    assert_eq!(receipts.len(), 3, "expected 1 CCV + executor + network");
    assert_eq!(
        receipts.get(1).unwrap().issuer,
        lane.default_executor,
        "use-default sentinel must resolve to the lane default_executor before receipt"
    );
    assert!(
        receipts.get(1).unwrap().fee_token_amount > 0,
        "resolved default executor must charge a positive fee (flat + exec gas)"
    );

    // H-3: the executor fee is transferred to the resolved (concrete) executor.
    let fee_token_client = token::Client::new(env, &lane.fee_token);
    assert!(
        fee_token_client.balance(&lane.default_executor) > 0,
        "resolved default executor must receive the fee transfer (H-3)"
    );
}

/// M-5 / INV-ENC-5 (hash-stability half): the OnRamp must resolve the
/// "use default" sentinel to `default_executor` BEFORE computing
/// `ccv_and_executor_hash`, so a message sent with the sentinel commits the
/// SAME hash as a message sent with the concrete `default_executor` directly
/// (identical CCVs, identical gas). The companion test above proves the
/// resolution (receipt issuer + fee); this one proves the hash consequence —
/// the part that keeps the message ID stable across the `address(0)→default`
/// parity. `ccv_and_executor_hash` excludes the sequence number, so the two
/// sends (which increment the sequence) still produce comparable hashes,
/// isolating the executor-resolution effect.
#[test]
fn test_use_default_sentinel_hash_matches_concrete_default() {
    let lane = setup_data_only_lane(25, 0);
    let env = &lane.env;

    // Send with the use-default sentinel.
    let use_default = GenericExtraArgsV3::use_default_executor_address(env);
    let sentinel_args = GenericExtraArgsV3 {
        gas_limit: 100_000,
        block_confirmations: 0,
        ccvs: Vec::new(env),
        ccv_args: Vec::new(env),
        executor: use_default,
        executor_args: Bytes::new(env),
        token_receiver: Bytes::new(env),
        token_args: Bytes::new(env),
    };
    let (_, encoded_sentinel) = lane.send_data_only_full(sentinel_args);
    let hash_sentinel = CcipMessageV1::from_bytes(env, &encoded_sentinel)
        .expect("decode sentinel send")
        .ccv_and_executor_hash;

    // Send the same message with the concrete default_executor directly.
    let concrete_args = GenericExtraArgsV3 {
        gas_limit: 100_000,
        block_confirmations: 0,
        ccvs: Vec::new(env),
        ccv_args: Vec::new(env),
        executor: lane.default_executor.clone(),
        executor_args: Bytes::new(env),
        token_receiver: Bytes::new(env),
        token_args: Bytes::new(env),
    };
    let (_, encoded_concrete) = lane.send_data_only_full(concrete_args);
    let hash_concrete = CcipMessageV1::from_bytes(env, &encoded_concrete)
        .expect("decode concrete send")
        .ccv_and_executor_hash;

    assert_eq!(
        hash_sentinel, hash_concrete,
        "use-default sentinel must hash identically to the concrete default_executor \
         (resolution before hashing — M-5 message-ID-stability parity)"
    );
}

/// H-8 / INV-FIN-EXEC-1/2, INV-FEE-8: the executor layer of the 5-layer FTF
/// opt-in matrix is enforced end-to-end through the cross-contract
/// `Executor::get_fee` call. The executor allows only WAIT_FOR_FINALITY (0);
/// requesting WAIT_FOR_SAFE (a valid single mode, so the OnRamp's own
/// malformed-finality check passes) must make the executor revert with
/// InvalidRequestedFinality (#315).
#[test]
#[should_panic(expected = "Error(Contract, #315)")] // InvalidRequestedFinality
fn test_disallowed_finality_reverts_end_to_end_via_executor() {
    let lane = setup_data_only_lane(25, 0); // executor allows WAIT_FOR_FINALITY only
    let env = &lane.env;

    let extra_args = GenericExtraArgsV3 {
        gas_limit: 0,
        block_confirmations: EXEC_TEST_WAIT_FOR_SAFE, // valid shape, but disallowed by executor
        ccvs: Vec::new(env),
        ccv_args: Vec::new(env),
        executor: lane.default_executor.clone(),
        executor_args: Bytes::new(env),
        token_receiver: Bytes::new(env),
        token_args: Bytes::new(env),
    };

    let message = StellarToAnyMessage {
        receiver: Bytes::from_array(env, &[0x33u8; 20]),
        data: Bytes::from_slice(env, b"disallowed finality"),
        token_amounts: Vec::new(env),
        fee_token: lane.fee_token.clone(),
        extra_args: extra_args.to_xdr(env),
    };

    lane.onramp_client
        .get_fee(&lane.evm_chain_selector, &message);
}

/// A lane's `default_executor` may not be the no-execution sentinel — the
/// default must be a real, auto-executing contract (EVM `OnRamp.sol:653-656`).
/// `apply_dest_chain_config_updates` must reject it with InvalidAddress (#56).
#[test]
#[should_panic(expected = "Error(Contract, #56)")] // InvalidAddress
fn test_apply_dest_chain_updates_rejects_no_exec_sentinel_default_executor() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);
    client.initialize(
        &owner,
        &create_test_static_config(&env),
        &create_test_dynamic_config(&env),
    );

    let mut args = create_test_dest_chain_config_args(&env, 67890);
    args.default_executor = GenericExtraArgsV3::no_execution_address(&env);
    client.apply_dest_chain_config_updates(&vec![&env, args]);
}

/// A lane's `default_executor` may not be the zero account (EVM `address(0)`
/// parity). `apply_dest_chain_config_updates` must reject it with
/// InvalidAddress (#56).
#[test]
#[should_panic(expected = "Error(Contract, #56)")] // InvalidAddress
fn test_apply_dest_chain_updates_rejects_zero_account_default_executor() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let contract_id = env.register(OnRampContract, ());
    let client = OnRampContractClient::new(&env, &contract_id);
    client.initialize(
        &owner,
        &create_test_static_config(&env),
        &create_test_dynamic_config(&env),
    );

    let mut args = create_test_dest_chain_config_args(&env, 67890);
    args.default_executor = Address::from_str(
        &env,
        "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
    );
    client.apply_dest_chain_config_updates(&vec![&env, args]);
}
