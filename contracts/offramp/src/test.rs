#![cfg(test)]

use common_message::{CcipMessageV1, MessageIdCompute, ToBytes, MESSAGE_V1_VERSION};
use rmn_proxy::{RmnProxyContract, RmnProxyContractClient};
use rmn_remote::{RmnRemoteContract, RmnRemoteContractClient};
use soroban_sdk::{
    contract, contractimpl, symbol_short, testutils::Address as _, xdr::ToXdr, Address, Bytes,
    BytesN, Env, Symbol, Vec,
};

use crate::types::{
    DataKey, MessageExecutionState, SourceChainConfig, SourceChainConfigArgs, StaticConfig,
};
use crate::{OffRampContract, OffRampContractClient};
use common_error::CCIPError;
use common_interfaces::ccip_receiver::CcvsAndFinalityConfig;

fn setup_env() -> (Env, Address, OffRampContractClient<'static>) {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(OffRampContract, ());
    let client = OffRampContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    (env, owner, client)
}

fn default_static_config(env: &Env) -> StaticConfig {
    StaticConfig {
        chain_selector: 1234,
        rmn_proxy: Address::generate(env),
        token_admin_registry: Address::generate(env),
    }
}

#[test]
fn test_initialize() {
    let (env, owner, client) = setup_env();
    let static_config = default_static_config(&env);

    client.initialize(&owner, &static_config);

    let stored_config = client.get_static_config();
    assert_eq!(stored_config.chain_selector, 1234);
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")]
fn test_double_initialize_fails() {
    let (env, owner, client) = setup_env();
    let static_config = default_static_config(&env);

    client.initialize(&owner, &static_config);
    client.initialize(&owner, &static_config);
}

#[test]
fn test_get_execution_state_untouched() {
    let (env, owner, client) = setup_env();
    let static_config = default_static_config(&env);
    client.initialize(&owner, &static_config);

    let message_id = BytesN::from_array(&env, &[0u8; 32]);
    let state = client.get_execution_state(&message_id);
    assert_eq!(state, MessageExecutionState::Untouched);
}

#[test]
#[should_panic(expected = "Error(Contract, #106)")]
fn test_extend_execution_state_ttl_requires_storage_entry() {
    let (env, owner, client) = setup_env();
    let static_config = default_static_config(&env);
    client.initialize(&owner, &static_config);

    let message_id = BytesN::from_array(&env, &[9u8; 32]);
    let _ = client.extend_execution_state_ttl(&message_id);
}

#[test]
fn test_extend_execution_state_ttl_ok() {
    let env = Env::default();
    env.mock_all_auths();
    let contract_id = env.register(OffRampContract, ());
    let client = OffRampContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let static_config = default_static_config(&env);
    client.initialize(&owner, &static_config);

    let message_id = BytesN::from_array(&env, &[7u8; 32]);
    let state_key = DataKey::ExecState(message_id.clone());

    env.as_contract(&contract_id, || {
        env.storage()
            .persistent()
            .set(&state_key, &MessageExecutionState::Failure);
        env.storage()
            .persistent()
            .extend_ttl(&state_key, 518_400, 3_110_400);
    });

    client.extend_execution_state_ttl(&message_id);
}

#[test]
fn test_apply_source_chain_config() {
    let (env, owner, client) = setup_env();
    let static_config = default_static_config(&env);
    client.initialize(&owner, &static_config);

    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let onramp_bytes = Bytes::from_array(&env, &[1u8; 32]);

    let mut on_ramps = Vec::new(&env);
    on_ramps.push_back(onramp_bytes);

    let mut default_ccvs = Vec::new(&env);
    default_ccvs.push_back(default_ccv);

    let args = SourceChainConfigArgs {
        source_chain_selector: 5678,
        router: router.clone(),
        is_enabled: true,
        on_ramps,
        default_ccvs,
        lane_mandated_ccvs: Vec::new(&env),
    };

    let mut updates = Vec::new(&env);
    updates.push_back(args);

    client.apply_source_chain_cfg_updates(&updates);

    let config = client.get_source_chain_config(&5678);
    assert_eq!(config.is_enabled, true);
    assert_eq!(config.router, router);
}

#[test]
fn test_get_all_source_chain_configs() {
    let (env, owner, client) = setup_env();
    let static_config = default_static_config(&env);
    client.initialize(&owner, &static_config);

    let onramp_bytes = Bytes::from_array(&env, &[1u8; 32]);

    let mut on_ramps = Vec::new(&env);
    on_ramps.push_back(onramp_bytes.clone());

    let mut default_ccvs = Vec::new(&env);
    default_ccvs.push_back(Address::generate(&env));

    let args1 = SourceChainConfigArgs {
        source_chain_selector: 100,
        router: Address::generate(&env),
        is_enabled: true,
        on_ramps: on_ramps.clone(),
        default_ccvs: default_ccvs.clone(),
        lane_mandated_ccvs: Vec::new(&env),
    };

    let args2 = SourceChainConfigArgs {
        source_chain_selector: 200,
        router: Address::generate(&env),
        is_enabled: false,
        on_ramps,
        default_ccvs,
        lane_mandated_ccvs: Vec::new(&env),
    };

    let mut updates = Vec::new(&env);
    updates.push_back(args1);
    updates.push_back(args2);

    client.apply_source_chain_cfg_updates(&updates);

    let (selectors, configs) = client.get_all_source_chain_configs();
    assert_eq!(selectors.len(), 2);
    assert_eq!(configs.len(), 2);
}

#[test]
#[should_panic(expected = "Error(Contract, #101)")]
fn test_source_chain_config_zero_selector_fails() {
    let (env, owner, client) = setup_env();
    let static_config = default_static_config(&env);
    client.initialize(&owner, &static_config);

    let args = SourceChainConfigArgs {
        source_chain_selector: 0,
        router: Address::generate(&env),
        is_enabled: true,
        on_ramps: Vec::new(&env),
        default_ccvs: Vec::new(&env),
        lane_mandated_ccvs: Vec::new(&env),
    };

    let mut updates = Vec::new(&env);
    updates.push_back(args);
    client.apply_source_chain_cfg_updates(&updates);
}

// ============================================================
// execute() — validation paths
// ============================================================

const EXEC_TEST_SRC_CHAIN: u64 = 5678;
const EXEC_TEST_DEST_CHAIN: u64 = 1234;

/// OffRamp `execute` calls `require_not_cursed`, which invokes the configured RMN proxy. Random
/// addresses in `default_static_config` are not valid proxy contracts — deploy Remote + Proxy like production.
fn setup_initialized_offramp_for_execute() -> (Env, OffRampContractClient<'static>) {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    RmnRemoteContractClient::new(&env, &rmn_remote_id)
        .initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    RmnProxyContractClient::new(&env, &rmn_proxy_id).initialize(&owner, &rmn_remote_id);

    let static_config = StaticConfig {
        chain_selector: EXEC_TEST_DEST_CHAIN,
        rmn_proxy: rmn_proxy_id,
        token_admin_registry: Address::generate(&env),
    };

    let contract_id = env.register(OffRampContract, ());
    let client = OffRampContractClient::new(&env, &contract_id);
    client.initialize(&owner, &static_config);

    (env, client)
}

/// Wire-format `offramp_address` bytes: last 32 bytes of the contract's Soroban Address XDR.
fn offramp_address_field_from_contract(env: &Env, contract: &Address) -> Bytes {
    let xdr = contract.to_xdr(env);
    let start = xdr.len() - 32;
    xdr.slice(start..xdr.len())
}

fn sample_onramp_bytes(env: &Env) -> Bytes {
    Bytes::from_array(env, &[1u8; 32])
}

fn apply_source_lane(
    env: &Env,
    client: &OffRampContractClient,
    router: Address,
    default_ccv: Address,
    onramp: Bytes,
    enabled: bool,
) {
    let mut on_ramps = Vec::new(env);
    on_ramps.push_back(onramp);

    let mut default_ccvs = Vec::new(env);
    default_ccvs.push_back(default_ccv);

    let args = SourceChainConfigArgs {
        source_chain_selector: EXEC_TEST_SRC_CHAIN,
        router,
        is_enabled: enabled,
        on_ramps,
        default_ccvs,
        lane_mandated_ccvs: Vec::new(env),
    };

    let mut updates = Vec::new(env);
    updates.push_back(args);
    client.apply_source_chain_cfg_updates(&updates);
}

fn valid_execute_message(env: &Env, offramp_contract: &Address, onramp: Bytes) -> CcipMessageV1 {
    CcipMessageV1 {
        source_chain_selector: EXEC_TEST_SRC_CHAIN,
        dest_chain_selector: EXEC_TEST_DEST_CHAIN,
        sequence_number: 1,
        execution_gas_limit: 0,
        ccip_receive_gas_limit: 0,
        finality: 0,
        ccv_and_executor_hash: BytesN::from_array(env, &[0u8; 32]),
        onramp_address: onramp,
        offramp_address: offramp_address_field_from_contract(env, offramp_contract),
        sender: Bytes::from_array(env, &[2u8; 20]),
        receiver: Bytes::from_array(env, &[0u8; 32]),
        dest_blob: Bytes::new(env),
        token_transfer: Bytes::new(env),
        data: Bytes::new(env),
    }
}

#[test]
#[should_panic(expected = "Error(Contract, #100)")]
fn test_execute_source_chain_not_enabled() {
    let (env, client) = setup_initialized_offramp_for_execute();

    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(&env, &client, router, default_ccv, onramp.clone(), false);

    let msg = valid_execute_message(&env, &client.address, onramp);
    let encoded = msg.to_bytes(&env);
    assert_eq!(encoded.get(0).unwrap(), MESSAGE_V1_VERSION);

    let ccvs = Vec::new(&env);
    let verifier_results = Vec::new(&env);
    client.execute(&encoded, &ccvs, &verifier_results, &0u32);
}

#[test]
#[should_panic(expected = "Error(Contract, #100)")]
fn test_execute_source_chain_not_configured() {
    let (env, client) = setup_initialized_offramp_for_execute();

    let onramp = sample_onramp_bytes(&env);
    let msg = valid_execute_message(&env, &client.address, onramp);
    let encoded = msg.to_bytes(&env);

    let ccvs = Vec::new(&env);
    let verifier_results = Vec::new(&env);
    client.execute(&encoded, &ccvs, &verifier_results, &0u32);
}

#[test]
#[should_panic(expected = "Error(Contract, #102)")]
fn test_execute_invalid_onramp() {
    let (env, client) = setup_initialized_offramp_for_execute();

    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let allowed_onramp = sample_onramp_bytes(&env);
    apply_source_lane(&env, &client, router, default_ccv, allowed_onramp, true);

    let bad_onramp = Bytes::from_array(&env, &[99u8; 32]);
    let msg = valid_execute_message(&env, &client.address, bad_onramp);
    let encoded = msg.to_bytes(&env);

    let ccvs = Vec::new(&env);
    let verifier_results = Vec::new(&env);
    client.execute(&encoded, &ccvs, &verifier_results, &0u32);
}

#[test]
#[should_panic(expected = "Error(Contract, #103)")]
fn test_execute_invalid_offramp_address() {
    let (env, client) = setup_initialized_offramp_for_execute();

    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(&env, &client, router, default_ccv, onramp.clone(), true);

    let mut msg = valid_execute_message(&env, &client.address, onramp);
    msg.offramp_address = Bytes::from_array(&env, &[0xEEu8; 32]);
    let encoded = msg.to_bytes(&env);

    let ccvs = Vec::new(&env);
    let verifier_results = Vec::new(&env);
    client.execute(&encoded, &ccvs, &verifier_results, &0u32);
}

#[test]
#[should_panic(expected = "Error(Contract, #104)")]
fn test_execute_invalid_message_destination() {
    let (env, client) = setup_initialized_offramp_for_execute();

    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(&env, &client, router, default_ccv, onramp.clone(), true);

    let mut msg = valid_execute_message(&env, &client.address, onramp);
    msg.dest_chain_selector = 999_999;
    let encoded = msg.to_bytes(&env);

    let ccvs = Vec::new(&env);
    let verifier_results = Vec::new(&env);
    client.execute(&encoded, &ccvs, &verifier_results, &0u32);
}

#[test]
#[should_panic(expected = "Error(Contract, #107)")]
fn test_execute_ccv_length_mismatch() {
    let (env, client) = setup_initialized_offramp_for_execute();

    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(&env, &client, router, default_ccv, onramp.clone(), true);

    let msg = valid_execute_message(&env, &client.address, onramp);
    let encoded = msg.to_bytes(&env);

    let mut ccvs = Vec::new(&env);
    ccvs.push_back(Address::generate(&env));
    let verifier_results = Vec::new(&env);
    client.execute(&encoded, &ccvs, &verifier_results, &0u32);
}

#[test]
#[should_panic(expected = "Error(Contract, #110)")]
fn test_execute_gas_limit_override_too_low() {
    let (env, client) = setup_initialized_offramp_for_execute();

    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(&env, &client, router, default_ccv, onramp.clone(), true);

    let mut msg = valid_execute_message(&env, &client.address, onramp);
    msg.ccip_receive_gas_limit = 10_000;
    let encoded = msg.to_bytes(&env);

    let ccvs = Vec::new(&env);
    let verifier_results = Vec::new(&env);
    client.execute(&encoded, &ccvs, &verifier_results, &100u32);
}

#[test]
#[should_panic(expected = "Error(Contract, #105)")]
fn test_execute_message_already_success() {
    let (env, client) = setup_initialized_offramp_for_execute();

    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(&env, &client, router, default_ccv, onramp.clone(), true);

    let msg = valid_execute_message(&env, &client.address, onramp);
    let encoded = msg.to_bytes(&env);
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);
    let state_key = DataKey::ExecState(message_id);

    env.as_contract(&client.address, || {
        env.storage()
            .persistent()
            .set(&state_key, &MessageExecutionState::Success);
        env.storage()
            .persistent()
            .extend_ttl(&state_key, 518_400, 3_110_400);
    });

    let ccvs = Vec::new(&env);
    let verifier_results = Vec::new(&env);
    client.execute(&encoded, &ccvs, &verifier_results, &0u32);
}

