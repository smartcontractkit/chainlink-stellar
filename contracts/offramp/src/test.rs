#![cfg(test)]

use common_error::CCIPError;
use common_interfaces::token_pool::{
    MessageDirection, PoolRequiredCCVs, ReleaseOrMintIn, ReleaseOrMintOut,
};
use common_message::{
    CcipMessageV1, CcipTokenTransferV1, MessageIdCompute, ToBytes, MESSAGE_V1_VERSION,
};
use rmn_proxy::{RmnProxyContract, RmnProxyContractClient};
use rmn_remote::{RmnRemoteContract, RmnRemoteContractClient};
use soroban_sdk::{
    contract, contractimpl, testutils::Address as _, vec, xdr::ToXdr, Address, Bytes, BytesN, Env,
    Symbol, Vec,
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
    pub fn get_required_ccvs(
        env: Env,
        _local_token: Address,
        _remote_chain_selector: u64,
        _amount: i128,
        _requested_finality: u32,
        _extra_data: Bytes,
        _direction: MessageDirection,
    ) -> PoolRequiredCCVs {
        // No pool-specific CCVs; append lane defaults (include_defaults = true).
        PoolRequiredCCVs {
            ccvs: Vec::new(&env),
            include_defaults: true,
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
        token_transfer: token_transfer.to_bytes(&env),
        data: Bytes::new(&env),
    };
    let encoded = msg.to_bytes(&env);
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
