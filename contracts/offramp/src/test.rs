#![cfg(test)]

extern crate alloc;

use alloc::vec::Vec as HostVec;
use ccvs_committee_verifier::{
    types::{DynamicConfig, SignatureQuorumConfig},
    CommitteeVerifierContract, CommitteeVerifierContractClient, DEFAULT_VERIFIER_VERSION_TAG,
};
use ccvs_versioned_verifier_resolver::{
    InboundImplementationUpdate, VersionedVerifierResolverContract,
    VersionedVerifierResolverContractClient,
};
use common_helpers::finality_codec::{WAIT_FOR_FINALITY_FLAG, WAIT_FOR_SAFE_FLAG};
use common_interfaces::ccip_receiver::CcvsAndFinalityConfig;
use k256::ecdsa::{signature::hazmat::PrehashSigner, SigningKey};
use sha3::{Digest, Keccak256};

use common_error::CCIPError;
use common_interfaces::token_pool::{
    MessageDirection, PoolRequiredCCVs, ReleaseOrMintIn, ReleaseOrMintOut,
};
use common_message::{
    AnyToStellarMessage, CcipMessageV1, CcipTokenTransferV1, MessageIdCompute, ToBytes,
    MESSAGE_V1_VERSION,
};
use rmn_proxy::{RmnProxyContract, RmnProxyContractClient};
use rmn_remote::{RmnRemoteContract, RmnRemoteContractClient};
use soroban_sdk::{
    contract, contractimpl, symbol_short, testutils::Address as _, testutils::Events as _, vec,
    xdr::ToXdr, Address, Bytes, BytesN, Env, IntoVal, InvokeError, Map, Symbol, TryFromVal,
    TryIntoVal, Val, Vec,
};

use crate::types::{DataKey, MessageExecutionState, SourceChainConfigArgs, StaticConfig};
use crate::{OffRampContract, OffRampContractClient};

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

// REQ (Claim 3): setting a source chain's `default_ccvs` /
// `lane_mandated_ccvs` (i.e. which CCVs a destination accepts from that source)
// is owner-gated at `apply_source_chain_cfg_updates` (lib.rs:328). A non-owner
// must be rejected. Args are empty so the only gate exercised is the auth check.
#[test]
fn test_apply_source_chain_cfg_updates_is_owner_only() {
    let (env, owner, client) = setup_env();
    let static_config = default_static_config(&env);
    client.initialize(&owner, &static_config);
    // Turn off mock_all_auths so the owner's require_auth() is not satisfied.
    env.mock_auths(&[]);
    let r = client.try_apply_source_chain_cfg_updates(&Vec::new(&env));
    assert!(
        r.is_err(),
        "non-owner must be rejected from setting default/lane-mandated CCVs for a source chain"
    );
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
    let encoded = msg.to_bytes(&env).unwrap();
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
    let encoded = msg.to_bytes(&env).unwrap();

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
    let encoded = msg.to_bytes(&env).unwrap();

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
    let encoded = msg.to_bytes(&env).unwrap();

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
    let encoded = msg.to_bytes(&env).unwrap();

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
    let encoded = msg.to_bytes(&env).unwrap();

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
    let encoded = msg.to_bytes(&env).unwrap();

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
    let encoded = msg.to_bytes(&env).unwrap();
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
    let encoded = msg.to_bytes(&env).unwrap();
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
    let encoded = msg.to_bytes(&env).unwrap();
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
// M-3: uint256 → i128 narrowing (INV-ENC-10/12)
// ============================================================

#[test]
fn test_bytes32_to_i128_zero_ok() {
    let env = Env::default();
    let bytes = BytesN::from_array(&env, &[0u8; 32]);
    assert_eq!(OffRampContract::bytes32_to_i128(&env, &bytes), Ok(0));
}

#[test]
fn test_bytes32_to_i128_small_ok() {
    let env = Env::default();
    let mut arr = [0u8; 32];
    arr[31] = 42;
    let bytes = BytesN::from_array(&env, &arr);
    assert_eq!(OffRampContract::bytes32_to_i128(&env, &bytes), Ok(42));
}

#[test]
fn test_bytes32_to_i128_max_i128_ok() {
    // 2^127 - 1 = i128::MAX: high bit of byte 16 is clear ⇒ accepted.
    let env = Env::default();
    let mut arr = [0u8; 32];
    arr[16] = 0x7f;
    for b in &mut arr[17..32] {
        *b = 0xff;
    }
    let bytes = BytesN::from_array(&env, &arr);
    assert_eq!(
        OffRampContract::bytes32_to_i128(&env, &bytes),
        Ok(i128::MAX)
    );
}

#[test]
fn test_bytes32_to_i128_upper_bytes_nonzero_rejected() {
    // Value ≥ 2^128 (upper 16 bytes non-zero) ⇒ TokenHandlingError.
    let env = Env::default();
    let mut arr = [0u8; 32];
    arr[0] = 0x01;
    let bytes = BytesN::from_array(&env, &arr);
    assert_eq!(
        OffRampContract::bytes32_to_i128(&env, &bytes),
        Err(CCIPError::TokenHandlingError)
    );
}

#[test]
fn test_bytes32_to_i128_sign_bit_rejected() {
    // INV-ENC-10/12: value ∈ [2^127, 2^128) — bit 127 set, upper 16 bytes zero.
    // Pre-fix this decoded to i128::MIN (-2^127); it must now be cleanly rejected.
    let env = Env::default();
    let mut arr = [0u8; 32];
    arr[16] = 0x80;
    let bytes = BytesN::from_array(&env, &arr);
    assert_eq!(
        OffRampContract::bytes32_to_i128(&env, &bytes),
        Err(CCIPError::TokenHandlingError)
    );
}

// ============================================================
// H-2 / INV-TR-3 inbound fixtures — mock registry, pool, VVR, verifier
// ============================================================

/// Mock TokenAdminRegistry: returns a configured pool for any token.
#[contract]
pub struct MockTokenAdminRegistry;

#[contractimpl]
impl MockTokenAdminRegistry {
    pub fn set_pool(env: Env, _token: Address, pool: Address) {
        env.storage()
            .instance()
            .set(&Symbol::new(&env, "pool"), &pool);
    }
    pub fn get_pool(env: Env, _token: Address) -> Result<Option<Address>, CCIPError> {
        Ok(env.storage().instance().get(&Symbol::new(&env, "pool")))
    }
}

/// Mock TokenPool: records the receiver passed to `release_or_mint` so the test can assert
/// the H-2 fallback resolved an empty `token_receiver` to `message.receiver`.
#[contract]
pub struct MockTokenPool;

#[contractimpl]
impl MockTokenPool {
    /// Override the pool's required-CCV response (H-10: a pool that mandates its own CCVs and
    /// does NOT fold lane defaults). When unset, the pool reports no own CCVs and
    /// `include_defaults = true` (the pre-V2 fallback used by the H-2 test).
    pub fn set_required_ccvs(env: Env, ccvs: Vec<Address>, include_defaults: bool) {
        env.storage()
            .instance()
            .set(&Symbol::new(&env, "reqccvs"), &ccvs);
        env.storage()
            .instance()
            .set(&Symbol::new(&env, "incldef"), &include_defaults);
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
        let ccvs: Vec<Address> = env
            .storage()
            .instance()
            .get(&Symbol::new(&env, "reqccvs"))
            .unwrap_or_else(|| Vec::new(&env));
        let include_defaults: bool = env
            .storage()
            .instance()
            .get(&Symbol::new(&env, "incldef"))
            .unwrap_or(true);
        PoolRequiredCCVs {
            ccvs,
            include_defaults,
        }
    }

    pub fn release_or_mint(
        env: Env,
        _caller: Address,
        input: ReleaseOrMintIn,
        _requested_finality: u32,
    ) -> Result<ReleaseOrMintOut, CCIPError> {
        env.storage()
            .instance()
            .set(&Symbol::new(&env, "lastrecv"), &input.receiver);
        Ok(ReleaseOrMintOut {
            destination_amount: input.amount,
        })
    }

    pub fn last_receiver(env: Env) -> Address {
        env.storage()
            .instance()
            .get(&Symbol::new(&env, "lastrecv"))
            .expect("release_or_mint was not called")
    }
}

/// Mock VersionedVerifierResolver: returns a configured verifier for any result.
#[contract]
pub struct MockVvr;

#[contractimpl]
impl MockVvr {
    pub fn set_verifier(env: Env, verifier: Address) {
        env.storage()
            .instance()
            .set(&Symbol::new(&env, "verifier"), &verifier);
    }
    pub fn get_inbound_implementation(
        env: Env,
        _verifier_results: Bytes,
    ) -> Result<Address, CCIPError> {
        Ok(env
            .storage()
            .instance()
            .get(&Symbol::new(&env, "verifier"))
            .expect("verifier not set"))
    }
}

/// Mock committee verifier: `verify_message` always succeeds.
#[contract]
pub struct MockVerifier;

#[contractimpl]
impl MockVerifier {
    pub fn verify_message(
        _env: Env,
        _source_chain_selector: u64,
        _message_hash: BytesN<32>,
        _result: Bytes,
    ) -> Result<(), CCIPError> {
        Ok(())
    }
}

/// Mock committee verifier whose `verify_message` ALWAYS fails (`Err(InvalidConfig)`).
/// Used by the M-12 / INV-DST-20 regression test to prove that an extra CCV supplied by
/// the executor but NOT in the required/counted-optional set is never forwarded to a
/// verifier: if it were, this verifier's `Err` would propagate through `verify_ccvs_at`
/// and the message would record `Failure`, not `Success`. A trap/panic variant would
/// surface as a host `InvokeError` instead; the typed `Err` keeps the discriminator
/// deterministic and non-trapping.
#[contract]
pub struct RevertingVerifier;

#[contractimpl]
impl RevertingVerifier {
    pub fn verify_message(
        _env: Env,
        _source_chain_selector: u64,
        _message_hash: BytesN<32>,
        _result: Bytes,
    ) -> Result<(), CCIPError> {
        Err(CCIPError::InvalidConfig)
    }
}

/// H-2 / INV-TR-3: an inbound token transfer with an empty `token_receiver` must release
/// tokens to `message.receiver` instead of reverting with `InvalidReceiverLength`. EVM
/// guarantees this outbound (`OnRamp.sol:311`); the OffRamp defends inbound for sources that
/// don't default. On a Stellar destination `message.receiver` is the 32-byte contract id hash,
/// the exact format `address_from_token_bytes` resolves, so the fallback yields the same
/// `Address` as `ccip_receiver_contract_address`.
#[test]
fn test_execute_empty_token_receiver_falls_back_to_message_receiver() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    RmnRemoteContractClient::new(&env, &rmn_remote_id)
        .initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    RmnProxyContractClient::new(&env, &rmn_proxy_id).initialize(&owner, &rmn_remote_id);

    // Mock registry → mock pool.
    let registry_id = env.register(MockTokenAdminRegistry, ());
    let registry_client = MockTokenAdminRegistryClient::new(&env, &registry_id);
    let pool_id = env.register(MockTokenPool, ());
    let pool_client = MockTokenPoolClient::new(&env, &pool_id);
    registry_client.set_pool(&Address::generate(&env), &pool_id);

    // Mock VVR → mock verifier so `verify_ccv_quorum` passes.
    let verifier_id = env.register(MockVerifier, ());
    let vvr_id = env.register(MockVvr, ());
    let vvr_client = MockVvrClient::new(&env, &vvr_id);
    vvr_client.set_verifier(&verifier_id);

    let static_config = StaticConfig {
        chain_selector: EXEC_TEST_DEST_CHAIN,
        rmn_proxy: rmn_proxy_id,
        token_admin_registry: registry_id,
    };

    let contract_id = env.register(OffRampContract, ());
    let client = OffRampContractClient::new(&env, &contract_id);
    client.initialize(&owner, &static_config);

    // Source lane: the default CCV is the mock VVR (resolves to the always-Ok verifier).
    let router = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(&env, &client, router, vvr_id.clone(), onramp.clone(), true);

    // A throwaway contract whose 32-byte hash is `message.receiver`; the fallback must
    // resolve the empty `token_receiver` to this address.
    let receiver_contract = env.register(MockVerifier, ());
    let receiver_field = offramp_address_field_from_contract(&env, &receiver_contract);

    // Token transfer with an EMPTY `token_receiver` (the H-2 condition).
    let mut amount_bytes = [0u8; 32];
    amount_bytes[31] = 100;
    let token_transfer = CcipTokenTransferV1 {
        version: MESSAGE_V1_VERSION,
        amount: BytesN::from_array(&env, &amount_bytes),
        source_pool_address: Bytes::from_array(&env, &[0x11u8; 20]),
        source_token_address: Bytes::from_array(&env, &[0x22u8; 20]),
        dest_token_address: Bytes::from_array(&env, &[0xBBu8; 32]),
        token_receiver: Bytes::new(&env),
        extra_data: Bytes::new(&env),
    };

    // Token-only message (no data, no ccipReceive gas) so receiver routing is skipped.
    let msg = CcipMessageV1 {
        source_chain_selector: EXEC_TEST_SRC_CHAIN,
        dest_chain_selector: EXEC_TEST_DEST_CHAIN,
        sequence_number: 1,
        execution_gas_limit: 0,
        ccip_receive_gas_limit: 0,
        finality: 0,
        ccv_and_executor_hash: BytesN::from_array(&env, &[0u8; 32]),
        onramp_address: onramp,
        offramp_address: offramp_address_field_from_contract(&env, &contract_id),
        sender: Bytes::from_array(&env, &[2u8; 20]),
        receiver: receiver_field,
        dest_blob: Bytes::new(&env),
        token_transfer: token_transfer.to_bytes(&env).unwrap(),
        data: Bytes::new(&env),
    };
    let encoded = msg.to_bytes(&env).unwrap();
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    let ccvs = vec![&env, vvr_id.clone()];
    let verifier_results = vec![&env, Bytes::new(&env)];

    let res = client.try_execute(&encoded, &ccvs, &verifier_results, &0u32);
    assert!(res.is_ok(), "execute must not trap: {:?}", res.err());
    assert_eq!(
        client.get_execution_state(&message_id),
        MessageExecutionState::Success,
        "token-only message with empty token_receiver must succeed"
    );

    // The pool received `message.receiver` via the fallback, not an InvalidReceiverLength error.
    assert_eq!(
        pool_client.last_receiver(),
        receiver_contract,
        "empty token_receiver must fall back to message.receiver"
    );
}