#[test]
#[should_panic(expected = "Error(Contract, #105)")]
fn test_execute_message_already_in_progress() {
    let (env, client) = setup_initialized_offramp_for_execute();

    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(&env, &client, router, default_ccv, onramp.clone(), true);

    let msg = valid_execute_message(&env, &client.address, onramp);
    let encoded = msg.to_bytes(&env);
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);
    let state_key = DataKey::ExecState(message_id);

    env.as_contract(&client.address, || {
        env.storage()
            .persistent()
            .set(&state_key, &MessageExecutionState::InProgress);
        env.storage()
            .persistent()
            .extend_ttl(&state_key, 518_400, 3_110_400);
    });

    let ccvs = Vec::new(&env);
    let verifier_results = Vec::new(&env);
    client.execute(&encoded, &ccvs, &verifier_results, &0u32);
}

#[test]
fn test_execute_reexecute_after_failure_succeeds() {
    let (env, client) = setup_initialized_offramp_for_execute();

    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(&env, &client, router, default_ccv, onramp.clone(), true);

    let msg = valid_execute_message(&env, &client.address, onramp);
    let encoded = msg.to_bytes(&env);
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    let ccvs = Vec::new(&env);
    let verifier_results = Vec::new(&env);

    assert!(client
        .try_execute(&encoded, &ccvs, &verifier_results, &0u32)
        .is_ok());
    assert_eq!(
        client.get_execution_state(&message_id),
        MessageExecutionState::Failure
    );

    assert!(client
        .try_execute(&encoded, &ccvs, &verifier_results, &0u32)
        .is_ok());
    assert_eq!(
        client.get_execution_state(&message_id),
        MessageExecutionState::Failure
    );
}

