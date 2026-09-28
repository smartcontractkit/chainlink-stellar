//! Advanced pool hooks — Stellar analogue of EVM
//! [`AdvancedPoolHooks`](https://github.com/smartcontractkit/chainlink-ccip/blob/develop/chains/evm/contracts/pools/AdvancedPoolHooks.sol).
//!
//! An optional external contract a token pool points at via
//! `set_advanced_pool_hooks`. It lets a token issuer persist and edit, per
//! remote chain, the cross-chain verifier (CCV) choice that the pool advertises
//! through `get_required_ccvs`, plus an optional sender allowlist enforced in
//! `preflight_check`. This is the contract that closes the CCV-7 gap: the pool
//! layer that *delegates* to hooks already ships, but until now no production
//! hooks contract existed on Stellar for an issuer to actually configure.
//!
//! The three `PoolHooksInterface` methods (`preflight_check`,
//! `postflight_check`, `get_required_ccvs`) are exposed with signatures
//! matching the hand-written `common_interfaces::pool_hooks::PoolHooksInterface`
//! verbatim, so the pool's `PoolHooksClient` can invoke them cross-contract.
//! They are declared as free functions rather than a formal `impl
//! PoolHooksInterface` because that trait carries only `#[contractclient]`, not
//! `#[contracttrait]` — the same ABI-match approach used by the pool test mocks.
//!
//! # Parity status vs EVM `AdvancedPoolHooks.sol`
//!
//! Shipped now:
//! - Sender allowlist with an immutable enable flag set at `initialize` (EVM
//!   `i_allowlistEnabled` + `s_allowlist`), checked in `preflight_check` via
//!   `check_allowlist`.
//! - Per-chain CCV config storage + `apply_ccv_config_updates` (owner-only),
//!   with the full four-list per-direction shape and EVM-equivalent validation.
//! - Threshold-amount mechanism: `set_threshold_amount` + base/threshold CCV
//!   resolution in `get_required_ccvs`.
//! - `get_ccv_config` / `get_all_ccv_configs` readers.
//!
//! Deferred parity gaps (documented):
//! - **Policy engine** — EVM runs `IPolicyEngine.run` in both preflight and
//!   postflight. `preflight_check` is allowlist-only and `postflight_check` is
//!   a no-op here; there is no `set_policy_engine` surface yet.
//!
//! Shipped (EVM parity):
//! - **Authorized-callers invocation gating** — EVM `AdvancedPoolHooks extends
//!   AuthorizedCallers` and calls `_validateCaller()` at the top of preflight
//!   and postflight so only the configured pools may invoke them. Soroban has no
//!   `msg.sender`, so the caller is passed explicitly as the first argument of
//!   `preflight_check`/`postflight_check`: the hooks call `caller.require_auth()`
//!   (proving `caller` is in the call chain) and then check membership in the
//!   stored `authorized_callers` set, reverting `CallerNotAuthorized` otherwise.
//!   This mirrors the pool's own `require_authorized_onramp`/`require_authorized_offramp`
//!   pattern. The pool passes its own address (`env.current_contract_address()`)
//!   as `caller`; a direct unconfigured caller cannot satisfy both the auth and
//!   the set check. `get_required_ccvs` stays ungated, as on EVM. The
//!   `authorized_callers` set is seeded at `initialize` and edited owner-only via
//!   `apply_authorized_callers_updates` (EVM `applyAuthorizedCallerUpdates`).
//!
//! Diverged (Soroban cannot support faithfully):
//! - **`address(0)` sentinel** — `include_defaults: bool` replaces EVM's
//!   `address(0)` sentinel throughout, since Soroban `Address` has no zero form.
#![no_std]

mod events;
pub mod types;

pub use types::{CCVConfig, CCVConfigArg};

use common_authorization::{Ownable, Upgradeable};
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_helpers::validation::Validatable;
use common_interfaces::token_pool::{
    LockOrBurnIn, MessageDirection, PoolRequiredCCVs, ReleaseOrMintIn,
};
use soroban_sdk::{contract, contractimpl, symbol_short, Address, BytesN, Env, Map, Symbol, Vec};