// ============================================================
// H-10 / INV-TO-4: token-only destination skips the default-CCV floor
// ============================================================

/// A token-only transfer (no data, no ccipReceive gas, tokens present) to a pool that mandates
/// its OWN CCVs — distinct from the lane defaults — and does NOT fold defaults, must succeed when
/// exactly the pool's CCVs attest. The previous "≥1 default verifies when no lane-mandated CCVs
/// exist" floor rejected this (CCVQuorumNotMet) even though EVM `_getCCVsForMessage`'s token-only
/// arm accepts it: required = pool-required + lane-mandated only, no default floor.
#[test]
fn test_execute_token_only_skips_default_ccv_floor() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    RmnRemoteContractClient::new(&env, &rmn_remote_id)
        .initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    RmnProxyContractClient::new(&env, &rmn_proxy_id).initialize(&owner, &rmn_remote_id);

    let registry_id = env.register(MockTokenAdminRegistry, ());
    let registry_client = MockTokenAdminRegistryClient::new(&env, &registry_id);
    let pool_id = env.register(MockTokenPool, ());
    let pool_client = MockTokenPoolClient::new(&env, &pool_id);
    registry_client.set_pool(&Address::generate(&env), &pool_id);

    // Two independent VVR→verifier chains: the lane default, and the pool's own mandated CCV.
    let default_verifier_id = env.register(MockVerifier, ());
    let default_vvr_id = env.register(MockVvr, ());
    MockVvrClient::new(&env, &default_vvr_id).set_verifier(&default_verifier_id);

    let pool_verifier_id = env.register(MockVerifier, ());
    let pool_vvr_id = env.register(MockVvr, ());
    MockVvrClient::new(&env, &pool_vvr_id).set_verifier(&pool_verifier_id);

    // The pool mandates its own CCV and does NOT fold lane defaults.
    pool_client.set_required_ccvs(&vec![&env, pool_vvr_id.clone()], &false);

    let static_config = StaticConfig {
        chain_selector: EXEC_TEST_DEST_CHAIN,
        rmn_proxy: rmn_proxy_id,
        token_admin_registry: registry_id,
    };

    let contract_id = env.register(OffRampContract, ());
    let client = OffRampContractClient::new(&env, &contract_id);
    client.initialize(&owner, &static_config);

    // Lane carries a default CCV (the lane default) and NO lane-mandated CCVs. The H-10 bug
    // is that this default was force-required for token-only transfers regardless of the pool.
    let router = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(
        &env,
        &client,
        router,
        default_vvr_id.clone(),
        onramp.clone(),
        true,
    );

    let receiver_contract = env.register(MockVerifier, ());
    let receiver_field = offramp_address_field_from_contract(&env, &receiver_contract);

    let mut amount_bytes = [0u8; 32];
    amount_bytes[31] = 100;
    let token_transfer = CcipTokenTransferV1 {
        version: MESSAGE_V1_VERSION,
        amount: BytesN::from_array(&env, &amount_bytes),
        source_pool_address: Bytes::from_array(&env, &[0x11u8; 20]),
        source_token_address: Bytes::from_array(&env, &[0x22u8; 20]),
        dest_token_address: Bytes::from_array(&env, &[0xBBu8; 32]),
        token_receiver: receiver_field.clone(),
        extra_data: Bytes::new(&env),
    };

    // Token-only message (no data, no ccipReceive gas).
    let msg = CcipMessageV1 {
        source_chain_selector: EXEC_TEST_SRC_CHAIN,
        dest_chain_selector: EXEC_TEST_DEST_CHAIN,
        sequence_number: 1,
        execution_gas_limit: 0,
        ccip_receive_gas_limit: 0,
        finality: 0,
        ccv_and_executor_hash: BytesN::from_array(&env, &[0u8; 32]),
        onramp_address: onramp,
        offramp_address: offramp_address_field_from_contract(&env, &contract_id),
        sender: Bytes::from_array(&env, &[2u8; 20]),
        receiver: receiver_field,
        dest_blob: Bytes::new(&env),
        token_transfer: token_transfer.to_bytes(&env).unwrap(),
        data: Bytes::new(&env),
    };
    let encoded = msg.to_bytes(&env).unwrap();
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    // The DON supplies ONLY the pool's mandated CCV — NOT the lane default. Before H-10 this
    // failed with CCVQuorumNotMet (mandated_count==0 && default_verified==0); now it succeeds.
    let ccvs = vec![&env, pool_vvr_id.clone()];
    let verifier_results = vec![&env, Bytes::new(&env)];

    let res = client.try_execute(&encoded, &ccvs, &verifier_results, &0u32);
    assert!(
        res.is_ok(),
        "token-only to a pool-mandated CCV must not trap: {:?}",
        res.err()
    );
    assert_eq!(
        client.get_execution_state(&message_id),
        MessageExecutionState::Success,
        "token-only transfer with only the pool's own CCV must succeed (H-10: no default floor)"
    );
}