// ============================================================
// C-1 + H-7 — receiver CCV/finality consultation (non-token-only)
//
// `merge_receiver_ccvs` and `ensure_quorum_present` are factored as pure helpers so the
// merge / threshold / defaults-sentinel / quorum logic is unit-testable without a deployed
// receiver or a live verifier. The `execute` test below proves the fail-fast Wasm check on a
// non-contract receiver is non-trapping and records `Failure`. The `try_invoke` arms (non-V2
// missing symbol / trap / typed `Err` / successful config) are covered by the mock-receiver
// tests at the bottom of this file. The one path NOT coverable here is the full happy path
// (consult ok → quorum ok → verify-all loop), which needs a live verifier — that stays a
// Go/integration (nix) follow-up.
// ============================================================

fn src_config(
    env: &Env,
    default_ccvs: Vec<Address>,
    lane_mandated_ccvs: Vec<Address>,
) -> SourceChainConfig {
    SourceChainConfig {
        router: Address::generate(env),
        is_enabled: true,
        on_ramps: Vec::new(env),
        default_ccvs,
        lane_mandated_ccvs,
    }
}

fn ccvs_and_finality(
    required_ccvs: Vec<Address>,
    optional_ccvs: Vec<Address>,
    optional_threshold: u32,
    allowed_finality_config: u32,
) -> CcvsAndFinalityConfig {
    CcvsAndFinalityConfig {
        allowed_finality_config,
        optional_ccvs,
        optional_threshold,
        required_ccvs,
    }
}

