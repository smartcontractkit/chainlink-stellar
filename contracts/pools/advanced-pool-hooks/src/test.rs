#![cfg(test)]

use soroban_sdk::{testutils::Address as _, testutils::Events as _, vec, Address, Bytes, Env, Vec};

use crate::types::CCVConfigArg;
use crate::{AdvancedPoolHooksContract, AdvancedPoolHooksContractClient};
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
    client.initialize(&owner, &Vec::new(&env), &0i128, &Vec::new(&env));

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
    client.initialize(&owner, &allowlist, &0i128, &Vec::new(&env));

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
// postflight (deferred policy -> no-op)
// ============================================================

#[test]
fn test_postflight_is_noop() {
    let (env, client, _owner) = setup();
    let caller = authorize_caller(&client);
    // Non-try call panics on Err; reaching the end means Ok (no-op).
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
fn test_initialize_seeds_authorized_callers_with_dedup_and_zero_skip() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let real = Address::generate(&env);
    let pool = Address::generate(&env);
    // [zero, real, real, pool] -> stored = [real, pool] (zero skipped, dup collapsed).
    let authorized = vec![
        &env,
        zero_addr(&env),
        real.clone(),
        real.clone(),
        pool.clone(),
    ];
    let id = env.register(AdvancedPoolHooksContract, ());
    let client = AdvancedPoolHooksContractClient::new(&env, &id);
    client.initialize(&owner, &Vec::new(&env), &0i128, &authorized);

    let stored = client.get_all_authorized_callers();
    assert_eq!(stored.len(), 2);
    assert_eq!(stored.get(0).unwrap(), real);
    assert_eq!(stored.get(1).unwrap(), pool);
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
    client.initialize(&owner, &allowlist, &0i128, &Vec::new(&env));

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