// ============================================================
// Storage Keys
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
/// Allowed original-sender addresses (EVM `s_allowlist`).
const ALLOWLIST: Symbol = symbol_short!("ALWLIST");
/// Immutable enable flag for the sender allowlist (EVM `i_allowlistEnabled`),
/// derived at `initialize` from whether a non-empty allowlist was supplied.
const ALLOWLIST_ENABLED: Symbol = symbol_short!("ALWENBL");
/// Token-transfer amount at which above-threshold CCVs become required
/// (EVM `s_thresholdAmountForAdditionalCCVs`). 0 means no threshold.
const THRESHOLD_AMOUNT: Symbol = symbol_short!("THRESH");
/// Per-remote-chain CCV config (EVM `s_verifierConfig`).
const VERIFIER_CONFIG: Symbol = symbol_short!("VRFCONF");
/// Authorized hook invokers — the set of pools allowed to call
/// `preflight_check`/`postflight_check` (EVM `AuthorizedCallers.s_authorizedCallers`).
const AUTHORIZED_CALLERS: Symbol = symbol_short!("AUTHCALL");

// ============================================================
// Contract
// ============================================================

#[contract]
pub struct AdvancedPoolHooksContract;

#[contractimpl]
impl Initializable for AdvancedPoolHooksContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for AdvancedPoolHooksContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl(contracttrait)]
impl Upgradeable for AdvancedPoolHooksContract {}