/// H-10 negative case: the pool's mandated CCV must still be supplied. Omitting it fails with
/// `RequiredCCVMissing` (the floor is gone, but the required-set model still enforces pool CCVs).
#[test]
fn test_execute_token_only_still_requires_pool_ccvs() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    RmnRemoteContractClient::new(&env, &rmn_remote_id)
        .initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    RmnProxyContractClient::new(&env, &rmn_proxy_id).initialize(&owner, &rmn_remote_id);

    let registry_id = env.register(MockTokenAdminRegistry, ());
    let registry_client = MockTokenAdminRegistryClient::new(&env, &registry_id);
    let pool_id = env.register(MockTokenPool, ());
    let pool_client = MockTokenPoolClient::new(&env, &pool_id);
    registry_client.set_pool(&Address::generate(&env), &pool_id);

    let default_verifier_id = env.register(MockVerifier, ());
    let default_vvr_id = env.register(MockVvr, ());
    MockVvrClient::new(&env, &default_vvr_id).set_verifier(&default_verifier_id);

    let pool_verifier_id = env.register(MockVerifier, ());
    let pool_vvr_id = env.register(MockVvr, ());
    MockVvrClient::new(&env, &pool_vvr_id).set_verifier(&pool_verifier_id);

    pool_client.set_required_ccvs(&vec![&env, pool_vvr_id.clone()], &false);

    let static_config = StaticConfig {
        chain_selector: EXEC_TEST_DEST_CHAIN,
        rmn_proxy: rmn_proxy_id,
        token_admin_registry: registry_id,
    };

    let contract_id = env.register(OffRampContract, ());
    let client = OffRampContractClient::new(&env, &contract_id);
    client.initialize(&owner, &static_config);

    let router = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(
        &env,
        &client,
        router,
        default_vvr_id.clone(),
        onramp.clone(),
        true,
    );

    let receiver_contract = env.register(MockVerifier, ());
    let receiver_field = offramp_address_field_from_contract(&env, &receiver_contract);

    let mut amount_bytes = [0u8; 32];
    amount_bytes[31] = 100;
    let token_transfer = CcipTokenTransferV1 {
        version: MESSAGE_V1_VERSION,
        amount: BytesN::from_array(&env, &amount_bytes),
        source_pool_address: Bytes::from_array(&env, &[0x11u8; 20]),
        source_token_address: Bytes::from_array(&env, &[0x22u8; 20]),
        dest_token_address: Bytes::from_array(&env, &[0xBBu8; 32]),
        token_receiver: receiver_field.clone(),
        extra_data: Bytes::new(&env),
    };

    let msg = CcipMessageV1 {
        source_chain_selector: EXEC_TEST_SRC_CHAIN,
        dest_chain_selector: EXEC_TEST_DEST_CHAIN,
        sequence_number: 1,
        execution_gas_limit: 0,
        ccip_receive_gas_limit: 0,
        finality: 0,
        ccv_and_executor_hash: BytesN::from_array(&env, &[0u8; 32]),
        onramp_address: onramp,
        offramp_address: offramp_address_field_from_contract(&env, &contract_id),
        sender: Bytes::from_array(&env, &[2u8; 20]),
        receiver: receiver_field,
        dest_blob: Bytes::new(&env),
        token_transfer: token_transfer.to_bytes(&env).unwrap(),
        data: Bytes::new(&env),
    };
    let encoded = msg.to_bytes(&env).unwrap();

    // Supply the lane DEFAULT instead of the pool's mandated CCV. The default is not in the
    // required set (pool said include_defaults=false), so this must be recorded as `Failure`
    // — with `RequiredCCVMissing` (#116), NOT the old default-floor `CCVQuorumNotMet` (#108).
    // `execute` catches per-message contract errors and records `Failure` (returns Ok), so we
    // assert on the execution state rather than a trap.
    let ccvs = vec![&env, default_vvr_id.clone()];
    let verifier_results = vec![&env, Bytes::new(&env)];

    let res = client.try_execute(&encoded, &ccvs, &verifier_results, &0u32);
    assert!(
        res.is_ok(),
        "execute must not trap on a quorum failure: {:?}",
        res.err()
    );
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);
    assert_eq!(
        client.get_execution_state(&message_id),
        MessageExecutionState::Failure,
        "token-only transfer must fail when the pool's mandated CCV is missing (RequiredCCVMissing)"
    );
}

