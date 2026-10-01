#![cfg(test)]

use soroban_sdk::{
    contract, contractimpl, symbol_short, testutils::Address as _, testutils::Events as _, vec,
    Address, Bytes, BytesN, Env, Map, String, Symbol, TryFromVal, TryIntoVal, Val, Vec,
};

use crate::types::CCVConfigArg;
use crate::{AdvancedPoolHooksContract, AdvancedPoolHooksContractClient};
use common_error::CCIPError;
use common_interfaces::policy_engine::{Payload, PolicyData};
use common_interfaces::pool_hooks::{PoolHooksPayloadData, PreflightPayload};
use common_interfaces::token_pool::{LockOrBurnIn, MessageDirection, ReleaseOrMintIn};

const REMOTE_CHAIN: u64 = 5009297550715157269;
const REMOTE_CHAIN_2: u64 = 6112472187016712253;

fn setup() -> (Env, AdvancedPoolHooksContractClient<'static>, Address) {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let id = env.register(AdvancedPoolHooksContract, ());
    let client = AdvancedPoolHooksContractClient::new(&env, &id);
    // No allowlist, no threshold, no authorized callers by default.
    client.initialize(&owner, &Vec::new(&env), &0i128, &Vec::new(&env), &None);

    (env, client, owner)
}

/// Like `setup` but seeds `n` generated sender addresses into the allowlist,
/// returning them so tests can reference the allowed sender. The allowlist is
/// enabled iff `n > 0` (EVM `i_allowlistEnabled = allowlist.length > 0`).
fn setup_with_allowlist(
    n: usize,
) -> (
    Env,
    AdvancedPoolHooksContractClient<'static>,
    Address,
    Vec<Address>,
) {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let mut allowlist = Vec::new(&env);
    for _ in 0..n {
        allowlist.push_back(Address::generate(&env));
    }
    let id = env.register(AdvancedPoolHooksContract, ());
    let client = AdvancedPoolHooksContractClient::new(&env, &id);
    client.initialize(&owner, &allowlist, &0i128, &Vec::new(&env), &None);

    (env, client, owner, allowlist)
}

/// One outbound-only config arg with no threshold CCVs.
fn outbound_arg(
    env: &Env,
    selector: u64,
    ccvs: Vec<Address>,
    include_defaults: bool,
) -> CCVConfigArg {
    CCVConfigArg {
        remote_chain_selector: selector,
        outbound_ccvs: ccvs,
        threshold_outbound_ccvs: Vec::new(env),
        inbound_ccvs: Vec::new(env),
        threshold_inbound_ccvs: Vec::new(env),
        outbound_include_defaults: include_defaults,
        inbound_include_defaults: true,
    }
}

fn lock_or_burn(env: &Env, sender: Address) -> LockOrBurnIn {
    LockOrBurnIn {
        receiver: Bytes::new(env),
        remote_chain_selector: REMOTE_CHAIN,
        original_sender: sender,
        amount: 100,
        local_token: Address::generate(env),
    }
}

/// The zero Stellar account (EVM `address(0)` parity for allowlist rejection).
fn zero_addr(env: &Env) -> Address {
    Address::from_str(
        env,
        "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
    )
}

/// The zero Stellar contract — contract hash `0^32`, strkey `C…`. EVM
/// `address(0)` is type-agnostic, so the role-2 guards must reject a zero
/// *contract* too, not only a zero account. This exercises the parity gap the
/// shared `is_zero_address` helper closes (the old `is_zero_account` only
/// matched the account strkey).
fn zero_contract_addr(env: &Env) -> Address {
    Address::from_str(
        env,
        "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4",
    )
}

/// Adds one authorized caller to `client` (owner-gated; auths mocked) and
/// returns it, so preflight/postflight tests can pass it as the `caller` arg
/// (EVM `_validateCaller` — only authorized pools may invoke the hooks).
fn authorize_caller(client: &AdvancedPoolHooksContractClient<'static>) -> Address {
    let caller = Address::generate(&client.env);
    client.apply_authorized_callers_updates(
        &Vec::new(&client.env),
        &vec![&client.env, caller.clone()],
    );
    caller
}

// ============================================================
// CCV config admin
// ============================================================

#[test]
fn test_apply_ccv_config_updates_is_owner_only() {
    let (env, client, _owner) = setup();
    let ccv = Address::generate(&env);
    let arg = outbound_arg(&env, REMOTE_CHAIN, vec![&env, ccv], false);
    env.mock_auths(&[]);
    let r = client.try_apply_ccv_config_updates(&vec![&env, arg]);
    assert!(r.is_err(), "non-owner / unauthed call must fail");
}

#[test]
fn test_apply_ccv_config_updates_persists_and_reads_back() {
    let (env, client, _owner) = setup();
    let a = Address::generate(&env);
    let b = Address::generate(&env);
    let arg = outbound_arg(&env, REMOTE_CHAIN, vec![&env, a.clone(), b.clone()], false);
    client.apply_ccv_config_updates(&vec![&env, arg]);

    let cfg = client.get_ccv_config(&REMOTE_CHAIN).expect("config stored");
    assert_eq!(cfg.outbound_ccvs.len(), 2);
    assert_eq!(cfg.outbound_ccvs.get(0).unwrap(), a);
    assert_eq!(cfg.outbound_ccvs.get(1).unwrap(), b);
    assert!(!cfg.outbound_include_defaults);

    let all = client.get_all_ccv_configs();
    assert_eq!(all.len(), 1);
    assert_eq!(all.get(0).unwrap().remote_chain_selector, REMOTE_CHAIN);
}

#[test]
#[should_panic(expected = "Error(Contract, #320)")] // DuplicateCCVNotAllowed
fn test_apply_ccv_config_updates_rejects_duplicate_ccv() {
    let (env, client, _owner) = setup();
    let ccv = Address::generate(&env);
    let arg = outbound_arg(&env, REMOTE_CHAIN, vec![&env, ccv.clone(), ccv], false);
    client.apply_ccv_config_updates(&vec![&env, arg]);
}

#[test]
#[should_panic(expected = "Error(Contract, #52)")] // InvalidConfig (threshold without base)
fn test_apply_ccv_config_updates_rejects_threshold_without_base() {
    let (env, client, _owner) = setup();
    let base = Address::generate(&env);
    let threshold = Address::generate(&env);
    let arg = CCVConfigArg {
        remote_chain_selector: REMOTE_CHAIN,
        outbound_ccvs: Vec::new(&env), // no base
        threshold_outbound_ccvs: vec![&env, threshold],
        inbound_ccvs: vec![&env, base],
        threshold_inbound_ccvs: Vec::new(&env),
        // include_defaults counts as a base entry (EVM address(0)); to exercise
        // the genuine "threshold without ANY base" reject, defaults must be off.
        outbound_include_defaults: false,
        inbound_include_defaults: true,
    };
    client.apply_ccv_config_updates(&vec![&env, arg]);
}

