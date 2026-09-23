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
use common_helpers::fee_math;
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

/// Generalized `deploy_default_ccv_resolver`: deploy a VVR wired to the given
/// verifier implementation (e.g. a fee-charging mock) for `dest_chain_selector`.
/// The VVR address is what the OnRamp pays CCV fees to (the receipt `issuer`).
fn deploy_ccv_resolver_with_verifier(
    env: &Env,
    owner: &Address,
    dest_chain_selector: u64,
    verifier_id: &Address,
) -> Address {
    let vvr_id = env.register(VersionedVerifierResolverContract, ());
    let vvr = VersionedVerifierResolverContractClient::new(env, &vvr_id);
    vvr.initialize(owner, &Address::generate(env));
    vvr.apply_outbound_impl_updates(&vec![
        env,
        OutboundImplementationUpdate {
            dest_chain_selector,
            verifier: Some(verifier_id.clone()),
        },
    ]);
    vvr_id
}

/// Mock outbound CCV verifier that charges a configurable non-zero fee (stored
/// in instance storage via `set_fee`), so H-3 CCV-fee-distribution tests can
/// observe non-trivial CCV fee slices. `MockOutboundCcvVerifier` above always
/// returns `fee: 0`.
#[contract]
pub struct MockOutboundCcvVerifierFee;