#[contractimpl]
impl AdvancedPoolHooksContract {
    /// Initializes the hooks with `owner`, an optional sender `allowlist`, the
    /// initial `threshold_amount`, and the initial `authorized_callers` set (EVM
    /// constructor `AuthorizedCallers(authorizedCallers)`). The allowlist is
    /// enabled iff a non-empty list is supplied (EVM constructor
    /// `i_allowlistEnabled = allowlist.length > 0`); the flag is immutable
    /// thereafter. Zero-account entries are skipped and duplicates collapsed in
    /// both the allowlist and the authorized-callers set, matching EVM
    /// `_applyAllowListUpdates` / `_applyAuthorizedCallerUpdates` bookkeeping.
    pub fn initialize(
        env: Env,
        owner: Address,
        allowlist: Vec<Address>,
        threshold_amount: i128,
        authorized_callers: Vec<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;

        let allowlist_enabled = !allowlist.is_empty();
        let mut stored: Vec<Address> = Vec::new(&env);
        if allowlist_enabled {
            for i in 0..allowlist.len() {
                if let Some(sender) = allowlist.get(i) {
                    if is_zero_account(&env, &sender) {
                        continue;
                    }
                    if !contains(&stored, &sender) {
                        stored.push_back(sender.clone());
                        events::AllowListAddEvent {
                            sender: sender.clone(),
                        }
                        .publish(&env);
                    }
                }
            }
        }

        env.storage().instance().set(&ALLOWLIST, &stored);
        env.storage()
            .instance()
            .set(&ALLOWLIST_ENABLED, &allowlist_enabled);
        env.storage()
            .instance()
            .set(&THRESHOLD_AMOUNT, &threshold_amount);
        env.storage()
            .instance()
            .set(&VERIFIER_CONFIG, &Map::<u64, CCVConfig>::new(&env));

        // Seed the authorized-callers set (EVM constructor →
        // `_applyAuthorizedCallerUpdates({added: authorizedCallers, removed: []})`).
        let mut auth: Vec<Address> = Vec::new(&env);
        for i in 0..authorized_callers.len() {
            if let Some(caller) = authorized_callers.get(i) {
                if is_zero_account(&env, &caller) {
                    continue;
                }
                if !contains(&auth, &caller) {
                    auth.push_back(caller.clone());
                    events::AuthorizedCallerAddedEvent {
                        caller: caller.clone(),
                    }
                    .publish(&env);
                }
            }
        }
        env.storage().instance().set(&AUTHORIZED_CALLERS, &auth);

        events::ThresholdAmountSetEvent { threshold_amount }.publish(&env);
        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "AdvancedPoolHooks 2.0.0-dev")
    }

    // ========================================
    // CCV config
    // ========================================

    /// Owner-only batch upsert of per-chain CCV config (EVM
    /// `applyCCVConfigUpdates`). Each entry is validated before storage. A
    /// config with no base CCVs in either direction removes the chain's entry,
    /// matching EVM's `s_configuredChainSelectors` bookkeeping.
    pub fn apply_ccv_config_updates(env: Env, configs: Vec<CCVConfigArg>) -> Result<(), CCIPError> {
        <Self as Ownable>::require_owner(&env)?;

        let mut map: Map<u64, CCVConfig> = env
            .storage()
            .instance()
            .get(&VERIFIER_CONFIG)
            .unwrap_or(Map::new(&env));

        for i in 0..configs.len() {
            if let Some(arg) = configs.get(i) {
                arg.validate()?;
                let cfg = CCVConfig {
                    outbound_ccvs: arg.outbound_ccvs.clone(),
                    threshold_outbound_ccvs: arg.threshold_outbound_ccvs.clone(),
                    inbound_ccvs: arg.inbound_ccvs.clone(),
                    threshold_inbound_ccvs: arg.threshold_inbound_ccvs.clone(),
                    outbound_include_defaults: arg.outbound_include_defaults,
                    inbound_include_defaults: arg.inbound_include_defaults,
                };

                // EVM keeps the entry when the per-chain config has any base
                // contribution in either direction, where `address(0)` counts as a
                // base contribution (AdvancedPoolHooks.sol#L285). `include_defaults`
                // is Stellar's `address(0)` analogue, so a direction with
                // `include_defaults: true` and an empty list still counts as having
                // a base — without this, a defaults-only config would be silently
                // dropped by `map.remove`.
                let has_outbound = !cfg.outbound_ccvs.is_empty() || cfg.outbound_include_defaults;
                let has_inbound = !cfg.inbound_ccvs.is_empty() || cfg.inbound_include_defaults;
                if has_outbound || has_inbound {
                    map.set(arg.remote_chain_selector, cfg.clone());
                } else {
                    map.remove(arg.remote_chain_selector);
                }

                // EVM emits `CCVConfigUpdated` unconditionally — outside the
                // add/remove branch (AdvancedPoolHooks.sol#L291) — so a removal
                // (empty base) emits too, carrying the emptied config. Mirrored
                // here: the event fires for both the set and the remove.
                events::CCVConfigUpdatedEvent {
                    remote_chain_selector: arg.remote_chain_selector,
                    config: cfg,
                }
                .publish(&env);
            }
        }

        env.storage().instance().set(&VERIFIER_CONFIG, &map);
        Ok(())
    }

    /// Returns the CCV config for a remote chain, or `None` if unconfigured
    /// (EVM `getCCVConfig`, which returns an empty struct instead of `None`).
    pub fn get_ccv_config(env: Env, remote_chain_selector: u64) -> Option<CCVConfig> {
        let map: Map<u64, CCVConfig> = env
            .storage()
            .instance()
            .get(&VERIFIER_CONFIG)
            .unwrap_or(Map::new(&env));
        map.get(remote_chain_selector)
    }

    /// Returns all configured CCV configs (EVM `getAllCCVConfigs`).
    pub fn get_all_ccv_configs(env: Env) -> Vec<CCVConfigArg> {
        let map: Map<u64, CCVConfig> = env
            .storage()
            .instance()
            .get(&VERIFIER_CONFIG)
            .unwrap_or(Map::new(&env));
        let mut out = Vec::new(&env);
        for (selector, cfg) in map.iter() {
            out.push_back(CCVConfigArg {
                remote_chain_selector: selector,
                outbound_ccvs: cfg.outbound_ccvs.clone(),
                threshold_outbound_ccvs: cfg.threshold_outbound_ccvs.clone(),
                inbound_ccvs: cfg.inbound_ccvs.clone(),
                threshold_inbound_ccvs: cfg.threshold_inbound_ccvs.clone(),
                outbound_include_defaults: cfg.outbound_include_defaults,
                inbound_include_defaults: cfg.inbound_include_defaults,
            });
        }
        out
    }

    // ========================================
    // Threshold amount
    // ========================================

    /// Owner-only set of the threshold above which additional CCVs are required
    /// (EVM `setThresholdAmount`).
    pub fn set_threshold_amount(env: Env, threshold_amount: i128) -> Result<(), CCIPError> {
        <Self as Ownable>::require_owner(&env)?;
        env.storage()
            .instance()
            .set(&THRESHOLD_AMOUNT, &threshold_amount);
        events::ThresholdAmountSetEvent { threshold_amount }.publish(&env);
        Ok(())
    }

    /// Returns the threshold amount (EVM `getThresholdAmount`); 0 means none.
    pub fn get_threshold_amount(env: Env) -> i128 {
        env.storage().instance().get(&THRESHOLD_AMOUNT).unwrap_or(0)
    }

    // ========================================
    // Sender allowlist
    // ========================================

    /// Returns whether the allowlist is enabled (EVM `getAllowListEnabled`).
    pub fn get_allowlist_enabled(env: Env) -> bool {
        env.storage()
            .instance()
            .get(&ALLOWLIST_ENABLED)
            .unwrap_or(false)
    }

    /// Returns the allowed senders (EVM `getAllowList`).
    pub fn get_allowlist(env: Env) -> Vec<Address> {
        env.storage()
            .instance()
            .get(&ALLOWLIST)
            .unwrap_or(Vec::new(&env))
    }

    /// Owner-only update of the allowlist contents (EVM `applyAllowListUpdates`).
    /// Reverts `FeatureNotEnabled` if the allowlist was disabled at `initialize`,
    /// matching EVM `AllowListNotEnabled`. Removals first, then validated adds
    /// (zero-account skipped, duplicates collapsed).
    pub fn apply_allowlist_updates(
        env: Env,
        removes: Vec<Address>,
        adds: Vec<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Ownable>::require_owner(&env)?;

        if !Self::get_allowlist_enabled(env.clone()) {
            return Err(CCIPError::FeatureNotEnabled);
        }

        let mut allow: Vec<Address> = env
            .storage()
            .instance()
            .get(&ALLOWLIST)
            .unwrap_or(Vec::new(&env));

        for i in 0..removes.len() {
            if let Some(to_remove) = removes.get(i) {
                let mut filtered = Vec::new(&env);
                let mut removed = false;
                for j in 0..allow.len() {
                    if let Some(addr) = allow.get(j) {
                        if addr == to_remove {
                            removed = true;
                        } else {
                            filtered.push_back(addr);
                        }
                    }
                }
                if removed {
                    events::AllowListRemoveEvent {
                        sender: to_remove.clone(),
                    }
                    .publish(&env);
                }
                allow = filtered;
            }
        }

        for i in 0..adds.len() {
            if let Some(to_add) = adds.get(i) {
                if is_zero_account(&env, &to_add) {
                    continue;
                }
                if !contains(&allow, &to_add) {
                    allow.push_back(to_add.clone());
                    events::AllowListAddEvent {
                        sender: to_add.clone(),
                    }
                    .publish(&env);
                }
            }
        }

        env.storage().instance().set(&ALLOWLIST, &allow);
        Ok(())
    }

    // ========================================
    // Authorized callers (EVM `AuthorizedCallers`)
    // ========================================

    /// Returns all authorized callers (EVM `getAllAuthorizedCallers`).
    pub fn get_all_authorized_callers(env: Env) -> Vec<Address> {
        env.storage()
            .instance()
            .get(&AUTHORIZED_CALLERS)
            .unwrap_or(Vec::new(&env))
    }

    /// Owner-only batch update of the authorized-callers set (EVM
    /// `applyAuthorizedCallerUpdates`). Removals are applied first, then adds.
    /// Zero-account adds are skipped and duplicate adds collapse (no-op), as the
    /// stored set is membership-based; removals of absent callers are no-ops.
    /// `AuthorizedCallerAdded`/`AuthorizedCallerRemoved` fire only for entries
    /// actually added/removed.
    pub fn apply_authorized_callers_updates(
        env: Env,
        removes: Vec<Address>,
        adds: Vec<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Ownable>::require_owner(&env)?;

        let mut auth: Vec<Address> = env
            .storage()
            .instance()
            .get(&AUTHORIZED_CALLERS)
            .unwrap_or(Vec::new(&env));

        for i in 0..removes.len() {
            if let Some(to_remove) = removes.get(i) {
                let mut filtered = Vec::new(&env);
                let mut removed = false;
                for j in 0..auth.len() {
                    if let Some(addr) = auth.get(j) {
                        if addr == to_remove {
                            removed = true;
                        } else {
                            filtered.push_back(addr);
                        }
                    }
                }
                if removed {
                    events::AuthorizedCallerRemovedEvent {
                        caller: to_remove.clone(),
                    }
                    .publish(&env);
                }
                auth = filtered;
            }
        }

        for i in 0..adds.len() {
            if let Some(to_add) = adds.get(i) {
                if is_zero_account(&env, &to_add) {
                    continue;
                }
                if !contains(&auth, &to_add) {
                    auth.push_back(to_add.clone());
                    events::AuthorizedCallerAddedEvent {
                        caller: to_add.clone(),
                    }
                    .publish(&env);
                }
            }
        }

        env.storage().instance().set(&AUTHORIZED_CALLERS, &auth);
        Ok(())
    }

    // ========================================
    // PoolHooksInterface (ABI-matched free functions)
    // ========================================

    /// Returns the required CCVs for a transfer in `direction`
    /// (EVM `getRequiredCCVs`). Base CCVs are always returned; above-threshold
    /// CCVs are appended when `amount >= threshold_amount` and a threshold list
    /// is configured. `include_defaults` reflects the per-direction flag stored
    /// on the config, replacing EVM's `address(0)` sentinel.
    ///
    /// A chain with no stored config yields `{ ccvs: [], include_defaults: true
    /// }`, matching the pool's own no-hooks default so wiring an unconfigured
    /// hooks contract is equivalent to having none.
    pub fn get_required_ccvs(
        env: Env,
        _local_token: Address,
        remote_chain_selector: u64,
        amount: i128,
        _requested_finality: u32,
        _extra_data: soroban_sdk::Bytes,
        direction: MessageDirection,
    ) -> PoolRequiredCCVs {
        let map: Map<u64, CCVConfig> = env
            .storage()
            .instance()
            .get(&VERIFIER_CONFIG)
            .unwrap_or(Map::new(&env));
        let cfg = map
            .get(remote_chain_selector)
            .unwrap_or_else(|| CCVConfig::empty(&env));

        let (base, threshold, include_defaults) = match direction {
            MessageDirection::Outbound => (
                cfg.outbound_ccvs,
                cfg.threshold_outbound_ccvs,
                cfg.outbound_include_defaults,
            ),
            MessageDirection::Inbound => (
                cfg.inbound_ccvs,
                cfg.threshold_inbound_ccvs,
                cfg.inbound_include_defaults,
            ),
        };

        let threshold_amount = Self::get_threshold_amount(env.clone());
        let ccvs = resolve_required_ccvs(&env, &base, &threshold, amount, threshold_amount);
        PoolRequiredCCVs {
            ccvs,
            include_defaults,
        }
    }

    /// Outbound preflight (EVM `preflightCheck`). First validates the caller
    /// against the `authorized_callers` set (EVM `_validateCaller`), then performs
    /// the sender allowlist check; policy-engine validation is deferred. `caller`
    /// is the invoking pool's address — the hooks require it to authenticate and
    /// be a member of the authorized set.
    pub fn preflight_check(
        env: Env,
        caller: Address,
        lock_or_burn_in: LockOrBurnIn,
        _requested_finality: u32,
        _token_args: soroban_sdk::Bytes,
        _amount: i128,
    ) -> Result<(), CCIPError> {
        Self::require_authorized_caller(&env, &caller)?;
        if Self::get_allowlist_enabled(env.clone()) {
            let allow: Vec<Address> = env
                .storage()
                .instance()
                .get(&ALLOWLIST)
                .unwrap_or(Vec::new(&env));
            if !contains(&allow, &lock_or_burn_in.original_sender) {
                return Err(CCIPError::SenderNotAllowed);
            }
        }
        Ok(())
    }

    /// Inbound postflight (EVM `postflightCheck`). First validates the caller
    /// against the `authorized_callers` set (EVM `_validateCaller`); the body is a
    /// no-op (policy-engine validation deferred).
    pub fn postflight_check(
        env: Env,
        caller: Address,
        _release_or_mint_in: ReleaseOrMintIn,
        _local_amount: i128,
        _requested_finality: u32,
    ) -> Result<(), CCIPError> {
        Self::require_authorized_caller(&env, &caller)?;
        Ok(())
    }
}