#[test]
fn test_apply_ccv_config_updates_removes_empty_config() {
    let (env, client, _owner) = setup();
    let a = Address::generate(&env);
    client.apply_ccv_config_updates(&vec![
        &env,
        outbound_arg(&env, REMOTE_CHAIN, vec![&env, a], false),
    ]);
    assert!(client.get_ccv_config(&REMOTE_CHAIN).is_some());

    // Re-apply an all-empty config -> entry removed (EVM s_configuredChainSelectors bookkeeping).
    // include_defaults counts as a base contribution, so a defaults-only config is
    // KEPT, not removed; to exercise the genuine removal path, defaults must be off.
    let empty = CCVConfigArg {
        remote_chain_selector: REMOTE_CHAIN,
        outbound_ccvs: Vec::new(&env),
        threshold_outbound_ccvs: Vec::new(&env),
        inbound_ccvs: Vec::new(&env),
        threshold_inbound_ccvs: Vec::new(&env),
        outbound_include_defaults: false,
        inbound_include_defaults: false,
    };
    client.apply_ccv_config_updates(&vec![&env, empty]);
    assert!(client.get_ccv_config(&REMOTE_CHAIN).is_none());
    assert_eq!(client.get_all_ccv_configs().len(), 0);
}

#[test]
fn test_apply_ccv_config_updates_emits_on_removal() {
    // EVM emits `CCVConfigUpdated` outside the add/remove branch
    // (AdvancedPoolHooks.sol#L291), so an all-empty (removal) config ALSO emits
    // — carrying the emptied config. The Stellar port mirrors that: the removal
    // call must emit exactly one `CCVConfigUpdated` event (parity with EVM's
    // unconditional emit), not zero.
    let (env, client, _owner) = setup();
    let a = Address::generate(&env);
    client.apply_ccv_config_updates(&vec![
        &env,
        outbound_arg(&env, REMOTE_CHAIN, vec![&env, a], false),
    ]);
    assert!(client.get_ccv_config(&REMOTE_CHAIN).is_some());

    let empty = CCVConfigArg {
        remote_chain_selector: REMOTE_CHAIN,
        outbound_ccvs: Vec::new(&env),
        threshold_outbound_ccvs: Vec::new(&env),
        inbound_ccvs: Vec::new(&env),
        threshold_inbound_ccvs: Vec::new(&env),
        // include_defaults counts as a base contribution, so defaults-only is KEPT;
        // to exercise the genuine removal path (which the emit-on-removal parity
        // asserts), defaults must be off here.
        outbound_include_defaults: false,
        inbound_include_defaults: false,
    };
    client.apply_ccv_config_updates(&vec![&env, empty]);
    // Read events before any later contract call clears the test-env event view.
    assert_eq!(
        env.events().all().events().len(),
        1,
        "removal must emit CCVConfigUpdated (EVM emits unconditionally)"
    );
    assert!(client.get_ccv_config(&REMOTE_CHAIN).is_none());
}

#[test]
fn test_apply_ccv_config_updates_accepts_threshold_with_defaults_only() {
    // EVM parity: `outboundCCVs = [address(0)]` (defaults) + a threshold list is
    // VALID — `address(0)` satisfies the "must specify base if threshold" check
    // (AdvancedPoolHooks.sol#L258). The Stellar analogue is an empty base list
    // with `outbound_include_defaults: true`. Before the fix, validate() rejected
    // this (threshold without base, defaults not counted); now it is accepted.
    let (env, client, _owner) = setup();
    let threshold = Address::generate(&env);
    let arg = CCVConfigArg {
        remote_chain_selector: REMOTE_CHAIN,
        outbound_ccvs: Vec::new(&env), // no explicit base — defaults stand in
        threshold_outbound_ccvs: vec![&env, threshold.clone()],
        inbound_ccvs: Vec::new(&env),
        threshold_inbound_ccvs: Vec::new(&env),
        outbound_include_defaults: true,
        inbound_include_defaults: false,
    };
    client.apply_ccv_config_updates(&vec![&env, arg]);

    let stored = client.get_ccv_config(&REMOTE_CHAIN);
    assert!(
        stored.is_some(),
        "defaults-only base + threshold must be kept"
    );
    let cfg = stored.unwrap();
    assert!(cfg.outbound_ccvs.is_empty());
    assert!(cfg.outbound_include_defaults);
    assert_eq!(cfg.threshold_outbound_ccvs.len(), 1);
    assert_eq!(cfg.threshold_outbound_ccvs.get(0).unwrap(), threshold);
}

#[test]
fn test_apply_ccv_config_updates_keeps_defaults_only_config() {
    // EVM parity: `outboundCCVs = [address(0)]` with no threshold list is a real,
    // keepable config ("use the lane defaults, nothing extra") and must NOT be
    // dropped by the s_configuredChainSelectors bookkeeping
    // (AdvancedPoolHooks.sol#L285). The Stellar analogue is an all-empty config
    // with `outbound_include_defaults: true`. Before the fix, has_base was false
    // and map.remove silently dropped it; now include_defaults counts as a base
    // contribution and the entry is kept.
    let (env, client, _owner) = setup();
    let arg = CCVConfigArg {
        remote_chain_selector: REMOTE_CHAIN,
        outbound_ccvs: Vec::new(&env),
        threshold_outbound_ccvs: Vec::new(&env),
        inbound_ccvs: Vec::new(&env),
        threshold_inbound_ccvs: Vec::new(&env),
        outbound_include_defaults: true,
        inbound_include_defaults: false,
    };
    client.apply_ccv_config_updates(&vec![&env, arg]);

    let stored = client.get_ccv_config(&REMOTE_CHAIN);
    assert!(
        stored.is_some(),
        "defaults-only config must be kept, not removed"
    );
    let cfg = stored.unwrap();
    assert!(cfg.outbound_ccvs.is_empty());
    assert!(cfg.outbound_include_defaults);
    assert_eq!(client.get_all_ccv_configs().len(), 1);
}

// ============================================================
// get_required_ccvs
// ============================================================

#[test]
fn test_get_required_ccvs_returns_stored_config() {
    let (env, client, _owner) = setup();
    let a = Address::generate(&env);
    let b = Address::generate(&env);
    client.apply_ccv_config_updates(&vec![
        &env,
        outbound_arg(&env, REMOTE_CHAIN, vec![&env, a.clone(), b.clone()], false),
    ]);

    let v = client.get_required_ccvs(
        &Address::generate(&env),
        &REMOTE_CHAIN,
        &100i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Outbound,
    );
    assert_eq!(v.ccvs.len(), 2);
    assert_eq!(v.ccvs.get(0).unwrap(), a);
    assert_eq!(v.ccvs.get(1).unwrap(), b);
    assert!(!v.include_defaults);
}

#[test]
fn test_get_required_ccvs_default_when_no_config() {
    let (env, client, _owner) = setup();
    let v = client.get_required_ccvs(
        &Address::generate(&env),
        &REMOTE_CHAIN,
        &100i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Outbound,
    );
    assert_eq!(v.ccvs.len(), 0);
    assert!(
        v.include_defaults,
        "unconfigured chain must fall back to lane defaults"
    );
}