#[test]
fn test_merge_receiver_ccvs_threshold_validation() {
    // optional_threshold (1) > optional_ccvs.len() (0) ⇒ #117 (EVM InvalidOptionalThreshold).
    let env = Env::default();
    let config = ccvs_and_finality(Vec::new(&env), Vec::new(&env), 1, 0);
    let sc = src_config(&env, Vec::new(&env), Vec::new(&env));
    let res = OffRampContract::merge_receiver_ccvs(&env, &config, &Vec::new(&env), &sc);
    assert_eq!(res, Err(CCIPError::InvalidOptionalThreshold));
}

#[test]
fn test_merge_receiver_ccvs_empty_required_uses_defaults() {
    // Empty receiver-required + threshold 0 ⇒ fold in the lane default CCVs (Stellar sentinel;
    // EVM uses an `address(0)` marker). Lane-mandated is always merged.
    let env = Env::default();
    let default_ccv = Address::generate(&env);
    let lane_ccv = Address::generate(&env);
    let mut defaults = Vec::new(&env);
    defaults.push_back(default_ccv.clone());
    let mut lane = Vec::new(&env);
    lane.push_back(lane_ccv.clone());
    let sc = src_config(&env, defaults, lane);

    let config = ccvs_and_finality(Vec::new(&env), Vec::new(&env), 0, 0);
    let (required, optional, threshold) =
        OffRampContract::merge_receiver_ccvs(&env, &config, &Vec::new(&env), &sc).unwrap();
    // Merge order is receiver → pool → lane-mandated → defaults (EVM); with receiver/pool empty,
    // lane-mandated comes first, then the defaults sentinel.
    assert_eq!(required.len(), 2);
    assert_eq!(required.get(0).unwrap(), lane_ccv);
    assert_eq!(required.get(1).unwrap(), default_ccv);
    assert_eq!(optional.len(), 0);
    assert_eq!(threshold, 0);
}

