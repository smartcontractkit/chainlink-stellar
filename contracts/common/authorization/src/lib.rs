#![no_std]

//! # Authorization Library
//!
//! A reusable authorization library for Stellar/Soroban contracts providing:
//! - **Ownable**: Single owner with two-step transfer
//! - **AuthorizedCallers**: Dynamic set of authorized addresses (optional)
//! - **AccessControl**: Role-based permissions (optional)
//!
//! ## Usage
//!
//! ```ignore
//! use common_authorization::{Ownable, DefaultOwnable, AuthorizedCallers, AccessControl, CCIPError};
//!
//! // In your contract's initialize function (implement Ownable for your contract):
//! impl Ownable for MyContract {}
//! <MyContract as Ownable>::init(&env, &owner);
//! AuthorizedCallers::init(&env, authorized_callers); // Optional
//! AccessControl::init(&env); // Optional
//!
//! // In protected functions:
//! <MyContract as Ownable>::require_owner(&env)?;
//! AuthorizedCallers::require_authorized_caller(&env, &updater)?;
//! AccessControl::require_role(&env, &caller, symbol_short!("MINTER"))?;
//! ```

pub mod allowlist;
pub mod events;
pub mod ownable;
pub mod upgradeable;

pub use events::*;
pub use ownable::{DefaultOwnable, Ownable};
pub use upgradeable::{Upgradeable, Upgraded};

use common_error::CCIPError;
use soroban_sdk::{symbol_short, Address, Env, Symbol, Vec};

// ============================================================
// Storage Keys
// ============================================================

const AUTH_CALLERS: Symbol = symbol_short!("AUTHCALL");
const AUTH_ROLES: Symbol = symbol_short!("AUTHROLE");

// ============================================================
// Predefined Roles
// ============================================================

/// Administrator role - can grant/revoke other roles.
pub const ROLE_ADMIN: Symbol = symbol_short!("ADMIN");

/// Minter role - typically used for token minting permissions.
pub const ROLE_MINTER: Symbol = symbol_short!("MINTER");

/// Burner role - typically used for token burning permissions.
pub const ROLE_BURNER: Symbol = symbol_short!("BURNER");

// ============================================================
// AuthorizedCallers
// ============================================================

/// Manages a set of authorized caller addresses.
///
/// This is an optional feature that must be explicitly initialized.
/// If not initialized, `is_enabled()` returns false and `require_authorized_caller` returns `CCIPError::FeatureNotEnabled`.
pub struct AuthorizedCallers;

impl AuthorizedCallers {
    /// Initialize the authorized callers list.
    /// This enables the feature and sets the initial list of authorized callers.
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `initial_callers` - Initial list of authorized addresses
    pub fn init(env: &Env, initial_callers: Vec<Address>) {
        env.storage()
            .instance()
            .set(&AUTH_CALLERS, &initial_callers);

        // Emit events for each initial caller
        for caller in initial_callers.iter() {
            AuthorizedCallerAddedEvent { caller }.publish(env);
        }
    }

    /// Check if the AuthorizedCallers feature is enabled.
    pub fn is_enabled(env: &Env) -> bool {
        env.storage().instance().has(&AUTH_CALLERS)
    }

    /// Add authorized callers.
    /// Requires owner authorization.
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `callers` - Addresses to add
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AuthorizedCallers not initialized
    /// * `NotOwner` - Owner not set in storage (see [`Ownable::require_owner`])
    ///
    /// # Panics
    ///
    /// If the owner did not authorize this invocation (`require_auth`).
    pub fn add_callers(env: &Env, callers: Vec<Address>) -> Result<(), CCIPError> {
        if !Self::is_enabled(env) {
            return Err(CCIPError::FeatureNotEnabled);
        }
        DefaultOwnable::require_owner(env)?;

        let mut authorized: Vec<Address> = env
            .storage()
            .instance()
            .get(&AUTH_CALLERS)
            .unwrap_or(Vec::new(env));

        for caller in callers.iter() {
            // Check if already authorized
            let mut found = false;
            for existing in authorized.iter() {
                if existing == caller {
                    found = true;
                    break;
                }
            }

            if !found {
                authorized.push_back(caller.clone());
                AuthorizedCallerAddedEvent {
                    caller: caller.clone(),
                }
                .publish(env);
            }
        }

        env.storage().instance().set(&AUTH_CALLERS, &authorized);
        Ok(())
    }