#[test]
fn test_get_required_ccvs_threshold_appends_above_threshold() {
    let (env, client, _owner) = setup();
    let base = Address::generate(&env);
    let extra = Address::generate(&env);
    let arg = CCVConfigArg {
        remote_chain_selector: REMOTE_CHAIN,
        outbound_ccvs: vec![&env, base.clone()],
        threshold_outbound_ccvs: vec![&env, extra.clone()],
        inbound_ccvs: Vec::new(&env),
        threshold_inbound_ccvs: Vec::new(&env),
        outbound_include_defaults: false,
        inbound_include_defaults: true,
    };
    client.apply_ccv_config_updates(&vec![&env, arg]);
    client.set_threshold_amount(&1_000i128);

    // Below threshold -> base only.
    let below = client.get_required_ccvs(
        &Address::generate(&env),
        &REMOTE_CHAIN,
        &500i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Outbound,
    );
    assert_eq!(below.ccvs.len(), 1);
    assert_eq!(below.ccvs.get(0).unwrap(), base);

    // At threshold -> base + extra.
    let above = client.get_required_ccvs(
        &Address::generate(&env),
        &REMOTE_CHAIN,
        &1_000i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Outbound,
    );
    assert_eq!(above.ccvs.len(), 2);
    assert_eq!(above.ccvs.get(0).unwrap(), base);
    assert_eq!(above.ccvs.get(1).unwrap(), extra);
}

// ============================================================
// Threshold amount
// ============================================================

#[test]
fn test_set_threshold_amount_is_owner_only_and_reads_back() {
    let (env, client, _owner) = setup();
    env.mock_auths(&[]);
    let r = client.try_set_threshold_amount(&123i128);
    assert!(r.is_err(), "non-owner / unauthed call must fail");

    // Owner (mocked) can set.
    env.mock_all_auths_allowing_non_root_auth();
    client.set_threshold_amount(&123i128);
    assert_eq!(client.get_threshold_amount(), 123);
}

// ============================================================
// Sender allowlist + preflight
// ============================================================

#[test]
fn test_allowlist_enabled_iff_supplied() {
    let (_env, client, _owner, _allowlist) = setup_with_allowlist(1);
    assert!(client.get_allowlist_enabled());
    assert_eq!(client.get_allowlist().len(), 1);

    let (_env2, client2, _owner2) = setup();
    assert!(!client2.get_allowlist_enabled());
    assert_eq!(client2.get_allowlist().len(), 0);
}

#[test]
#[should_panic(expected = "Error(Contract, #49)")] // SenderNotAllowed
fn test_preflight_rejects_non_allowlisted_sender() {
    let (env, client, _owner, _allowlist) = setup_with_allowlist(1);
    let caller = authorize_caller(&client);
    let stranger = Address::generate(&env);
    let lob = lock_or_burn(&env, stranger);
    client.preflight_check(&caller, &lob, &0u32, &Bytes::new(&env), &0i128);
}

#[test]
fn test_preflight_allows_allowlisted_sender() {
    let (env, client, _owner, allowlist) = setup_with_allowlist(1);
    let caller = authorize_caller(&client);
    let allowed = allowlist.get(0).unwrap();
    let lob = lock_or_burn(&env, allowed);
    // Non-try call panics on Err; reaching the end means Ok.
    client.preflight_check(&caller, &lob, &0u32, &Bytes::new(&env), &0i128);
}

#[test]
fn test_preflight_noop_when_allowlist_disabled() {
    let (env, client, _owner) = setup(); // allowlist off
    let caller = authorize_caller(&client);
    let lob = lock_or_burn(&env, Address::generate(&env));
    client.preflight_check(&caller, &lob, &0u32, &Bytes::new(&env), &0i128);
}

#[test]
fn test_check_allow_list_noop_when_disabled() {
    // EVM `checkAllowList` is a no-op when `i_allowlistEnabled == false`: any
    // sender (even a stranger) passes.
    let (env, client, _owner) = setup(); // allowlist off
    let stranger = Address::generate(&env);
    client.check_allow_list(&stranger);
}

#[test]
fn test_check_allow_list_accepts_allowlisted_sender() {
    let (_env, client, _owner, allowlist) = setup_with_allowlist(1);
    let allowed = allowlist.get(0).unwrap();
    client.check_allow_list(&allowed);
}

#[test]
#[should_panic(expected = "Error(Contract, #49)")] // SenderNotAllowed
fn test_check_allow_list_rejects_non_allowlisted_sender() {
    let (env, client, _owner, _allowlist) = setup_with_allowlist(1);
    let stranger = Address::generate(&env);
    client.check_allow_list(&stranger);
}

#[test]
#[should_panic(expected = "Error(Contract, #10)")] // FeatureNotEnabled (AllowListNotEnabled)
fn test_apply_allowlist_updates_rejected_when_disabled() {
    let (env, client, _owner) = setup(); // allowlist disabled at init
    client.apply_allowlist_updates(&Vec::new(&env), &vec![&env, Address::generate(&env)]);
}

#[test]
fn test_apply_allowlist_updates_adds_and_removes() {
    let (_env, client, _owner, allowlist) = setup_with_allowlist(1);
    let initial = allowlist.get(0).unwrap();

    let extra = Address::generate(&client.env);
    client.apply_allowlist_updates(&Vec::new(&client.env), &vec![&client.env, extra.clone()]);
    assert_eq!(client.get_allowlist().len(), 2);

    client.apply_allowlist_updates(&vec![&client.env, initial], &Vec::new(&client.env));
    assert_eq!(client.get_allowlist().len(), 1);
    assert_eq!(client.get_allowlist().get(0).unwrap(), extra);
}

// ============================================================
// postflight with no engine attached (dormant policy -> no-op)
// ============================================================

#[test]
fn test_postflight_noop_without_engine() {
    let (env, client, _owner) = setup();
    let caller = authorize_caller(&client);
    // No engine attached -> the policy run is skipped; reaching the end
    // means Ok (EVM address(0) short-circuit).
    client.postflight_check(
        &caller,
        &ReleaseOrMintIn {
            original_sender: Bytes::new(&env),
            remote_chain_selector: REMOTE_CHAIN,
            receiver: Address::generate(&env),
            amount: 100,
            local_token: Address::generate(&env),
            source_pool_address: Bytes::new(&env),
            source_pool_data: Bytes::new(&env),
        },
        &100i128,
        &0u32,
    );
}

// ============================================================
// Authorized-callers invocation gating (EVM `_validateCaller`)
// ============================================================

#[test]
#[should_panic(expected = "Error(Contract, #6)")] // CallerNotAuthorized
fn test_preflight_rejects_unauthorized_caller() {
    let (env, client, _owner) = setup(); // no authorized callers seeded
    let stranger = Address::generate(&env); // not in the authorized set
    let lob = lock_or_burn(&env, Address::generate(&env));
    client.preflight_check(&stranger, &lob, &0u32, &Bytes::new(&env), &0i128);
}