#[test]
fn test_merge_receiver_ccvs_merges_receiver_pool_lane_deduped() {
    // required = receiver.required + pool-required + lane-mandated, deduped. Receiver-required
    // is non-empty so the defaults sentinel does not fire.
    let env = Env::default();
    let r1 = Address::generate(&env);
    let pool1 = Address::generate(&env);
    let lane1 = Address::generate(&env);
    let mut req = Vec::new(&env);
    req.push_back(r1.clone());
    let mut pool = Vec::new(&env);
    pool.push_back(pool1.clone());
    let mut lane = Vec::new(&env);
    lane.push_back(lane1.clone());
    let sc = src_config(&env, Vec::new(&env), lane);

    let config = ccvs_and_finality(req, Vec::new(&env), 0, 0);
    let (required, optional, threshold) =
        OffRampContract::merge_receiver_ccvs(&env, &config, &pool, &sc).unwrap();
    assert_eq!(required.len(), 3);
    assert_eq!(required.get(0).unwrap(), r1);
    assert_eq!(required.get(1).unwrap(), pool1);
    assert_eq!(required.get(2).unwrap(), lane1);
    assert_eq!(optional.len(), 0);
    assert_eq!(threshold, 0);
}

#[test]
fn test_merge_receiver_ccvs_optional_minus_required_decrements_threshold() {
    // An optional entry that is also in `required` is dropped and the threshold is decremented.
    let env = Env::default();
    let r1 = Address::generate(&env);
    let o_keep = Address::generate(&env); // not in required ⇒ stays
    let mut req = Vec::new(&env);
    req.push_back(r1.clone());
    let mut opt = Vec::new(&env);
    opt.push_back(o_keep.clone());
    opt.push_back(r1.clone()); // duplicates a required ⇒ removed, threshold 2 → 1
    let sc = src_config(&env, Vec::new(&env), Vec::new(&env));

    let config = ccvs_and_finality(req, opt, 2, 0);
    let (required, optional, threshold) =
        OffRampContract::merge_receiver_ccvs(&env, &config, &Vec::new(&env), &sc).unwrap();
    assert_eq!(required.len(), 1);
    assert_eq!(required.get(0).unwrap(), r1);
    assert_eq!(optional.len(), 1);
    assert_eq!(optional.get(0).unwrap(), o_keep);
    assert_eq!(threshold, 1);
}

#[test]
fn test_ensure_quorum_required_missing() {
    // A required CCV absent from `ccvs` ⇒ #116.
    let env = Env::default();
    let mut required = Vec::new(&env);
    required.push_back(Address::generate(&env));
    let res =
        OffRampContract::ensure_quorum_present(&required, &Vec::new(&env), 0, &Vec::new(&env));
    assert_eq!(res, Err(CCIPError::RequiredCCVMissing));
}

#[test]
fn test_ensure_quorum_optional_not_reached() {
    // Fewer than `optional_threshold` optional CCVs present ⇒ #118.
    let env = Env::default();
    let o1 = Address::generate(&env);
    let o2 = Address::generate(&env);
    let mut optional = Vec::new(&env);
    optional.push_back(o1.clone());
    optional.push_back(o2);
    let mut ccvs = Vec::new(&env);
    ccvs.push_back(o1); // only 1 of 2 required optional present
    let res = OffRampContract::ensure_quorum_present(&Vec::new(&env), &optional, 2, &ccvs);
    assert_eq!(res, Err(CCIPError::OptionalCCVQuorumNotReached));
}

#[test]
fn test_ensure_quorum_ok() {
    // All required present and ≥ optional_threshold optional present ⇒ Ok.
    let env = Env::default();
    let r1 = Address::generate(&env);
    let o1 = Address::generate(&env);
    let o2 = Address::generate(&env);
    let mut required = Vec::new(&env);
    required.push_back(r1.clone());
    let mut optional = Vec::new(&env);
    optional.push_back(o1.clone());
    optional.push_back(o2);
    let mut ccvs = Vec::new(&env);
    ccvs.push_back(r1);
    ccvs.push_back(o1); // 1 optional present ≥ threshold 1
    let res = OffRampContract::ensure_quorum_present(&required, &optional, 1, &ccvs);
    assert!(res.is_ok());
}