    /// Remove authorized callers.
    /// Requires owner authorization.
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `callers` - Addresses to remove
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AuthorizedCallers not initialized
    /// * `NotOwner` - Owner not set in storage (see [`Ownable::require_owner`])
    ///
    /// # Panics
    ///
    /// If the owner did not authorize this invocation (`require_auth`).
    pub fn remove_callers(env: &Env, callers: Vec<Address>) -> Result<(), CCIPError> {
        if !Self::is_enabled(env) {
            return Err(CCIPError::FeatureNotEnabled);
        }
        DefaultOwnable::require_owner(env)?;

        let authorized: Vec<Address> = env
            .storage()
            .instance()
            .get(&AUTH_CALLERS)
            .unwrap_or(Vec::new(env));

        let mut new_authorized: Vec<Address> = Vec::new(env);

        for existing in authorized.iter() {
            let mut should_remove = false;
            for to_remove in callers.iter() {
                if existing == to_remove {
                    should_remove = true;
                    AuthorizedCallerRemovedEvent {
                        caller: existing.clone(),
                    }
                    .publish(env);
                    break;
                }
            }
            if !should_remove {
                new_authorized.push_back(existing);
            }
        }

        env.storage().instance().set(&AUTH_CALLERS, &new_authorized);
        Ok(())
    }

    /// Get all authorized callers.
    pub fn get_callers(env: &Env) -> Vec<Address> {
        env.storage()
            .instance()
            .get(&AUTH_CALLERS)
            .unwrap_or(Vec::new(env))
    }

    /// Check if an address is authorized.
    pub fn is_authorized(env: &Env, addr: &Address) -> bool {
        let callers = Self::get_callers(env);
        for caller in callers.iter() {
            if caller == *addr {
                return true;
            }
        }
        false
    }

    /// Require that `caller` is in the authorized-callers allowlist **and** has authorized this
    /// contract invocation via Soroban auth (`caller.require_auth()`).
    ///
    /// This matches EVM `AuthorizedCallers` / `FeeQuoter` semantics: the configured addresses form an **OR**-list of allowed
    /// principals, but each transaction must identify **one** principal (here: the `caller`
    /// argument — the Stellar analogue of `msg.sender`) that is both allowlisted and has signed /
    /// authorized this call.
    ///
    /// # Security
    ///
    /// Callers **must** pass their own [`Address`] as `caller`. Soroban does not support
    /// iterating `require_auth()` over every allowlisted address: the first failed `require_auth`
    /// would **panic**, and order would become security-sensitive. The allowlist check runs
    /// **before** `require_auth` so unknown addresses get `CCIPError::CallerNotAuthorized`
    /// instead of a host auth panic.
    ///
    /// **Direct invoker vs `caller` (EVM `msg.sender` parity):** Soroban protocol 25 does not
    /// expose a host function that returns the immediate invoker (parent frame / `msg.sender`).
    /// `caller.require_auth()` proves `caller` authorized **this** contract invocation with the
    /// **actual argument vector** of the entry function (see `require_auth` host docs), including
    /// valid delegated authorization via invoker-contract auth trees (`authorize_as_current_contract`).
    /// So `caller` can still differ from the Wasm direct caller when a relay contract invokes this
    /// contract while another address’s authorization is attached to that nested call. Tighter
    /// “must be the same entity as the direct caller” semantics require protocol/SDK support or
    /// operational controls (allowlist composition, audited relayers, strict `__check_auth` on custom
    /// accounts).
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AuthorizedCallers not initialized
    /// * `CallerNotAuthorized` - `caller` is not in the allowlist
    ///
    /// # Panics
    ///
    /// If `caller` is allowlisted but did not authorize this invocation (same as `require_auth`).
    pub fn require_authorized_caller(env: &Env, caller: &Address) -> Result<(), CCIPError> {
        if !Self::is_enabled(env) {
            return Err(CCIPError::FeatureNotEnabled);
        }
        if !Self::is_authorized(env, caller) {
            return Err(CCIPError::CallerNotAuthorized);
        }
        caller.require_auth();
        Ok(())
    }
}

// ============================================================
// AccessControl
// ============================================================

/// Role-based access control.
///
/// This is an optional feature that must be explicitly initialized.
/// Roles are identified by `Symbol` values (up to 9 characters).
///
/// The owner can always grant/revoke any role.
/// Addresses with the ADMIN role can also grant/revoke other roles.
pub struct AccessControl;

