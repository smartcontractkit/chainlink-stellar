use soroban_sdk::{contracttrait, contracttype, vec, Address, Env, IntoVal, Map, Symbol, Vec};

use common_error::CCIPError;
use common_helpers::validation::Validatable;

use crate::Ownable;

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AllowListUpdate {
    pub dest_chain_selector: u64,
    pub allowlist_enabled: bool,
    pub added_allowlisted_senders: Vec<Address>,
    pub removed_allowlisted_senders: Vec<Address>,
}

impl Validatable for AllowListUpdate {
    fn validate(&self) -> Result<(), CCIPError> {
        if self.dest_chain_selector == 0 {
            return Err(CCIPError::InvalidConfig);
        }
        Ok(())
    }
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AllowListEntry {
    pub allowlist_enabled: bool,
    pub allowlist: Vec<Address>,
}

/// Internal hooks for [`AllowListable`]. Plain trait on purpose: `#[contracttrait]`
/// exports every method as a public entrypoint — an exported event hook would let
/// anyone forge allowlist events, and gating it would double-`require_auth` the same
/// address in one frame (host trap `Error(Auth, ExistingValue)`). Direction is
/// `AllowListable: AllowListHooks` because the main trait's default bodies call these
/// hooks; the reverse would not compile.
pub trait AllowListHooks {
    /// Emit the update's events (EVM `BaseVerifier._applyAllowlistUpdates` parity):
    /// state-changed only on an actual flip, per-sender events only for addresses that
    /// actually changed membership. `new_enabled_state` is `Some(new_value)` on a flip
    /// and `None` when unchanged — it is only the changed-vs-unchanged signal and is
    /// never emitted itself; events carry the new state outright (like EVM
    /// `AllowListStateChanged(uint64, bool)`), so listeners need no prior-state copy.
    fn emit_allowlist_updated_events(
        env: &Env,
        key: u64,
        new_enabled_state: Option<bool>,
        added_addresses: &Vec<Address>,
        removed_addresses: &Vec<Address>,
    );

    /// EVM `CommitteeVerifier`: allowlist updates are owner-or-allowlistAdmin.
    /// Default `None` = owner-only.
    fn allowlist_admin(env: &Env) -> Option<Address> {
        let _ = env;
        None
    }
}

/// A trait to maintain a set of allowed addresses for a any purpose.
/// It can be used for authorization as well as guarding access to certain functions.
///
/// For example: The Committee Verifier contract can use this trait to maintain a set of allowed addresses for a given destination chain.
///
/// The allow list is a map of u64 to vector of addresses.
#[contracttrait]
pub trait AllowListable: Ownable + AllowListHooks {
    const ALLOW_LIST: Symbol; // Storage key for the allow list data

    /// Initialize the authorized callers list.
    /// This enables the feature and sets the initial list of authorized callers.
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `initial_callers` - Initial list of authorized addresses
    fn init_allowlist(env: &Env, initial_allowlist: Map<u64, Vec<Address>>) {
        if env.storage().instance().has(&Self::ALLOW_LIST) {
            return;
        }

        let mut allowlist_map: Map<u64, AllowListEntry> = Map::new(env);

        initial_allowlist.iter().for_each(|(key, value)| {
            allowlist_map.set(
                key,
                AllowListEntry {
                    allowlist_enabled: true,
                    allowlist: value.clone(),
                },
            );
        });

        env.storage()
            .instance()
            .set(&Self::ALLOW_LIST, &allowlist_map);
    }

    /// Check if the allow list is enabled for a given key.
    fn is_allowlist_enabled(env: &Env, key: u64) -> bool {
        // TODO: use persistent storage instead to avoid having to load the entire map all the time due to unbounded size.
        env.storage()
            .instance()
            .get(&Self::ALLOW_LIST)
            .map(|map: Option<Map<u64, AllowListEntry>>| match map {
                Some(map) => map
                    .get(key)
                    .map(|entry| entry.allowlist_enabled)
                    .unwrap_or(false),
                None => false,
            })
            .unwrap_or(false)
    }