#[test]
fn test_execute_non_token_only_non_contract_receiver_rejected_non_trapping() {
    // A non-token-only message (data non-empty) routes through the C-1 receiver-consultation
    // path. The receiver bytes ([0u8;32]) decode to a contract address with no ledger entry, so
    // the fail-fast Wasm-existence check in `get_ccvs_for_message` rejects it up front with
    // `ReceiverDoesNotExist` (#114) — *before* the pool call, *before* invoking the receiver, and
    // *before* consulting `ccvs`. `execute` wraps this as `Failure` + outer `Ok`.
    //
    // `ccvs` is deliberately NON-empty (it carries the lane `default_ccv`) to make the fail-fast
    // path observable: under the *old* defaults-fallback behavior the receiver consult would have
    // "succeeded" (not-V2 ⇒ defaults ⇒ required = [default_ccv]), the required CCV would be
    // present, and execution would proceed into the verify-all loop — which traps invoking the
    // verifier on the generated `default_ccv` address, leaving `try_execute` `Err` and the state
    // `InProgress`. The new fail-fast path instead returns `Failure` + `Ok` regardless of `ccvs`,
    // proving both that the reject-before-consult path is taken AND that it is non-trapping.
    let (env, client) = setup_initialized_offramp_for_execute();

    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(
        &env,
        &client,
        router,
        default_ccv.clone(),
        onramp.clone(),
        true,
    );

    let mut msg = valid_execute_message(&env, &client.address, onramp);
    msg.data = Bytes::from_array(&env, &[0xAA, 0xBB, 0xCC, 0xDD]); // ⇒ non-token-only
    let encoded = msg.to_bytes(&env);
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    let mut ccvs = Vec::new(&env);
    ccvs.push_back(default_ccv.clone());
    let mut verifier_results = Vec::new(&env);
    verifier_results.push_back(Bytes::new(&env));

    assert!(client
        .try_execute(&encoded, &ccvs, &verifier_results, &0u32)
        .is_ok());
    assert_eq!(
        client.get_execution_state(&message_id),
        MessageExecutionState::Failure
    );
}

// ============================================================
// C-1 receiver-consultation arms — mock receivers
// (get_ccvs_and_finality_config). Inline `#[contract]` types
// registered via `env.register(.., ())`; no extra crate deps.
// ============================================================

/// A Wasm contract that does NOT export `get_ccvs_and_finality_config` (a legacy / non-V2
/// receiver). The OffRamp consult must fail fast with `ReceiverError`.
#[contract]
pub struct MockNonV2Receiver;
#[contractimpl]
impl MockNonV2Receiver {
    pub fn noop(_env: Env) {}
}

/// A V2-shaped receiver that traps inside `get_ccvs_and_finality_config`. The trap must surface
/// as `ReceiverError` + `Failure` (retryable), NOT as an OffRamp trap (`InProgress`).
#[contract]
pub struct MockTrappingReceiver;
#[contractimpl]
impl MockTrappingReceiver {
    pub fn get_ccvs_and_finality_config(
        _env: Env,
        _source_chain_selector: u64,
        _unused: Bytes,
    ) -> Result<CcvsAndFinalityConfig, CCIPError> {
        panic!("receiver consult trap");
    }
}

/// A V2-shaped receiver that returns a typed `CCIPError`. It must be propagated (recorded as
/// `Failure`), not silently defaulted.
#[contract]
pub struct MockErrReceiver;
#[contractimpl]
impl MockErrReceiver {
    pub fn get_ccvs_and_finality_config(
        _env: Env,
        _source_chain_selector: u64,
        _unused: Bytes,
    ) -> Result<CcvsAndFinalityConfig, CCIPError> {
        Err(CCIPError::InvalidConfig)
    }
}

/// A V2-shaped receiver that returns a config staged via `set_config`. Used to exercise the
/// success arm of the consult + the merge + quorum + finality logic.
#[contract]
pub struct MockConfigReceiver;
const MOCK_CFG_KEY: Symbol = symbol_short!("CFG");
#[contractimpl]
impl MockConfigReceiver {
    pub fn set_config(env: Env, cfg: CcvsAndFinalityConfig) {
        env.storage().instance().set(&MOCK_CFG_KEY, &cfg);
    }
    pub fn get_ccvs_and_finality_config(
        env: Env,
        _source_chain_selector: u64,
        _unused: Bytes,
    ) -> Result<CcvsAndFinalityConfig, CCIPError> {
        Ok(env
            .storage()
            .instance()
            .get(&MOCK_CFG_KEY)
            .unwrap_or(CcvsAndFinalityConfig {
                required_ccvs: Vec::new(&env),
                optional_ccvs: Vec::new(&env),
                optional_threshold: 0,
                allowed_finality_config: 0,
            }))
    }
}

