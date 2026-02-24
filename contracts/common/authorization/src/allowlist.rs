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

    fn emit_allowlist_updated_event(
        env: &Env,
        key: u64,
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
        // TODO: add a guard here to make sure this method is only called once during initialization.

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
    /// * `FeatureNotEnabled` - Allow list not initialized
    /// * `NotInitialized` - Owner not set
    fn apply_allowlist_updates(env: &Env, updates: &Vec<AllowListUpdate>) -> Result<(), CCIPError> {
        <Self as Ownable>::require_owner(env)?;

        for update in updates.iter() {
            update.validate()?;

            let key = update.dest_chain_selector;
            let to_add = update.added_allowlisted_senders;
            let to_remove = update.removed_allowlisted_senders;

            // The call to `is_allowlist_enabled` will return false if the allowlist has never been set.
            if !Self::is_allowlist_enabled(env, key) && !update.allowlist_enabled {
                return Err(CCIPError::FeatureNotEnabled);
            }

            let mut data: Map<u64, AllowListEntry> = env
                .storage()
                .instance()
                .get(&Self::ALLOW_LIST)
                .unwrap_or(Map::new(env));

            let mut allowlist_entry = data.get(key).unwrap_or(AllowListEntry {
                allowlist_enabled: update.allowlist_enabled,
                allowlist: Vec::new(env),
            });

            for address in to_add.iter() {
                if !allowlist_entry.allowlist.contains(address.clone()) {
                    allowlist_entry.allowlist.push_back(address.clone());
                }
            }

            for address in to_remove.iter() {
                if allowlist_entry.allowlist.contains(address.clone()) {
                    allowlist_entry.allowlist.remove(
                        allowlist_entry
                            .allowlist
                            .first_index_of(address.clone())
                            .unwrap(),
                    );
                }
            }

            data.set(key, allowlist_entry);
            env.storage().instance().set(&Self::ALLOW_LIST, &data);

            Self::emit_allowlist_updated_event(env, key, &to_add, &to_remove);
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

    /// Require that a given address is in the allow list.
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AuthorizedCallers not initialized
    /// * `CallerNotAuthorized` - No authorized caller provided auth
    fn require_in_allowlist(env: &Env, key: u64, address: &Address) -> Result<(), CCIPError> {
        if !Self::is_allowlist_enabled(env, key) {
            return Err(CCIPError::FeatureNotEnabled);
        }

        if !Self::is_in_allowlist(env, key, address) {
            return Err(CCIPError::CallerNotAuthorized);
        }

        Ok(())
    }
}