/// M-13 / INV-DST-9: a token-only inbound message whose pool returns an EMPTY required
/// CCV list with `include_defaults = false` must NOT be executed with zero verification.
/// EVM `OffRamp._getCCVsForMessage` treats an empty pool-returned list as "use lane
/// defaults"; the Stellar flattener must do the same, so the lane `default_ccvs` become
/// the required verify set. Two assertions close the bypass hole:
///  1. Supplying the lane default CCV SUCCEEDS (the default was upgraded into the
///     required set by the empty-pool⇒defaults short-circuit).
///  2. Supplying NO CCVs FAILS with `RequiredCCVMissing` — the OLD (pre-M-13) code would
///     have bypassed verification entirely and executed the message with zero checks.
#[test]
fn test_execute_token_only_empty_pool_falls_back_to_defaults() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    RmnRemoteContractClient::new(&env, &rmn_remote_id)
        .initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    RmnProxyContractClient::new(&env, &rmn_proxy_id).initialize(&owner, &rmn_remote_id);

    let registry_id = env.register(MockTokenAdminRegistry, ());
    let registry_client = MockTokenAdminRegistryClient::new(&env, &registry_id);
    let pool_id = env.register(MockTokenPool, ());
    let pool_client = MockTokenPoolClient::new(&env, &pool_id);
    registry_client.set_pool(&Address::generate(&env), &pool_id);

    let default_verifier_id = env.register(MockVerifier, ());
    let default_vvr_id = env.register(MockVvr, ());
    MockVvrClient::new(&env, &default_vvr_id).set_verifier(&default_verifier_id);

    // Pool returns NO required CCVs and does NOT fold in lane defaults. Before M-13 this
    // yielded an empty `flattened` set; the empty-pool⇒defaults upgrade must turn this
    // into the lane defaults instead.
    pool_client.set_required_ccvs(&Vec::new(&env), &false);

    let static_config = StaticConfig {
        chain_selector: EXEC_TEST_DEST_CHAIN,
        rmn_proxy: rmn_proxy_id,
        token_admin_registry: registry_id,
    };

    let contract_id = env.register(OffRampContract, ());
    let client = OffRampContractClient::new(&env, &contract_id);
    client.initialize(&owner, &static_config);

    let router = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(
        &env,
        &client,
        router,
        default_vvr_id.clone(),
        onramp.clone(),
        true,
    );

    let receiver_contract = env.register(MockVerifier, ());
    let receiver_field = offramp_address_field_from_contract(&env, &receiver_contract);

    let mut amount_bytes = [0u8; 32];
    amount_bytes[31] = 100;
    let token_transfer = CcipTokenTransferV1 {
        version: MESSAGE_V1_VERSION,
        amount: BytesN::from_array(&env, &amount_bytes),
        source_pool_address: Bytes::from_array(&env, &[0x11u8; 20]),
        source_token_address: Bytes::from_array(&env, &[0x22u8; 20]),
        dest_token_address: Bytes::from_array(&env, &[0xBBu8; 32]),
        token_receiver: receiver_field.clone(),
        extra_data: Bytes::new(&env),
    };

    let msg = CcipMessageV1 {
        source_chain_selector: EXEC_TEST_SRC_CHAIN,
        dest_chain_selector: EXEC_TEST_DEST_CHAIN,
        sequence_number: 1,
        execution_gas_limit: 0,
        ccip_receive_gas_limit: 0,
        finality: 0,
        ccv_and_executor_hash: BytesN::from_array(&env, &[0u8; 32]),
        onramp_address: onramp,
        offramp_address: offramp_address_field_from_contract(&env, &contract_id),
        sender: Bytes::from_array(&env, &[2u8; 20]),
        receiver: receiver_field,
        dest_blob: Bytes::new(&env),
        token_transfer: token_transfer.to_bytes(&env).unwrap(),
        data: Bytes::new(&env),
    };
    let encoded = msg.to_bytes(&env).unwrap();
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    // (1) Supplying the lane default SUCCEEDS — the empty pool list was upgraded to defaults.
    let ccvs = vec![&env, default_vvr_id.clone()];
    let verifier_results = vec![&env, Bytes::new(&env)];
    let res = client.try_execute(&encoded, &ccvs, &verifier_results, &0u32);
    assert!(
        res.is_ok(),
        "execute must not trap when the upgraded default CCV is supplied: {:?}",
        res.err()
    );
    assert_eq!(
        client.get_execution_state(&message_id),
        MessageExecutionState::Success,
        "token-only transfer with an empty pool list must succeed by verifying the lane \
         defaults (M-13: empty-pool⇒defaults), not bypass verification"
    );

    // (2) A second, identical message supplying NO CCVs must FAIL — the defaults are now
    // REQUIRED, so omitting them is `RequiredCCVMissing`. Pre-M-13 this would have executed
    // with zero verifier checks (the INV-DST-9 bypass). Bump the sequence so it is a fresh
    // execution-state entry.
    let mut msg2 = msg.clone();
    msg2.sequence_number = 2;
    let encoded2 = msg2.to_bytes(&env).unwrap();
    let message_id2 = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded2);

    let empty_ccvs: Vec<Address> = Vec::new(&env);
    let empty_results: Vec<Bytes> = Vec::new(&env);
    let res2 = client.try_execute(&encoded2, &empty_ccvs, &empty_results, &0u32);
    assert!(
        res2.is_ok(),
        "execute must not trap on a quorum failure: {:?}",
        res2.err()
    );
    assert_eq!(
        client.get_execution_state(&message_id2),
        MessageExecutionState::Failure,
        "token-only transfer with an empty pool list and NO supplied CCVs must FAIL \
         (RequiredCCVMissing) — the lane defaults are required, closing the INV-DST-9 \
         verification-bypass hole"
    );
}

// ============================================================
// M-12 / INV-DST-20: extra executor-supplied CCVs are dropped, not verified
// ============================================================

/// M-12 / INV-DST-20 (pure helper): `build_verify_indices` must queue ONLY the required
/// CCVs plus the first `optional_threshold` present optionals — mirroring EVM
/// `_ensureCCVQuorumIsReached`'s `ccvsToQuery`/`dataIndexes` (OffRamp.sol:624-683). Any
/// other supplied CCV (an extra, or an optional beyond the threshold) is dropped and never
/// verified, so a reverting/slow extra cannot grief delivery. This exercises the helper
/// directly (no contract setup / live verifier) and pins both dropped branches:
///   - the extra `X` (index 3, not required/optional) is excluded;
///   - the over-threshold optional `O2` (index 2) is excluded once the threshold (1) is met
///     by `O1` (the `optional_present >= optional_threshold` break at lib.rs:883).
#[test]
fn test_build_verify_indices_drops_extra_and_over_threshold_optional() {
    let env = Env::default();

    let required = vec![
        &env,
        Address::generate(&env), // R  → index 0
    ];
    let optional = vec![
        &env,
        Address::generate(&env), // O1 → index 1
        Address::generate(&env), // O2 → index 2 (over threshold, must be dropped)
    ];
    // ccvs ordering: [R, O1, O2, X] — X is an extra (index 3) not in required/optional.
    let ccvs = vec![
        &env,
        required.get(0).unwrap().clone(),
        optional.get(0).unwrap().clone(),
        optional.get(1).unwrap().clone(),
        Address::generate(&env), // X → index 3 (extra, must be dropped)
    ];

    let indices = OffRampContract::build_verify_indices(&env, &required, &optional, 1, &ccvs)
        .expect("quorum is present (R + at least 1 optional)");

    // Only R (0) and the first present optional O1 (1) are queued; O2 (2) and X (3) are
    // dropped — a reverting verifier at either of those indices would never be queried.
    assert_eq!(
        indices,
        vec![&env, 0u32, 1u32],
        "extras and over-threshold optionals must be dropped from the verify set"
    );
}

