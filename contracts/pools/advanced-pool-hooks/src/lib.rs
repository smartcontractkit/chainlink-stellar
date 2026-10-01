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
//! - **Policy engine** — the full EVM `s_policyEngine` surface: the engine is
//!   set/cleared owner-only via `set_policy_engine` (strict) or
//!   `force_set_policy_engine` (tolerating a reverting old-engine detach, EVM
//!   `setPolicyEngineAllowFailedDetach`), read back via `get_policy_engine`,
//!   and run with the preflight/postflight payload in both `preflight_check`
//!   and `postflight_check` (EVM `IPolicyEngine.run`). Dormant when unset, as
//!   on EVM `address(0)`. No engine contract exists on Stellar yet — these
//!   calls target the `PolicyEngineInterface` seam
//!   (`common_interfaces::policy_engine`), ready to be pointed at a real
//!   engine once one ships.
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
//! - **Policy-engine detach reason bytes** — EVM's
//!   `PolicyEngineDetachReverted(engine, err)` / `PolicyEngineDetachFailed(engine,
//!   reason)` carry the revert reason; Soroban's typed `try_` call API does not
//!   surface it cheaply, so the error/event carry only the engine address.
//! - **Postflight policy context** — EVM passes `releaseOrMintIn.offchainTokenData`
//!   as the run context; that field is unused in v2+ (always empty), and
//!   `ReleaseOrMintIn` here has no such field, so empty `Bytes` is passed.
#![no_std]

mod events;
pub mod types;

pub use types::{CCVConfig, CCVConfigArg};

