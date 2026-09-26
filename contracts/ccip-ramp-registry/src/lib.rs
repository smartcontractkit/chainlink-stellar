#![no_std]

//! # CCIP Ramp Registry
//!
//! Stores CCIP OnRamp / OffRamp configuration separately from the Router so token pools
//! can authorize ramp callers without re-entering the Router during outbound sends.
//!
//! ## Storage Layout
//!
//! - `ONRAMPS` (`Map<u64, Address>`, persistent): destination chain selector → onramp address.
//! - `ONRAMP_KEYS` (`Vec<u64>`, persistent): ordered set of registered destination selectors.
//! - `OFFRAMPS` (`Map<OffRampKey, ()>`, persistent): registered (source_chain, offramp) pairs.
//! - `OFFRAMP_KEYS` (`Vec<OffRampKey>`, persistent): ordered set of all registrations.

mod events;
mod types;

use common_helpers::map_updater::MapUpdater;
use soroban_sdk::{contract, contractimpl, symbol_short, Address, BytesN, Env, Map, Symbol, Vec};

use common_authorization::{Ownable, Upgradeable};
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use types::OffRampKey;
pub use types::{OffRampEntry, OffRampUpdate, OnRampEntry, OnRampUpdate};

pub(crate) const INITIALIZED: Symbol = symbol_short!("INIT");
pub(crate) const OWNER: Symbol = symbol_short!("OWNER");
pub(crate) const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
pub(crate) const ONRAMPS: Symbol = symbol_short!("ONRAMPS");
pub(crate) const ONRAMP_KEYS: Symbol = symbol_short!("ONRAMPKS");
pub(crate) const OFFRAMPS: Symbol = symbol_short!("OFFRAMPS");
pub(crate) const OFFRAMP_KEYS: Symbol = symbol_short!("OFFRMPKS");

#[contract]
pub struct RampRegistryContract;

#[contractimpl]
impl Initializable for RampRegistryContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for RampRegistryContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl(contracttrait)]
impl Upgradeable for RampRegistryContract {}

#[contractimpl]
impl RampRegistryContract {
    /// Initialize the registry. Owner-only mutations mirror the Router ramp admin API.
    pub fn initialize(env: Env, owner: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as Initializable>::init(&env)?;

        let onramps: Map<u64, Address> = Map::new(&env);
        env.storage().persistent().set(&ONRAMPS, &onramps);
        let onramp_keys: Vec<u64> = Vec::new(&env);
        env.storage().persistent().set(&ONRAMP_KEYS, &onramp_keys);

        let offramps: Map<OffRampKey, ()> = Map::new(&env);
        env.storage().persistent().set(&OFFRAMPS, &offramps);
        let offramp_keys: Vec<OffRampKey> = Vec::new(&env);
        env.storage().persistent().set(&OFFRAMP_KEYS, &offramp_keys);

        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "RampRegistry 2.0.0")
    }

    /// Returns the onramp registered for the given destination chain.
    pub fn get_onramp(env: Env, dest_chain_selector: u64) -> Result<Address, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let onramps: Map<u64, Address> = env
            .storage()
            .persistent()
            .get(&ONRAMPS)
            .ok_or(CCIPError::UnsupportedDestinationChain)?;

        onramps
            .get(dest_chain_selector)
            .ok_or(CCIPError::UnsupportedDestinationChain)
    }

    /// Returns whether `offramp` is registered for the given source chain.
    pub fn is_offramp(
        env: Env,
        source_chain_selector: u64,
        offramp: Address,
    ) -> Result<bool, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let offramps: Map<OffRampKey, ()> = env
            .storage()
            .persistent()
            .get(&OFFRAMPS)
            .unwrap_or(Map::new(&env));

        Ok(offramps.contains_key(OffRampKey {
            source_chain_selector,
            offramp,
        }))
    }

    pub fn get_onramps(env: Env) -> Result<Vec<OnRampEntry>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let onramp_keys: Vec<u64> = env
            .storage()
            .persistent()
            .get(&ONRAMP_KEYS)
            .unwrap_or(Vec::new(&env));

        let onramps: Map<u64, Address> = env
            .storage()
            .persistent()
            .get(&ONRAMPS)
            .unwrap_or(Map::new(&env));

        let mut result: Vec<OnRampEntry> = Vec::new(&env);
        for dest_chain_selector in onramp_keys.iter() {
            if let Some(onramp) = onramps.get(dest_chain_selector) {
                result.push_back(OnRampEntry {
                    dest_chain_selector,
                    onramp,
                });
            }
        }

        Ok(result)
    }

    pub fn get_offramps(env: Env) -> Result<Vec<OffRampEntry>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let offramp_keys: Vec<OffRampKey> = env
            .storage()
            .persistent()
            .get(&OFFRAMP_KEYS)
            .unwrap_or(Vec::new(&env));

        let mut result: Vec<OffRampEntry> = Vec::new(&env);
        for key in offramp_keys.iter() {
            result.push_back(OffRampEntry {
                source_chain_selector: key.source_chain_selector,
                offramp: key.offramp,
            });
        }

        Ok(result)
    }

    /// Apply a batch of onramp updates atomically.
    ///
    /// For each entry: `onramp = Some(addr)` sets the entry, `onramp = None` removes it.
    pub fn apply_onramp_updates(env: Env, updates: Vec<OnRampUpdate>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let onramps: Map<u64, Address> = env
            .storage()
            .persistent()
            .get(&ONRAMPS)
            .unwrap_or(Map::new(&env));

        onramps.apply_updates(&env, &updates)?;

        Ok(())
    }

    /// Apply a batch of offramp updates atomically.
    ///
    /// Each update targets a single (source_chain, offramp) pair:
    /// `enabled = Some(())` registers the offramp, `enabled = None` removes it.
    pub fn apply_offramp_updates(env: Env, updates: Vec<OffRampUpdate>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let offramps: Map<OffRampKey, ()> = env
            .storage()
            .persistent()
            .get(&OFFRAMPS)
            .unwrap_or(Map::new(&env));

        offramps.apply_updates(&env, &updates)?;

        Ok(())
    }
}

#[cfg(test)]
mod test;