    /// Apply allowlist updates per destination chain (EVM
    /// `CommitteeVerifier.applyAllowlistUpdates`).
    ///
    /// Authorized: the owner OR the configured [`AllowListHooks::allowlist_admin`].
    /// Bind-then-auth (same pattern as `AccessControl::require_admin_or_owner`):
    /// check `caller`'s identity first, then one `require_auth_for_args` binding the
    /// exact updates payload — Soroban has no caller introspection, and stacking
    /// `require_auth` in one frame traps.
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `caller` - The identity to authorize (must be the owner or the allowlist admin)
    /// * `updates` - Per-chain allowlist updates
    ///
    /// # Errors
    /// * `CallerNotAuthorized` - Caller is neither the owner nor the allowlist admin
    /// * `InvalidAllowListRequest` - Update adds senders while setting `allowlist_enabled = false`
    /// * `InvalidConfig` - Invalid update payload (e.g. zero `dest_chain_selector`)
    ///
    /// # Panics
    /// If `caller` did not authorize this exact invocation (`require_auth_for_args`).
    fn apply_allowlist_updates(
        env: &Env,
        caller: &Address,
        updates: &Vec<AllowListUpdate>,
    ) -> Result<(), CCIPError> {
        let is_owner = <Self as Ownable>::is_owner(env, caller);
        let is_admin = <Self as AllowListHooks>::allowlist_admin(env).as_ref() == Some(caller);
        if !is_owner && !is_admin {
            return Err(CCIPError::CallerNotAuthorized);
        }
        // Bind the auth to this exact payload, so a signed authorization cannot be
        // replayed against a different set of updates.
        caller.require_auth_for_args(vec![
            &env,
            caller.clone().into_val(env),
            updates.clone().into_val(env),
        ]);

        // The same instance-storage map backs every update: load once, mutate in memory
        // per iteration, persist once at the end. Error paths return `Err`, which fails
        // the whole invocation and rolls back its writes, so partial application is
        // impossible either way.
        let mut data: Map<u64, AllowListEntry> = env
            .storage()
            .instance()
            .get(&Self::ALLOW_LIST)
            .unwrap_or(Map::new(env));
        let mut touched = false;

        for update in updates.iter() {
            update.validate()?;

            let key = update.dest_chain_selector;
            let to_add = update.added_allowlisted_senders;
            let to_remove = update.removed_allowlisted_senders;

            // EVM parity (`BaseVerifier._applyAllowlistUpdates`, BaseVerifier.sol:272-287):
            // an update that keeps or sets the allowlist disabled must not add senders —
            // the additions would be ineffective.
            if !update.allowlist_enabled && !to_add.is_empty() {
                return Err(CCIPError::InvalidAllowListRequest);
            }

            // EVM parity: disabling a never-configured chain is a no-op (zero-value slot — no
            // state change, no event). Only reachable with empty additions — the additions
            // case is rejected above via InvalidAllowListRequest, as on EVM.
            if !data.contains_key(key) && !update.allowlist_enabled && to_add.is_empty() {
                continue;
            }

            // An absent entry reads as disabled, so a first-time enable counts as a flip.
            let old_enabled = data
                .get(key)
                .map(|entry| entry.allowlist_enabled)
                .unwrap_or(false);
            let flipped = match old_enabled != update.allowlist_enabled {
                true => Some(update.allowlist_enabled),
                false => None,
            };

            let mut allowlist_entry = data.get(key).unwrap_or(AllowListEntry {
                allowlist_enabled: update.allowlist_enabled,
                allowlist: Vec::new(env),
            });

            // EVM parity: the enabled flag is rewritten from the payload on every update,
            // not only when the entry is first created.
            allowlist_entry.allowlist_enabled = update.allowlist_enabled;

            // Removals before adds, matching EVM's event ordering. Events report only
            // actual set changes (EVM emits per-sender events iff add/remove returned true).
            let mut actually_removed: Vec<Address> = Vec::new(env);
            for address in to_remove.iter() {
                if let Some(idx) = allowlist_entry.allowlist.first_index_of(address.clone()) {
                    allowlist_entry.allowlist.remove(idx);
                    actually_removed.push_back(address.clone());
                }
            }

            // No zero-address guard (EVM rejects address(0), BaseVerifier.sol:276-278):
            // address(0) is EVM's default/unset sentinel, leaked by zero-initialized
            // fields; Soroban senders are explicit strkeys with no zero-default pathway.
            let mut actually_added: Vec<Address> = Vec::new(env);
            for address in to_add.iter() {
                if !allowlist_entry.allowlist.contains(address.clone()) {
                    allowlist_entry.allowlist.push_back(address.clone());
                    actually_added.push_back(address.clone());
                }
            }

            data.set(key, allowlist_entry);
            touched = true;

            <Self as AllowListHooks>::emit_allowlist_updated_events(
                env,
                key,
                flipped,
                &actually_added,
                &actually_removed,
            );
        }

        if touched {
            env.storage().instance().set(&Self::ALLOW_LIST, &data);
        }

        Ok(())
    }

