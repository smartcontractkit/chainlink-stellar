#![no_std]

mod events;

use soroban_sdk::{contract, contractimpl, symbol_short, Address, BytesN, Env, Map, Symbol, Vec};

use common_authorization::{AuthorizedCallerAddedEvent, AuthorizedCallerRemovedEvent, Ownable};
use common_error::CCIPError;
use common_guard::initializable::Initializable;

use events::{CursedEvent, UncursedEvent};

// ============================================================
// Storage Keys
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const CURSED: Symbol = symbol_short!("CURSED");
const CURSE_ADMINS: Symbol = symbol_short!("CRSADM");

// ============================================================
// Constants
// ============================================================

/// Global curse subject — an active curse on this subject causes `is_cursed()` to return true.
/// Equivalent to EVM `GLOBAL_CURSE_SUBJECT = 0x01000000000000000000000000000001`.
const GLOBAL_CURSE_SUBJECT: [u8; 16] = [
    0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
];

// ============================================================
// Contract
// ============================================================

/// RMN Remote contract for Stellar/Soroban.
///
/// Port of the EVM `RMN.sol` curse surface. Provides:
///   - **Cursing**: owner or curse admins may curse; only owner may uncurse or manage curse admins
#[contract]
pub struct RmnRemoteContract;

#[contractimpl]
impl Initializable for RmnRemoteContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for RmnRemoteContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

impl RmnRemoteContract {
    fn load_curse_admins(env: &Env) -> Vec<Address> {
        env.storage()
            .instance()
            .get(&CURSE_ADMINS)
            .unwrap_or_else(|| Vec::new(env))
    }

    fn store_curse_admins(env: &Env, admins: &Vec<Address>) {
        env.storage().instance().set(&CURSE_ADMINS, admins);
    }

    fn is_curse_admin(env: &Env, addr: &Address) -> bool {
        let admins = Self::load_curse_admins(env);
        for admin in admins.iter() {
            if admin == *addr {
                return true;
            }
        }
        false
    }

    /// Owner may curse without being listed as a curse admin (EVM: skip auth when `msg.sender == owner()`).
    /// Curse admins must be on the allowlist and authorize this invocation.
    fn require_can_curse(env: &Env, caller: &Address) -> Result<(), CCIPError> {
        if <Self as Ownable>::is_owner(env, caller) {
            caller.require_auth();
            return Ok(());
        }
        if Self::is_curse_admin(env, caller) {
            caller.require_auth();
            return Ok(());
        }
        Err(CCIPError::CallerNotAuthorized)
    }

    fn contains_address(list: &Vec<Address>, addr: &Address) -> bool {
        for entry in list.iter() {
            if entry == *addr {
                return true;
            }
        }
        false
    }
}

#[contractimpl]
impl RmnRemoteContract {
    // ========================================
    // Initialization
    // ========================================

