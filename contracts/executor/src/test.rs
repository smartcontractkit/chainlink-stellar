#![cfg(test)]

use soroban_sdk::{
    symbol_short, testutils::Address as _, token, vec, Address, Bytes, Env, Map, Vec,
};

use crate::types::{DynamicConfig, RemoteChainConfig, RemoteChainConfigArgs};
use crate::{ExecutorContract, ExecutorContractClient};

// WAIT_FOR_FINALITY (0) and WAIT_FOR_SAFE (0x00010000) from `finality_codec`.
const WAIT_FOR_FINALITY: u32 = 0;
const WAIT_FOR_SAFE: u32 = 1 << 16;

fn default_dynamic_config(_env: &Env, fee_aggregator: Option<Address>) -> DynamicConfig {
    DynamicConfig {
        fee_aggregator,
        // Allow only full finality by default; tests opt into WAIT_FOR_SAFE where needed.
        allowed_finality_config: WAIT_FOR_FINALITY,
        ccv_allowlist_enabled: false,
    }
}

fn remote_chain(usd_cents_fee: u32, enabled: bool) -> RemoteChainConfig {
    RemoteChainConfig {
        usd_cents_fee,
        enabled,
    }
}

fn setup() -> (Env, ExecutorContractClient<'static>, Address) {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let dynamic_config = default_dynamic_config(&env, Some(Address::generate(&env)));
    client.initialize(&owner, &2, &dynamic_config);

    (env, client, owner)
}

fn add_dest_chain(client: &ExecutorContractClient, selector: u64, config: RemoteChainConfig) {
    let to_add = vec![
        &client.env,
        RemoteChainConfigArgs {
            dest_chain_selector: selector,
            config,
        },
    ];
    client.apply_dest_chain_updates(&Vec::new(&client.env), &to_add);
}

// ============================================================
// Initialization Tests
// ============================================================

#[test]
fn test_initialize() {
    let (env, client, owner) = setup();
    assert_eq!(client.owner(), Some(owner));
    assert_eq!(client.get_max_ccvs_per_message(), 2);
    assert_eq!(
        client.type_and_version(),
        soroban_sdk::String::from_str(&env, "Executor 2.0.0-dev")
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")] // AlreadyInitialized
fn test_double_initialize_fails() {
    let (env, client, _owner) = setup();
    let dynamic_config = default_dynamic_config(&env, Some(Address::generate(&env)));
    client.initialize(&Address::generate(&env), &2, &dynamic_config);
}

#[test]
#[should_panic(expected = "Error(Contract, #52)")] // InvalidConfig (max_ccvs_per_msg == 0)
fn test_initialize_rejects_zero_max_ccvs() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);
    let dynamic_config = default_dynamic_config(&env, Some(Address::generate(&env)));
    client.initialize(&Address::generate(&env), &0, &dynamic_config);
}

// ============================================================
// getFee Tests
// ============================================================

#[test]
fn test_get_fee_returns_configured_fee() {
    let (env, client, _owner) = setup();
    add_dest_chain(&client, 405, remote_chain(123, true));

    let ccvs: Vec<Address> = vec![&env];
    let fee = client.get_fee(
        &405,
        &WAIT_FOR_FINALITY,
        &ccvs,
        &Bytes::new(&env),
        &Address::generate(&env),
    );
    assert_eq!(fee, 123);
}