#[test]
#[should_panic(expected = "Error(Contract, #6)")] // CallerNotAuthorized
fn test_postflight_rejects_unauthorized_caller() {
    let (env, client, _owner) = setup(); // no authorized callers seeded
    let stranger = Address::generate(&env);
    client.postflight_check(
        &stranger,
        &ReleaseOrMintIn {
            original_sender: Bytes::new(&env),
            remote_chain_selector: REMOTE_CHAIN,
            receiver: Address::generate(&env),
            amount: 100,
            local_token: Address::generate(&env),
            source_pool_address: Bytes::new(&env),
            source_pool_data: Bytes::new(&env),
        },
        &100i128,
        &0u32,
    );
}

#[test]
fn test_apply_authorized_callers_updates_is_owner_only() {
    let (env, client, _owner) = setup();
    let extra = Address::generate(&env);
    env.mock_auths(&[]);
    let r = client.try_apply_authorized_callers_updates(&Vec::new(&env), &vec![&env, extra]);
    assert!(r.is_err(), "non-owner / unauthed call must fail");
}

#[test]
fn test_apply_authorized_callers_updates_adds_and_removes() {
    let (_env, client, _owner) = setup();
    assert_eq!(client.get_all_authorized_callers().len(), 0);

    let a = Address::generate(&client.env);
    let b = Address::generate(&client.env);
    client.apply_authorized_callers_updates(
        &Vec::new(&client.env),
        &vec![&client.env, a.clone(), b.clone()],
    );
    let stored = client.get_all_authorized_callers();
    assert_eq!(stored.len(), 2);

    // Removal of `a` only; `b` remains.
    client.apply_authorized_callers_updates(&vec![&client.env, a], &Vec::new(&client.env));
    let stored = client.get_all_authorized_callers();
    assert_eq!(stored.len(), 1);
    assert_eq!(stored.get(0).unwrap(), b);
}

#[test]
fn test_initialize_seeds_authorized_callers_with_dedup() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let real = Address::generate(&env);
    let pool = Address::generate(&env);
    // [real, real, pool] -> stored = [real, pool] (dup collapsed; no zero entry).
    let authorized = vec![&env, real.clone(), real.clone(), pool.clone()];
    let id = env.register(AdvancedPoolHooksContract, ());
    let client = AdvancedPoolHooksContractClient::new(&env, &id);
    client.initialize(&owner, &Vec::new(&env), &0i128, &authorized, &None);

    let stored = client.get_all_authorized_callers();
    assert_eq!(stored.len(), 2);
    assert_eq!(stored.get(0).unwrap(), real);
    assert_eq!(stored.get(1).unwrap(), pool);
}

#[test]
#[should_panic(expected = "Error(Contract, #808)")] // ZeroAddressNotAllowed
fn test_initialize_rejects_zero_authorized_caller() {
    // EVM constructor → `_applyAuthorizedCallerUpdates` reverts
    // `ZeroAddressNotAllowed` on a zero add; Stellar matches (unlike the
    // allowlist seeding, which skips zeros per `_applyAllowListUpdates`).
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let real = Address::generate(&env);
    let authorized = vec![&env, zero_addr(&env), real.clone()];
    let id = env.register(AdvancedPoolHooksContract, ());
    let client = AdvancedPoolHooksContractClient::new(&env, &id);
    client.initialize(&owner, &Vec::new(&env), &0i128, &authorized, &None);
}

#[test]
#[should_panic(expected = "Error(Contract, #808)")] // ZeroAddressNotAllowed
fn test_apply_authorized_callers_updates_rejects_zero_add() {
    // EVM `_applyAuthorizedCallerUpdates` reverts `ZeroAddressNotAllowed` on a
    // zero add; Stellar matches.
    let (_env, client, _owner) = setup();
    let zero = zero_addr(&client.env);
    client.apply_authorized_callers_updates(&Vec::new(&client.env), &vec![&client.env, zero]);
}

#[test]
#[should_panic(expected = "Error(Contract, #808)")] // ZeroAddressNotAllowed
fn test_initialize_rejects_zero_contract_authorized_caller() {
    // EVM `address(0)` is type-agnostic — a zero is a zero whether the slot
    // expects an EOA or a contract. The shared `is_zero_address` helper checks
    // both strkey kinds, so a zero *contract* hash is rejected just like a zero
    // account. This is the gap the old account-only `is_zero_account` left open.
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let real = Address::generate(&env);
    let authorized = vec![&env, zero_contract_addr(&env), real.clone()];
    let id = env.register(AdvancedPoolHooksContract, ());
    let client = AdvancedPoolHooksContractClient::new(&env, &id);
    client.initialize(&owner, &Vec::new(&env), &0i128, &authorized, &None);
}

#[test]
#[should_panic(expected = "Error(Contract, #808)")] // ZeroAddressNotAllowed
fn test_apply_authorized_callers_updates_rejects_zero_contract_add() {
    // Same type-agnostic parity as the initialize variant, via the update path.
    let (_env, client, _owner) = setup();
    let zero = zero_contract_addr(&client.env);
    client.apply_authorized_callers_updates(&Vec::new(&client.env), &vec![&client.env, zero]);
}

#[test]
fn test_type_and_version() {
    let (env, client, _owner) = setup();
    assert_eq!(
        client.type_and_version(),
        soroban_sdk::String::from_str(&env, "AdvancedPoolHooks 2.0.0-dev")
    );
}

// ============================================================
// Inbound direction + include_defaults relay
// ============================================================

#[test]
fn test_get_required_ccvs_inbound_direction() {
    let (env, client, _owner) = setup();
    let out = Address::generate(&env);
    let inc = Address::generate(&env);
    // A config that distinguishes outbound from inbound base lists.
    let arg = CCVConfigArg {
        remote_chain_selector: REMOTE_CHAIN,
        outbound_ccvs: vec![&env, out.clone()],
        threshold_outbound_ccvs: Vec::new(&env),
        inbound_ccvs: vec![&env, inc.clone()],
        threshold_inbound_ccvs: Vec::new(&env),
        outbound_include_defaults: false,
        inbound_include_defaults: false,
    };
    client.apply_ccv_config_updates(&vec![&env, arg]);

    let v_out = client.get_required_ccvs(
        &Address::generate(&env),
        &REMOTE_CHAIN,
        &100i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Outbound,
    );
    assert_eq!(v_out.ccvs.len(), 1);
    assert_eq!(v_out.ccvs.get(0).unwrap(), out);
    assert!(!v_out.include_defaults);

    let v_in = client.get_required_ccvs(
        &Address::generate(&env),
        &REMOTE_CHAIN,
        &100i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Inbound,
    );
    assert_eq!(v_in.ccvs.len(), 1);
    assert_eq!(v_in.ccvs.get(0).unwrap(), inc);
    assert!(!v_in.include_defaults);
}

#[test]
fn test_get_required_ccvs_include_defaults_relayed() {
    let (env, client, _owner) = setup();
    let a = Address::generate(&env);
    client.apply_ccv_config_updates(&vec![
        &env,
        outbound_arg(&env, REMOTE_CHAIN, vec![&env, a], true),
    ]);
    let v = client.get_required_ccvs(
        &Address::generate(&env),
        &REMOTE_CHAIN,
        &100i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Outbound,
    );
    // The hooks only relays the flag; the pool appends lane defaults. Here we
    // assert the flag propagates so the pool knows to append.
    assert!(v.include_defaults);
    assert_eq!(v.ccvs.len(), 1);
}