    /// Initialize the RMN Remote contract.
    ///
    /// # Arguments
    /// * `owner` - The owner address (uncurse, curse-admin updates)
    /// * `curse_admins` - Initial curse admins (may be empty); mirrors EVM `RMN` constructor `curseAdmins`
    pub fn initialize(
        env: Env,
        owner: Address,
        curse_admins: Vec<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;

        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;

        let cursed: Map<BytesN<16>, bool> = Map::new(&env);
        env.storage().instance().set(&CURSED, &cursed);

        let mut stored_admins: Vec<Address> = Vec::new(&env);
        for admin in curse_admins.iter() {
            if !Self::contains_address(&stored_admins, &admin) {
                stored_admins.push_back(admin.clone());
            }
        }
        Self::store_curse_admins(&env, &stored_admins);

        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "RMN-dev 2.0.0")
    }

    // ========================================
    // Curse admin management (owner only)
    // ========================================

    /// Add and/or remove curse admins. Only callable by owner.
    ///
    /// Mirrors EVM `AuthorizedCallers.applyAuthorizedCallerUpdates` on `RMN`.
    pub fn apply_curse_admin_updates(
        env: Env,
        added_admins: Vec<Address>,
        removed_admins: Vec<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut admins = Self::load_curse_admins(&env);

        for to_remove in removed_admins.iter() {
            let mut next: Vec<Address> = Vec::new(&env);
            for admin in admins.iter() {
                if admin == to_remove {
                    AuthorizedCallerRemovedEvent {
                        caller: admin.clone(),
                    }
                    .publish(&env);
                } else {
                    next.push_back(admin);
                }
            }
            admins = next;
        }

        for to_add in added_admins.iter() {
            if !Self::contains_address(&admins, &to_add) {
                admins.push_back(to_add.clone());
                AuthorizedCallerAddedEvent {
                    caller: to_add.clone(),
                }
                .publish(&env);
            }
        }

        Self::store_curse_admins(&env, &admins);
        Ok(())
    }

    /// Returns addresses allowed to call `curse` (excluding the owner, who may always curse).
    pub fn get_curse_admins(env: Env) -> Result<Vec<Address>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Ok(Self::load_curse_admins(&env))
    }

    // ========================================
    // Cursing
    // ========================================

    /// Curse one or more subjects. Callable by owner or a curse admin.
    ///
    /// `caller` must be the invoker (owner or curse admin) and must authorize this call.
    /// Already-cursed subjects and duplicates in `subjects` are silently skipped (EVM `RMN.curse`).
    /// Emits `Cursed` only for newly cursed subjects; no-op if none are new.
    pub fn curse(env: Env, caller: Address, subjects: Vec<BytesN<16>>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Self::require_can_curse(&env, &caller)?;

        let mut cursed: Map<BytesN<16>, bool> = env
            .storage()
            .instance()
            .get(&CURSED)
            .ok_or(CCIPError::NotInitialized)?;

        let mut newly_cursed: Vec<BytesN<16>> = Vec::new(&env);
        for subject in subjects.iter() {
            if cursed.get(subject.clone()).unwrap_or(false) {
                continue;
            }
            cursed.set(subject.clone(), true);
            newly_cursed.push_back(subject);
        }

        if newly_cursed.is_empty() {
            return Ok(());
        }

        env.storage().instance().set(&CURSED, &cursed);
        CursedEvent {
            subjects: newly_cursed,
        }
        .publish(&env);

        Ok(())
    }

    /// Uncurse one or more subjects. Only callable by owner.
    ///
    /// Reverts if any subject is not currently cursed.
    pub fn uncurse(env: Env, subjects: Vec<BytesN<16>>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut cursed: Map<BytesN<16>, bool> = env
            .storage()
            .instance()
            .get(&CURSED)
            .ok_or(CCIPError::NotInitialized)?;

        subjects.iter().try_for_each(|subject| {
            if !cursed.get(subject.clone()).unwrap_or(false) {
                return Err(CCIPError::NotCursed);
            }
            cursed.remove(subject);
            Ok(())
        })?;

        env.storage().instance().set(&CURSED, &cursed);

        UncursedEvent { subjects }.publish(&env);

        Ok(())
    }

    /// Returns all currently cursed subjects.
    pub fn get_cursed_subjects(env: Env) -> Result<Vec<BytesN<16>>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let cursed: Map<BytesN<16>, bool> = env
            .storage()
            .instance()
            .get(&CURSED)
            .ok_or(CCIPError::NotInitialized)?;

        let mut result: Vec<BytesN<16>> = Vec::new(&env);
        for key in cursed.keys() {
            result.push_back(key);
        }
        Ok(result)
    }

    /// Check if the network is globally cursed.
    ///
    /// Returns `true` if the `GLOBAL_CURSE_SUBJECT` is in the cursed set.
    /// This is the function called by the RMN Proxy (via `RmnProxyClient::is_cursed()`).
    pub fn is_cursed(env: Env) -> bool {
        let cursed: Map<BytesN<16>, bool> = match env.storage().instance().get(&CURSED) {
            Some(c) => c,
            None => return false,
        };

        if cursed.is_empty() {
            return false;
        }

        let global = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
        cursed.get(global).unwrap_or(false)
    }

    /// Check if a specific subject is cursed.
    ///
    /// Returns `true` if `subject` OR the `GLOBAL_CURSE_SUBJECT` is cursed.
    pub fn is_cursed_by_subject(env: Env, subject: BytesN<16>) -> bool {
        let cursed: Map<BytesN<16>, bool> = match env.storage().instance().get(&CURSED) {
            Some(c) => c,
            None => return false,
        };

        if cursed.is_empty() {
            return false;
        }

        let global = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
        cursed.get(subject).unwrap_or(false) || cursed.get(global).unwrap_or(false)
    }
}

#[cfg(test)]
mod test;
