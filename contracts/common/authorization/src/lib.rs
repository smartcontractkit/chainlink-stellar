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
//! AuthorizedCallers::require_authorized(&env)?;
//! AccessControl::require_role(&env, symbol_short!("MINTER"))?;
//! ```

pub mod allowlist;
pub mod events;
pub mod ownable;

pub use events::*;
pub use ownable::{DefaultOwnable, Ownable};

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
/// If not initialized, `is_enabled()` returns false and `require_authorized()` will fail.
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
    /// * `NotInitialized` - Owner not set
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
    /// * `NotInitialized` - Owner not set
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

    /// Require that the caller is authorized.
    /// This finds an authorized caller that provided auth and validates it.
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AuthorizedCallers not initialized
    /// * `CallerNotAuthorized` - No authorized caller provided auth
    pub fn require_authorized(env: &Env) -> Result<Address, CCIPError> {
        if !Self::is_enabled(env) {
            return Err(CCIPError::FeatureNotEnabled);
        }

        let callers = Self::get_callers(env);

        // Try each authorized caller
        for caller in callers.iter() {
            caller.require_auth();
            return Ok(caller);
        }

        Err(CCIPError::CallerNotAuthorized)
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
    /// * `role` - The role to grant
    /// * `account` - The address to receive the role
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AccessControl not initialized
    /// * `NotInitialized` - Owner not set (if not using ADMIN)
    pub fn grant_role(env: &Env, role: Symbol, account: &Address) -> Result<(), CCIPError> {
        if !Self::is_enabled(env) {
            return Err(CCIPError::FeatureNotEnabled);
        }

        // Require owner or ADMIN role
        let sender = Self::require_admin_or_owner(env)?;

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
            sender,
        }
        .publish(env);

        Ok(())
    }

    /// Revoke a role from an address.
    /// Requires owner or ADMIN role authorization.
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `role` - The role to revoke
    /// * `account` - The address to revoke the role from
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AccessControl not initialized
    /// * `NotInitialized` - Owner not set (if not using ADMIN)
    pub fn revoke_role(env: &Env, role: Symbol, account: &Address) -> Result<(), CCIPError> {
        if !Self::is_enabled(env) {
            return Err(CCIPError::FeatureNotEnabled);
        }

        // Require owner or ADMIN role
        let sender = Self::require_admin_or_owner(env)?;

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
                        sender,
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

    /// Require that the caller has a specific role.
    ///
    /// # Arguments
    /// * `env` - The environment
    /// * `role` - The required role
    ///
    /// # Errors
    /// * `FeatureNotEnabled` - AccessControl not initialized
    /// * `RoleNotGranted` - Caller doesn't have the required role
    pub fn require_role(env: &Env, role: Symbol) -> Result<Address, CCIPError> {
        if !Self::is_enabled(env) {
            return Err(CCIPError::FeatureNotEnabled);
        }

        let members = Self::get_role_members(env, role.clone());

        // Try each member with the role
        for member in members.iter() {
            member.require_auth();
            return Ok(member);
        }

        Err(CCIPError::RoleNotGranted)
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

    /// Internal: Require owner or ADMIN role.
    fn require_admin_or_owner(env: &Env) -> Result<Address, CCIPError> {
        match DefaultOwnable::owner(env) {
            Some(owner) => {
                DefaultOwnable::require_owner(env)?;
                Ok(owner)
            }
            None => {
                let _admin_members = Self::get_role_members(env, ROLE_ADMIN);
                // TODO: need to invoke require_auth on only the address that matches the caller
                unimplemented!();
            }
        }
    }
}

#[cfg(test)]
mod test;