#[test]
fn test_get_required_ccvs_threshold_inbound() {
    let (env, client, _owner) = setup();
    let base = Address::generate(&env);
    let extra = Address::generate(&env);
    let arg = CCVConfigArg {
        remote_chain_selector: REMOTE_CHAIN,
        outbound_ccvs: Vec::new(&env),
        threshold_outbound_ccvs: Vec::new(&env),
        inbound_ccvs: vec![&env, base.clone()],
        threshold_inbound_ccvs: vec![&env, extra.clone()],
        outbound_include_defaults: true,
        inbound_include_defaults: false,
    };
    client.apply_ccv_config_updates(&vec![&env, arg]);
    client.set_threshold_amount(&1_000i128);

    let below = client.get_required_ccvs(
        &Address::generate(&env),
        &REMOTE_CHAIN,
        &500i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Inbound,
    );
    assert_eq!(below.ccvs.len(), 1);
    assert_eq!(below.ccvs.get(0).unwrap(), base);

    let above = client.get_required_ccvs(
        &Address::generate(&env),
        &REMOTE_CHAIN,
        &1_000i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Inbound,
    );
    assert_eq!(above.ccvs.len(), 2);
    assert_eq!(above.ccvs.get(0).unwrap(), base);
    assert_eq!(above.ccvs.get(1).unwrap(), extra);
}

#[test]
fn test_get_required_ccvs_threshold_never_appended_when_zero() {
    let (env, client, _owner) = setup();
    let base = Address::generate(&env);
    let extra = Address::generate(&env);
    let arg = CCVConfigArg {
        remote_chain_selector: REMOTE_CHAIN,
        outbound_ccvs: vec![&env, base.clone()],
        threshold_outbound_ccvs: vec![&env, extra], // configured but unused
        inbound_ccvs: Vec::new(&env),
        threshold_inbound_ccvs: Vec::new(&env),
        outbound_include_defaults: false,
        inbound_include_defaults: true,
    };
    client.apply_ccv_config_updates(&vec![&env, arg]);
    // threshold_amount left at default 0 -> threshold CCVs never appended,
    // even for a huge amount.
    let v = client.get_required_ccvs(
        &Address::generate(&env),
        &REMOTE_CHAIN,
        &1_000_000i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Outbound,
    );
    assert_eq!(v.ccvs.len(), 1);
    assert_eq!(v.ccvs.get(0).unwrap(), base);
}

// ============================================================
// Multi-chain batch + cross-list validation
// ============================================================

#[test]
fn test_apply_ccv_config_updates_multi_chain_batch() {
    let (env, client, _owner) = setup();
    let a1 = Address::generate(&env);
    let a2 = Address::generate(&env);
    let b1 = Address::generate(&env);
    client.apply_ccv_config_updates(&vec![
        &env,
        outbound_arg(&env, REMOTE_CHAIN, vec![&env, a1, a2], false),
        outbound_arg(&env, REMOTE_CHAIN_2, vec![&env, b1], false),
    ]);

    assert_eq!(client.get_all_ccv_configs().len(), 2);
    assert_eq!(
        client
            .get_ccv_config(&REMOTE_CHAIN)
            .unwrap()
            .outbound_ccvs
            .len(),
        2
    );
    assert_eq!(
        client
            .get_ccv_config(&REMOTE_CHAIN_2)
            .unwrap()
            .outbound_ccvs
            .len(),
        1
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #320)")] // DuplicateCCVNotAllowed (base/threshold cross-overlap)
fn test_apply_ccv_config_updates_rejects_cross_list_overlap() {
    let (env, client, _owner) = setup();
    let shared = Address::generate(&env);
    let arg = CCVConfigArg {
        remote_chain_selector: REMOTE_CHAIN,
        outbound_ccvs: vec![&env, shared.clone()],
        threshold_outbound_ccvs: vec![&env, shared], // same CCV in base + threshold
        inbound_ccvs: Vec::new(&env),
        threshold_inbound_ccvs: Vec::new(&env),
        outbound_include_defaults: false,
        inbound_include_defaults: true,
    };
    client.apply_ccv_config_updates(&vec![&env, arg]);
}

#[test]
#[should_panic(expected = "Error(Contract, #52)")] // InvalidConfig (inbound threshold without inbound base)
fn test_apply_ccv_config_updates_rejects_inbound_threshold_without_base() {
    let (env, client, _owner) = setup();
    let arg = CCVConfigArg {
        remote_chain_selector: REMOTE_CHAIN,
        outbound_ccvs: vec![&env, Address::generate(&env)], // outbound base present
        threshold_outbound_ccvs: Vec::new(&env),
        inbound_ccvs: Vec::new(&env), // no inbound base
        threshold_inbound_ccvs: vec![&env, Address::generate(&env)],
        outbound_include_defaults: false,
        // include_defaults counts as a base entry (EVM address(0)); to exercise
        // the genuine "inbound threshold without ANY base" reject, defaults must
        // be off.
        inbound_include_defaults: false,
    };
    client.apply_ccv_config_updates(&vec![&env, arg]);
}

// ============================================================
// Allowlist zero-skip + dedup (init + updates)
// ============================================================

#[test]
fn test_initialize_skips_zero_and_dedups_allowlist() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let real = Address::generate(&env);
    // [zero, real, real] -> enabled (non-empty), stored = [real] only.
    let allowlist = vec![&env, zero_addr(&env), real.clone(), real.clone()];
    let id = env.register(AdvancedPoolHooksContract, ());
    let client = AdvancedPoolHooksContractClient::new(&env, &id);
    client.initialize(&owner, &allowlist, &0i128, &Vec::new(&env), &None);

    assert!(client.get_allowlist_enabled());
    let stored = client.get_allowlist();
    assert_eq!(stored.len(), 1);
    assert_eq!(stored.get(0).unwrap(), real);
}

#[test]
fn test_apply_allowlist_updates_skips_zero_and_dedups() {
    let (_env, client, _owner, _allowlist) = setup_with_allowlist(1);
    let extra = Address::generate(&client.env);
    // adds = [zero, extra, extra] -> only `extra` appended (zero skipped, dup collapsed).
    client.apply_allowlist_updates(
        &Vec::new(&client.env),
        &vec![
            &client.env,
            zero_addr(&client.env),
            extra.clone(),
            extra.clone(),
        ],
    );
    let stored = client.get_allowlist();
    // 1 (initial) + 1 (extra) = 2; zero and the duplicate extra ignored.
    assert_eq!(stored.len(), 2);
    let mut found_extra = false;
    for i in 0..stored.len() {
        if stored.get(i).unwrap() == extra {
            found_extra = true;
        }
    }
    assert!(found_extra, "extra must be present after the dedup'd add");
}

// ============================================================
// Upgrade tests (shared `common_authorization::Upgradeable` opt-in)
// ============================================================

// Reuse the checked-in data-feeds fixture that exposes `peek() -> u32`.
// AdvancedPoolHooks has no `peek`, so a successful `peek` at the hooks address
// after `upgrade` proves the executable was swapped in place. No `stellar` CLI
// in this env, so we reuse this fixture instead of adding one.
const UPGRADE_TARGET_WASM: &[u8] =
    include_bytes!("../../../data-feeds/data-feeds-common/test_fixtures/upgrade_target.wasm");

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

    // AdvancedPoolHooks has no `peek`; the fixture does. A successful `peek` at
    // the hooks address proves the executable was swapped in place. Instance
    // storage is preserved by `update_current_contract_wasm` (host guarantee),
    // and the hooks never wrote the fixture's "slot" key, so peek reads back 0.
    let peeked: u32 = env.invoke_contract(
        &client.address,
        &symbol_short!("peek"),
        Vec::<Val>::new(&env),
    );
    assert_eq!(
        peeked, 0,
        "fixture peek must run at the advanced pool hooks address after upgrade"
    );
}