/// Build a non-token-only message (`data` non-empty) addressed to `receiver`, with the given
/// `finality`. Reuses [`valid_execute_message`] and overrides the receiver bytes + data + finality.
fn non_token_only_message(
    env: &Env,
    offramp_contract: &Address,
    onramp: Bytes,
    receiver: &Address,
    finality: u32,
) -> CcipMessageV1 {
    let mut msg = valid_execute_message(env, offramp_contract, onramp);
    msg.receiver = offramp_address_field_from_contract(env, receiver);
    msg.data = Bytes::from_array(env, &[0xAA, 0xBB, 0xCC, 0xDD]); // ⇒ non-token-only
    msg.finality = finality;
    msg
}

/// Assert the OffRamp records `Failure` and returns outer `Ok` — i.e. the inner rejection is
/// non-trapping. For the consult-failure arms `ccvs` is `[default_ccv]` (quorum-satisfying) so
/// fail-fast (`Failure`+`Ok`) is distinguishable from any "default-and-proceed" path, which would
/// reach the verify-all loop and trap (`Err`+`InProgress`).
fn assert_rejected_non_trapping(
    client: &OffRampContractClient,
    encoded: &Bytes,
    message_id: &BytesN<32>,
    ccvs: Vec<Address>,
    verifier_results: Vec<Bytes>,
) {
    assert!(
        client
            .try_execute(encoded, &ccvs, &verifier_results, &0u32)
            .is_ok(),
        "execute should return outer Ok (Failure), not trap"
    );
    assert_eq!(
        client.get_execution_state(message_id),
        MessageExecutionState::Failure,
        "state should be Failure, not InProgress (a trap would leave InProgress)"
    );
}

/// Common scaffolding: initialized OffRamp + a source lane with one `default_ccv` and no
/// lane-mandated CCVs. Returns `(env, client, default_ccv, onramp)`.
fn setup_lane_with_default_ccv() -> (Env, OffRampContractClient<'static>, Address, Bytes) {
    let (env, client) = setup_initialized_offramp_for_execute();
    let router = Address::generate(&env);
    let default_ccv = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(
        &env,
        &client,
        router,
        default_ccv.clone(),
        onramp.clone(),
        true,
    );
    (env, client, default_ccv, onramp)
}

#[test]
fn test_consult_non_v2_receiver_rejected() {
    // Wasm receiver missing `get_ccvs_and_finality_config` ⇒ consult Abort ⇒ `ReceiverError`
    // (fail-fast), recorded as `Failure` + outer `Ok`. `ccvs = [default_ccv]` makes this
    // observable: a "default-and-proceed" path would satisfy quorum and then trap in the
    // verify-all loop on the non-contract verifier address.
    let (env, client, default_ccv, onramp) = setup_lane_with_default_ccv();

    let receiver = env.register(MockNonV2Receiver, ());
    let msg = non_token_only_message(&env, &client.address, onramp, &receiver, 0);
    let encoded = msg.to_bytes(&env);
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    let mut ccvs = Vec::new(&env);
    ccvs.push_back(default_ccv.clone());
    let mut verifier_results = Vec::new(&env);
    verifier_results.push_back(Bytes::new(&env));

    assert_rejected_non_trapping(&client, &encoded, &message_id, ccvs, verifier_results);
}

#[test]
fn test_consult_trapping_receiver_rejected_non_trapping() {
    // V2-shaped receiver that panics inside the consult. The trap must be caught by
    // `try_invoke_contract` and mapped to `ReceiverError` (Failure + Ok), NOT propagate as an
    // OffRamp trap. A trap would leave `try_execute` `Err` and the state `InProgress`.
    let (env, client, default_ccv, onramp) = setup_lane_with_default_ccv();

    let receiver = env.register(MockTrappingReceiver, ());
    let msg = non_token_only_message(&env, &client.address, onramp, &receiver, 0);
    let encoded = msg.to_bytes(&env);
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    let mut ccvs = Vec::new(&env);
    ccvs.push_back(default_ccv.clone());
    let mut verifier_results = Vec::new(&env);
    verifier_results.push_back(Bytes::new(&env));

    assert_rejected_non_trapping(&client, &encoded, &message_id, ccvs, verifier_results);
}