use common_authorization::{Ownable, Upgradeable};
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_helpers::validation::{is_zero_address, Validatable};
use common_interfaces::policy_engine::{Payload, PolicyData, PolicyEngineClient};
use common_interfaces::pool_hooks::{PoolHooksPayloadData, PostflightPayload, PreflightPayload};
use common_interfaces::token_pool::{
    LockOrBurnIn, MessageDirection, PoolRequiredCCVs, ReleaseOrMintIn,
};
use soroban_sdk::{
    contract, contractimpl, symbol_short, Address, Bytes, BytesN, Env, Map, Symbol, Vec,
};

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
/// The policy engine attached to these hooks, or `None` to disable policy
/// checks (EVM `s_policyEngine`; `address(0)` => disabled). `None` is the
/// dormant default — the `run` calls in preflight/postflight short-circuit,
/// matching EVM's `if (address(policyEngine) == address(0)) return;`.
const POLICY_ENGINE: Symbol = symbol_short!("POLENG");

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
    /// initial `threshold_amount`, the initial `authorized_callers` set (EVM
    /// constructor `AuthorizedCallers(authorizedCallers)`), and an optional
    /// `policy_engine` (EVM constructor `policyEngine` arg). The allowlist is
    /// enabled iff a non-empty list is supplied (EVM constructor
    /// `i_allowlistEnabled = allowlist.length > 0`); the flag is immutable
    /// thereafter. Zero-account entries are skipped and duplicates collapsed in
    /// both the allowlist and the authorized-callers set, matching EVM
    /// `_applyAllowListUpdates` / `_applyAuthorizedCallerUpdates` bookkeeping.
    /// `policy_engine = None` leaves policy checks dormant (EVM `address(0)`).
    pub fn initialize(
        env: Env,
        owner: Address,
        allowlist: Vec<Address>,
        threshold_amount: i128,
        authorized_callers: Vec<Address>,
        policy_engine: Option<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;

        let allowlist_enabled = !allowlist.is_empty();
        let mut stored: Vec<Address> = Vec::new(&env);
        if allowlist_enabled {
            for i in 0..allowlist.len() {
                if let Some(sender) = allowlist.get(i) {
                    if is_zero_address(&env, &sender) {
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
        // A zero-account entry reverts `ZeroAddressNotAllowed`, matching EVM's
        // `_applyAuthorizedCallerUpdates` (unlike the allowlist seeding above,
        // which skips zeros per `_applyAllowListUpdates`). Duplicates collapse.
        let mut auth: Vec<Address> = Vec::new(&env);
        for i in 0..authorized_callers.len() {
            if let Some(caller) = authorized_callers.get(i) {
                if is_zero_address(&env, &caller) {
                    return Err(CCIPError::ZeroAddressNotAllowed);
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

        // Attach the initial policy engine if supplied (EVM constructor
        // `_setPolicyEngine(policyEngine, false)`). `None` => dormant.
        Self::set_policy_engine_impl(&env, &policy_engine, false)?;

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

    /// Checks if `sender` is allowed to perform an operation (EVM `checkAllowList`).
    /// A no-op when the allowlist is disabled; otherwise reverts `SenderNotAllowed`
    /// iff `sender` is not in the stored allowlist. This is the same check
    /// `preflight_check` applies to `lock_or_burn_in.original_sender`, exposed as a
    /// public view so callers can pre-validate a sender without invoking preflight.
    pub fn check_allow_list(env: Env, sender: Address) -> Result<(), CCIPError> {
        Self::require_allowlisted(&env, &sender)
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
                if is_zero_address(&env, &to_add) {
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
    /// A zero-account add reverts `ZeroAddressNotAllowed` (EVM
    /// `_applyAuthorizedCallerUpdates` reverts `ZeroAddressNotAllowed` on a zero
    /// add — unlike the allowlist path, which skips zeros). Duplicate adds
    /// collapse (no-op), as the stored set is membership-based; removals of
    /// absent callers are no-ops. `AuthorizedCallerAdded`/`AuthorizedCallerRemoved`
    /// fire only for entries actually added/removed.
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
                if is_zero_address(&env, &to_add) {
                    return Err(CCIPError::ZeroAddressNotAllowed);
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
    // Policy engine (EVM `s_policyEngine` + `_setPolicyEngine`)
    // ========================================

    /// Returns the attached policy engine, or `None` if policy checks are
    /// disabled (EVM `getPolicyEngine`; `address(0)` => `None`).
    pub fn get_policy_engine(env: Env) -> Option<Address> {
        // Stored as `Option<Address>` (`Some` = enabled, `None`/absent = dormant,
        // EVM `address(0)`). Read it back as `Option<Address>`: `unwrap_or(None)`
        // collapses the outer "key absent" `None` and the inner "cleared" `None`
        // into a single dormant result.
        env.storage().instance().get(&POLICY_ENGINE).unwrap_or(None)
    }

    /// Owner-only. Sets a new policy engine, detaching the previous one and
    /// reverting if that detach reverts (EVM `setPolicyEngine`).
    pub fn set_policy_engine(
        env: Env,
        new_policy_engine: Option<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Ownable>::require_owner(&env)?;
        Self::set_policy_engine_impl(&env, &new_policy_engine, false)
    }

    /// Owner-only. Sets a new policy engine while tolerating a revert from the
    /// previous engine's `detach` — the escape hatch for an adversarial old
    /// engine whose `detach()` reverts (EVM `setPolicyEngineAllowFailedDetach`).
    /// Named `force_set_policy_engine` to fit Soroban's 32-char function-name cap.
    pub fn force_set_policy_engine(
        env: Env,
        new_policy_engine: Option<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Ownable>::require_owner(&env)?;
        Self::set_policy_engine_impl(&env, &new_policy_engine, true)
    }

    /// Mirrors EVM `_setPolicyEngine`. No-op when unchanged. Detaches the old
    /// engine (tolerating the revert iff `allow_failed_detach`), stores the new
    /// address, and attaches the new engine when present. The detach/attach
    /// calls use the generated `try_`/direct client methods: a returned
    /// `Err` or a host revert from `detach` is the "detach failed" branch; a
    /// `run`/`attach` revert propagates and aborts, as on EVM.
    fn set_policy_engine_impl(
        env: &Env,
        new_policy_engine: &Option<Address>,
        allow_failed_detach: bool,
    ) -> Result<(), CCIPError> {
        let old = Self::get_policy_engine(env.clone());
        if new_policy_engine == &old {
            return Ok(());
        }

        if let Some(old_addr) = old.clone() {
            let old_client = PolicyEngineClient::new(env, &old_addr);
            let self_addr = env.current_contract_address();
            // `try_detach` returns `Result<Result<(), CCIPEngine>, InvokeError>`:
            // `Ok(Ok(()))` is the only success path; a returned `Err` or a host
            // revert both count as "detach failed" (EVM `try/catch`).
            let detached = matches!(old_client.try_detach(&self_addr), Ok(Ok(())));
            if !detached {
                if !allow_failed_detach {
                    return Err(CCIPError::PolicyEngineDetachReverted);
                }
                events::PolicyEngineDetachFailedEvent {
                    policy_engine: old_addr,
                }
                .publish(env);
            }
        }

        env.storage()
            .instance()
            .set(&POLICY_ENGINE, new_policy_engine);

        if let Some(new_addr) = new_policy_engine {
            let new_client = PolicyEngineClient::new(env, new_addr);
            // Non-`try_` attach: a revert/Err aborts `set_policy_engine`, as on
            // EVM where `attach()` is outside the try/catch.
            new_client.attach(&env.current_contract_address());
        }

        events::PolicyEngineAttachedEvent {
            policy_engine: new_policy_engine.clone(),
        }
        .publish(env);
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
    /// the sender allowlist check, then runs the policy engine over the
    /// preflight payload if one is attached (EVM `policyEngine.run`). `caller`
    /// is the invoking pool's address — the hooks require it to authenticate and
    /// be a member of the authorized set. `amount_post_fee` is EVM
    /// `amountPostFee`; `token_args` is forwarded as the policy-engine `context`
    /// (EVM passes `tokenArgs` as `context`). With no engine attached the
    /// `run` is skipped — the dormant default (EVM `address(0)` short-circuit).
    pub fn preflight_check(
        env: Env,
        caller: Address,
        lock_or_burn_in: LockOrBurnIn,
        requested_finality: u32,
        token_args: soroban_sdk::Bytes,
        amount_post_fee: i128,
    ) -> Result<(), CCIPError> {
        Self::require_authorized_caller(&env, &caller)?;
        Self::require_allowlisted(&env, &lock_or_burn_in.original_sender)?;
        Self::run_policy_engine(
            &env,
            &caller,
            PolicyData::PoolHooks(PoolHooksPayloadData::Preflight(PreflightPayload {
                lock_or_burn_in,
                requested_finality,
                amount_post_fee,
            })),
            token_args,
        )?;
        Ok(())
    }

    /// Inbound postflight (EVM `postflightCheck`). First validates the caller
    /// against the `authorized_callers` set (EVM `_validateCaller`), then runs
    /// the policy engine over the postflight payload if one is attached. The
    /// `context` is `offchain_token_data`; EVM notes it is unused in v2+
    /// TokenPools, so empty `Bytes` is the current-faithful value — a real field
    /// would be threaded here if reintroduced. With no engine attached the `run`
    /// is skipped (dormant default).
    pub fn postflight_check(
        env: Env,
        caller: Address,
        release_or_mint_in: ReleaseOrMintIn,
        local_amount: i128,
        requested_finality: u32,
    ) -> Result<(), CCIPError> {
        Self::require_authorized_caller(&env, &caller)?;
        // EVM `releaseOrMintIn.offchainTokenData` — unused in v2+ (always empty).
        let context = Bytes::new(&env);
        Self::run_policy_engine(
            &env,
            &caller,
            PolicyData::PoolHooks(PoolHooksPayloadData::Postflight(PostflightPayload {
                release_or_mint_in,
                local_amount,
                requested_finality,
            })),
            context,
        )?;
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

    /// EVM `checkAllowList` analogue. A no-op when the allowlist is disabled
    /// (`i_allowlistEnabled == false`); otherwise reverts `SenderNotAllowed` iff
    /// `sender` is absent from the stored allowlist. Shared by the public
    /// `check_allow_list` view and `preflight_check`.
    fn require_allowlisted(env: &Env, sender: &Address) -> Result<(), CCIPError> {
        if Self::get_allowlist_enabled(env.clone()) {
            let allow: Vec<Address> = env
                .storage()
                .instance()
                .get(&ALLOWLIST)
                .unwrap_or(Vec::new(env));
            if !contains(&allow, sender) {
                return Err(CCIPError::SenderNotAllowed);
            }
        }
        Ok(())
    }

    /// Runs the attached policy engine over `data` + `context`, the Stellar
    /// analogue of EVM `policyEngine.run(Payload{selector, sender, data,
    /// context})`. Dormant when no engine is attached (EVM `address(0)`
    /// short-circuit). The `selector` identifies the hooks method and is
    /// derived from the payload variant — the extractor dispatches on it. A
    /// policy rejection propagates as `Err` (or a host revert aborts), blocking
    /// the transfer, as on EVM.
    fn run_policy_engine(
        env: &Env,
        sender: &Address,
        data: PolicyData,
        context: Bytes,
    ) -> Result<(), CCIPError> {
        let Some(pe) = Self::get_policy_engine(env.clone()) else {
            return Ok(());
        };
        let selector = match &data {
            PolicyData::PoolHooks(PoolHooksPayloadData::Preflight(_)) => {
                Symbol::new(env, "preflight_check")
            }
            PolicyData::PoolHooks(PoolHooksPayloadData::Postflight(_)) => {
                Symbol::new(env, "postflight_check")
            }
        };
        let payload = Payload {
            selector,
            sender: sender.clone(),
            data,
            context,
        };
        let client = PolicyEngineClient::new(env, &pe);
        // `target` = this contract's own address — on EVM the engine reads its
        // `msg.sender` (the hooks contract) to key the policy chain; Soroban
        // has no `msg.sender`, so the target is passed explicitly and the
        // engine authenticates it with `require_auth` (it is in the call tree
        // as the direct caller). Non-`try_` run: a policy rejection
        // reverts/aborts, propagating up and blocking the transfer — EVM
        // `policyEngine.run` revert parity. The pool already treats any hooks
        // failure as an abort.
        client.run(&env.current_contract_address(), &payload);
        Ok(())
    }
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