#[test]
#[should_panic(expected = "Error(Auth, InvalidAction)")]
fn test_upgrade_by_non_owner_rejected() {
    let env = Env::default();
    // No `mock_all_auths`: nobody is authorized, so `require_owner` ->
    // `owner.require_auth()` fails. `initialize` needs no auth (it only guards
    // against double-init), so the contract is set up with the owner stored.
    let contract_id = env.register(AdvancedPoolHooksContract, ());
    let client = AdvancedPoolHooksContractClient::new(&env, &contract_id);

    let owner = Address::generate(&env);
    client.initialize(&owner, &Vec::new(&env), &0i128, &Vec::new(&env), &None);

    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    // Caller is not the owner and the owner does not authorize -> reject before
    // the executable is touched.
    client.upgrade(&hash);
}

// ============================================================
// Policy engine (EVM `s_policyEngine` + `_setPolicyEngine`)
// ============================================================

/// In-workspace mock policy engine. Implements the `PolicyEngineInterface` ABI
/// (`attach`/`detach`/`run`/`check`/`type_and_version`) so the hooks' `PolicyEngineClient`
/// can call it cross-contract. Records calls + the last `run` payload, and can
/// be configured to revert on `detach` (adversarial old engine) or reject on
/// `run` (policy rejection), to exercise the detach escape hatch and the
/// transfer-blocking path.
#[contract]
pub struct MockPolicyEngineContract;

const MOCK_REVERT_DETACH: Symbol = symbol_short!("MREVDET");
const MOCK_REVERT_RUN: Symbol = symbol_short!("MREVRUN");
const MOCK_LAST_PAYLOAD: Symbol = symbol_short!("MLASTPL");
const MOCK_ATTACH_COUNT: Symbol = symbol_short!("MATTACH");
const MOCK_DETACH_COUNT: Symbol = symbol_short!("MDETACH");

#[contractimpl]
impl MockPolicyEngineContract {
    pub fn attach(env: Env, target: Address) -> Result<(), CCIPError> {
        target.require_auth();
        let c: u32 = env
            .storage()
            .instance()
            .get(&MOCK_ATTACH_COUNT)
            .unwrap_or(0);
        env.storage().instance().set(&MOCK_ATTACH_COUNT, &(c + 1));
        Ok(())
    }

    pub fn detach(env: Env, target: Address) -> Result<(), CCIPError> {
        target.require_auth();
        if env
            .storage()
            .instance()
            .get(&MOCK_REVERT_DETACH)
            .unwrap_or(false)
        {
            // Adversarial engine whose detach reverts (EVM try/catch `catch` branch).
            panic!("mock detach reverted");
        }
        let c: u32 = env
            .storage()
            .instance()
            .get(&MOCK_DETACH_COUNT)
            .unwrap_or(0);
        env.storage().instance().set(&MOCK_DETACH_COUNT, &(c + 1));
        Ok(())
    }

    pub fn run(env: Env, target: Address, payload: Payload) -> Result<(), CCIPError> {
        // A real engine requires `target.require_auth()` — this proves the
        // target authorized the run, not that it is the direct caller
        // (trusted-relay model, see the trait docs).
        target.require_auth();
        if env
            .storage()
            .instance()
            .get(&MOCK_REVERT_RUN)
            .unwrap_or(false)
        {
            // Policy rejection — non-`try_` `PolicyEngineClient::run` panics on
            // this, aborting the hooks call and blocking the transfer.
            return Err(CCIPError::Unauthorized);
        }
        env.storage()
            .instance()
            .set(&MOCK_LAST_PAYLOAD, &Some(payload));
        Ok(())
    }

    pub fn check(env: Env, _target: Address, _payload: Payload) -> Result<(), CCIPError> {
        // Offchain pre-validation (EVM `IPolicyEngine.check`): rejects iff `run`
        // with the same target + payload would. No recording — `check` is a
        // pure view; the target is asserted, not authenticated (offchain there
        // is no call tree, matching EVM's unauthenticated `eth_call`).
        if env
            .storage()
            .instance()
            .get(&MOCK_REVERT_RUN)
            .unwrap_or(false)
        {
            return Err(CCIPError::Unauthorized);
        }
        Ok(())
    }

    pub fn type_and_version(env: Env) -> String {
        String::from_str(&env, "MockPolicyEngine 1.0.0")
    }

    // ---- test-only configuration / inspection ----
    pub fn set_revert_on_detach(env: Env, v: bool) {
        env.storage().instance().set(&MOCK_REVERT_DETACH, &v);
    }
    pub fn set_revert_on_run(env: Env, v: bool) {
        env.storage().instance().set(&MOCK_REVERT_RUN, &v);
    }
    pub fn last_payload(env: Env) -> Option<Payload> {
        env.storage()
            .instance()
            .get(&MOCK_LAST_PAYLOAD)
            .unwrap_or(None)
    }
    pub fn attach_count(env: Env) -> u32 {
        env.storage()
            .instance()
            .get(&MOCK_ATTACH_COUNT)
            .unwrap_or(0)
    }
    pub fn detach_count(env: Env) -> u32 {
        env.storage()
            .instance()
            .get(&MOCK_DETACH_COUNT)
            .unwrap_or(0)
    }
}

/// Registers a fresh mock policy engine and returns its address + client.
fn register_mock_engine(env: &Env) -> (Address, MockPolicyEngineContractClient<'static>) {
    let id = env.register(MockPolicyEngineContract, ());
    let client = MockPolicyEngineContractClient::new(env, &id);
    (id, client)
}

#[test]
fn test_policy_engine_dormant_by_default() {
    let (env, client, _owner) = setup();
    assert_eq!(client.get_policy_engine(), None);
    // No engine => preflight/postflight must not touch any engine. Allowlist is
    // off in `setup`, so this completes (no panic) and records no payload.
    let caller = authorize_caller(&client);
    let lob = lock_or_burn(&env, Address::generate(&env));
    client.preflight_check(&caller, &lob, &0u32, &Bytes::new(&env), &0i128);
}