#[test]
fn test_consult_receiver_returning_error_propagated() {
    // V2-shaped receiver returning `Err(CCIPError::InvalidConfig)` ⇒ propagated (Failure + Ok),
    // not silently defaulted. `ccvs = [default_ccv]` distinguishes propagation (Failure) from a
    // default-and-proceed path (which would trap in verify-all).
    let (env, client, default_ccv, onramp) = setup_lane_with_default_ccv();

    let receiver = env.register(MockErrReceiver, ());
    let msg = non_token_only_message(&env, &client.address, onramp, &receiver, 0);
    let encoded = msg.to_bytes(&env);
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    let mut ccvs = Vec::new(&env);
    ccvs.push_back(default_ccv.clone());
    let mut verifier_results = Vec::new(&env);
    verifier_results.push_back(Bytes::new(&env));

    assert_rejected_non_trapping(&client, &encoded, &message_id, ccvs, verifier_results);
}

#[test]
fn test_consult_success_required_ccv_missing() {
    // Conformant V2 receiver returns required=[X]; `ccvs` does not contain X ⇒ consult succeeds,
    // merge runs, `ensure_quorum_present` rejects with `RequiredCCVMissing` (#116) before the
    // verify-all loop. Exercises the real `Ok(Ok(Ok(config)))` arm.
    let (env, client, _default_ccv, onramp) = setup_lane_with_default_ccv();

    let required_ccv = Address::generate(&env);
    let receiver = env.register(MockConfigReceiver, ());
    MockConfigReceiverClient::new(&env, &receiver).set_config(&CcvsAndFinalityConfig {
        required_ccvs: {
            let mut v = Vec::new(&env);
            v.push_back(required_ccv.clone());
            v
        },
        optional_ccvs: Vec::new(&env),
        optional_threshold: 0,
        allowed_finality_config: 0,
    });

    let msg = non_token_only_message(&env, &client.address, onramp, &receiver, 0);
    let encoded = msg.to_bytes(&env);
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    // ccvs deliberately empty and NOT containing the required CCV.
    let ccvs = Vec::new(&env);
    let verifier_results = Vec::new(&env);

    assert_rejected_non_trapping(&client, &encoded, &message_id, ccvs, verifier_results);
}

#[test]
fn test_consult_success_optional_quorum_not_reached() {
    // Conformant V2 receiver returns optional=[X,Y], threshold=2; `ccvs` contains only X ⇒
    // `OptionalCCVQuorumNotReached` (#118) before the verify-all loop.
    let (env, client, _default_ccv, onramp) = setup_lane_with_default_ccv();

    let opt_a = Address::generate(&env);
    let opt_b = Address::generate(&env);
    let receiver = env.register(MockConfigReceiver, ());
    MockConfigReceiverClient::new(&env, &receiver).set_config(&CcvsAndFinalityConfig {
        required_ccvs: Vec::new(&env),
        optional_ccvs: {
            let mut v = Vec::new(&env);
            v.push_back(opt_a.clone());
            v.push_back(opt_b.clone());
            v
        },
        optional_threshold: 2,
        allowed_finality_config: 0,
    });

    let msg = non_token_only_message(&env, &client.address, onramp, &receiver, 0);
    let encoded = msg.to_bytes(&env);
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    // Only one of the two optional CCVs is present (< threshold 2).
    let mut ccvs = Vec::new(&env);
    ccvs.push_back(opt_a.clone());
    let mut verifier_results = Vec::new(&env);
    verifier_results.push_back(Bytes::new(&env));

    assert_rejected_non_trapping(&client, &encoded, &message_id, ccvs, verifier_results);
}

#[test]
fn test_consult_success_finality_disallowed() {
    // H-7: receiver returns `allowed_finality_config = 0` (no fast-finality flags, no depth), and
    // the message requests `WAIT_FOR_SAFE` (0x10000). `ensure_requested_finality_allowed` rejects
    // with `InvalidRequestedFinality` BEFORE quorum. With `ccvs = [default_ccv]` (quorum-satisfying
    // via the empty-config defaults sentinel) and a consult that succeeds, the only reject is the
    // finality check — proving H-7 fires ahead of quorum/verify-all.
    let (env, client, default_ccv, onramp) = setup_lane_with_default_ccv();

    let receiver = env.register(MockConfigReceiver, ());
    MockConfigReceiverClient::new(&env, &receiver).set_config(&CcvsAndFinalityConfig {
        required_ccvs: Vec::new(&env),
        optional_ccvs: Vec::new(&env),
        optional_threshold: 0,
        allowed_finality_config: 0, // disallows WAIT_FOR_SAFE
    });

    let msg = non_token_only_message(&env, &client.address, onramp, &receiver, 0x0001_0000);
    let encoded = msg.to_bytes(&env);
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    let mut ccvs = Vec::new(&env);
    ccvs.push_back(default_ccv.clone());
    let mut verifier_results = Vec::new(&env);
    verifier_results.push_back(Bytes::new(&env));

    assert_rejected_non_trapping(&client, &encoded, &message_id, ccvs, verifier_results);
}
