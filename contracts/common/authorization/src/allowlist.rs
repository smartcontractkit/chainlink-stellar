use soroban_sdk::{contracttrait, contracttype, Address, Env, Map, Symbol, Vec};

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

/// A trait to maintain a set of allowed addresses for a any purpose.
/// It can be used for authorization as well as guarding access to certain functions.
///
/// For example: The Committee Verifier contract can use this trait to maintain a set of allowed addresses for a given destination chain.
///
/// The allow list is a map of u64 to vector of addresses.
#[contracttrait]
pub trait AllowListable: Ownable {
    const ALLOW_LIST: Symbol; // Storage key for the allow list data

    /// Emit the update's events (EVM `BaseVerifier._applyAllowlistUpdates` parity):
    /// `new_enabled_state` is `Some(v)` iff the enabled flag actually flipped (emit a
    /// state-changed event only then), and `added_addresses`/`removed_addresses` carry
    /// only the addresses that actually changed set membership.
    fn emit_allowlist_updated_events(
        env: &Env,
        key: u64,
        new_enabled_state: Option<bool>,
        added_addresses: &Vec<Address>,
        removed_addresses: &Vec<Address>,
    );

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

    /// Add addresses to the allow list.
    /// Requires owner authorization.
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `callers` - Addresses to add
    ///
    /// # Errors
    /// * `InvalidAllowListRequest` - Update adds senders while setting `allowlist_enabled = false`
    /// * `InvalidConfig` - Invalid update payload (e.g. zero `dest_chain_selector`)
    /// * `NotOwner` - Owner not set in storage (see [`Ownable::require_owner`])
    ///
    /// # Panics
    ///
    /// If the owner did not authorize this invocation (`require_auth`).
    fn apply_allowlist_updates(env: &Env, updates: &Vec<AllowListUpdate>) -> Result<(), CCIPError> {
        <Self as Ownable>::require_owner(env)?;

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

            let mut data: Map<u64, AllowListEntry> = env
                .storage()
                .instance()
                .get(&Self::ALLOW_LIST)
                .unwrap_or(Map::new(env));

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
            let flipped =
                (old_enabled != update.allowlist_enabled).then_some(update.allowlist_enabled);

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

            let mut actually_added: Vec<Address> = Vec::new(env);
            for address in to_add.iter() {
                if !allowlist_entry.allowlist.contains(address.clone()) {
                    allowlist_entry.allowlist.push_back(address.clone());
                    actually_added.push_back(address.clone());
                }
            }

            data.set(key, allowlist_entry);
            env.storage().instance().set(&Self::ALLOW_LIST, &data);

            Self::emit_allowlist_updated_events(
                env,
                key,
                flipped,
                &actually_added,
                &actually_removed,
            );
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