#[contractimpl]
impl MockOutboundCcvVerifierFee {
    pub fn set_fee(env: Env, fee: u32) {
        env.storage().instance().set(&symbol_short!("fee"), &fee);
    }
    pub fn get_fee(
        env: Env,
        _dest_chain_selector: u64,
        _message: Bytes,
        _extra_args: Bytes,
        _block_confirmations: u32,
    ) -> Result<FeeResponse, CCIPError> {
        let fee: u32 = env
            .storage()
            .instance()
            .get(&symbol_short!("fee"))
            .unwrap_or(0);
        Ok(FeeResponse {
            dest_bytes_overhead: 0,
            dest_gas_limit: 0,
            fee,
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

/// Deploy a fee-charging CCV (VVR → `MockOutboundCcvVerifierFee` with `fee`
/// cents). Returns the VVR address (the CCV fee recipient / receipt issuer).
fn deploy_fee_charging_ccv(
    env: &Env,
    owner: &Address,
    dest_chain_selector: u64,
    fee_usd_cents: u32,
) -> Address {
    let verifier_id = env.register(MockOutboundCcvVerifierFee, ());
    let verifier = MockOutboundCcvVerifierFeeClient::new(env, &verifier_id);
    verifier.set_fee(&fee_usd_cents);
    deploy_ccv_resolver_with_verifier(env, owner, dest_chain_selector, &verifier_id)
}

/// Default fee-quoter setup for the non-LINK lanes: the fee token is a generic
/// SAC asset (not LINK), so a throwaway `link_token` is registered and
/// `link_premium_percent` is irrelevant (`premium_multiplier` is 100 whenever
/// `fee_token != link_token`). All existing H-3/non-LINK tests use this.
fn setup_fee_quoter(
    env: &Env,
    owner: &Address,
    dest_chain_selector: u64,
    fee_token: &Address,
    transfer_token: &Address,
) -> Address {
    let throwaway_link = Address::generate(env);
    setup_fee_quoter_with_link_premium(
        env,
        owner,
        dest_chain_selector,
        fee_token,
        transfer_token,
        &throwaway_link,
        90,
    )
}

/// Fee-quoter setup that lets the caller bind `link_token` (so the fee token CAN
/// be LINK) and set `link_premium_percent`. Used by the INV-FEE-13 LINK-fee-token
/// lane to exercise the premium/discount on CCV/pool/executor-flat fees.
fn setup_fee_quoter_with_link_premium(
    env: &Env,
    owner: &Address,
    dest_chain_selector: u64,
    fee_token: &Address,
    transfer_token: &Address,
    link_token: &Address,
    link_premium_percent: u32,
) -> Address {
    env.ledger().with_mut(|li| {
        li.timestamp = 1000;
    });

    let fee_quoter_id = env.register(FeeQuoterContract, ());
    let fee_quoter_client = FeeQuoterContractClient::new(env, &fee_quoter_id);

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
        link_premium_percent,
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

// ============================================================
// Mock pool that captures `token_args` from `get_fee` (H-13 / INV-POOL-8)
// ============================================================

mod mock_pool {
    use soroban_sdk::{contract, contractimpl, symbol_short, Address, Bytes, Env, Symbol, Vec};

    use common_error::CCIPError;
    use common_interfaces::token_pool::{
        LockOrBurnIn, LockOrBurnOut, MessageDirection, PoolFeeResult, PoolRequiredCCVs,
    };

    const CAPTURED_GET_FEE_TOKEN_ARGS_KEY: Symbol = symbol_short!("GFTA");

    /// A minimal `TokenPoolInterface` mock whose `get_fee` captures the
    /// `token_args` argument into instance storage, so a test can assert the
    /// OnRamp threads `extra_args.token_args` end-to-end to the pool's `get_fee`
    /// (EVM `IPoolV2.getFee(…, tokenArgs)` parity). `get_fee` returns a
    /// *disabled* `PoolFeeResult` so the OnRamp falls back to the FeeQuoter for
    /// the actual fee/overhead — the capture still happens because the OnRamp
    /// calls `pool.get_fee` before inspecting `is_enabled`.
    #[contract]
    pub struct MockPoolCapturesGetFeeTokenArgs;

    #[contractimpl]
    impl MockPoolCapturesGetFeeTokenArgs {
        pub fn get_captured_get_fee_token_args(env: Env) -> Bytes {
            env.storage()
                .instance()
                .get(&CAPTURED_GET_FEE_TOKEN_ARGS_KEY)
                .unwrap_or_else(|| Bytes::new(&env))
        }

        pub fn get_fee(
            env: Env,
            _dest_chain_selector: u64,
            _amount: i128,
            _requested_finality: u32,
            token_args: Bytes,
        ) -> Result<PoolFeeResult, CCIPError> {
            env.storage()
                .instance()
                .set(&CAPTURED_GET_FEE_TOKEN_ARGS_KEY, &token_args);
            Ok(PoolFeeResult {
                fee_usd_cents: 0,
                dest_gas_overhead: 0,
                dest_bytes_overhead: 0,
                token_fee_bps: 0,
                is_enabled: false,
            })
        }

        /// Minimal valid return for any `lock_or_burn` the send path may make.
        pub fn lock_or_burn(
            env: Env,
            _caller: Address,
            input: LockOrBurnIn,
            _requested_finality: u32,
            _token_args: Bytes,
        ) -> Result<LockOrBurnOut, CCIPError> {
            Ok(LockOrBurnOut {
                dest_token_address: Bytes::new(&env),
                dest_token_amount: input.amount,
                dest_pool_data: Bytes::new(&env),
            })
        }

        /// No pool-mandated CCVs → the OnRamp uses lane defaults.
        pub fn get_required_ccvs(
            env: Env,
            _local_token: Address,
            _remote_chain_selector: u64,
            _amount: i128,
            _requested_finality: u32,
            _extra_data: Bytes,
            _direction: MessageDirection,
        ) -> PoolRequiredCCVs {
            PoolRequiredCCVs {
                ccvs: Vec::new(&env),
                include_defaults: true,
            }
        }
    }

    /// A minimal `TokenPoolInterface` mock whose `get_required_ccvs` returns an
    /// EMPTY CCV list with `include_defaults = false`. Combined with a token-only
    /// message (which skips the user-fallback defaults path in
    /// `build_merged_outbound_ccv_lists`) and a lane with no lane-mandated CCVs,
    /// this yields a zero-length merged CCV list — the exact H-1 / INV-CC-1
    /// scenario the OnRamp must reject. `get_fee` returns a *disabled*
    /// `PoolFeeResult` so the OnRamp falls back to the FeeQuoter; the H-1 guard
    /// fires inside `build_merged_outbound_ccv_lists` before `lock_or_burn` is
    /// ever reached, but a valid stub is included for completeness.
    #[contract]
    pub struct MockPoolEmptyRequiredNoDefaults;

    #[contractimpl]
    impl MockPoolEmptyRequiredNoDefaults {
        pub fn get_fee(
            _env: Env,
            _dest_chain_selector: u64,
            _amount: i128,
            _requested_finality: u32,
            _token_args: Bytes,
        ) -> Result<PoolFeeResult, CCIPError> {
            Ok(PoolFeeResult {
                fee_usd_cents: 0,
                dest_gas_overhead: 0,
                dest_bytes_overhead: 0,
                token_fee_bps: 0,
                is_enabled: false,
            })
        }

        /// Minimal valid return for any `lock_or_burn` the send path may make
        /// (unreached once H-1 lands, but kept for a complete interface stub).
        pub fn lock_or_burn(
            env: Env,
            _caller: Address,
            input: LockOrBurnIn,
            _requested_finality: u32,
            _token_args: Bytes,
        ) -> Result<LockOrBurnOut, CCIPError> {
            Ok(LockOrBurnOut {
                dest_token_address: Bytes::new(&env),
                dest_token_amount: input.amount,
                dest_pool_data: Bytes::new(&env),
            })
        }

        /// No pool-mandated CCVs AND do NOT fold in lane defaults — the
        /// discriminator for H-1. With a token-only message (user-fallback
        /// defaults skipped) and no lane-mandated CCVs, the merged CCV list is
        /// empty, which the OnRamp must reject rather than emit unverified.
        pub fn get_required_ccvs(
            env: Env,
            _local_token: Address,
            _remote_chain_selector: u64,
            _amount: i128,
            _requested_finality: u32,
            _extra_data: Bytes,
            _direction: MessageDirection,
        ) -> PoolRequiredCCVs {
            PoolRequiredCCVs {
                ccvs: Vec::new(&env),
                include_defaults: false,
            }
        }
    }

    /// A `TokenPoolInterface` mock whose `lock_or_burn` returns a `dest_pool_data` of a
    /// configured length, so M-15 / INV-POOL-21 can be exercised: when that length exceeds
    /// the FeeQuoter's `dest_bytes_overhead` (the bytes the sender paid for in the pool
    /// receipt), the OnRamp must reject with `SourceTokenDataTooLarge`. `get_fee` returns a
    /// *disabled* `PoolFeeResult` so the OnRamp falls back to the FeeQuoter (overhead 64 in
    /// the harness), and `get_required_ccvs` folds lane defaults so the merged CCV list is
    /// non-empty (clearing the H-1 zero-CCV guard before `lock_or_burn` runs).
    #[contract]
    pub struct MockPoolLargeDestPoolData;

    #[contractimpl]
    impl MockPoolLargeDestPoolData {
        pub fn set_dest_pool_data_len(env: Env, len: u32) {
            env.storage()
                .instance()
                .set(&Symbol::new(&env, "dplen"), &len);
        }

        pub fn get_fee(
            _env: Env,
            _dest_chain_selector: u64,
            _amount: i128,
            _requested_finality: u32,
            _token_args: Bytes,
        ) -> Result<PoolFeeResult, CCIPError> {
            Ok(PoolFeeResult {
                fee_usd_cents: 0,
                dest_gas_overhead: 0,
                dest_bytes_overhead: 0,
                token_fee_bps: 0,
                is_enabled: false,
            })
        }

        pub fn lock_or_burn(
            env: Env,
            _caller: Address,
            input: LockOrBurnIn,
            _requested_finality: u32,
            _token_args: Bytes,
        ) -> Result<LockOrBurnOut, CCIPError> {
            let len: u32 = env
                .storage()
                .instance()
                .get(&Symbol::new(&env, "dplen"))
                .unwrap_or(0);
            // `dest_pool_data` of `len` non-zero bytes (non-zero so a 0-len/empty carve-out
            // is distinguishable if ever needed). Length alone is what M-15 checks.
            let mut data = Bytes::new(&env);
            for _ in 0..len {
                data.push_back(0xABu8);
            }
            Ok(LockOrBurnOut {
                dest_token_address: Bytes::new(&env),
                dest_token_amount: input.amount,
                dest_pool_data: data,
            })
        }

        pub fn get_required_ccvs(
            env: Env,
            _local_token: Address,
            _remote_chain_selector: u64,
            _amount: i128,
            _requested_finality: u32,
            _extra_data: Bytes,
            _direction: MessageDirection,
        ) -> PoolRequiredCCVs {
            PoolRequiredCCVs {
                ccvs: Vec::new(&env),
                include_defaults: true,
            }
        }
    }
}

// ============================================================
// Token-transfer lane harness (H-5 / INV-SRC-5 fee-pricing tests)
// ============================================================
//
// A reusable one-token outbound lane with a real lock-release pool, real
// executor (auto-execute), and a FeeQuoter whose per-token
// `TokenTransferFeeConfig.dest_gas_overhead` is the pool receipt's gas. Used to
// prove the pool overhead is *priced* into the executor fee, not just
// advertised in `execution_gas_limit`.

struct TokenTransferLane {
    env: Env,
    sender: Address,
    evm_chain_selector: u64,
    router_client: RouterContractClient<'static>,
    onramp_id: Address,
    fee_token: Address,
    fee_token_sac: token::StellarAssetClient<'static>,
    transfer_token: Address,
    transfer_token_sac: token::StellarAssetClient<'static>,
    fee_quoter_client: FeeQuoterContractClient<'static>,
    /// When set, the lane was wired with this mock pool (which captures
    /// `token_args` from `get_fee`) instead of the real lock-release pool.
    mock_pool_id: Option<Address>,
}

impl TokenTransferLane {
    /// Send a 1-token transfer with auto-execution (empty `extra_args` resolves
    /// to the lane's concrete `default_executor`) and return the receipts from
    /// the `CCIPMessageSent` event. Receipt extraction runs immediately after
    /// `ccip_send`, before any other contract call clears the event view.
    fn send(&self) -> Vec<Receipt> {
        let env = &self.env;
        let mut token_amounts: Vec<TokenAmount> = Vec::new(env);
        token_amounts.push_back(TokenAmount {
            token: self.transfer_token.clone(),
            amount: 1_000_000,
        });
        let message = StellarToAnyMessage {
            receiver: Bytes::from_array(env, &[0x33u8; 20]),
            data: Bytes::from_slice(env, b"token send with data"),
            token_amounts,
            fee_token: self.fee_token.clone(),
            extra_args: Bytes::new(env),
        };
        let required_fee = self
            .router_client
            .get_fee(&self.evm_chain_selector, &message);
        assert!(required_fee > 0, "quoted fee must be positive");
        self.fee_token_sac.mint(&self.sender, &(required_fee * 2));
        self.transfer_token_sac.mint(&self.sender, &1_000_000);
        self.router_client.ccip_send(
            &self.sender,
            &self.evm_chain_selector,
            &message,
            &required_fee,
        );
        receipts_from_last_onramp_ccip_event(env, &self.onramp_id)
    }

    /// Send a 1-token transfer with an explicit `extra_args` payload (already
    /// XDR-encoded, e.g. a `GenericExtraArgsV3::to_xdr`) and return both the
    /// `CCIPMessageSent` receipts and the encoded on-wire message. Receipt /
    /// message extraction runs immediately after `ccip_send`, before any other
    /// contract call clears the event view.
    fn send_with_extra_args(&self, extra_args: Bytes) -> (Vec<Receipt>, Bytes) {
        let env = &self.env;
        let mut token_amounts: Vec<TokenAmount> = Vec::new(env);
        token_amounts.push_back(TokenAmount {
            token: self.transfer_token.clone(),
            amount: 1_000_000,
        });
        let message = StellarToAnyMessage {
            receiver: Bytes::from_array(env, &[0x33u8; 20]),
            data: Bytes::from_slice(env, b"token send with data"),
            token_amounts,
            fee_token: self.fee_token.clone(),
            extra_args,
        };
        let required_fee = self
            .router_client
            .get_fee(&self.evm_chain_selector, &message);
        assert!(required_fee > 0, "quoted fee must be positive");
        self.fee_token_sac.mint(&self.sender, &(required_fee * 2));
        self.transfer_token_sac.mint(&self.sender, &1_000_000);
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

    /// Reconfigure the FeeQuoter's per-token transfer fee for this lane's
    /// `(dest, transfer_token)`, overwriting `dest_gas_overhead` (the pool
    /// receipt's gas). `apply_token_fee_configs` replaces an existing entry in
    /// place, so no remove is needed. `dest_bytes_overhead` stays at 64 (≥ the
    // `CCIP_LOCK_OR_BURN_V1_RET_BYTES` minimum enforced by the FeeQuoter).
    fn set_pool_dest_gas_overhead(&self, dest_gas_overhead: u32) {
        let env = &self.env;
        self.fee_quoter_client.apply_token_fee_configs(
            &vec![
                env,
                TokenFeeConfigArgs {
                    dest_chain_selector: self.evm_chain_selector,
                    token: self.transfer_token.clone(),
                    config: TokenTransferFeeConfig {
                        fee_usd_cents: 5000,
                        dest_gas_overhead,
                        dest_bytes_overhead: 64,
                        is_enabled: true,
                    },
                },
            ],
            &Vec::new(env),
        );
    }

    /// The executor receipt (index 2 = [CCV, Pool, Executor, NetworkFee]).
    fn executor_receipt(receipts: &Vec<Receipt>) -> Receipt {
        receipts
            .get(2)
            .expect("expected [CCV, Pool, Executor, NetworkFee]")
            .clone()
    }
}

fn setup_token_transfer_lane() -> TokenTransferLane {
    setup_token_transfer_lane_with_pool(None)
}

/// Build a one-token outbound lane. When `mock_pool` is `None`, a real
/// lock-release pool (+ lockbox) is wired via the TAR. When `Some(addr)`, that
/// mock pool address is registered as the transfer token's pool instead (and no
/// lockbox is set up) — used by tests that need to observe `get_fee` arguments.
fn setup_token_transfer_lane_with_pool(mock_pool: Option<Address>) -> TokenTransferLane {
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

    // Wire the pool: either the real lock-release pool (+ lockbox) or a
    // caller-supplied mock pool address (no lockbox needed for fee-only tests).
    let pool_id = if let Some(mp) = mock_pool.clone() {
        mp
    } else {
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

        let remote_pool = Bytes::from_slice(&env, &[0x11u8; 20]);
        let remote_token = Bytes::from_slice(&env, &[0x22u8; 20]);
        pool_client.apply_chain_updates(
            &vec![
                &env,
                ChainUpdate {
                    remote_chain_selector: evm_chain_selector,
                    remote_pool_addresses: vec![&env, remote_pool],
                    remote_token_address: remote_token,
                    outbound_rate_limiter_config: RateLimitConfig::disabled(),
                    inbound_rate_limiter_config: RateLimitConfig::disabled(),
                },
            ],
            &Vec::new(&env),
        );

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
        pool_id
    };

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
    let fee_quoter_client = FeeQuoterContractClient::new(&env, &fee_quoter_id);

    let static_config = StaticConfig {
        chain_selector: stellar_chain_selector,
        token_admin_registry: tar_id.clone(),
        rmn_proxy: rmn_proxy_id.clone(),
        max_usd_cents_per_message: 100_000,
    };
    let dynamic_config = DynamicConfig {
        fee_quoter: fee_quoter_id,
        fee_aggregator: Address::generate(&env),
    };
    onramp_client.initialize(&owner, &static_config, &dynamic_config);

    let default_ccv = deploy_default_ccv_resolver(&env, &owner, evm_chain_selector);
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

    TokenTransferLane {
        env,
        sender,
        evm_chain_selector,
        router_client,
        onramp_id,
        fee_token,
        fee_token_sac,
        transfer_token,
        transfer_token_sac,
        fee_quoter_client,
        mock_pool_id: mock_pool,
    }
}

/// H-5 / INV-FEE-10: the pool's `dest_gas_overhead` must be *priced* into the
/// executor fee, not merely advertised in `execution_gas_limit`. EVM
/// `OnRamp._getReceipts` (L1075-1097) calls `quoteGasForExec(gasLimitSum, …)`
/// where `gasLimitSum` includes the pool receipt's `destGasLimit` (L1055), and
/// the resulting `execCostInUSDCents` is added to the executor receipt
/// (L1096). Stellar mirrors this: `execution_gas_limit` (now including
/// `pool_dest_gas_limit`) is priced via `quote_gas_for_exec`, and the cost joins
/// the executor receipt's `fee_token_amount` (flat + exec cost). This test
/// proves the pricing is wired by showing the executor receipt fee strictly
/// increases with the pool overhead (75_000 → 0), holding everything else fixed.
#[test]
fn test_pool_dest_gas_overhead_is_priced_into_executor_fee() {
    let lane = setup_token_transfer_lane();

    // Baseline: pool dest_gas_overhead = 75_000 (the `setup_fee_quoter` default).
    let receipts_high = lane.send();
    let executor_fee_high = TokenTransferLane::executor_receipt(&receipts_high).fee_token_amount;

    // Drop the pool overhead to 0 (reconfigures the same (dest, token) entry),
    // then send an identical message. Only `execution_gas_limit`'s pool term
    // changes (275_000 → 200_000), so only the priced exec-gas cost changes.
    lane.set_pool_dest_gas_overhead(0);
    let receipts_low = lane.send();
    let executor_fee_low = TokenTransferLane::executor_receipt(&receipts_low).fee_token_amount;

    assert!(
        executor_fee_high > executor_fee_low,
        "executor receipt fee must include the priced pool dest_gas_overhead \
         (EVM OnRamp.sol:1096 parity): got high={:?} low={:?}",
        executor_fee_high,
        executor_fee_low
    );
}

/// H-13 / INV-POOL-8: the sender-supplied `token_args` (carried in
/// `extra_args.token_args`) must reach the pool's `get_fee` byte-for-byte
/// (EVM `IPoolV2.getFee(localToken, destChainSelector, amount, feeToken,
/// requestedFinalityConfig, tokenArgs)` — `OnRamp.sol:1036-1043`). The Stellar
/// OnRamp resolves the pool via the TAR and calls `pool.get_fee(dest, amount,
/// requestedFinality, extra_args.token_args)` (`onramp/src/lib.rs:267-272`).
/// This test wires a mock pool that captures `token_args` from `get_fee` and
/// asserts, via `router.get_fee`, that the sender's payload arrives unchanged.
#[test]
fn test_get_fee_threads_token_args_to_pool() {
    let mut lane = setup_token_transfer_lane_with_pool(None);

    // Register the capturing mock pool and rebind the transfer token's pool
    // binding (in the OnRamp's TAR) to point at it. `rebind_pool_to_mock`
    // recovers the TAR address from the OnRamp's static config.
    let mock_pool_id = lane
        .env
        .register(mock_pool::MockPoolCapturesGetFeeTokenArgs, ());
    let mock_client =
        mock_pool::MockPoolCapturesGetFeeTokenArgsClient::new(&lane.env, &mock_pool_id);
    lane.mock_pool_id = Some(mock_pool_id.clone());
    rebind_pool_to_mock(&lane, &mock_pool_id);

    // Build a message with a non-empty `token_args` and otherwise-default
    // extra_args (use-default executor sentinel → resolves to the lane's
    // concrete default executor; empty CCVs → lane defaults).
    let env = &lane.env;
    let token_args = Bytes::from_array(env, &[0xde, 0xad, 0xbe, 0xef]);
    let extra_args = GenericExtraArgsV3 {
        gas_limit: 0,
        block_confirmations: 0,
        ccvs: Vec::new(env),
        ccv_args: Vec::new(env),
        executor: GenericExtraArgsV3::use_default_executor_address(env),
        executor_args: Bytes::new(env),
        token_receiver: Bytes::new(env),
        token_args: token_args.clone(),
    };
    let mut token_amounts: Vec<TokenAmount> = Vec::new(env);
    token_amounts.push_back(TokenAmount {
        token: lane.transfer_token.clone(),
        amount: 1_000_000,
    });
    let message = StellarToAnyMessage {
        receiver: Bytes::from_array(env, &[0x33u8; 20]),
        data: Bytes::new(env),
        token_amounts,
        fee_token: lane.fee_token.clone(),
        extra_args: extra_args.to_xdr(env),
    };

    // Empty before the call proves the hook actually ran and captured.
    assert_eq!(
        mock_client.get_captured_get_fee_token_args(),
        Bytes::new(env),
        "capture must be empty before get_fee"
    );

    // `router.get_fee` is a view that drives `compute_outbound_fee_breakdown`,
    // which calls `pool.get_fee` with `extra_args.token_args`.
    let _ = lane
        .router_client
        .get_fee(&lane.evm_chain_selector, &message);

    assert_eq!(
        mock_client.get_captured_get_fee_token_args(),
        token_args,
        "pool.get_fee must receive the sender's extra_args.token_args unchanged (EVM IPoolV2.getFee parity)"
    );
}

/// H-1 / INV-CC-1: an outbound message must carry at least one CCV. A token-only
/// transfer skips the user-fallback defaults path in `build_merged_outbound_ccv_lists`,
/// so when the pool returns `{ccvs:[], include_defaults:false}` and the lane has no
/// lane-mandated CCVs, the merged CCV list is empty. `get_fee` routes through that
/// same merge point and must reject with `CCVQuorumNotMet` (#108) instead of quoting
/// a fee for an unverified message.
#[test]
#[should_panic(expected = "Error(Contract, #108)")] // CCVQuorumNotMet
fn test_get_fee_rejects_zero_ccv() {
    let mut lane = setup_token_transfer_lane_with_pool(None);

    // Rebind the transfer token's pool to a mock that returns no required CCVs
    // and asks the OnRamp NOT to fold in lane defaults. The lane itself still
    // carries a non-empty `default_ccvs` (so `DestChainConfigArgs::validate`
    // accepts it), but `include_defaults = false` means those defaults are never
    // appended for this pool — the precise H-1 gap.
    let mock_pool_id = lane
        .env
        .register(mock_pool::MockPoolEmptyRequiredNoDefaults, ());
    lane.mock_pool_id = Some(mock_pool_id.clone());
    rebind_pool_to_mock(&lane, &mock_pool_id);

    let env = &lane.env;
    // Token-only: empty data, one token amount, gas_limit 0, empty user CCVs.
    let extra_args = GenericExtraArgsV3 {
        gas_limit: 0,
        block_confirmations: 0,
        ccvs: Vec::new(env),
        ccv_args: Vec::new(env),
        executor: GenericExtraArgsV3::use_default_executor_address(env),
        executor_args: Bytes::new(env),
        token_receiver: Bytes::new(env),
        token_args: Bytes::new(env),
    };
    let mut token_amounts: Vec<TokenAmount> = Vec::new(env);
    token_amounts.push_back(TokenAmount {
        token: lane.transfer_token.clone(),
        amount: 1_000_000,
    });
    let message = StellarToAnyMessage {
        receiver: Bytes::from_array(env, &[0x33u8; 20]),
        data: Bytes::new(env),
        token_amounts,
        fee_token: lane.fee_token.clone(),
        extra_args: extra_args.to_xdr(env),
    };

    lane.router_client
        .get_fee(&lane.evm_chain_selector, &message);
}

/// H-1 / INV-CC-1: the send path shares the same merge point as `get_fee`, so the
/// same zero-CCV token-only scenario must be rejected at send time with
/// `CCVQuorumNotMet` (#108). The guard fires inside `build_merged_outbound_ccv_lists`,
/// which `forward_from_router` calls before fee validation and `lock_or_burn`, so no
/// fee tokens need minting and a zero fee suffices. Invoked directly (the lane's
/// `mock_all_auths` satisfies the router + sender auth checks) to target
/// `forward_from_router` precisely.
#[test]
#[should_panic(expected = "Error(Contract, #108)")] // CCVQuorumNotMet
fn test_forward_from_router_rejects_zero_ccv_token_only() {
    let mut lane = setup_token_transfer_lane_with_pool(None);

    let mock_pool_id = lane
        .env
        .register(mock_pool::MockPoolEmptyRequiredNoDefaults, ());
    lane.mock_pool_id = Some(mock_pool_id.clone());
    rebind_pool_to_mock(&lane, &mock_pool_id);

    let env = &lane.env;
    let extra_args = GenericExtraArgsV3 {
        gas_limit: 0,
        block_confirmations: 0,
        ccvs: Vec::new(env),
        ccv_args: Vec::new(env),
        executor: GenericExtraArgsV3::use_default_executor_address(env),
        executor_args: Bytes::new(env),
        token_receiver: Bytes::new(env),
        token_args: Bytes::new(env),
    };
    let mut token_amounts: Vec<TokenAmount> = Vec::new(env);
    token_amounts.push_back(TokenAmount {
        token: lane.transfer_token.clone(),
        amount: 1_000_000,
    });
    let message = StellarToAnyMessage {
        receiver: Bytes::from_array(env, &[0x33u8; 20]),
        data: Bytes::new(env),
        token_amounts,
        fee_token: lane.fee_token.clone(),
        extra_args: extra_args.to_xdr(env),
    };

    let onramp_client = OnRampContractClient::new(&lane.env, &lane.onramp_id);
    onramp_client.forward_from_router(&lane.evm_chain_selector, &message, &0_i128, &lane.sender);
}

/// Re-register the lane's transfer-token pool binding to point at `mock_pool`.
/// The OnRamp resolves the pool via the TAR it was initialized with; that TAR
/// is not exposed on `TokenTransferLane`, so this helper re-creates a TAR
/// client at the OnRamp's configured TAR address and rebinds the pool.
fn rebind_pool_to_mock(lane: &TokenTransferLane, mock_pool: &Address) {
    // The OnRamp's static_config.token_admin_registry is the TAR it queries.
    // The harness doesn't expose it, so recover it from the OnRamp's config.
    let onramp = OnRampContractClient::new(&lane.env, &lane.onramp_id);
    let tar_address = onramp.get_static_config().token_admin_registry;
    let tar = TokenAdminRegistryContractClient::new(&lane.env, &tar_address);
    tar.set_pool(&lane.transfer_token, &Some(mock_pool.clone()));
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
                remote_pool_addresses: vec![&env, remote_pool],
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

    // H-5 / INV-SRC-5: the on-wire `execution_gas_limit` must include the pool's
    // `dest_gas_overhead` (75_000 here) on top of the CCV gas (0, the mock
    // verifier returns no dest gas) and the lane's `base_execution_gas_cost`
    // (200_000), mirroring EVM `OnRamp._getReceipts` L1009/1055/1065
    // (CCV gas → pool gas → executor gas). Without this, an auto-executed token
    // transfer advertises and is priced for less gas than `release_or_mint`
    // requires, stranding the destination call with an out-of-gas revert.
    assert_eq!(
        decoded.execution_gas_limit, 275_000,
        "execution_gas_limit must include the pool dest_gas_overhead (75_000) + base_execution_gas_cost (200_000)"
    );

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

// ============================================================
// H-3: send-time fee distribution (EVM `OnRamp._distributeFees` parity)
// ============================================================
//
// `forward_from_router` must, at send time, transfer each CCV fee → that CCV's
// VVR (the receipt `issuer`), the pool fee → the token pool, and the executor
// fee → the executor — and LEAVE the network fee on the OnRamp for the
// permissionless `withdraw_fee_tokens` sweep (INV-FEE-18/19/20/21). The three
// tests below share a full lane (`FeeDistLane`) wired with TWO fee-charging
// CCVs (30 & 70 USD-cent fees), a real lock-release pool (5000-cent pool fee
// via the FeeQuoter per-token config), and a real Executor (25-cent flat fee),
// so each fee slice is non-trivial and independently checkable.

struct FeeDistLane {
    env: Env,
    sender: Address,
    evm_chain_selector: u64,
    router_client: RouterContractClient<'static>,
    onramp_client: OnRampContractClient<'static>,
    onramp_id: Address,
    fee_token: Address,
    fee_token_sac: token::StellarAssetClient<'static>,
    transfer_token: Address,
    transfer_token_sac: token::StellarAssetClient<'static>,
    fee_quoter_client: FeeQuoterContractClient<'static>,
    fee_aggregator: Address,
    pool_id: Address,
    default_executor: Address,
    /// CCV VVR addresses (the CCV fee recipients / receipt issuers), configured
    /// as the lane's `default_ccvs` in this order: ccv_a charges 30, ccv_b 70.
    ccv_a: Address,
    ccv_b: Address,
}

impl FeeDistLane {
    /// Quote, fund, and `ccip_send` `message`; return `(receipts, message,
    /// required_fee)`. Receipt extraction runs immediately after the send,
    /// before any later contract call clears the test-env event view.
    fn send(&self, message: StellarToAnyMessage) -> (Vec<Receipt>, StellarToAnyMessage, i128) {
        let env = &self.env;
        let required_fee = self
            .router_client
            .get_fee(&self.evm_chain_selector, &message);
        assert!(required_fee > 0, "quoted fee must be positive");
        self.fee_token_sac.mint(&self.sender, &(required_fee * 2));
        if !message.token_amounts.is_empty() {
            self.transfer_token_sac.mint(&self.sender, &1_000_000);
        }
        self.router_client.ccip_send(
            &self.sender,
            &self.evm_chain_selector,
            &message,
            &required_fee,
        );
        let receipts = receipts_from_last_onramp_ccip_event(env, &self.onramp_id);
        (receipts, message, required_fee)
    }

    /// 1-token transfer with default extra_args (use-default executor sentinel →
    /// lane default; empty CCVs → lane defaults ccv_a/ccv_b).
    fn send_token_transfer(&self) -> (Vec<Receipt>, StellarToAnyMessage, i128) {
        let env = &self.env;
        let mut token_amounts: Vec<TokenAmount> = Vec::new(env);
        token_amounts.push_back(TokenAmount {
            token: self.transfer_token.clone(),
            amount: 1_000_000,
        });
        let message = StellarToAnyMessage {
            receiver: Bytes::from_array(env, &[0x33u8; 20]),
            data: Bytes::from_slice(env, b"h3 token send"),
            token_amounts,
            fee_token: self.fee_token.clone(),
            extra_args: Bytes::new(env),
        };
        self.send(message)
    }

    /// Data-only message (no tokens → no pool receipt).
    fn send_data_only(&self) -> (Vec<Receipt>, StellarToAnyMessage, i128) {
        let env = &self.env;
        let message = StellarToAnyMessage {
            receiver: Bytes::from_array(env, &[0x33u8; 20]),
            data: Bytes::from_slice(env, b"h3 data only"),
            token_amounts: Vec::new(env),
            fee_token: self.fee_token.clone(),
            extra_args: Bytes::new(env),
        };
        self.send(message)
    }

    /// `message_fee.fee_token_price` for `message` — the exact price the OnRamp
    /// uses to convert USD-cent fee slices to fee-token units, so tests compute
    /// expected transferred amounts with the same `fee_math` helper the contract
    /// uses (no hand-rolled scaling). Must be called after receipt extraction.
    fn fee_token_price(&self, message: &StellarToAnyMessage) -> u128 {
        self.fee_quoter_client
            .get_message_fee(&self.evm_chain_selector, message)
            .fee_token_price
    }
}

fn setup_fee_dist_lane_impl(fee_token_is_link: bool, link_premium_percent: u32) -> FeeDistLane {
    let env = Env::default();
    env.mock_all_auths();
    // This lane wires two fee-charging CCVs (extra cross-contract get_fee /
    // forward_to_verifier calls) on top of the full token-transfer path, which
    // exceeds the default Soroban test budget. Lift the budget for the H-3
    // distribution tests only (the rest of the suite keeps `Env::default()`).
    env.budget().reset_unlimited();

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

    // EVM parity: when the fee token IS LINK, the fee-quoter's LINK premium
    // (`link_premium_percent`) applies to CCV/pool/executor-flat fees too
    // (INV-FEE-13). `link_token` is immutable post-init in FqStaticConfig, so
    // it must be passed in at setup; for the non-LINK lane it is a throwaway.
    let link_token = if fee_token_is_link {
        fee_token.clone()
    } else {
        Address::generate(&env)
    };

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

    let remote_pool = Bytes::from_slice(&env, &[0x11u8; 20]);
    let remote_token = Bytes::from_slice(&env, &[0x22u8; 20]);
    pool_client.apply_chain_updates(
        &vec![
            &env,
            ChainUpdate {
                remote_chain_selector: evm_chain_selector,
                remote_pool_addresses: vec![&env, remote_pool],
                remote_token_address: remote_token,
                outbound_rate_limiter_config: RateLimitConfig::disabled(),
                inbound_rate_limiter_config: RateLimitConfig::disabled(),
            },
        ],
        &Vec::new(&env),
    );

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

    let fee_quoter_id = setup_fee_quoter_with_link_premium(
        &env,
        &owner,
        evm_chain_selector,
        &fee_token,
        &transfer_token,
        &link_token,
        link_premium_percent,
    );
    let fee_quoter_client = FeeQuoterContractClient::new(&env, &fee_quoter_id);

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

    // Two fee-charging CCVs (30 & 70 USD-cent fees) as the lane defaults.
    let ccv_a = deploy_fee_charging_ccv(&env, &owner, evm_chain_selector, 30);
    let ccv_b = deploy_fee_charging_ccv(&env, &owner, evm_chain_selector, 70);
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
        default_ccvs: vec![&env, ccv_a.clone(), ccv_b.clone()],
        off_ramp: Bytes::from_array(&env, &[0u8; 20]),
    };
    onramp_client.apply_dest_chain_config_updates(&vec![&env, dest_chain_config]);
    router_client.set_onramp(&evm_chain_selector, &onramp_id);

    FeeDistLane {
        env,
        sender,
        evm_chain_selector,
        router_client,
        onramp_client,
        onramp_id,
        fee_token,
        fee_token_sac,
        transfer_token,
        transfer_token_sac,
        fee_quoter_client,
        fee_aggregator,
        pool_id,
        default_executor,
        ccv_a,
        ccv_b,
    }
}

/// Non-LINK lane (the H-3 default): a generic SAC fee token, `premium_multiplier`
/// is 100, so the premium helper is bit-identical to the bare conversion. All H-3
/// distribution tests build on this and stay unchanged.
fn setup_fee_dist_lane() -> FeeDistLane {
    setup_fee_dist_lane_impl(false, 90)
}

/// H-3 / INV-FEE-18: each CCV fee is transferred at send time to that CCV's VVR
/// (the receipt `issuer`), in the per-receipt fee-token amount. Two distinct
/// CCV fees (30 & 70 cents) prove per-recipient distribution — each VVR receives
/// exactly its own individually-converted slice, not a pooled share. Data-only
/// message isolates the CCV slice (no pool receipt).
#[test]
fn test_send_distributes_ccv_fees_to_resolvers() {
    let lane = setup_fee_dist_lane();
    let env = &lane.env;

    let (receipts, message, _required_fee) = lane.send_data_only();
    // Data-only: [CCV_a, CCV_b, Executor, NetworkFee] (no pool row).
    assert_eq!(receipts.len(), 4, "expected 2 CCV + executor + network");
    assert_eq!(receipts.get(0).unwrap().issuer, lane.ccv_a);
    assert_eq!(receipts.get(1).unwrap().issuer, lane.ccv_b);
    assert_eq!(receipts.get(0).unwrap().fee_token_amount, 30);
    assert_eq!(receipts.get(1).unwrap().fee_token_amount, 70);

    let price = lane.fee_token_price(&message);
    let expected_a = fee_math::usd_cents_to_fee_token(30_u128, price).expect("convert ccv_a fee");
    let expected_b = fee_math::usd_cents_to_fee_token(70_u128, price).expect("convert ccv_b fee");

    // Balance checks are contract calls — run after all event extraction.
    let fee_token_client = token::Client::new(env, &lane.fee_token);
    assert_eq!(
        fee_token_client.balance(&lane.ccv_a),
        expected_a,
        "ccv_a fee must be transferred to its VVR at send time (H-3)"
    );
    assert_eq!(
        fee_token_client.balance(&lane.ccv_b),
        expected_b,
        "ccv_b fee must be transferred to its VVR at send time (H-3)"
    );
}

/// H-3 / INV-FEE-20: the token-pool fee is transferred at send time to the pool
/// (the pool receipt `issuer`). Stellar pools are all V2 post-H-13, so the fee
/// is always moved (EVM's V1 leave-it-for-sweep branch is N/A). The pool fee
/// here is the FeeQuoter per-token config (5000 cents, `is_enabled` fallback
/// since the real pool's `get_fee` is disabled). Token transfer exercises the
/// full receipt row [CCV_a, CCV_b, Pool, Executor, NetworkFee].
#[test]
fn test_send_distributes_pool_fee_to_pool() {
    let lane = setup_fee_dist_lane();
    let env = &lane.env;

    let (receipts, message, _required_fee) = lane.send_token_transfer();
    assert_eq!(
        receipts.len(),
        5,
        "expected 2 CCV + pool + executor + network"
    );
    assert_eq!(receipts.get(2).unwrap().issuer, lane.pool_id);
    assert_eq!(receipts.get(2).unwrap().fee_token_amount, 5000);

    let price = lane.fee_token_price(&message);
    let expected_pool =
        fee_math::usd_cents_to_fee_token(5000_u128, price).expect("convert pool fee");

    let fee_token_client = token::Client::new(env, &lane.fee_token);
    assert_eq!(
        fee_token_client.balance(&lane.pool_id),
        expected_pool,
        "pool fee must be transferred to the token pool at send time (H-3)"
    );
}

/// H-3 / INV-FEE-21: the network fee is LEFT on the OnRamp at send time (EVM:
/// "network fee receipt which must remain in the onRamp") and the
/// permissionless `withdraw_fee_tokens` later sweeps exactly that residual to
/// `fee_aggregator`. After distribution the OnRamp holds `required_fee` minus
/// the sum of the CCV/pool/executor slices transferred — i.e. the network-fee
/// slice + floor-division rounding dust, all of which belongs to
/// `fee_aggregator`. End-to-end leave-and-sweep proof.
#[test]
fn test_withdraw_fee_tokens_sweeps_network_fee_residual() {
    let lane = setup_fee_dist_lane();
    let env = &lane.env;

    let (_receipts, _message, required_fee) = lane.send_token_transfer();
    let fee_token_client = token::Client::new(env, &lane.fee_token);

    // The four slices transferred at send time.
    let ccv_a_bal = fee_token_client.balance(&lane.ccv_a);
    let ccv_b_bal = fee_token_client.balance(&lane.ccv_b);
    let pool_bal = fee_token_client.balance(&lane.pool_id);
    let exec_bal = fee_token_client.balance(&lane.default_executor);

    // Conservation: the OnRamp received `required_fee`; after distribution it
    // holds only the network-fee slice + rounding dust.
    let residual = required_fee - ccv_a_bal - ccv_b_bal - pool_bal - exec_bal;
    assert!(
        residual > 0,
        "network fee residual must remain on the OnRamp"
    );
    assert_eq!(
        fee_token_client.balance(&lane.onramp_id),
        residual,
        "OnRamp must hold only the network-fee residual after distribution"
    );
    assert_eq!(
        fee_token_client.balance(&lane.fee_aggregator),
        0,
        "fee aggregator must have received nothing yet (network fee is left, not eagerly sent)"
    );

    // Permissionless sweep moves exactly the residual to the fee aggregator.
    lane.onramp_client
        .withdraw_fee_tokens(&vec![env, lane.fee_token.clone()]);
    assert_eq!(
        fee_token_client.balance(&lane.onramp_id),
        0,
        "withdraw_fee_tokens must sweep the full residual off the OnRamp"
    );
    assert_eq!(
        fee_token_client.balance(&lane.fee_aggregator),
        residual,
        "the network-fee residual must reach the fee aggregator via the sweep"
    );
}

// ---------------------------------------------------------------------------
// INV-FEE-13 / M-10: LINK premium must apply to CCV / pool / executor-flat fees
// ---------------------------------------------------------------------------
// EVM `OnRamp._getReceipts` applies `feeMultiplier = percentMultiplier * 1e32 /
// feeTokenPrice` to EVERY receipt (OnRamp.sol:1090), then adds exec cost WITHOUT
// the multiplier (:1096). Stellar previously applied the LINK premium only to the
// message fee (gas + network) via the fee-quoter, converting the additional
// (CCV/pool/executor-flat) slices with the BARE helper — over-charging and
// over-distributing them for a LINK fee token. The fix routes those slices
// through `usd_cents_to_fee_token_with_premium`. These tests build a LINK lane
// (`fee_token == link_token`, `link_premium_percent = 90` ⇒ 10% discount) and
// assert the discounted, premium-aware amounts land on-chain. The non-LINK
// regression guard is the existing H-3 suite above (`setup_fee_dist_lane()` ⇒
// `premium_multiplier = 100` ⇒ the premium helper is bit-identical to the bare
// one, so those tests still assert the bare amounts and stay green).

/// INV-FEE-13: with a LINK fee token (premium 90), each CCV fee is transferred to
/// its VVR in the DISCOUNTED fee-token amount `usd_cents_to_fee_token_with_premium(
/// fee, 90, price)`, not the bare conversion. The receipt still carries USD cents
/// (30/70) — only the converted/distributed amount changes. Data-only isolates the
/// CCV slice (no pool row).
#[test]
fn test_send_distributes_ccv_fees_with_link_premium() {
    let lane = setup_fee_dist_lane_impl(true, 90);
    let env = &lane.env;

    let (receipts, message, _required_fee) = lane.send_data_only();
    assert_eq!(receipts.len(), 4, "expected 2 CCV + executor + network");
    // Receipts still carry USD cents (off-chain parsing is unchanged).
    assert_eq!(receipts.get(0).unwrap().issuer, lane.ccv_a);
    assert_eq!(receipts.get(1).unwrap().issuer, lane.ccv_b);
    assert_eq!(receipts.get(0).unwrap().fee_token_amount, 30);
    assert_eq!(receipts.get(1).unwrap().fee_token_amount, 70);

    let price = lane.fee_token_price(&message);
    // Discounted (premium 90) — strictly less than the bare conversion.
    let expected_a =
        fee_math::usd_cents_to_fee_token_with_premium(30_u128, 90, price).expect("convert ccv_a");
    let expected_b =
        fee_math::usd_cents_to_fee_token_with_premium(70_u128, 90, price).expect("convert ccv_b");
    let bare_a = fee_math::usd_cents_to_fee_token(30_u128, price).expect("bare ccv_a");
    assert!(
        expected_a < bare_a,
        "LINK premium must discount the CCV fee vs the bare conversion"
    );

    let fee_token_client = token::Client::new(env, &lane.fee_token);
    assert_eq!(
        fee_token_client.balance(&lane.ccv_a),
        expected_a,
        "ccv_a fee must be the premium-discounted amount (INV-FEE-13)"
    );
    assert_eq!(
        fee_token_client.balance(&lane.ccv_b),
        expected_b,
        "ccv_b fee must be the premium-discounted amount (INV-FEE-13)"
    );
}

/// INV-FEE-13: with a LINK fee token (premium 90), the pool fee is transferred to
/// the token pool in the DISCOUNTED amount. Token transfer exercises the full
/// receipt row [CCV_a, CCV_b, Pool, Executor, NetworkFee].
#[test]
fn test_send_distributes_pool_fee_with_link_premium() {
    let lane = setup_fee_dist_lane_impl(true, 90);
    let env = &lane.env;

    let (receipts, message, _required_fee) = lane.send_token_transfer();
    assert_eq!(
        receipts.len(),
        5,
        "expected 2 CCV + pool + executor + network"
    );
    assert_eq!(receipts.get(2).unwrap().issuer, lane.pool_id);
    assert_eq!(receipts.get(2).unwrap().fee_token_amount, 5000);

    let price = lane.fee_token_price(&message);
    let expected_pool =
        fee_math::usd_cents_to_fee_token_with_premium(5000_u128, 90, price).expect("convert pool");
    let bare_pool = fee_math::usd_cents_to_fee_token(5000_u128, price).expect("bare pool");
    assert!(
        expected_pool < bare_pool,
        "LINK premium must discount the pool fee vs the bare conversion"
    );

    let fee_token_client = token::Client::new(env, &lane.fee_token);
    assert_eq!(
        fee_token_client.balance(&lane.pool_id),
        expected_pool,
        "pool fee must be the premium-discounted amount (INV-FEE-13)"
    );
}

/// INV-FEE-13 (quote path): `get_fee` must quote the premium-aware (discounted)
/// total for a LINK fee token. Two lanes with identical USD fee configs — one
/// non-LINK (`premium_multiplier = 100`), one LINK (premium 90) — quote the same
/// data-only message; the LINK total must be strictly lower. This proves the
/// discount reaches the quoted total (not only the distribution), without
/// coupling the assertion to gas-pricing internals.
#[test]
fn test_get_fee_link_total_is_discounted_vs_non_link() {
    let non_link_lane = setup_fee_dist_lane_impl(false, 90);
    let link_lane = setup_fee_dist_lane_impl(true, 90);

    // Each message must be built from its OWN lane's env — Soroban objects
    // (Bytes/Vec/Address) cannot cross `Env` instances ("unknown object
    // reference"). Both lanes use identical USD fee configs; only the fee token
    // differs (non-LINK ⇒ `premium_multiplier = 100`, LINK ⇒ 90).
    let nl_env = &non_link_lane.env;
    let non_link_message = StellarToAnyMessage {
        receiver: Bytes::from_array(nl_env, &[0x33u8; 20]),
        data: Bytes::from_slice(nl_env, b"inv-fee-13 quote"),
        token_amounts: Vec::new(nl_env),
        fee_token: non_link_lane.fee_token.clone(),
        extra_args: Bytes::new(nl_env),
    };
    let non_link_fee = non_link_lane
        .router_client
        .get_fee(&non_link_lane.evm_chain_selector, &non_link_message);

    let lk_env = &link_lane.env;
    let link_message = StellarToAnyMessage {
        receiver: Bytes::from_array(lk_env, &[0x33u8; 20]),
        data: Bytes::from_slice(lk_env, b"inv-fee-13 quote"),
        token_amounts: Vec::new(lk_env),
        fee_token: link_lane.fee_token.clone(),
        extra_args: Bytes::new(lk_env),
    };
    let link_fee = link_lane
        .router_client
        .get_fee(&link_lane.evm_chain_selector, &link_message);

    assert!(
        link_fee < non_link_fee,
        "LINK fee-token quote must be discounted vs the non-LINK quote (INV-FEE-13): \
         link={link_fee} non_link={non_link_fee}"
    );
    assert!(
        non_link_fee > 0 && link_fee > 0,
        "both quotes must be positive"
    );
}

/// INV-FEE-13 × H-3 overdraw coupling: with a LINK fee token the charged
/// `additional_in_fee_token` is the DISCOUNTED sum, and the per-receipt
/// distribution is also discounted. The floor-division invariant
/// `Σ premium_convert(each) ≤ premium_convert(Σ)` keeps the distributed sum ≤ the
/// funded additional, so the OnRamp is never over-drawn — the residual left on it
/// is exactly the network-fee slice + rounding dust, swept by
/// `withdraw_fee_tokens`. This is the LINK-fee-token analogue of
/// `test_withdraw_fee_tokens_sweeps_network_fee_residual`.
#[test]
fn test_link_lane_no_overdraw_conservation() {
    let lane = setup_fee_dist_lane_impl(true, 90);
    let env = &lane.env;

    let (_receipts, _message, required_fee) = lane.send_token_transfer();
    let fee_token_client = token::Client::new(env, &lane.fee_token);

    let ccv_a_bal = fee_token_client.balance(&lane.ccv_a);
    let ccv_b_bal = fee_token_client.balance(&lane.ccv_b);
    let pool_bal = fee_token_client.balance(&lane.pool_id);
    let exec_bal = fee_token_client.balance(&lane.default_executor);

    // No overdraw: the four distributed slices never exceed the funded fee, and
    // the OnRamp holds the (positive) network-fee residual + dust.
    let residual = required_fee - ccv_a_bal - ccv_b_bal - pool_bal - exec_bal;
    assert!(
        residual > 0,
        "network fee residual must remain on the OnRamp under the LINK discount (no overdraw)"
    );
    assert_eq!(
        fee_token_client.balance(&lane.onramp_id),
        residual,
        "OnRamp must hold only the network-fee residual after LINK-premium distribution"
    );
    assert_eq!(
        fee_token_client.balance(&lane.fee_aggregator),
        0,
        "fee aggregator must have received nothing yet (network fee is left, not eagerly sent)"
    );

    // Permissionless sweep moves exactly the residual to the fee aggregator.
    lane.onramp_client
        .withdraw_fee_tokens(&vec![env, lane.fee_token.clone()]);
    assert_eq!(
        fee_token_client.balance(&lane.onramp_id),
        0,
        "withdraw_fee_tokens must sweep the full residual off the OnRamp"
    );
    assert_eq!(
        fee_token_client.balance(&lane.fee_aggregator),
        residual,
        "the network-fee residual must reach the fee aggregator via the sweep"
    );
}

/// INV-FEE-14: the executor receipt's `dest_bytes_overhead` is the payload length
/// (EVM `_getExecutionFee`: `destBytesOverhead = message.data.length`), and the
/// payload bytes are priced — non-premium — into the executor exec-cost. Two
/// data-only sends that differ ONLY in `data.len()` must produce executor receipts
/// whose USD-cent value (flat + exec_cost) is strictly higher for the larger
/// payload, and whose `dest_bytes_overhead` equals the payload length. Data-only
/// ⇒ no pool receipt, so `calldata_size` is just `data.len()` (the mock CCVs report
/// zero `dest_bytes_overhead`).
#[test]
fn test_executor_receipt_prices_payload_bytes() {
    let lane = setup_fee_dist_lane(); // non-LINK, premium_multiplier = 100
    let env = &lane.env;

    let mk = |data: Bytes| -> StellarToAnyMessage {
        StellarToAnyMessage {
            receiver: Bytes::from_array(env, &[0x33u8; 20]),
            data,
            token_amounts: Vec::new(env),
            fee_token: lane.fee_token.clone(),
            extra_args: Bytes::new(env),
        }
    };
    let (receipts_small, _, _) = lane.send(mk(Bytes::from_slice(env, b"short")));
    let (receipts_large, _, _) = lane.send(mk(Bytes::from_slice(env, &[0u8; 500])));

    // Data-only receipt row: [ccv_a, ccv_b, executor, network] ⇒ executor at idx 2.
    let exec_small = receipts_small.get(2).unwrap();
    let exec_large = receipts_large.get(2).unwrap();

    // INV-FEE-14 executor-receipt fix: dest_bytes_overhead == payload length.
    assert_eq!(exec_small.dest_bytes_overhead, 5, "short payload = 5 bytes");
    assert_eq!(
        exec_large.dest_bytes_overhead, 500,
        "large payload = 500 bytes"
    );

    // Both share the same executor flat fee (25); the exec-cost grows with
    // payload bytes (data.len() × dest_gas_per_payload_byte), so the larger
    // payload's executor receipt USD (flat + exec_cost) must be strictly higher.
    assert!(
        exec_large.fee_token_amount > exec_small.fee_token_amount,
        "executor receipt must price payload bytes (INV-FEE-14): \
         small={} large={}",
        exec_small.fee_token_amount,
        exec_large.fee_token_amount
    );
}

/// INV-FEE-14: gas routes to the EXECUTOR (non-premium), not to the fee
/// aggregator. After a data-only send:
///   - the executor's fee-token balance == `with_premium(flat, pm, price)` +
///     `bare(exec_cost, price)` — the exec-cost (gas) slice is converted with the
///     BARE helper (no LINK discount), exactly `executor_fee_tokens`;
///   - the OnRamp holds the NETWORK-only message fee + flat-fee floor dust (NO gas
///     term — gas is no longer in `get_message_fee`), i.e. the residual equals
///     `message_fee.fee_token_amount + (with_premium(125) - with_premium(30) -
///     with_premium(70) - with_premium(25))` where 125 = ccv(100) + flat(25);
///   - the fee aggregator has received nothing yet (the network fee is left, not
///     eagerly sent), and the permissionless sweep moves exactly the residual.
/// The non-LINK lane (`pm = 100`) doubles as the INV-FEE-13/14 regression guard:
/// `with_premium(_, 100, _)` is bit-identical to `bare`, so CCV/pool/flat amounts
/// are unchanged from the pre-FEE-14 distribution.
#[test]
fn test_gas_routes_to_executor_not_fee_aggregator() {
    let lane = setup_fee_dist_lane(); // non-LINK, pm = 100
    let env = &lane.env;

    let (receipts, message, required_fee) = lane.send_data_only();
    let price = lane.fee_token_price(&message);
    let pm: u32 = 100;

    // Executor receipt at idx 2 (data-only: [ccv_a, ccv_b, executor, network]).
    let exec_receipt = receipts.get(2).unwrap();
    // Executor flat fee (from `Executor::get_fee` = 25, per `setup_executor`).
    const EXECUTOR_FLAT_USD: u128 = 25;
    let exec_cost_usd = (exec_receipt.fee_token_amount as u128) - EXECUTOR_FLAT_USD;

    let expected_executor =
        fee_math::usd_cents_to_fee_token_with_premium(EXECUTOR_FLAT_USD, pm, price)
            .expect("flat")
            .checked_add(fee_math::usd_cents_to_fee_token(exec_cost_usd, price).expect("exec_cost"))
            .unwrap();

    let fee_token_client = token::Client::new(env, &lane.fee_token);
    assert_eq!(
        fee_token_client.balance(&lane.default_executor),
        expected_executor,
        "executor must receive flat (premium) + exec_cost (BARE, non-premium) — gas routes to executor"
    );

    // Residual = network-only message fee + flat-fee floor dust (no gas term).
    let message_fee = lane
        .fee_quoter_client
        .get_message_fee(&lane.evm_chain_selector, &message);
    let dust = fee_math::usd_cents_to_fee_token_with_premium(125_u128, pm, price).unwrap()
        - fee_math::usd_cents_to_fee_token_with_premium(30_u128, pm, price).unwrap()
        - fee_math::usd_cents_to_fee_token_with_premium(70_u128, pm, price).unwrap()
        - fee_math::usd_cents_to_fee_token_with_premium(EXECUTOR_FLAT_USD, pm, price).unwrap();
    let expected_residual = message_fee.fee_token_amount + dust;

    let ccv_a_bal = fee_token_client.balance(&lane.ccv_a);
    let ccv_b_bal = fee_token_client.balance(&lane.ccv_b);
    let exec_bal = fee_token_client.balance(&lane.default_executor);
    let onramp_bal = fee_token_client.balance(&lane.onramp_id);

    assert_eq!(
        onramp_bal, expected_residual,
        "OnRamp residual must be network-only message fee + flat dust (NO gas)"
    );
    assert_eq!(
        required_fee - ccv_a_bal - ccv_b_bal - exec_bal,
        onramp_bal,
        "fee conservation: residual = funded - distributed"
    );
    assert_eq!(
        fee_token_client.balance(&lane.fee_aggregator),
        0,
        "fee aggregator must have received nothing yet (network fee left, not eagerly sent)"
    );

    // Permissionless sweep moves exactly the network-only residual to the aggregator.
    lane.onramp_client
        .withdraw_fee_tokens(&vec![env, lane.fee_token.clone()]);
    assert_eq!(
        fee_token_client.balance(&lane.onramp_id),
        0,
        "sweep must drain the OnRamp residual"
    );
    assert_eq!(
        fee_token_client.balance(&lane.fee_aggregator),
        expected_residual,
        "the network-only residual must reach the fee aggregator via the sweep"
    );
}

/// INV-FEE-14: for a LINK fee token (premium 90), the GAS (exec-cost) slice is NOT
/// discounted — it is converted with the BARE helper — while the network slice IS
/// discounted. Proved by:
///   - executor balance == `with_premium(flat=25, 90, price)` + `bare(exec_cost,
///     price)` (flat discounted, exec-cost bare);
///   - executor balance > `with_premium(flat + exec_cost, 90, price)` — if the
///     exec-cost were also discounted the balance would equal (or be below) this
///     lower value, so strict-greater proves the gas slice escapes the discount;
///   - `get_message_fee` quotes the network fee discounted (100 × 90% = 90 cents),
///     confirming the premium still applies to the network slice.
#[test]
fn test_link_gas_not_discounted() {
    let lane = setup_fee_dist_lane_impl(true, 90); // LINK fee token, pm = 90
    let env = &lane.env;

    let (receipts, message, _required_fee) = lane.send_data_only();
    let price = lane.fee_token_price(&message);
    const PM: u32 = 90;
    const EXECUTOR_FLAT_USD: u128 = 25;

    let exec_receipt = receipts.get(2).unwrap();
    let exec_cost_usd = (exec_receipt.fee_token_amount as u128) - EXECUTOR_FLAT_USD;
    assert!(
        exec_cost_usd > 0,
        "exec-cost must be positive for the not-discounted assertion to be meaningful"
    );

    let executor_balance =
        fee_math::usd_cents_to_fee_token_with_premium(EXECUTOR_FLAT_USD, PM, price)
            .expect("flat")
            .checked_add(fee_math::usd_cents_to_fee_token(exec_cost_usd, price).expect("exec_cost"))
            .unwrap();

    let fee_token_client = token::Client::new(env, &lane.fee_token);
    assert_eq!(
        fee_token_client.balance(&lane.default_executor),
        executor_balance,
        "executor = flat (discounted) + exec_cost (BARE) — gas not discounted (INV-FEE-14)"
    );

    // If the gas slice were discounted too, the executor balance would be at most
    // `with_premium(flat + exec_cost, 90, price)`. Strict-greater proves it is not.
    let fully_discounted =
        fee_math::usd_cents_to_fee_token_with_premium(EXECUTOR_FLAT_USD + exec_cost_usd, PM, price)
            .expect("fully discounted");
    assert!(
        executor_balance > fully_discounted,
        "gas slice must escape the LINK discount: executor={} fully_discounted={}",
        executor_balance,
        fully_discounted
    );

    // The network slice IS discounted: get_message_fee is network-only (100 cents
    // × 90% = 90 cents).
    let message_fee = lane
        .fee_quoter_client
        .get_message_fee(&lane.evm_chain_selector, &message);
    assert_eq!(
        message_fee.fee_usd_cents, 90,
        "network slice is LINK-discounted (100 × 90%)"
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

/// H-5 / INV-SRC-5 + M-7: the token-transfer counterpart to the data-only
/// no-exec test. With the no-execution sentinel as the message executor, the
/// executor fee (flat + exec-gas) and the H-3 fee transfer must both be zeroed
/// (no auto-execution), BUT `execution_gas_limit` is a *message property*,
/// not a priced cost — it is still computed as Σ CCV gas (0) + pool
/// `dest_gas_overhead` (75_000) + `base_execution_gas_cost` (200_000) + user
/// `gas_limit` (0) = 275_000, exactly as for an auto-executed transfer. This
/// pins EVM parity: `OnRamp._getReceipts` builds `gasLimitSum` (CCV + pool +
/// executor gas) and writes it to `message.header.executionGasLimit`
/// regardless of whether the executor is the no-exec sentinel, while the
/// no-exec branch (`Executor.isNoExecution`) zeroes only the executor *fee*.
#[test]
fn test_no_execution_sentinel_token_transfer_keeps_pool_gas_in_message() {
    let lane = setup_token_transfer_lane();
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

    let (receipts, encoded) = lane.send_with_extra_args(extra_args.to_xdr(env));

    // Token transfer: [CCV, Pool, Executor, NetworkFee].
    assert_eq!(
        receipts.len(),
        4,
        "expected 1 CCV + pool + executor + network"
    );
    // The pool receipt still carries its dest_gas_overhead (75_000) — the pool
    // ran `get_fee` regardless of the executor sentinel.
    assert_eq!(receipts.get(1).unwrap().dest_gas_limit, 75_000);

    let exec_receipt = TokenTransferLane::executor_receipt(&receipts);
    assert_eq!(
        exec_receipt.issuer, no_exec,
        "executor receipt issuer must be the no-execution sentinel"
    );
    assert_eq!(
        exec_receipt.fee_token_amount, 0,
        "no-execution sentinel must yield zero executor fee (flat + exec gas)"
    );

    // The on-wire message still advertises the full execution_gas_limit —
    // pool gas included — even though nothing is priced for it.
    let decoded = CcipMessageV1::from_bytes(env, &encoded).expect("decode encoded message");
    assert_eq!(
        decoded.execution_gas_limit, 275_000,
        "execution_gas_limit is a message property: it must include the pool \
         dest_gas_overhead (75_000) + base_execution_gas_cost (200_000) even \
         with the no-execution sentinel"
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

/// H-5 / INV-SRC-5 complement to `test_ccip_send_emits_token_pool_receipt...`:
/// a data-only send has no pool receipt, so `pool_dest_gas_limit` is 0 and the
/// on-wire `execution_gas_limit` must be just CCV gas (0, mock verifier) +
/// `base_execution_gas_cost` (200_000) + user `gas_limit` (0) = 200_000. This
/// pins that the pool-overhead term added to `execution_gas_limit` is scoped to
/// token transfers only and does not inflate data-only messages.
#[test]
fn test_data_only_execution_gas_limit_excludes_pool_overhead() {
    let lane = setup_data_only_lane(25, 0);
    let env = &lane.env;

    let extra_args = GenericExtraArgsV3::new(env, lane.default_executor.clone());
    let (_, encoded) = lane.send_data_only_full(extra_args);
    let decoded = CcipMessageV1::from_bytes(env, &encoded).expect("decode encoded message");

    assert_eq!(
        decoded.execution_gas_limit, 200_000,
        "data-only execution_gas_limit must be base_execution_gas_cost only (no pool overhead)"
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

// ============================================================
// H-11 / INV-CFG-7: config-time CCV set validation
// ============================================================
//
// `DestChainConfigArgs::validate` must reject a CCV configuration where either
// `default_ccvs` or `lane_mandated_ccvs` contains a duplicate, and where a CCV is
// classified as BOTH a default and lane-mandated (EVM
// `CCVConfigValidation._assertNoDuplicates`). Within-list duplicates surface as
// `DuplicateCCVNotAllowed` (#320); cross-list overlap surfaces as `InvalidConfig`
// (#52). A valid unique configuration applies cleanly.

/// Two identical CCVs in `default_ccvs` are rejected with `DuplicateCCVNotAllowed`
/// (#320) at config-apply time.
#[test]
#[should_panic(expected = "Error(Contract, #320)")] // DuplicateCCVNotAllowed
fn test_dest_chain_config_rejects_duplicate_default_ccvs() {
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

    let dup = Address::generate(&env);
    let mut args = create_test_dest_chain_config_args(&env, 67890);
    args.default_ccvs = vec![&env, dup.clone(), dup.clone()];
    client.apply_dest_chain_config_updates(&vec![&env, args]);
}

/// Two identical CCVs in `lane_mandated_ccvs` are rejected with
/// `DuplicateCCVNotAllowed` (#320) at config-apply time.
#[test]
#[should_panic(expected = "Error(Contract, #320)")] // DuplicateCCVNotAllowed
fn test_dest_chain_config_rejects_duplicate_mandated_ccvs() {
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

    let dup = Address::generate(&env);
    let mut args = create_test_dest_chain_config_args(&env, 67890);
    args.lane_mandated_ccvs = vec![&env, dup.clone(), dup.clone()];
    client.apply_dest_chain_config_updates(&vec![&env, args]);
}

/// A CCV present in BOTH `default_ccvs` and `lane_mandated_ccvs` is rejected with
/// `InvalidConfig` (#52) at config-apply time — a CCV must not be double-classified.
#[test]
#[should_panic(expected = "Error(Contract, #52)")] // InvalidConfig - cross-list CCV overlap
fn test_dest_chain_config_rejects_cross_list_ccv_overlap() {
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

    let shared = Address::generate(&env);
    let mut args = create_test_dest_chain_config_args(&env, 67890);
    args.default_ccvs = vec![&env, shared.clone()];
    args.lane_mandated_ccvs = vec![&env, shared];
    client.apply_dest_chain_config_updates(&vec![&env, args]);
}

/// A CCV set with non-empty, distinct `default_ccvs` and `lane_mandated_ccvs` applies
/// cleanly (positive control for the H-11 validation path).
#[test]
fn test_dest_chain_config_accepts_unique_ccv_set() {
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
    args.default_ccvs = vec![&env, Address::generate(&env), Address::generate(&env)];
    args.lane_mandated_ccvs = vec![&env, Address::generate(&env)];
    client.apply_dest_chain_config_updates(&vec![&env, args.clone()]);

    // Sanity: round-trips through storage.
    let stored = client.get_dest_chain_config(&67890);
    assert_eq!(stored.default_ccvs, args.default_ccvs);
    assert_eq!(stored.lane_mandated_ccvs, args.lane_mandated_ccvs);
}

// ============================================================
// M-15 / INV-POOL-21: bound dest_pool_data ≤ dest_bytes_overhead
// ============================================================

/// M-15 / INV-POOL-21: the pool's `dest_pool_data` (wire `extraData`) must not exceed the
/// `dest_bytes_overhead` quoted and paid for in the pool receipt. Mirrors EVM
/// `OnRamp.sol:317-323` (`actualExtraDataLength > maxExtraDataLength` ⇒ `SourceTokenDataTooLarge`).
/// The harness FeeQuoter advertises `dest_bytes_overhead = 64`; a mock pool returning 100 bytes
/// of `dest_pool_data` from `lock_or_burn` must make `ccip_send` revert with #35.
#[test]
#[should_panic(expected = "Error(Contract, #35)")] // SourceTokenDataTooLarge
fn test_ccip_send_rejects_dest_pool_data_exceeding_overhead() {
    let mut lane = setup_token_transfer_lane_with_pool(None);

    // Mock pool that returns 100 bytes of dest_pool_data ( > the 64-byte overhead).
    let mock_pool_id = lane.env.register(mock_pool::MockPoolLargeDestPoolData, ());
    let mock_client = mock_pool::MockPoolLargeDestPoolDataClient::new(&lane.env, &mock_pool_id);
    mock_client.set_dest_pool_data_len(&100);
    lane.mock_pool_id = Some(mock_pool_id.clone());
    rebind_pool_to_mock(&lane, &mock_pool_id);

    // `send` quotes a fee (pool.get_fee disabled ⇒ FeeQuoter overhead 64), funds the
    // sender, and calls `ccip_send` → `forward_from_router` → `lock_or_burn` returns 100
    // bytes ⇒ the M-15 guard reverts SourceTokenDataTooLarge before the message is emitted.
    let _ = lane.send();
}

/// M-15 boundary: `dest_pool_data` length exactly equal to `dest_bytes_overhead` (64) is
/// accepted (EVM uses strict `>`). Reuses the same mock pool with len = 64.
#[test]
fn test_ccip_send_accepts_dest_pool_data_equal_to_overhead() {
    let mut lane = setup_token_transfer_lane_with_pool(None);

    let mock_pool_id = lane.env.register(mock_pool::MockPoolLargeDestPoolData, ());
    let mock_client = mock_pool::MockPoolLargeDestPoolDataClient::new(&lane.env, &mock_pool_id);
    mock_client.set_dest_pool_data_len(&64); // == overhead ⇒ accepted (strict >)
    lane.mock_pool_id = Some(mock_pool_id.clone());
    rebind_pool_to_mock(&lane, &mock_pool_id);

    // Must not trap: the message is emitted and a token-pool receipt is present.
    let receipts = lane.send();
    let want: u32 = 4; // [CCV, Pool, Executor, NetworkFee]
    assert_eq!(
        receipts.len(),
        want,
        "equal-length dest_pool_data must be accepted (EVM strict `>` parity)"
    );
}

// ============================================================
// M-2 / INV-MSG-8: validate token_receiver length on send
// ============================================================

/// M-2 / INV-MSG-8 / INV-LCFG-3: a sender-specified `token_receiver` whose length does not
/// match the destination's `address_bytes_length` must be rejected on send. EVM validates the
/// token receiver in `_lockOrBurnSingleToken` via `_validateDestChainAddress(receiver,
/// destAddressBytesLength)` (OnRamp.sol:779). The lane's `address_bytes_length` is 20; a
/// 15-byte `token_receiver` ⇒ `InvalidDestChainAddress` (#43).
#[test]
#[should_panic(expected = "Error(Contract, #43)")] // InvalidDestChainAddress
fn test_ccip_send_rejects_wrong_length_token_receiver() {
    let lane = setup_token_transfer_lane();
    let env = &lane.env;

    // 15-byte token_receiver ≠ address_bytes_length (20). token_receiver_allowed is true,
    // so the allowed-flag gate passes and the length gate fires.
    let extra_args = GenericExtraArgsV3 {
        gas_limit: 0,
        block_confirmations: 0,
        ccvs: Vec::new(env),
        ccv_args: Vec::new(env),
        executor: GenericExtraArgsV3::use_default_executor_address(env),
        executor_args: Bytes::new(env),
        token_receiver: Bytes::from_array(env, &[0x44u8; 15]),
        token_args: Bytes::new(env),
    };

    let mut token_amounts: Vec<TokenAmount> = Vec::new(env);
    token_amounts.push_back(TokenAmount {
        token: lane.transfer_token.clone(),
        amount: 1_000_000,
    });
    let message = StellarToAnyMessage {
        receiver: Bytes::from_array(env, &[0x33u8; 20]), // valid 20-byte receiver
        data: Bytes::from_slice(env, b"token send with data"),
        token_amounts,
        fee_token: lane.fee_token.clone(),
        extra_args: extra_args.to_xdr(env),
    };

    let required_fee = lane
        .router_client
        .get_fee(&lane.evm_chain_selector, &message);
    assert!(required_fee > 0, "quoted fee must be positive");
    lane.fee_token_sac.mint(&lane.sender, &(required_fee * 2));
    lane.transfer_token_sac.mint(&lane.sender, &1_000_000);
    let _ = lane.router_client.ccip_send(
        &lane.sender,
        &lane.evm_chain_selector,
        &message,
        &required_fee,
    );
}

/// M-2 positive: a sender-specified `token_receiver` of the correct length (20) is accepted.
#[test]
fn test_ccip_send_accepts_correct_length_token_receiver() {
    let lane = setup_token_transfer_lane();
    let env = &lane.env;

    let extra_args = GenericExtraArgsV3 {
        gas_limit: 0,
        block_confirmations: 0,
        ccvs: Vec::new(env),
        ccv_args: Vec::new(env),
        executor: GenericExtraArgsV3::use_default_executor_address(env),
        executor_args: Bytes::new(env),
        token_receiver: Bytes::from_array(env, &[0x55u8; 20]), // matches address_bytes_length
        token_args: Bytes::new(env),
    };

    let mut token_amounts: Vec<TokenAmount> = Vec::new(env);
    token_amounts.push_back(TokenAmount {
        token: lane.transfer_token.clone(),
        amount: 1_000_000,
    });
    let message = StellarToAnyMessage {
        receiver: Bytes::from_array(env, &[0x33u8; 20]),
        data: Bytes::from_slice(env, b"token send with data"),
        token_amounts,
        fee_token: lane.fee_token.clone(),
        extra_args: extra_args.to_xdr(env),
    };

    let required_fee = lane
        .router_client
        .get_fee(&lane.evm_chain_selector, &message);
    assert!(required_fee > 0, "quoted fee must be positive");
    lane.fee_token_sac.mint(&lane.sender, &(required_fee * 2));
    lane.transfer_token_sac.mint(&lane.sender, &1_000_000);
    lane.router_client.ccip_send(
        &lane.sender,
        &lane.evm_chain_selector,
        &message,
        &required_fee,
    );

    // ccip_send did not trap; a full 4-receipt token send was emitted.
    let receipts = receipts_from_last_onramp_ccip_event(env, &lane.onramp_id);
    let want: u32 = 4; // [CCV, Pool, Executor, NetworkFee]
    assert_eq!(
        receipts.len(),
        want,
        "correct-length token_receiver must be accepted and emit a full token-send receipt set"
    );
}