/// M-12 / INV-DST-20 (end-to-end, token-only): an extra CCV supplied by the executor that
/// is NOT in the required set must be ignored, so a REVERTING extra cannot block delivery.
/// The pool mandates exactly one CCV (`pool_vvr`); the DON supplies `ccvs = [pool_vvr,
/// extra_vvr]` where `extra_vvr` resolves to `RevertingVerifier` (always `Err`). Pre-M-12
/// the verify loop iterated the whole `ccvs` list and would query `extra_vvr` ⇒ `Err`
/// propagated ⇒ `Failure`. Post-M-12 `build_verify_indices` queues only `pool_vvr`, so the
/// reverting extra is never queried and the message SUCCEEDS.
#[test]
fn test_execute_drops_reverting_extra_ccv() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    RmnRemoteContractClient::new(&env, &rmn_remote_id)
        .initialize(&owner, &soroban_sdk::Vec::new(&env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    RmnProxyContractClient::new(&env, &rmn_proxy_id).initialize(&owner, &rmn_remote_id);

    let registry_id = env.register(MockTokenAdminRegistry, ());
    let registry_client = MockTokenAdminRegistryClient::new(&env, &registry_id);
    let pool_id = env.register(MockTokenPool, ());
    let pool_client = MockTokenPoolClient::new(&env, &pool_id);
    registry_client.set_pool(&Address::generate(&env), &pool_id);

    // Pool-mandated CCV → a verifier that succeeds.
    let pool_verifier_id = env.register(MockVerifier, ());
    let pool_vvr_id = env.register(MockVvr, ());
    MockVvrClient::new(&env, &pool_vvr_id).set_verifier(&pool_verifier_id);

    // EXTRA CCV → a verifier that always fails. If M-12 ever regresses and this is
    // queried, the message records `Failure` instead of `Success`.
    let extra_verifier_id = env.register(RevertingVerifier, ());
    let extra_vvr_id = env.register(MockVvr, ());
    MockVvrClient::new(&env, &extra_vvr_id).set_verifier(&extra_verifier_id);

    // The pool mandates its own CCV and does NOT fold in lane defaults.
    pool_client.set_required_ccvs(&vec![&env, pool_vvr_id.clone()], &false);

    let static_config = StaticConfig {
        chain_selector: EXEC_TEST_DEST_CHAIN,
        rmn_proxy: rmn_proxy_id,
        token_admin_registry: registry_id,
    };

    let contract_id = env.register(OffRampContract, ());
    let client = OffRampContractClient::new(&env, &contract_id);
    client.initialize(&owner, &static_config);

    // Lane carries a distinct default CCV (unused here — the pool mandates its own and
    // does not fold defaults), so the merged required set is exactly [pool_vvr].
    let default_verifier_id = env.register(MockVerifier, ());
    let default_vvr_id = env.register(MockVvr, ());
    MockVvrClient::new(&env, &default_vvr_id).set_verifier(&default_verifier_id);

    let router = Address::generate(&env);
    let onramp = sample_onramp_bytes(&env);
    apply_source_lane(
        &env,
        &client,
        router,
        default_vvr_id.clone(),
        onramp.clone(),
        true,
    );

    let receiver_contract = env.register(MockVerifier, ());
    let receiver_field = offramp_address_field_from_contract(&env, &receiver_contract);

    let mut amount_bytes = [0u8; 32];
    amount_bytes[31] = 100;
    let token_transfer = CcipTokenTransferV1 {
        version: MESSAGE_V1_VERSION,
        amount: BytesN::from_array(&env, &amount_bytes),
        source_pool_address: Bytes::from_array(&env, &[0x11u8; 20]),
        source_token_address: Bytes::from_array(&env, &[0x22u8; 20]),
        dest_token_address: Bytes::from_array(&env, &[0xBBu8; 32]),
        token_receiver: receiver_field.clone(),
        extra_data: Bytes::new(&env),
    };

    // Token-only message (no data, no ccipReceive gas).
    let msg = CcipMessageV1 {
        source_chain_selector: EXEC_TEST_SRC_CHAIN,
        dest_chain_selector: EXEC_TEST_DEST_CHAIN,
        sequence_number: 1,
        execution_gas_limit: 0,
        ccip_receive_gas_limit: 0,
        finality: 0,
        ccv_and_executor_hash: BytesN::from_array(&env, &[0u8; 32]),
        onramp_address: onramp,
        offramp_address: offramp_address_field_from_contract(&env, &contract_id),
        sender: Bytes::from_array(&env, &[2u8; 20]),
        receiver: receiver_field,
        dest_blob: Bytes::new(&env),
        token_transfer: token_transfer.to_bytes(&env).unwrap(),
        data: Bytes::new(&env),
    };
    let encoded = msg.to_bytes(&env).unwrap();
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    // The DON supplies the required pool CCV FIRST, then a reverting EXTRA. The extra is
    // not in the required set, so M-12 drops it — the reverting verifier is never queried.
    let ccvs = vec![&env, pool_vvr_id.clone(), extra_vvr_id.clone()];
    let verifier_results = vec![&env, Bytes::new(&env), Bytes::new(&env)];

    let res = client.try_execute(&encoded, &ccvs, &verifier_results, &0u32);
    assert!(
        res.is_ok(),
        "execute must not trap when an extra (non-required) CCV is supplied: {:?}",
        res.err()
    );
    assert_eq!(
        client.get_execution_state(&message_id),
        MessageExecutionState::Success,
        "a reverting extra CCV must be dropped (M-12): delivery succeeds because the \
         extra verifier is never queried"
    );
}

// ============================================================
// M-2: destination-address decode validation (INV-MSG-8)
// ============================================================

#[test]
fn test_address_from_token_bytes_raw_32_ok() {
    // A raw 32-byte Stellar contract hash (no prefix) resolves cleanly.
    let env = Env::default();
    let bytes = Bytes::from_array(&env, &[0xABu8; 32]);
    assert!(OffRampContract::address_from_token_bytes(&env, &bytes).is_ok());
}

#[test]
fn test_address_from_token_bytes_zero_padded_prefix_ok() {
    // A >32-byte input whose dropped prefix is all-zero padding is accepted (EVM ABI-padding parity).
    let env = Env::default();
    let mut raw = [0u8; 40];
    raw[8..].copy_from_slice(&[0xCDu8; 32]);
    let bytes = Bytes::from_array(&env, &raw);
    assert!(OffRampContract::address_from_token_bytes(&env, &bytes).is_ok());
}

#[test]
fn test_address_from_token_bytes_nonzero_prefix_rejected() {
    // INV-MSG-8: a dropped prefix that is not all-zero must be rejected rather than silently
    // discarded (EVM `OnRamp.sol:483` `if (word >> (addressBytesLength*8) != 0) revert`).
    let env = Env::default();
    let mut raw = [0u8; 40];
    raw[0] = 0x01; // non-zero discriminant/prefix
    raw[8..].copy_from_slice(&[0xCDu8; 32]);
    let bytes = Bytes::from_array(&env, &raw);
    let err = OffRampContract::address_from_token_bytes(&env, &bytes);
    assert!(
        matches!(err, Err(CCIPError::InvalidReceiverAddress)),
        "non-zero prefix must be rejected, got {:?}",
        err
    );
}

// ============================================================
// H-11 / INV-CFG-5 + INV-CFG-7: config-time CCV set validation
// ============================================================
//
// `SourceChainConfigArgs::validate` must (a) mandate a non-empty `default_ccvs`
// (INV-CFG-5 — the OffRamp's lane fallback verification set; lane-mandated alone
// does not satisfy), and (b) reject duplicates within either CCV list and a CCV
// present in both (INV-CFG-7). Within-list duplicates surface as
// `DuplicateCCVNotAllowed` (#320); empty defaults and cross-list overlap surface
// as `InvalidSourceChainConfig` (#101). A valid unique configuration applies
// cleanly.

/// Build a structurally-valid `SourceChainConfigArgs` with a single default CCV and
/// one on-ramp, used as the base for the H-11 negative overrides.
fn valid_source_chain_args(env: &Env, selector: u64) -> SourceChainConfigArgs {
    let mut on_ramps = Vec::new(env);
    on_ramps.push_back(Bytes::from_array(env, &[1u8; 32]));

    let mut default_ccvs = Vec::new(env);
    default_ccvs.push_back(Address::generate(env));

    SourceChainConfigArgs {
        source_chain_selector: selector,
        router: Address::generate(env),
        is_enabled: true,
        on_ramps,
        default_ccvs,
        lane_mandated_ccvs: Vec::new(env),
    }
}

/// Two identical CCVs in `default_ccvs` are rejected with `DuplicateCCVNotAllowed`
/// (#320) at config-apply time.
#[test]
#[should_panic(expected = "Error(Contract, #320)")] // DuplicateCCVNotAllowed
fn test_source_chain_config_rejects_duplicate_default_ccvs() {
    let (env, owner, client) = setup_env();
    client.initialize(&owner, &default_static_config(&env));

    let dup = Address::generate(&env);
    let mut args = valid_source_chain_args(&env, 5678);
    args.default_ccvs = vec![&env, dup.clone(), dup.clone()];

    let mut updates = Vec::new(&env);
    updates.push_back(args);
    client.apply_source_chain_cfg_updates(&updates);
}

/// Two identical CCVs in `lane_mandated_ccvs` are rejected with
/// `DuplicateCCVNotAllowed` (#320) at config-apply time.
#[test]
#[should_panic(expected = "Error(Contract, #320)")] // DuplicateCCVNotAllowed
fn test_source_chain_config_rejects_duplicate_mandated_ccvs() {
    let (env, owner, client) = setup_env();
    client.initialize(&owner, &default_static_config(&env));

    let dup = Address::generate(&env);
    let mut args = valid_source_chain_args(&env, 5678);
    args.lane_mandated_ccvs = vec![&env, dup.clone(), dup.clone()];

    let mut updates = Vec::new(&env);
    updates.push_back(args);
    client.apply_source_chain_cfg_updates(&updates);
}

/// A CCV present in BOTH `default_ccvs` and `lane_mandated_ccvs` is rejected with
/// `InvalidSourceChainConfig` (#101) — a CCV must not be double-classified.
#[test]
#[should_panic(expected = "Error(Contract, #101)")] // InvalidSourceChainConfig - cross-list overlap
fn test_source_chain_config_rejects_cross_list_ccv_overlap() {
    let (env, owner, client) = setup_env();
    client.initialize(&owner, &default_static_config(&env));

    let shared = Address::generate(&env);
    let mut args = valid_source_chain_args(&env, 5678);
    args.default_ccvs = vec![&env, shared.clone()];
    args.lane_mandated_ccvs = vec![&env, shared];

    let mut updates = Vec::new(&env);
    updates.push_back(args);
    client.apply_source_chain_cfg_updates(&updates);
}

/// INV-CFG-5 discriminator: empty `default_ccvs` is rejected with
/// `InvalidSourceChainConfig` (#101) even when `lane_mandated_ccvs` is non-empty —
/// the OffRamp always requires a default verification set.
#[test]
#[should_panic(expected = "Error(Contract, #101)")] // InvalidSourceChainConfig - empty defaults (INV-CFG-5)
fn test_source_chain_config_rejects_empty_defaults_with_mandated() {
    let (env, owner, client) = setup_env();
    client.initialize(&owner, &default_static_config(&env));

    let mut args = valid_source_chain_args(&env, 5678);
    args.default_ccvs = Vec::new(&env);
    args.lane_mandated_ccvs = vec![&env, Address::generate(&env)];

    let mut updates = Vec::new(&env);
    updates.push_back(args);
    client.apply_source_chain_cfg_updates(&updates);
}

/// A CCV set with non-empty, distinct `default_ccvs` and `lane_mandated_ccvs` applies
/// cleanly (positive control for the H-11 validation path).
#[test]
fn test_source_chain_config_accepts_unique_ccv_set() {
    let (env, owner, client) = setup_env();
    client.initialize(&owner, &default_static_config(&env));

    let mut args = valid_source_chain_args(&env, 5678);
    args.default_ccvs = vec![&env, Address::generate(&env), Address::generate(&env)];
    args.lane_mandated_ccvs = vec![&env, Address::generate(&env)];

    let mut updates = Vec::new(&env);
    updates.push_back(args.clone());
    client.apply_source_chain_cfg_updates(&updates);

    let config = client.get_source_chain_config(&5678);
    assert_eq!(config.default_ccvs, args.default_ccvs);
    assert_eq!(config.lane_mandated_ccvs, args.lane_mandated_ccvs);
}

// ============================================================
// Real-CCV OffRamp↔CommitteeVerifier integration tests
//
// Closes the SHARED ROOT GAP of claims 4/5/6 in docs/claims-verification.md:
// the only tests that wire a REAL CommitteeVerifier (live ecrecover quorum) behind a
// REAL OffRamp.execute, signing the attestation over a real `CcipMessageV1`-
// `compute_message_id` and reaching it through the real execute → verify_ccv_quorum →
// VersionedVerifierResolver → verify_message path. Offramp tests otherwise use
// blob-agnostic MockVerifier/RevertingVerifier; committee-verifier crypto is otherwise
// exercised only in isolation over an arbitrary BytesN<32> hash.
// ============================================================

// ---- host-side signing helpers (mirror of committee-verifier/src/test.rs:20-114) ----
// Copied verbatim so the offramp crate can drive a real CommitteeVerifier without a shared
// dev-helper crate. Phase B will centralize the keccak256(version_tag‖messageID) signed-
// payload / version-tag invariant behind a shared trait + helper.

fn make_signing_key(seed: u8) -> SigningKey {
    let mut bytes = [0u8; 32];
    bytes[0] = seed;
    SigningKey::from_slice(&bytes).expect("valid secp256k1 secret key")
}

/// Left-zero-padded 32-byte Ethereum address derived from a secp256k1 signing key.
fn eth_address_padded(sk: &SigningKey) -> [u8; 32] {
    let vk = sk.verifying_key();
    let uncompressed = vk.to_encoded_point(false);
    let hash = Keccak256::digest(&uncompressed.as_bytes()[1..]);
    let mut padded = [0u8; 32];
    padded[12..].copy_from_slice(&hash[12..]);
    padded
}

/// Deterministic secp256k1 keys from seeds, sorted by Ethereum address (ascending) — the
/// wire order `verify_message` requires (OutOfOrderSignatures otherwise).
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

fn signers_to_soroban_vec(env: &Env, pairs: &HostVec<(SigningKey, [u8; 32])>) -> Vec<BytesN<32>> {
    let mut out = Vec::new(env);
    for (_, addr) in pairs {
        out.push_back(BytesN::from_array(env, addr));
    }
    out
}

/// `keccak256(version_tag ‖ message_hash)` — must match CommitteeVerifier::verify_message.
fn keccak_signed_hash_with_tag(env: &Env, tag: &[u8; 4], message_hash: &BytesN<32>) -> BytesN<32> {
    let mut signed_payload = Bytes::new(env);
    signed_payload.append(&Bytes::from_array(env, tag));
    signed_payload.append(&Bytes::from_array(env, &message_hash.to_array()));
    env.crypto().keccak256(&signed_payload).into()
}

/// Blob layout consumed by CommitteeVerifier::verify_message:
/// `version_tag(4) ‖ u16_len(2, big-endian) ‖ sig_payload(len)`, where sig_payload is the
/// concatenation of EIP-2098 compact signatures in ascending-signer wire order.
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

/// EIP-2098 compact ECDSA signature (64 bytes): r(32) ‖ yParityAndS(32), recovery id in
/// bit 255 of S — matches `decode_compact_sig` (common/signature quorum.rs:17-22).
fn sign_compact(sk: &SigningKey, prehash: &[u8; 32]) -> [u8; 64] {
    let (sig, recid) = sk.sign_prehash(prehash).expect("signing must succeed");
    let sig_bytes = sig.to_bytes();
    let mut compact = [0u8; 64];
    compact[..32].copy_from_slice(&sig_bytes[..32]); // r
    compact[32..].copy_from_slice(&sig_bytes[32..]); // s
    compact[32] |= (recid.to_byte() & 1) << 7; // recovery id → bit 255 of S
    compact
}

/// Concatenate compact signatures for the given (wire-ordered) signers over `signed_hash`.
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

/// Minimal test Router: forwards a verified message to `receiver.ccip_receive(message)`,
/// mirroring the real Router's single-arg forwarding (router/src/lib.rs:256-263) without its
/// RMN-curse / offramp-registration machinery — the router is not the seam under test here.
#[contract]
pub struct TestRouter;

#[contractimpl]
impl TestRouter {
    pub fn route_message(
        env: Env,
        _offramp: Address,
        _source_chain_selector: u64,
        receiver: Address,
        message: AnyToStellarMessage,
    ) -> Result<(), CCIPError> {
        let mut recv_args = Vec::new(&env);
        recv_args.push_back(message.into_val(&env));
        match env.try_invoke_contract::<Result<(), CCIPError>, InvokeError>(
            &receiver,
            &Symbol::new(&env, "ccip_receive"),
            recv_args,
        ) {
            Ok(Ok(Ok(()))) => Ok(()),
            Ok(Ok(Err(e))) => Err(e),
            Ok(Err(_)) => Err(CCIPError::ReceiverError),
            Err(_) => Err(CCIPError::ReceiverError),
        }
    }
}

/// Configurable test receiver implementing the V2 CCIP receiver surface by symbol —
/// `get_ccvs_and_finality_config` (offramp consult, lib.rs:701-704) and `ccip_receive`
/// (delivery via the router, lib.rs:1059-1068). A settable allowed-finality policy and
/// required-CCV set drive the on-chain finality gate (H-7) and the real-verifier dispatch;
/// `ccip_receive` records the delivered `message_id` so tests assert end-to-end delivery
/// (proving CCVs attest to the whole message via messageID, not a Burn event — claim 6).
#[contract]
pub struct TestReceiver;

#[contractimpl]
impl TestReceiver {
    pub fn set_allowed_finality(env: Env, allowed_finality_config: u32) {
        env.storage().instance().set(
            &Symbol::new(&env, "allowed_finality"),
            &allowed_finality_config,
        );
    }

    pub fn set_required_ccvs(env: Env, required_ccvs: Vec<Address>) {
        env.storage()
            .instance()
            .set(&Symbol::new(&env, "required_ccvs"), &required_ccvs);
    }

    pub fn get_ccvs_and_finality_config(
        env: Env,
        _source_chain_selector: u64,
        _sender: Bytes,
    ) -> Result<CcvsAndFinalityConfig, CCIPError> {
        let allowed_finality_config: u32 = env
            .storage()
            .instance()
            .get(&Symbol::new(&env, "allowed_finality"))
            .unwrap_or(WAIT_FOR_FINALITY_FLAG);
        let required_ccvs: Vec<Address> = env
            .storage()
            .instance()
            .get(&Symbol::new(&env, "required_ccvs"))
            .unwrap_or_else(|| Vec::new(&env));
        Ok(CcvsAndFinalityConfig {
            allowed_finality_config,
            optional_ccvs: Vec::new(&env),
            optional_threshold: 0,
            required_ccvs,
        })
    }

    pub fn ccip_receive(env: Env, message: AnyToStellarMessage) -> Result<(), CCIPError> {
        env.storage()
            .instance()
            .set(&Symbol::new(&env, "last_msg_id"), &message.message_id);
        Ok(())
    }

    pub fn last_received_message_id(env: Env) -> BytesN<32> {
        env.storage()
            .instance()
            .get(&Symbol::new(&env, "last_msg_id"))
            .expect("no message delivered")
    }
}

/// Wire a REAL CommitteeVerifier behind a REAL VersionedVerifierResolver behind a REAL
/// OffRamp, plus a minimal TestRouter + configurable TestReceiver. 3 signers, threshold 2,
/// configured for `EXEC_TEST_SRC_CHAIN` under the default 2.0 version tag.
///
/// Returns everything an integration test needs to build a signed `verifier_results` blob
/// and call `execute`. The receiver is configured with `allowed_finality` and required-CCV
/// = [vvr_id] (so the real VVR is in the required verify set).
#[allow(clippy::type_complexity)]
fn setup_real_verifier_execute_harness(
    allowed_finality: u32,
) -> (
    Env,
    OffRampContractClient<'static>,
    Address, // vvr_id
    Address, // committee_verifier_id
    TestReceiverClient<'static>,
    HostVec<(SigningKey, [u8; 32])>, // sorted signer pairs
) {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);

    // RMN stack (required by CommitteeVerifier::initialize / require_not_cursed).
    let rmn_remote_id = env.register(RmnRemoteContract, ());
    RmnRemoteContractClient::new(&env, &rmn_remote_id)
        .initialize(&owner, &soroban_sdk::Vec::new(&env));
    let rmn_proxy_id = env.register(RmnProxyContract, ());
    RmnProxyContractClient::new(&env, &rmn_proxy_id).initialize(&owner, &rmn_remote_id);

    // REAL CommitteeVerifier: init with the default 2.0 tag, then configure a 3-signer /
    // threshold-2 quorum for the test source chain.
    let committee_verifier_id = env.register(CommitteeVerifierContract, ());
    let cv_client = CommitteeVerifierContractClient::new(&env, &committee_verifier_id);
    let pairs = sorted_signers_from_seeds(&[3, 7, 11]);
    let cfg = SignatureQuorumConfig {
        source_chain_selector: EXEC_TEST_SRC_CHAIN,
        threshold: 2,
        signers: signers_to_soroban_vec(&env, &pairs),
    };
    cv_client.initialize(
        &owner,
        &DynamicConfig {
            fee_aggregator: Some(Address::generate(&env)),
            allowlist_admin: None,
        },
        &vec![&env], // storage_locations
        &rmn_proxy_id,
        &BytesN::from_array(&env, &DEFAULT_VERIFIER_VERSION_TAG),
    );
    cv_client.apply_signature_configs(&vec![&env], &vec![&env, cfg]);

    // REAL VVR: map the version tag → CommitteeVerifier so OffRamp's verify_ccvs_at
    // dispatches to the real ecrecover quorum.
    let vvr_id = env.register(VersionedVerifierResolverContract, ());
    let vvr_client = VersionedVerifierResolverContractClient::new(&env, &vvr_id);
    vvr_client.initialize(&owner, &Address::generate(&env));
    vvr_client.apply_inbound_impl_updates(&vec![
        &env,
        InboundImplementationUpdate {
            version: BytesN::from_array(&env, &DEFAULT_VERIFIER_VERSION_TAG),
            verifier: Some(committee_verifier_id.clone()),
        },
    ]);

    // TestRouter + configurable TestReceiver.
    let router_id = env.register(TestRouter, ());
    let receiver_id = env.register(TestReceiver, ());
    let receiver_client = TestReceiverClient::new(&env, &receiver_id);
    receiver_client.set_required_ccvs(&vec![&env, vvr_id.clone()]);
    receiver_client.set_allowed_finality(&allowed_finality);

    // REAL OffRamp; source lane's default CCV = the real VVR, router = the test router.
    let offramp_id = env.register(OffRampContract, ());
    let client = OffRampContractClient::new(&env, &offramp_id);
    client.initialize(
        &owner,
        &StaticConfig {
            chain_selector: EXEC_TEST_DEST_CHAIN,
            rmn_proxy: rmn_proxy_id,
            token_admin_registry: Address::generate(&env),
        },
    );
    apply_source_lane(
        &env,
        &client,
        router_id,
        vvr_id.clone(),
        sample_onramp_bytes(&env),
        true,
    );

    (
        env,
        client,
        vvr_id,
        committee_verifier_id,
        receiver_client,
        pairs,
    )
}

