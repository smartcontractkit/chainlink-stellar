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
use common_message::{StellarToAnyMessage, TokenAmount};
use common_pool::{ChainUpdate, RateLimitConfig};
use fee_quoter::{
    types::{
        DestChainConfig, DestChainConfigArgs as FqDestChainConfigArgs, GasPriceUpdate,
        PriceUpdates, StaticConfig as FqStaticConfig, TokenFeeConfigArgs, TokenPriceUpdate,
        TokenTransferFeeConfig,
    },
    FeeQuoterContract, FeeQuoterContractClient,
};
use pools_lock_release_pool::{LockReleaseTokenPoolContract, LockReleaseTokenPoolContractClient};
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
    let default_executor = Address::generate(&env);

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
    assert_eq!(receipts.get(1).unwrap().dest_gas_limit, 0);
    assert_eq!(receipts.get(1).unwrap().dest_bytes_overhead, 0);

    assert_eq!(receipts.get(2).unwrap().issuer, default_executor);
    assert_eq!(receipts.get(3).unwrap().issuer, router_id);
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
        receiver: Bytes::from_array(&env, &[0x33u8; 20]),
        data: Bytes::from_slice(&env, b"trips the per-message fee cap"),
        token_amounts: Vec::new(&env),
        fee_token: fee_token.clone(),
        extra_args: Bytes::new(&env),
    };

    // Must revert with FeeExceedsMaxAllowed (#44), not quote a fee.
    onramp_client.get_fee(&evm_chain_selector, &message);
}