#[test]
fn test_set_policy_engine_attaches_and_stores() {
    let (env, client, _owner) = setup();
    let (mock_addr, mock_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(mock_addr.clone()));
    assert_eq!(client.get_policy_engine(), Some(mock_addr));
    // EVM `_setPolicyEngine` calls `attach()` on the new engine.
    assert_eq!(mock_client.attach_count(), 1);
}

#[test]
fn test_set_policy_engine_is_owner_only() {
    let (env, client, _owner) = setup();
    let (mock_addr, _mock_client) = register_mock_engine(&env);
    env.mock_auths(&[]);
    let r = client.try_set_policy_engine(&Some(mock_addr));
    assert!(
        r.is_err(),
        "non-owner / unauthed set_policy_engine must fail"
    );
}

#[test]
fn test_set_policy_engine_swaps_detach_old_attach_new() {
    let (env, client, _owner) = setup();
    let (first, first_client) = register_mock_engine(&env);
    let (second, second_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(first.clone()));
    client.set_policy_engine(&Some(second.clone()));
    assert_eq!(client.get_policy_engine(), Some(second));
    assert_eq!(
        first_client.detach_count(),
        1,
        "old engine must be detached"
    );
    assert_eq!(
        second_client.attach_count(),
        1,
        "new engine must be attached"
    );
}

#[test]
fn test_set_policy_engine_none_detaches_and_disables() {
    let (env, client, _owner) = setup();
    let (mock_addr, mock_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(mock_addr));
    client.set_policy_engine(&None);
    assert_eq!(client.get_policy_engine(), None);
    assert_eq!(mock_client.detach_count(), 1);
}

#[test]
fn test_set_policy_engine_strict_reverts_on_detach_failure() {
    let (env, client, _owner) = setup();
    let (first, first_client) = register_mock_engine(&env);
    let (second, second_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(first.clone()));
    first_client.set_revert_on_detach(&true);

    // Strict path: old detach reverts -> PolicyEngineDetachReverted (#806), no swap.
    // Contract errors surface on the outer-Err side as `Err(Ok(CCIPError))`
    // (see lock-release-pool tests); `Ok(Err(_))` is a conversion failure.
    let r = client.try_set_policy_engine(&Some(second.clone()));
    match r {
        Ok(Ok(())) => panic!("strict set must not succeed when detach reverts"),
        Err(Ok(e)) => assert_eq!(e, CCIPError::PolicyEngineDetachReverted),
        Ok(Err(_)) => panic!("unexpected conversion error"),
        Err(Err(_)) => panic!("unexpected host error"),
    }
    assert_eq!(
        client.get_policy_engine(),
        Some(first),
        "engine must not swap"
    );
    assert_eq!(
        second_client.attach_count(),
        0,
        "new engine must not be attached"
    );
}

#[test]
fn test_force_set_policy_engine_tolerates_detach_failure() {
    let (env, client, _owner) = setup();
    let (first, _first_client) = register_mock_engine(&env);
    let (second, second_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(first.clone()));
    // Make the old engine's detach revert; the force path must proceed anyway.
    let first_client_for_flag = MockPolicyEngineContractClient::new(&env, &first);
    first_client_for_flag.set_revert_on_detach(&true);

    client.force_set_policy_engine(&Some(second.clone()));
    // A PolicyEngineDetachFailed event must be emitted (topic aph_PolicyEngineDetachFailed).
    // `env.events()` reflects only the most recent top-level call (see executor
    // contract gotcha), so read it IMMEDIATELY after the force call — before any
    // later client call clobbers the log — and filter to the hooks contract,
    // the same pattern as `upgraded_event_hash`.
    let detach_failed_topic = Symbol::new(&env, "aph_PolicyEngineDetachFailed");
    let mut saw_detach_failed = false;
    let evs = env.events().all().filter_by_contract(&client.address);
    for e in evs.events().iter() {
        let soroban_sdk::xdr::ContractEventBody::V0(ref v0) = e.body else {
            continue;
        };
        for t in v0.topics.iter() {
            let s: Symbol = t
                .clone()
                .try_into_val(&env)
                .unwrap_or(Symbol::new(&env, ""));
            if s == detach_failed_topic {
                saw_detach_failed = true;
            }
        }
    }
    assert!(
        saw_detach_failed,
        "force path must emit PolicyEngineDetachFailed"
    );
    assert_eq!(client.get_policy_engine(), Some(second));
    assert_eq!(second_client.attach_count(), 1);
}

fn release_or_mint(env: &Env) -> ReleaseOrMintIn {
    ReleaseOrMintIn {
        original_sender: Bytes::new(env),
        remote_chain_selector: REMOTE_CHAIN,
        receiver: Address::generate(env),
        amount: 100,
        local_token: Address::generate(env),
        source_pool_address: Bytes::new(env),
        source_pool_data: Bytes::new(env),
    }
}

#[test]
fn test_preflight_runs_engine_with_payload() {
    let (env, client, _owner) = setup();
    let (mock_addr, mock_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(mock_addr));
    let caller = authorize_caller(&client);
    let sender = Address::generate(&env);
    let lob = lock_or_burn(&env, sender.clone());
    let token_args = Bytes::from_slice(&env, &[0xaa, 0xbb]);

    client.preflight_check(&caller, &lob, &7u32, &token_args, &42i128);

    let payload = mock_client
        .last_payload()
        .expect("engine run must record payload");
    assert_eq!(payload.selector, Symbol::new(&env, "preflight_check"));
    assert_eq!(payload.sender, caller);
    assert_eq!(payload.context, token_args);
    match payload.data {
        PolicyData::PoolHooks(PoolHooksPayloadData::Preflight(p)) => {
            assert_eq!(p.amount_post_fee, 42);
            assert_eq!(p.requested_finality, 7);
            assert_eq!(p.lock_or_burn_in.original_sender, sender);
        }
        other => panic!("expected Preflight payload, got {:?}", other),
    }
}

#[test]
fn test_postflight_runs_engine_with_payload() {
    let (env, client, _owner) = setup();
    let (mock_addr, mock_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(mock_addr));
    let caller = authorize_caller(&client);
    let rom = release_or_mint(&env);

    client.postflight_check(&caller, &rom, &55i128, &9u32);

    let payload = mock_client
        .last_payload()
        .expect("engine run must record payload");
    assert_eq!(payload.selector, Symbol::new(&env, "postflight_check"));
    assert_eq!(payload.sender, caller);
    // offchain_token_data is unused in v2+ -> empty context.
    assert_eq!(payload.context, Bytes::new(&env));
    match payload.data {
        PolicyData::PoolHooks(PoolHooksPayloadData::Postflight(p)) => {
            assert_eq!(p.local_amount, 55);
            assert_eq!(p.requested_finality, 9);
            assert_eq!(p.release_or_mint_in.remote_chain_selector, REMOTE_CHAIN);
        }
        other => panic!("expected Postflight payload, got {:?}", other),
    }
}