impl AccessControl {
    /// Initialize the AccessControl feature.
    /// Must be called before using role-based access control.
    pub fn init(env: &Env) {
        // Initialize with an empty roles map marker
        let empty_roles: Vec<(Symbol, Vec<Address>)> = Vec::new(env);
        env.storage().instance().set(&AUTH_ROLES, &empty_roles);
    }

    /// Check if the AccessControl feature is enabled.
    pub fn is_enabled(env: &Env) -> bool {
        env.storage().instance().has(&AUTH_ROLES)
    }

    /// Grant a role to an address.
    /// Requires owner or ADMIN role authorization.
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `sender` - Address claiming this grant (must be the stored owner **or** hold the
    ///   `ADMIN` role when a owner is configured; if no owner is configured, must hold `ADMIN`);
    ///   must authorize this invocation via Soroban auth.
    /// * `role` - The role to grant
    /// * `account` - The address to receive the role
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AccessControl not initialized
    /// * `CallerNotAuthorized` - `sender` is neither the stored owner nor an `ADMIN` member (when an owner is configured)
    /// * `RoleNotGranted` - No owner configured and `sender` does not have `ADMIN`
    ///
    /// # Panics
    ///
    /// If `sender` is authorized to grant but did not authorize this invocation (`require_auth`).
    pub fn grant_role(
        env: &Env,
        sender: &Address,
        role: Symbol,
        account: &Address,
    ) -> Result<(), CCIPError> {
        if !Self::is_enabled(env) {
            return Err(CCIPError::FeatureNotEnabled);
        }

        // Require owner or ADMIN role
        let granter = Self::require_admin_or_owner(env, sender)?;

        // Get current roles
        let mut roles: Vec<(Symbol, Vec<Address>)> = env
            .storage()
            .instance()
            .get(&AUTH_ROLES)
            .unwrap_or(Vec::new(env));

        // Find or create the role entry
        let mut found_role_idx: Option<u32> = None;
        for i in 0..roles.len() {
            let (r, _) = roles.get(i).unwrap();
            if r == role {
                found_role_idx = Some(i);
                break;
            }
        }

        match found_role_idx {
            Some(idx) => {
                let (r, mut members) = roles.get(idx).unwrap();

                // Check if already has role
                let mut has_role = false;
                for m in members.iter() {
                    if m == *account {
                        has_role = true;
                        break;
                    }
                }

                if !has_role {
                    members.push_back(account.clone());
                    roles.set(idx, (r, members));
                }
            }
            None => {
                // Create new role entry
                let mut members: Vec<Address> = Vec::new(env);
                members.push_back(account.clone());
                roles.push_back((role.clone(), members));
            }
        }

        env.storage().instance().set(&AUTH_ROLES, &roles);

        RoleGrantedEvent {
            role: role.clone(),
            account: account.clone(),
            sender: granter,
        }
        .publish(env);

        Ok(())
    }

    /// Revoke a role from an address.
    /// Requires owner or ADMIN role authorization.
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `sender` - Address claiming this revocation (same rules as [`grant_role`](Self::grant_role)).
    /// * `role` - The role to revoke
    /// * `account` - The address to revoke the role from
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AccessControl not initialized
    /// * `CallerNotAuthorized` - `sender` is neither the stored owner nor an `ADMIN` member (when an owner is configured)
    /// * `RoleNotGranted` - No owner configured and `sender` does not have `ADMIN`
    ///
    /// # Panics
    ///
    /// If `sender` is authorized to revoke but did not authorize this invocation (`require_auth`).
    pub fn revoke_role(
        env: &Env,
        sender: &Address,
        role: Symbol,
        account: &Address,
    ) -> Result<(), CCIPError> {
        if !Self::is_enabled(env) {
            return Err(CCIPError::FeatureNotEnabled);
        }

        // Require owner or ADMIN role
        let granter = Self::require_admin_or_owner(env, sender)?;

        // Get current roles
        let mut roles: Vec<(Symbol, Vec<Address>)> = env
            .storage()
            .instance()
            .get(&AUTH_ROLES)
            .unwrap_or(Vec::new(env));

        // Find the role entry
        for i in 0..roles.len() {
            let (r, members) = roles.get(i).unwrap();
            if r == role {
                let mut new_members: Vec<Address> = Vec::new(env);
                let mut found = false;

                for m in members.iter() {
                    if m == *account {
                        found = true;
                    } else {
                        new_members.push_back(m);
                    }
                }

                if found {
                    roles.set(i, (r, new_members));
                    env.storage().instance().set(&AUTH_ROLES, &roles);

                    RoleRevokedEvent {
                        role: role.clone(),
                        account: account.clone(),
                        sender: granter,
                    }
                    .publish(env);
                }

                break;
            }
        }

        Ok(())
    }

    /// Renounce a role (remove it from yourself).
    /// The caller must have the role and provide authorization.
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `role` - The role to renounce
    /// * `account` - The account renouncing (must match caller)
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AccessControl not initialized
    /// * `CannotRenounceRole` - Account doesn't have the role
    pub fn renounce_role(env: &Env, role: Symbol, account: &Address) -> Result<(), CCIPError> {
        if !Self::is_enabled(env) {
            return Err(CCIPError::FeatureNotEnabled);
        }

        // Account must authorize their own renouncement
        account.require_auth();

        if !Self::has_role(env, role.clone(), account) {
            return Err(CCIPError::CannotRenounceRole);
        }

        // Get current roles
        let mut roles: Vec<(Symbol, Vec<Address>)> = env
            .storage()
            .instance()
            .get(&AUTH_ROLES)
            .unwrap_or(Vec::new(env));

        // Find and remove from the role
        for i in 0..roles.len() {
            let (r, members) = roles.get(i).unwrap();
            if r == role {
                let mut new_members: Vec<Address> = Vec::new(env);

                for m in members.iter() {
                    if m != *account {
                        new_members.push_back(m);
                    }
                }

                roles.set(i, (r, new_members));
                break;
            }
        }

        env.storage().instance().set(&AUTH_ROLES, &roles);

        RoleRevokedEvent {
            role: role.clone(),
            account: account.clone(),
            sender: account.clone(),
        }
        .publish(env);

        Ok(())
    }

    /// Check if an address has a specific role.
    pub fn has_role(env: &Env, role: Symbol, account: &Address) -> bool {
        if !Self::is_enabled(env) {
            return false;
        }

        let roles: Vec<(Symbol, Vec<Address>)> = env
            .storage()
            .instance()
            .get(&AUTH_ROLES)
            .unwrap_or(Vec::new(env));

        for (r, members) in roles.iter() {
            if r == role {
                for m in members.iter() {
                    if m == *account {
                        return true;
                    }
                }
                break;
            }
        }

        false
    }

    /// Require that `subject` has a specific role and has authorized this invocation.
    ///
    /// Callers **must** pass the same [`Address`] as `subject` that will satisfy
    /// `subject.require_auth()` (same pattern as [`AuthorizedCallers::require_authorized_caller`]).
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `subject` - Address claiming membership in `role`
    /// * `role` - The required role
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AccessControl not initialized
    /// * `RoleNotGranted` - `subject` is not a member of `role`
    ///
    /// # Panics
    ///
    /// If `subject` has the role but did not authorize this invocation (`require_auth`).
    pub fn require_role(env: &Env, subject: &Address, role: Symbol) -> Result<Address, CCIPError> {
        if !Self::is_enabled(env) {
            return Err(CCIPError::FeatureNotEnabled);
        }

        if !Self::has_role(env, role, subject) {
            return Err(CCIPError::RoleNotGranted);
        }

        subject.require_auth();
        Ok(subject.clone())
    }

    /// Get all addresses that have a specific role.
    pub fn get_role_members(env: &Env, role: Symbol) -> Vec<Address> {
        if !Self::is_enabled(env) {
            return Vec::new(env);
        }

        let roles: Vec<(Symbol, Vec<Address>)> = env
            .storage()
            .instance()
            .get(&AUTH_ROLES)
            .unwrap_or(Vec::new(env));

        for (r, members) in roles.iter() {
            if r == role {
                return members;
            }
        }

        Vec::new(env)
    }

    /// Internal: Require `subject` to be the stored owner, or to hold `ADMIN`, or (if no owner) to hold `ADMIN` only.
    fn require_admin_or_owner(env: &Env, subject: &Address) -> Result<Address, CCIPError> {
        match DefaultOwnable::owner(env) {
            Some(owner) => {
                if owner == *subject {
                    subject.require_auth();
                    Ok(owner)
                } else if Self::has_role(env, ROLE_ADMIN, subject) {
                    subject.require_auth();
                    Ok(subject.clone())
                } else {
                    Err(CCIPError::CallerNotAuthorized)
                }
            }
            None => Self::require_role(env, subject, ROLE_ADMIN),
        }
    }
}

#[cfg(test)]
mod test;