/// A data-only inbound message (no token transfer) with `ccip_receive_gas_limit > 0` and
/// non-empty `data` ⇒ non-token-only ⇒ receiver consult (C-1) + finality gate (H-7) +
/// routing to `receiver.ccip_receive` all run. `finality` is caller-controlled.
fn data_message(
    env: &Env,
    offramp_contract: &Address,
    onramp: Bytes,
    receiver_contract: &Address,
    finality: u32,
) -> CcipMessageV1 {
    CcipMessageV1 {
        source_chain_selector: EXEC_TEST_SRC_CHAIN,
        dest_chain_selector: EXEC_TEST_DEST_CHAIN,
        sequence_number: 1,
        execution_gas_limit: 0,
        ccip_receive_gas_limit: 100_000,
        finality,
        ccv_and_executor_hash: BytesN::from_array(env, &[0u8; 32]),
        onramp_address: onramp,
        offramp_address: offramp_address_field_from_contract(env, offramp_contract),
        sender: Bytes::from_array(env, &[2u8; 20]),
        receiver: offramp_address_field_from_contract(env, receiver_contract),
        dest_blob: Bytes::new(env),
        token_transfer: Bytes::new(env),
        data: Bytes::from_array(env, &[0xde, 0xad, 0xbe, 0xef]),
    }
}