#[test]
#[should_panic(expected = "Error(Contract, #25)")] // DestinationChainNotEnabled
fn test_get_fee_reverts_on_missing_dest() {
    let (env, client, _owner) = setup();
    let ccvs: Vec<Address> = vec![&env];
    client.get_fee(
        &999,
        &WAIT_FOR_FINALITY,
        &ccvs,
        &Bytes::new(&env),
        &Address::generate(&env),
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #25)")] // DestinationChainNotEnabled
fn test_get_fee_reverts_on_disabled_dest() {
    let (env, client, _owner) = setup();
    add_dest_chain(&client, 406, remote_chain(5, false));
    let ccvs: Vec<Address> = vec![&env];
    client.get_fee(
        &406,
        &WAIT_FOR_FINALITY,
        &ccvs,
        &Bytes::new(&env),
        &Address::generate(&env),
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #315)")] // InvalidRequestedFinality
fn test_get_fee_reverts_on_disallowed_finality() {
    // Default config allows only WAIT_FOR_FINALITY; request WAIT_FOR_SAFE.
    let (env, client, _owner) = setup();
    add_dest_chain(&client, 407, remote_chain(1, true));
    let ccvs: Vec<Address> = vec![&env];
    client.get_fee(
        &407,
        &WAIT_FOR_SAFE,
        &ccvs,
        &Bytes::new(&env),
        &Address::generate(&env),
    );
}

#[test]
fn test_get_fee_allows_fast_finality_when_configured() {
    let env = Env::default();
    env.mock_all_auths();
    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let dynamic_config = DynamicConfig {
        fee_aggregator: Some(Address::generate(&env)),
        allowed_finality_config: WAIT_FOR_SAFE,
        ccv_allowlist_enabled: false,
    };
    client.initialize(&owner, &2, &dynamic_config);
    add_dest_chain(&client, 408, remote_chain(7, true));

    let ccvs: Vec<Address> = vec![&env];
    let fee = client.get_fee(
        &408,
        &WAIT_FOR_SAFE,
        &ccvs,
        &Bytes::new(&env),
        &Address::generate(&env),
    );
    assert_eq!(fee, 7);
}

#[test]
#[should_panic(expected = "Error(Contract, #804)")] // ExceedsMaxCCVs
fn test_get_fee_reverts_when_ccvs_exceed_max() {
    let (env, client, _owner) = setup(); // max = 2
    add_dest_chain(&client, 409, remote_chain(1, true));
    let ccvs = vec![
        &env,
        Address::generate(&env),
        Address::generate(&env),
        Address::generate(&env),
    ];
    client.get_fee(
        &409,
        &WAIT_FOR_FINALITY,
        &ccvs,
        &Bytes::new(&env),
        &Address::generate(&env),
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #805)")] // CCVNotAllowed
fn test_get_fee_reverts_on_non_allowlisted_ccv() {
    let env = Env::default();
    env.mock_all_auths();
    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let allowed_ccv = Address::generate(&env);
    let dynamic_config = DynamicConfig {
        fee_aggregator: Some(Address::generate(&env)),
        allowed_finality_config: WAIT_FOR_FINALITY,
        ccv_allowlist_enabled: true,
    };
    client.initialize(&owner, &2, &dynamic_config);
    // Add one CCV to the allowlist.
    client.apply_allowed_ccv_updates(&Vec::new(&env), &vec![&env, allowed_ccv.clone()], &true);
    add_dest_chain(&client, 410, remote_chain(1, true));

    // Supply a *different* CCV -> not on the allowlist.
    let ccvs = vec![&env, Address::generate(&env)];
    client.get_fee(
        &410,
        &WAIT_FOR_FINALITY,
        &ccvs,
        &Bytes::new(&env),
        &Address::generate(&env),
    );
}

#[test]
fn test_get_fee_passes_allowlisted_ccv() {
    let env = Env::default();
    env.mock_all_auths();
    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let allowed_ccv = Address::generate(&env);
    let dynamic_config = DynamicConfig {
        fee_aggregator: Some(Address::generate(&env)),
        allowed_finality_config: WAIT_FOR_FINALITY,
        ccv_allowlist_enabled: true,
    };
    client.initialize(&owner, &2, &dynamic_config);
    client.apply_allowed_ccv_updates(&Vec::new(&env), &vec![&env, allowed_ccv.clone()], &true);
    add_dest_chain(&client, 411, remote_chain(42, true));

    let ccvs = vec![&env, allowed_ccv];
    let fee = client.get_fee(
        &411,
        &WAIT_FOR_FINALITY,
        &ccvs,
        &Bytes::new(&env),
        &Address::generate(&env),
    );
    assert_eq!(fee, 42);
}

// ============================================================
// CCV allowlist update Tests
// ============================================================

#[test]
#[should_panic(expected = "Error(Contract, #56)")] // InvalidAddress (zero account)
fn test_apply_allowed_ccv_updates_rejects_zero_account() {
    let (env, client, _owner) = setup();
    let zero_account = Address::from_str(
        &env,
        "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
    );
    client.apply_allowed_ccv_updates(&Vec::new(&env), &vec![&env, zero_account], &true);
}

#[test]
fn test_apply_allowed_ccv_updates_toggles_enablement() {
    let (_env, client, _owner) = setup();
    let ccv = Address::generate(&client.env);
    client.apply_allowed_ccv_updates(
        &Vec::new(&client.env),
        &vec![&client.env, ccv.clone()],
        &true,
    );
    assert_eq!(client.get_allowed_ccvs(), vec![&client.env, ccv]);
    let cfg = client.get_dynamic_config();
    assert!(cfg.ccv_allowlist_enabled);
}

// ============================================================
// Destination chain update Tests
// ============================================================

#[test]
fn test_apply_dest_chain_updates_add_and_remove() {
    let (_env, client, _owner) = setup();
    add_dest_chain(&client, 501, remote_chain(10, true));
    assert!(client.get_dest_chain_config(&501).enabled);

    // Remove it.
    client.apply_dest_chain_updates(&vec![&client.env, 501], &Vec::new(&client.env));
    // After removal, fetching the config reverts (absent).
    let map: Map<u64, RemoteChainConfig> = Map::new(&client.env);
    assert_eq!(map.len(), 0); // sanity that Map API is usable; real check below
    assert_eq!(client.get_dest_chains().len(), 0);
}

#[test]
#[should_panic(expected = "Error(Contract, #57)")] // InvalidChainSelector (selector == 0)
fn test_apply_dest_chain_updates_rejects_zero_selector() {
    let (env, client, _owner) = setup();
    let to_add = vec![
        &env,
        RemoteChainConfigArgs {
            dest_chain_selector: 0,
            config: remote_chain(1, true),
        },
    ];
    client.apply_dest_chain_updates(&Vec::new(&env), &to_add);
}

// ============================================================
// withdraw_fee_tokens Tests
// ============================================================

#[test]
fn test_withdraw_fee_tokens_sweeps_to_aggregator() {
    let env = Env::default();
    env.mock_all_auths();

    let fee_aggregator = Address::generate(&env);
    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let dynamic_config = default_dynamic_config(&env, Some(fee_aggregator.clone()));
    client.initialize(&owner, &2, &dynamic_config);

    // Mint fee tokens directly to the executor contract.
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
#[should_panic(expected = "Error(Contract, #803)")] // ZeroFeeAggregatorNotAllowed
fn test_withdraw_fee_tokens_reverts_on_zero_aggregator() {
    let env = Env::default();
    env.mock_all_auths();

    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    // No fee aggregator configured.
    let dynamic_config = default_dynamic_config(&env, None);
    client.initialize(&owner, &2, &dynamic_config);

    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin);
    let token_address = token_contract.address();
    client.withdraw_fee_tokens(&vec![&env, token_address]);
}

// ============================================================
// Dynamic config + owner-gating (happy path) Tests
// ============================================================

#[test]
fn test_set_dynamic_config() {
    let (env, client, _owner) = setup();
    let new_cfg = DynamicConfig {
        fee_aggregator: Some(Address::generate(&env)),
        allowed_finality_config: WAIT_FOR_SAFE,
        ccv_allowlist_enabled: true,
    };
    client.set_dynamic_config(&new_cfg);
    let got = client.get_dynamic_config();
    assert_eq!(got.allowed_finality_config, WAIT_FOR_SAFE);
    assert!(got.ccv_allowlist_enabled);
    // get_allowed_finality_config reads through the dynamic config.
    assert_eq!(client.get_allowed_finality_config(), WAIT_FOR_SAFE);
}

#[test]
fn test_storage_key_constants_compile() {
    // Smoke check that the symbol_short! constants used for storage are valid.
    let _ = symbol_short!("INIT");
    let _ = symbol_short!("DYNCFG");
    let _ = symbol_short!("RCHAINS");
    let _ = symbol_short!("ALWCCVS");
    let _ = symbol_short!("MAXCCVS");
}