    /// Get the allowlist for a specific key.
    fn get_allowlist_entry(env: &Env, key: u64) -> Option<AllowListEntry> {
        env.storage()
            .instance()
            .get(&Self::ALLOW_LIST)
            .unwrap_or(Map::new(env))
            .get(key)
    }

    /// Check if an address is in the allow list.
    fn is_in_allowlist(env: &Env, key: u64, addr: &Address) -> bool {
        if let Some(entry) = Self::get_allowlist_entry(env, key) {
            return entry.allowlist.contains(addr);
        }

        false
    }

    /// Require that a given address is in the allow list for `key`.
    ///
    /// This checks **storage membership only**. It does **not** call [`Address::require_auth`] on
    /// `address`. A malicious caller can still pass an allowlisted `address` as a plain argument
    /// unless the contract entrypoint also binds Soroban authorization to that address (for
    /// example `address.require_auth()` or `require_auth_for_args` with the same argument vector).
    ///
    /// When the allowlist is **disabled** for `key`, this returns `Ok(())` without inspecting
    /// `address` (open allow).
    ///
    /// For a combined membership + auth check, see [`require_in_allowlist_authorized`].
    ///
    /// # Errors
    /// * `CallerNotAuthorized` - Allowlist is enabled for `key` and `address` is not a member
    fn require_in_allowlist(env: &Env, key: u64, address: &Address) -> Result<(), CCIPError> {
        // If the allowlist is not enabled, we assume the address is allowed.
        if !Self::is_allowlist_enabled(env, key) {
            return Ok(());
        }

        if !Self::is_in_allowlist(env, key, address) {
            return Err(CCIPError::CallerNotAuthorized);
        }

        Ok(())
    }
}

/// Enforces [`AllowListable::require_in_allowlist`] and, when the allowlist is enabled for `key`,
/// [`Address::require_auth`] on `address`.
///
/// Use this when `address` is expected to be the **Soroban-authenticated principal** for the
/// current invocation (direct call, or nested call with correctly attached authorization).
///
/// **Nested contract calls:** If `T::require_in_allowlist` is invoked from a contract that is not
/// `address` (e.g. OnRamp calling a verifier with an `original_sender` argument), `require_auth`
/// on `address` will fail unless the transaction’s authorization payload includes that nested
/// invocation for `address` (invoker-contract auth trees). Until that wiring exists, keep using
/// [`AllowListable::require_in_allowlist`] alone at those boundaries and document the trust model.
///
/// # Errors
///
/// Same as [`AllowListable::require_in_allowlist`].
///
/// # Panics
///
/// If the allowlist applies and `address` is on the list but did not authorize this invocation.
pub fn require_in_allowlist_authorized<T: AllowListable>(
    env: &Env,
    key: u64,
    address: &Address,
) -> Result<(), CCIPError> {
    T::require_in_allowlist(env, key, address)?;
    if T::is_allowlist_enabled(env, key) {
        address.require_auth();
    }
    Ok(())
}