/// Build a valid `verifier_results` blob: `threshold` (2) compact signatures over
/// `keccak256(version_tag ‖ message_id)`, in ascending-signer wire order.
fn signed_verifier_results(
    env: &Env,
    pairs: &HostVec<(SigningKey, [u8; 32])>,
    message_id: &BytesN<32>,
) -> Bytes {
    let signed_hash = keccak_signed_hash_with_tag(env, &DEFAULT_VERIFIER_VERSION_TAG, message_id);
    let signed_bytes: [u8; 32] = signed_hash.to_array();
    let wire_subset = [pairs[0].clone(), pairs[1].clone()]; // 2 sigs == threshold
    let sig_payload = signature_payload_valid(&wire_subset, &signed_bytes);
    build_verifier_results_with_tag(env, &DEFAULT_VERIFIER_VERSION_TAG, &sig_payload)
}

/// Claims 4/6 POSITIVE: a REAL CommitteeVerifier ecrecover quorum verifies an attestation
/// signed over a real `compute_message_id`, reached end-to-end through
/// OffRamp.execute → verify_ccv_quorum → VVR → verify_message, and the verified message is
/// delivered to the receiver (`ccip_receive` records the exact on-wire messageID — proving
/// CCVs attest to the whole message via messageID, not a Burn event). Closes the shared
/// root gap of claims 4/5/6.
#[test]
fn test_real_verifier_e2e_execute_delivers_to_receiver() {
    let (env, client, vvr_id, _cv_id, receiver_client, pairs) =
        setup_real_verifier_execute_harness(WAIT_FOR_SAFE_FLAG);

    let onramp = sample_onramp_bytes(&env);
    let receiver_contract = receiver_client.address.clone();
    let msg = data_message(
        &env,
        &client.address,
        onramp,
        &receiver_contract,
        WAIT_FOR_SAFE_FLAG,
    );
    let encoded = msg.to_bytes(&env).unwrap();
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    let verifier_results = signed_verifier_results(&env, &pairs, &message_id);
    let ccvs = vec![&env, vvr_id.clone()];

    let res = client.try_execute(&encoded, &ccvs, &vec![&env, verifier_results], &0u32);
    assert!(
        res.is_ok(),
        "execute must not trap on a real-CCV-verified message: {:?}",
        res.err()
    );
    assert_eq!(
        client.get_execution_state(&message_id),
        MessageExecutionState::Success,
        "real-CCV-verified data message must execute successfully"
    );
    assert_eq!(
        receiver_client.last_received_message_id(),
        message_id,
        "ccip_receive must be reached with the exact on-wire messageID"
    );
}

