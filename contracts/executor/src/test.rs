#![cfg(test)]

use soroban_sdk::{
    contracttype, symbol_short, testutils::Address as _, testutils::Events as _, token, vec,
    Address, Bytes, BytesN, Env, Map, Symbol, TryFromVal, TryIntoVal, Val, Vec,
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

#[test]
fn test_get_fee_allows_non_allowlisted_ccv_when_disabled() {
    // OFF-positive (CCV-1 gap): with the CCV allowlist disabled, a CCV that is
    // NOT on the (empty) allowlist is accepted — enforcement is skipped. This is
    // the explicit counterpart to test_get_fee_reverts_on_non_allowlisted_ccv
    // (ON-negative), closing the "no OFF-state positive test" coverage gap.
    let env = Env::default();
    env.mock_all_auths();
    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    // allowlist OFF at init.
    let dynamic_config = DynamicConfig {
        fee_aggregator: Some(Address::generate(&env)),
        allowed_finality_config: WAIT_FOR_FINALITY,
        ccv_allowlist_enabled: false,
    };
    client.initialize(&owner, &2, &dynamic_config);
    add_dest_chain(&client, 412, remote_chain(42, true));

    // A CCV not on the (empty) allowlist — accepted because enforcement is off.
    let ccvs = vec![&env, Address::generate(&env)];
    let fee = client.get_fee(
        &412,
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

#[test]
fn test_apply_allowed_ccv_updates_toggles_on_then_off() {
    // ON→OFF toggle (CCV-1 gap): the allowlist can be turned off at runtime via
    // `apply_allowed_ccv_updates(&..., &false)` (EVM `DisableAllowlist`). With
    // the allowlist ON a non-allowed CCV is rejected; after toggling OFF, the
    // SAME non-allowed CCV is accepted. The existing
    // test_get_fee_reverts_on_non_allowlisted_ccv already proves ON rejects, so
    // this test focuses on the toggle direction + the OFF-acceptance that follows.
    let env = Env::default();
    env.mock_all_auths();
    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let allowed_ccv = Address::generate(&env);
    // allowlist ON at init.
    let dynamic_config = DynamicConfig {
        fee_aggregator: Some(Address::generate(&env)),
        allowed_finality_config: WAIT_FOR_FINALITY,
        ccv_allowlist_enabled: true,
    };
    client.initialize(&owner, &2, &dynamic_config);
    client.apply_allowed_ccv_updates(&Vec::new(&env), &vec![&env, allowed_ccv], &true);
    add_dest_chain(&client, 413, remote_chain(42, true));

    let stranger_ccv = Address::generate(&env);
    let ccvs = vec![&env, stranger_ccv.clone()];

    // ON: the stranger CCV (not on the allowlist) is rejected. `matches!(Ok(Ok(_)))`
    // treats only a genuine success as passing, so this holds whether try_get_fee
    // surfaces the returned Err as an outer Err (trap) or an inner Err.
    let r = client.try_get_fee(
        &413,
        &WAIT_FOR_FINALITY,
        &ccvs,
        &Bytes::new(&env),
        &Address::generate(&env),
    );
    assert!(
        !matches!(r, Ok(Ok(_))),
        "non-allowlisted CCV must be rejected while the allowlist is ON"
    );

    // Toggle the allowlist OFF (no list mutation, just the flag).
    client.apply_allowed_ccv_updates(&Vec::new(&env), &Vec::new(&env), &false);
    assert!(
        !client.get_dynamic_config().ccv_allowlist_enabled,
        "allowlist must be disabled after the toggle"
    );

    // OFF: the same stranger CCV is now accepted — enforcement is skipped.
    let fee = client.get_fee(
        &413,
        &WAIT_FOR_FINALITY,
        &ccvs,
        &Bytes::new(&env),
        &Address::generate(&env),
    );
    assert_eq!(fee, 42);
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

// ============================================================
// Event-extraction helper for withdraw_fee_tokens
// ============================================================

/// Decoded view of a `FeeTokenWithdrawnEvent` (mirrors `events::FeeTokenWithdrawnEvent`)
/// used to assert the parity event payload without depending on the event struct's
/// `Val` conversion shape directly.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
struct FeeWithdrawnRec {
    receiver: Address,
    fee_token: Address,
    amount: i128,
}

/// Collects every `FeeTokenWithdrawnEvent` emitted by `contract` in the most-recent
/// top-level invocation, in emission order. MUST be called immediately after
/// `withdraw_fee_tokens` and before any other contract call (e.g. a `balance`
/// query), since `env.events()` only reflects the most-recent top-level invocation.
fn fee_token_withdrawn_events(env: &Env, contract: &Address) -> Vec<FeeWithdrawnRec> {
    let evs = env.events().all().filter_by_contract(contract);
    let mut out = Vec::new(env);
    for e in evs.events().iter() {
        let soroban_sdk::xdr::ContractEventBody::V0(ref v0) = e.body else {
            continue;
        };
        let val: Val = match v0.data.clone().try_into_val(env) {
            Ok(v) => v,
            Err(_) => continue,
        };
        let Ok(map) = Map::<Symbol, Val>::try_from_val(env, &val) else {
            continue;
        };
        // Identify a FeeTokenWithdrawn event by its three field keys.
        let (Some(r), Some(f), Some(a)) = (
            map.get(Symbol::new(env, "receiver")),
            map.get(Symbol::new(env, "fee_token")),
            map.get(Symbol::new(env, "amount")),
        ) else {
            continue;
        };
        out.push_back(FeeWithdrawnRec {
            receiver: Address::try_from_val(env, &r).expect("receiver Address"),
            fee_token: Address::try_from_val(env, &f).expect("fee_token Address"),
            amount: i128::try_from_val(env, &a).expect("amount i128"),
        });
    }
    out
}

// ============================================================
// withdraw_fee_tokens event + multi-token Tests
// ============================================================

/// (c) `withdraw_fee_tokens` emits `FeeTokenWithdrawn(receiver, feeToken, amount)`
/// with the correct payload for each swept token — the parity event added to
/// mirror EVM `FeeTokenHandler._withdrawFeeTokens`. The existing balance-only
/// test does not assert the event; this one does.
#[test]
fn test_withdraw_fee_tokens_emits_fee_token_withdrawn_event() {
    let env = Env::default();
    env.mock_all_auths();

    let fee_aggregator = Address::generate(&env);
    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let dynamic_config = default_dynamic_config(&env, Some(fee_aggregator.clone()));
    client.initialize(&owner, &2, &dynamic_config);

    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin);
    let token_address = token_contract.address();
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    sac_client.mint(&contract_id, &1000);

    // Event extraction MUST run immediately after withdraw — see helper doc.
    client.withdraw_fee_tokens(&vec![&env, token_address.clone()]);
    let events = fee_token_withdrawn_events(&env, &contract_id);
    assert_eq!(
        events.len(),
        1,
        "exactly one FeeTokenWithdrawn event expected"
    );
    let rec = events.get(0).expect("first event");
    assert_eq!(
        rec.receiver, fee_aggregator,
        "receiver must be the fee aggregator"
    );
    assert_eq!(
        rec.fee_token, token_address,
        "fee_token must be the swept token"
    );
    assert_eq!(rec.amount, 1000, "amount must be the full swept balance");

    // Balance checks last (after event extraction).
    let token_client = token::Client::new(&env, &token_address);
    assert_eq!(token_client.balance(&contract_id), 0);
    assert_eq!(token_client.balance(&fee_aggregator), 1000);
}

/// (d) `withdraw_fee_tokens` with multiple fee tokens emits one
/// `FeeTokenWithdrawn` event per token that has a positive balance, and emits
/// none for a token with zero balance (the `balance > 0` skip branch). Mirrors
/// EVM `FeeTokenHandler`, which only sweeps and emits per token with a balance.
#[test]
fn test_withdraw_fee_tokens_multi_token_skips_zero_balance() {
    let env = Env::default();
    env.mock_all_auths();

    let fee_aggregator = Address::generate(&env);
    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let dynamic_config = default_dynamic_config(&env, Some(fee_aggregator.clone()));
    client.initialize(&owner, &2, &dynamic_config);

    // Token A: positive balance (swept + emits). Token B: zero balance (skipped, no emit).
    let admin_a = Address::generate(&env);
    let admin_b = Address::generate(&env);
    let token_a = env.register_stellar_asset_contract_v2(admin_a).address();
    let token_b = env.register_stellar_asset_contract_v2(admin_b).address();
    token::StellarAssetClient::new(&env, &token_a).mint(&contract_id, &750);
    // token_b deliberately left at 0 balance.

    client.withdraw_fee_tokens(&vec![&env, token_a.clone(), token_b.clone()]);
    let events = fee_token_withdrawn_events(&env, &contract_id);
    assert_eq!(
        events.len(),
        1,
        "only the positive-balance token emits FeeTokenWithdrawn"
    );
    let rec = events.get(0).expect("the one event");
    assert_eq!(
        rec.fee_token, token_a,
        "the emitted event must be for token_a"
    );
    assert_eq!(rec.amount, 750);

    // Balances last.
    let client_a = token::Client::new(&env, &token_a);
    let client_b = token::Client::new(&env, &token_b);
    assert_eq!(client_a.balance(&contract_id), 0);
    assert_eq!(client_a.balance(&fee_aggregator), 750);
    assert_eq!(client_b.balance(&contract_id), 0);
    assert_eq!(client_b.balance(&fee_aggregator), 0);
}

// ============================================================
// getFee ==max CCV boundary + apply_dest_chain_updates batch Tests
// ============================================================

/// (e) `get_fee` with a CCV count exactly equal to the immutable cap returns the
/// flat fee — the boundary complement to `test_get_fee_reverts_when_ccvs_exceed_max`
/// (which proves >max reverts). The cap is inclusive: `ccvs.len() <= max` passes.
#[test]
fn test_get_fee_allows_ccvs_exactly_at_max() {
    let (env, client, _owner) = setup(); // max = 2
    add_dest_chain(&client, 412, remote_chain(99, true));

    // Exactly 2 CCVs (== max) must be accepted and return the flat fee.
    let ccvs = vec![&env, Address::generate(&env), Address::generate(&env)];
    let fee = client.get_fee(
        &412,
        &WAIT_FOR_FINALITY,
        &ccvs,
        &Bytes::new(&env),
        &Address::generate(&env),
    );
    assert_eq!(fee, 99, "CCV count == max must be accepted (inclusive cap)");
}

/// (f) `apply_dest_chain_updates` applies removals before additions within a
/// single batch, so removing and re-adding the same selector in one call must
/// succeed and leave the new config in place (EVM `applyDestChainUpdates` removes
/// first, then validates+upserts). The existing add/remove test uses two separate
/// calls; this exercises the combined-batch ordering.
#[test]
fn test_apply_dest_chain_updates_remove_then_readd_same_selector_in_one_batch() {
    let (_env, client, _owner) = setup();
    // Seed selector 502 with an initial config.
    add_dest_chain(&client, 502, remote_chain(10, true));
    let before = client.get_dest_chain_config(&502);
    assert_eq!(before.usd_cents_fee, 10);

    // One batch: remove 502 AND add 502 back with a new config. Removals run
    // first, so the re-add is not treated as a duplicate; the new config wins.
    let to_remove = vec![&client.env, 502];
    let to_add = vec![
        &client.env,
        RemoteChainConfigArgs {
            dest_chain_selector: 502,
            config: remote_chain(77, true),
        },
    ];
    client.apply_dest_chain_updates(&to_remove, &to_add);

    let after = client.get_dest_chain_config(&502);
    assert_eq!(
        after.usd_cents_fee, 77,
        "re-add must install the new config"
    );
    assert!(after.enabled);
}

// ============================================================
// apply_allowed_ccv_updates owner-gating (Claim 3)
// ============================================================

// REQ (Claim 3): the Executor's allowlist of accepted CCVs (which CCVs may
// co-verify messages on this destination) is owner-gated at
// `apply_allowed_ccv_updates` (lib.rs:251). A non-owner must be rejected. The
// add/remove vectors are empty so the only gate exercised is the auth check.
#[test]
fn test_apply_allowed_ccv_updates_is_owner_only() {
    let (env, client, _owner) = setup();
    // Turn off mock_all_auths so the owner's require_auth() is not satisfied.
    env.mock_auths(&[]);
    let r = client.try_apply_allowed_ccv_updates(&Vec::new(&env), &Vec::new(&env), &false);
    assert!(
        r.is_err(),
        "non-owner must be rejected from adding/removing the Executor's allowed CCVs"
    );
}

// ============================================================
// Upgrade tests (shared `common_authorization::Upgradeable` opt-in)
// ============================================================

// Reuse the checked-in data-feeds fixture that exposes `peek() -> u32`.
// Executor has no `peek`, so a successful `peek` at the Executor address after
// `upgrade` proves the executable was swapped in place (same address, new Wasm).
// No `stellar` CLI in this env, so we reuse this fixture instead of adding one.
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
    let (env, client, _owner) = setup();

    // `setup` mocked all auths, so `require_owner` -> `owner.require_auth()` passes.
    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    client.upgrade(&hash);

    // The Upgraded event carries the new Wasm hash.
    assert_eq!(upgraded_event_hash(&env, &client.address), hash);

    // Executor has no `peek`; the fixture does. A successful `peek` at the
    // Executor address proves the executable was swapped in place. Instance
    // storage is preserved by `update_current_contract_wasm` (host guarantee),
    // and Executor never wrote the fixture's "slot" key, so peek reads back 0.
    let peeked: u32 = env.invoke_contract(
        &client.address,
        &symbol_short!("peek"),
        Vec::<Val>::new(&env),
    );
    assert_eq!(
        peeked, 0,
        "fixture peek must run at the executor address after upgrade"
    );
}

#[test]
#[should_panic(expected = "Error(Auth, InvalidAction)")]
fn test_upgrade_by_non_owner_rejected() {
    let env = Env::default();
    // No `mock_all_auths`: nobody is authorized, so `require_owner` ->
    // `owner.require_auth()` fails. `initialize` needs no auth (it only guards
    // against double-init), so the contract is set up with the owner stored.
    let contract_id = env.register(ExecutorContract, ());
    let client = ExecutorContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    let dynamic_config = default_dynamic_config(&env, Some(Address::generate(&env)));
    client.initialize(&owner, &2, &dynamic_config);

    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    // Caller is not the owner and the owner does not authorize -> reject before
    // the executable is touched.
    client.upgrade(&hash);
}
