#![no_std]

mod events;

use common_interfaces::rmn_remote::RmnRemoteClient;
use soroban_sdk::{contract, contractimpl, symbol_short, Address, BytesN, Env, Symbol};

use common_authorization::{Ownable, Upgradeable};
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use events::RmnSetEvent;

// ============================================================
// Storage Keys
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const RMN: Symbol = symbol_short!("RMN");

// ============================================================
// Contract
// ============================================================

/// RMN Proxy contract.
///
/// Modeled after the EVM RMNProxy.sol (setARM/getARM/fallback pattern),
/// adapted for Soroban where we expose explicit methods instead of a fallback.
///
/// The proxy holds a stable address and points to the current RMN implementation.
/// Consumers (Router, TokenPools) call `is_cursed()` on the proxy, which will
/// delegate to the RMN Remote implementation once it exists.
#[contract]
pub struct RmnProxyContract;

#[contractimpl]
impl Initializable for RmnProxyContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl]
impl Ownable for RmnProxyContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl(contracttrait)]
impl Upgradeable for RmnProxyContract {}

#[contractimpl]
impl RmnProxyContract {
    pub fn owner(env: Env) -> Option<Address> {
        <Self as Ownable>::owner(&env)
    }

    pub fn transfer_ownership(env: Env, new_owner: Address) -> Result<(), CCIPError> {
        <Self as Ownable>::transfer_ownership(&env, &new_owner)
    }

    pub fn accept_ownership(env: Env) -> Result<(), CCIPError> {
        <Self as Ownable>::accept_ownership(&env)
    }

    // ========================================
    // Initialization
    // ========================================

    /// Initialize the RMN Proxy contract.
    ///
    /// # Arguments
    /// * `owner` - The owner address (typically MCMS)
    /// * `rmn` - The initial RMN implementation address
    ///
    /// # Errors
    /// * `AlreadyInitialized` - If contract is already initialized
    pub fn initialize(env: Env, owner: Address, rmn: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;

        // Initialize owner via shared authorization lib
        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as Initializable>::init(&env)?;

        // Store the RMN implementation address
        env.storage().instance().set(&RMN, &rmn);

        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "RmnProxy-dev 2.0.0")
    }

    // ========================================
    // RMN Proxy Functions
    // ========================================

    /// Set the RMN implementation address. Only callable by owner.
    ///
    /// Diverges deliberately from EVM `setARM(address arm)` (no validation there):
    /// rotation must preserve curse state exactly, so a repoint can never silently
    /// lift — or add — a pause.
    ///
    /// # Arguments
    /// * `rmn` - The new RMN implementation address
    ///
    /// # Errors
    /// * `NotInitialized` - If contract is not initialized
    /// * `Unauthorized` - If caller is not the owner
    /// * `InvalidConfig` - If the new RMN's cursed-subject set differs from the current one
    pub fn set_rmn(env: Env, rmn: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let old_rmn: Address = env
            .storage()
            .instance()
            .get(&RMN)
            .ok_or(CCIPError::NotInitialized)?;

        let old_client = RmnRemoteClient::new(&env, &old_rmn);
        let new_client = RmnRemoteClient::new(&env, &rmn);

        // Client calls panic (and abort this call) on any error: unreadable old RMN,
        // uninitialized or non-RMN new target — all fail closed.
        let old_subjects = old_client.get_cursed_subjects();
        let new_subjects = new_client.get_cursed_subjects();

        if old_subjects.len() != new_subjects.len()
            || old_subjects
                .iter()
                .any(|s| !new_subjects.contains(s.clone()))
        {
            return Err(CCIPError::InvalidConfig);
        }

        env.storage().instance().set(&RMN, &rmn);

        RmnSetEvent { rmn }.publish(&env);

        Ok(())
    }

    /// Get the current RMN implementation address.
    ///
    /// Equivalent to EVM's `getARM()`.
    ///
    /// # Returns
    /// The current RMN implementation address
    pub fn get_rmn(env: Env) -> Result<Address, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&RMN)
            .ok_or(CCIPError::NotInitialized)
    }

    /// Check if the network is globally cursed.
    ///
    /// # Returns
    /// `true` if the network is cursed and operations should be halted
    pub fn is_cursed(env: Env) -> Result<bool, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        // Delegate to RMN Remote when it exists
        let rmn: Address = env
            .storage()
            .instance()
            .get(&RMN)
            .ok_or(CCIPError::NotInitialized)?;

        let rmn_client = RmnRemoteClient::new(&env, &rmn);
        Ok(rmn_client.is_cursed())
    }
}

mod test;