/// Claim 2 gap-1 (Rust, previously directive-blocked Go-e2e-only): the receiver-consulted
/// allowed-finality policy is enforced on-chain. The receiver admits only WAIT_FOR_FINALITY
/// (0); the message requests WAIT_FOR_SAFE ⇒ `ensure_requested_finality_allowed` rejects
/// with #315 (InvalidRequestedFinality) inside verify_ccv_quorum, BEFORE routing. `execute`
/// catches the per-message error and records `Failure` (returns Ok, no trap) — the
/// established offramp pattern (cf. M-12 test). The receiver is never reached.
#[test]
fn test_real_verifier_receiver_finality_gate_rejects_disallowed_finality() {
    let (env, client, vvr_id, _cv_id, receiver_client, pairs) =
        setup_real_verifier_execute_harness(WAIT_FOR_FINALITY_FLAG); // receiver admits only final

    let onramp = sample_onramp_bytes(&env);
    let receiver_contract = receiver_client.address.clone();
    // Message requests WAIT_FOR_SAFE, which the receiver's WAIT_FOR_FINALITY policy disallows.
    let msg = data_message(
        &env,
        &client.address,
        onramp,
        &receiver_contract,
        WAIT_FOR_SAFE_FLAG,
    );
    let encoded = msg.to_bytes(&env).unwrap();
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    let verifier_results = signed_verifier_results(&env, &pairs, &message_id);
    let ccvs = vec![&env, vvr_id.clone()];

    let res = client.try_execute(&encoded, &ccvs, &vec![&env, verifier_results], &0u32);
    assert!(
        res.is_ok(),
        "execute must not trap on a finality-gate failure: {:?}",
        res.err()
    );
    assert_eq!(
        client.get_execution_state(&message_id),
        MessageExecutionState::Failure,
        "disallowed requested finality (#315) must record Failure, not deliver"
    );
    assert!(
        receiver_client.try_last_received_message_id().is_err(),
        "receiver must NOT be reached when the finality gate rejects"
    );
}

/// Claims 4/6 NEGATIVE: a tampered attestation (one signature byte flipped) must NOT pass
/// the real CommitteeVerifier ecrecover quorum — the recovered signer falls outside the
/// configured set ⇒ quorum not met ⇒ execute records Failure and the receiver is never
/// reached. Proves the REAL verifier (not a blob-agnostic mock) is on the execute path and
/// that it binds the attestation to the real messageID.
#[test]
fn test_real_verifier_tampered_signature_is_rejected() {
    let (env, client, vvr_id, _cv_id, receiver_client, pairs) =
        setup_real_verifier_execute_harness(WAIT_FOR_SAFE_FLAG);

    let onramp = sample_onramp_bytes(&env);
    let receiver_contract = receiver_client.address.clone();
    let msg = data_message(
        &env,
        &client.address,
        onramp,
        &receiver_contract,
        WAIT_FOR_SAFE_FLAG,
    );
    let encoded = msg.to_bytes(&env).unwrap();
    let message_id = CcipMessageV1::compute_message_id_from_bytes(&env, &encoded);

    // Sign over a DIFFERENT hash than the real messageID the OffRamp will pass to
    // verify_message. The signatures stay cryptographically valid (ecrecover yields a real,
    // wrong pubkey) so the verifier returns a typed quorum-not-met Err — caught by execute
    // as `Failure` — rather than trapping on a malformed signature. The recovered signers
    // fall outside the configured set ⇒ quorum not met ⇒ no delivery.
    let mut tampered_id = message_id.to_array();
    tampered_id[0] ^= 0x01;
    let tampered_hash = BytesN::from_array(&env, &tampered_id);
    let signed_hash =
        keccak_signed_hash_with_tag(&env, &DEFAULT_VERIFIER_VERSION_TAG, &tampered_hash);
    let signed_bytes: [u8; 32] = signed_hash.to_array();
    let wire_subset = [pairs[0].clone(), pairs[1].clone()];
    let sig_payload: HostVec<u8> = signature_payload_valid(&wire_subset, &signed_bytes);
    let verifier_results =
        build_verifier_results_with_tag(&env, &DEFAULT_VERIFIER_VERSION_TAG, &sig_payload);

    let ccvs = vec![&env, vvr_id.clone()];

    let res = client.try_execute(&encoded, &ccvs, &vec![&env, verifier_results], &0u32);
    let state = client.get_execution_state(&message_id);
    // The attestation is signed over the wrong hash, so the recovered signers fall outside
    // the configured set ⇒ the message must NOT be successfully executed and the receiver
    // must NOT be reached. The real CommitteeVerifier traps via `secp256k1_recover`
    // [common/signature scheme.rs:62] on the non-matching signature. `verify_ccvs_at` now
    // dispatches through `CrossChainVerifierClient::try_verify_message` (EVM
    // `executeSingleMessage` try/catch parity), so that trap is caught and `execute` records
    // a retryable `Failure` (state == Failure) rather than reverting the whole tx. (Earlier,
    // when `verify_ccvs_at` used `env.invoke_contract`, the trap propagated and left state
    // `Untouched`; we assert the outcome — not Success, receiver unreached — which holds under
    // either surfacing, so the test stays robust to further dispatch changes.)
    assert_ne!(
        state,
        MessageExecutionState::Success,
        "tampered attestation must not execute successfully (state={:?}, res={:?})",
        state,
        res,
    );
    assert!(
        receiver_client.try_last_received_message_id().is_err(),
        "receiver must NOT be reached when the attestation fails verification (state={:?})",
        state,
    );
}

// ============================================================
// Upgradeability (shared `common_authorization::Upgradeable` trait)
// ============================================================

// Real, committed Wasm fixture (checked into data-feeds-common) that exposes a
// `peek() -> u32` reader. OffRamp has no `peek`, so a successful `peek` call at
// the OffRamp address after `upgrade` proves the executable was swapped in
// place. Reused instead of adding a new fixture crate (no `stellar` CLI here).
const UPGRADE_TARGET_WASM: &[u8] =
    include_bytes!("../../data-feeds/data-feeds-common/test_fixtures/upgrade_target.wasm");

/// Decode the `new_wasm_hash` field from the last `Upgraded` event emitted by
/// `contract`. `#[contractevent]` serializes the struct as a `Map<Symbol, Val>`
/// keyed by field name.
fn upgraded_event_hash(env: &Env, contract: &Address) -> BytesN<32> {
    let evs = env.events().all().filter_by_contract(contract);
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
        let Some(hval) = map.get(Symbol::new(env, "new_wasm_hash")) else {
            continue;
        };
        if let Ok(hash) = BytesN::<32>::try_from_val(env, &hval) {
            return hash;
        }
    }
    panic!("expected Upgraded event with new_wasm_hash from contract");
}

#[test]
fn test_upgrade_by_owner_swaps_executable_and_emits_event() {
    let (env, owner, client) = setup_env();
    client.initialize(&owner, &default_static_config(&env));

    // `mock_all_auths` (set in `setup_env`) authorizes the owner, so
    // `require_owner` -> `owner.require_auth()` passes.
    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    client.upgrade(&hash);

    // The `Upgraded` event was published carrying the new Wasm hash.
    assert_eq!(upgraded_event_hash(&env, &client.address), hash);

    // OffRamp has no `peek`; the fixture does. A successful `peek` at the
    // OffRamp address proves the executable was swapped in place (same address,
    // new code). Instance storage is preserved by `update_current_contract_wasm`
    // (host-level guarantee), and OffRamp never wrote the fixture's "slot" key,
    // so peek reads back 0.
    let peeked: u32 = env.invoke_contract(
        &client.address,
        &symbol_short!("peek"),
        Vec::<Val>::new(&env),
    );
    assert_eq!(
        peeked, 0,
        "fixture peek must run at the offramp address after upgrade"
    );
}

#[test]
#[should_panic(expected = "Error(Auth, InvalidAction)")]
fn test_upgrade_by_non_owner_rejected() {
    let env = Env::default();
    // No `mock_all_auths`: nobody is authorized, so `require_owner` ->
    // `owner.require_auth()` fails. `initialize` needs no auth (it only guards
    // against double-init), so the contract is set up with the owner stored.
    let contract_id = env.register(OffRampContract, ());
    let client = OffRampContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    client.initialize(&owner, &default_static_config(&env));

    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    // Caller is not the owner and the owner does not authorize -> reject.
    // `require_owner` fails at `owner.require_auth()` before the executable is
    // touched, so the hash need not correspond to a real upgrade target here.
    client.upgrade(&hash);
}