#[test]
#[should_panic(expected = "Error(Contract, #3)")] // Unauthorized -> policy rejection
fn test_preflight_blocks_when_engine_rejects() {
    let (env, client, _owner) = setup();
    let (mock_addr, mock_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(mock_addr));
    mock_client.set_revert_on_run(&true);
    let caller = authorize_caller(&client);
    let lob = lock_or_burn(&env, Address::generate(&env));
    // Non-try preflight panics when the engine rejects -> transfer blocked.
    client.preflight_check(&caller, &lob, &0u32, &Bytes::new(&env), &0i128);
}

#[test]
#[should_panic(expected = "Error(Contract, #3)")] // Unauthorized -> policy rejection
fn test_postflight_blocks_when_engine_rejects() {
    let (env, client, _owner) = setup();
    let (mock_addr, mock_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(mock_addr));
    mock_client.set_revert_on_run(&true);
    let caller = authorize_caller(&client);
    let rom = release_or_mint(&env);
    client.postflight_check(&caller, &rom, &0i128, &0u32);
}

#[test]
fn test_set_policy_engine_same_value_noop() {
    // EVM `test_setPolicyEngine_SameValue`: setting the same engine again is a
    // no-op — no detach, no re-attach, and no events emitted.
    let (env, client, _owner) = setup();
    let (mock_addr, mock_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(mock_addr.clone()));

    // Re-set the same value. `env.events()` reflects only the most recent
    // top-level call (see the force-detach test), so read the event log
    // IMMEDIATELY after this call, before any other client call clobbers it.
    // A no-op must emit NO events at all — not just no attach event, or a
    // buggy detach-then-fail path (detach + PolicyEngineDetachFailed) would
    // still pass.
    client.set_policy_engine(&Some(mock_addr.clone()));
    let mut saw_any = false;
    let evs = env.events().all().filter_by_contract(&client.address);
    for e in evs.events().iter() {
        if let soroban_sdk::xdr::ContractEventBody::V0(ref _v0) = e.body {
            saw_any = true;
        }
    }
    assert!(!saw_any, "same-value set must emit no events at all");
    // No re-attach and no detach: both engine call counters stay at their
    // post-initial-set values (attach 1, detach 0).
    assert_eq!(mock_client.attach_count(), 1);
    assert_eq!(mock_client.detach_count(), 0);
}

#[test]
fn test_set_policy_engine_reverts_when_old_engine_has_no_detach() {
    // EVM `test_setPolicyEngine_RevertWhen_OldEngineDoesNotImplementDetach`:
    // an "engine" without a `detach` function fails the detach on the strict
    // path — the Soroban `try_detach` invoke error counts as detach failure —
    // so `set_policy_engine` reverts `PolicyEngineDetachReverted` (#806) and
    // the engine is not swapped.
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let id = env.register(AdvancedPoolHooksContract, ());
    let client = AdvancedPoolHooksContractClient::new(&env, &id);
    client.initialize(&owner, &Vec::new(&env), &0i128, &Vec::new(&env), &None);

    let no_detach_addr = env.register(NoDetachEngineContract, ());
    let (real_addr, real_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(no_detach_addr.clone()));

    let r = client.try_set_policy_engine(&Some(real_addr.clone()));
    match r {
        Ok(Ok(())) => panic!("set must not succeed when the old engine has no detach"),
        Err(Ok(e)) => assert_eq!(e, CCIPError::PolicyEngineDetachReverted),
        Ok(Err(_)) => panic!("unexpected conversion error"),
        Err(Err(_)) => panic!("unexpected host error"),
    }
    assert_eq!(
        client.get_policy_engine(),
        Some(no_detach_addr),
        "engine must not swap"
    );
    assert_eq!(
        real_client.attach_count(),
        0,
        "new engine must not be attached"
    );
}

/// A mock policy engine that implements the ABI **except** `detach` — standing
/// in for EVM `MockPolicyEngineNoDetach` on the strict-detach path above.
#[contract]
pub struct NoDetachEngineContract;

#[contractimpl]
impl NoDetachEngineContract {
    pub fn attach(env: Env, target: Address) -> Result<(), CCIPError> {
        target.require_auth();
        let _ = &env; // no bookkeeping needed — the hooks only checks success
        Ok(())
    }

    pub fn run(env: Env, target: Address, _payload: Payload) -> Result<(), CCIPError> {
        target.require_auth();
        let _ = &env; // no bookkeeping needed — the hooks only checks success
        Ok(())
    }

    pub fn check(_env: Env, _target: Address, _payload: Payload) -> Result<(), CCIPError> {
        Ok(())
    }

    pub fn type_and_version(env: Env) -> String {
        String::from_str(&env, "NoDetachEngine 1.0.0")
    }
}

#[test]
fn test_preflight_allowlist_and_policy_engine() {
    // EVM `test_preflightCheck_AllowListAndPolicyEngine`: allowlist enabled AND
    // an engine attached — the allowlisted sender passes both gates and the
    // engine records the payload.
    let (env, client, _owner, allowlist) = setup_with_allowlist(1);
    let (mock_addr, mock_client) = register_mock_engine(&env);
    client.set_policy_engine(&Some(mock_addr));
    let caller = authorize_caller(&client);
    let allowed = allowlist.get(0).unwrap();
    let lob = lock_or_burn(&env, allowed.clone());
    let token_args = Bytes::from_slice(&env, &[0x11, 0x22]);

    client.preflight_check(&caller, &lob, &0u32, &token_args, &0i128);

    let payload = mock_client
        .last_payload()
        .expect("engine run must record payload");
    assert_eq!(payload.selector, Symbol::new(&env, "preflight_check"));
    assert_eq!(payload.sender, caller);
    assert_eq!(payload.context, token_args);
}

#[test]
fn test_policy_engine_check_round_trips() {
    // `PolicyEngineClient::check` (EVM `IPolicyEngine.check`): offchain
    // pre-validation — Ok when the run would pass, Err when it would reject.
    // The target is passed explicitly (EVM: tooling simulates `check` with
    // `from = target`, since the engine keys policies by its `msg.sender`).
    let (env, hooks_client, _owner) = setup();
    let (mock_addr, mock_client) = register_mock_engine(&env);
    let engine_client = common_interfaces::policy_engine::PolicyEngineClient::new(&env, &mock_addr);
    let payload = Payload {
        selector: Symbol::new(&env, "preflight_check"),
        sender: Address::generate(&env),
        data: PolicyData::PoolHooks(PoolHooksPayloadData::Preflight(PreflightPayload {
            lock_or_burn_in: lock_or_burn(&env, Address::generate(&env)),
            requested_finality: 0,
            amount_post_fee: 0,
        })),
        context: Bytes::new(&env),
    };

    engine_client.check(&hooks_client.address, &payload);

    mock_client.set_revert_on_run(&true);
    let r = engine_client.try_check(&hooks_client.address, &payload);
    match r {
        Ok(Ok(())) => panic!("check must not pass when the run would reject"),
        Err(Ok(e)) => assert_eq!(e, CCIPError::Unauthorized),
        Ok(Err(_)) => panic!("unexpected conversion error"),
        Err(Err(_)) => panic!("unexpected host error"),
    }
}