// ============================================================
// Helpers
// ============================================================

impl AdvancedPoolHooksContract {
    /// EVM `AuthorizedCallers._validateCaller` analogue. `caller.require_auth()`
    /// proves `caller` authenticated (is in the call chain — the invoking pool
    /// passes its own address, so this succeeds for a genuine pool call and
    /// fails for a forged direct caller that cannot authorize the pool's
    /// address). Membership in the stored `authorized_callers` set is then
    /// required, reverting `CallerNotAuthorized` otherwise. Mirrors the pool's
    /// own `require_authorized_onramp`/`require_authorized_offramp` pattern.
    fn require_authorized_caller(env: &Env, caller: &Address) -> Result<(), CCIPError> {
        caller.require_auth();
        let auth: Vec<Address> = env
            .storage()
            .instance()
            .get(&AUTHORIZED_CALLERS)
            .unwrap_or(Vec::new(env));
        if !contains(&auth, caller) {
            return Err(CCIPError::CallerNotAuthorized);
        }
        Ok(())
    }
}

/// True iff `addr` is the zero Stellar account (EVM `address(0)` parity for
/// allowlist-entry rejection). Mirrors `executor::is_zero_fee_recipient`.
fn is_zero_account(env: &Env, addr: &Address) -> bool {
    addr == &Address::from_str(
        env,
        "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
    )
}

/// Linear membership test (Soroban `Vec` has no `contains`).
fn contains(haystack: &Vec<Address>, needle: &Address) -> bool {
    for i in 0..haystack.len() {
        if let Some(addr) = haystack.get(i) {
            if addr == *needle {
                return true;
            }
        }
    }
    false
}

/// Mirrors EVM `_resolveRequiredCCVs`: when the amount is at or above the
/// threshold and a threshold list is configured, combine base + threshold;
/// otherwise return just the base list.
fn resolve_required_ccvs(
    env: &Env,
    base: &Vec<Address>,
    threshold: &Vec<Address>,
    amount: i128,
    threshold_amount: i128,
) -> Vec<Address> {
    if threshold_amount != 0 && amount >= threshold_amount && !threshold.is_empty() {
        let mut out = Vec::new(env);
        for i in 0..base.len() {
            if let Some(addr) = base.get(i) {
                out.push_back(addr);
            }
        }
        for i in 0..threshold.len() {
            if let Some(addr) = threshold.get(i) {
                out.push_back(addr);
            }
        }
        return out;
    }
    base.clone()
}

mod test;
