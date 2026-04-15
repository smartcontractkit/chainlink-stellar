#![no_std]

//! # Versioned Verifier Resolver
//!
//! A Soroban contract that maps verifier versions (4-byte prefixes) to inbound verifier
//! contract addresses, and destination chain selectors to outbound verifier contract addresses.
//!
//! This is the Stellar equivalent of the Solidity `VersionedVerifierResolver` contract.
//!
//! ## Storage Layout
//!
//! - `VERSION_TO_INBOUND` (Map<BytesN<4>, Address>): Maps a 4-byte version prefix to an inbound
//!   verifier contract address.
//! - `DEST_CHAIN_TO_OUTBOUND` (Map<u64, Address>): Maps a destination chain selector to an
//!   outbound verifier contract address.
//! - `SUPPORTED_VERSIONS` (Vec<BytesN<4>>): Set of registered verifier version prefixes.
//! - `SUPPORTED_DEST_CHAINS` (Vec<u64>): Set of supported destination chain selectors.
//! - `FEE_AGGREGATOR` (Address): The fee aggregator address.

pub mod events;
pub mod types;

use common_helpers::map_updater::MapUpdater;
use soroban_sdk::{
    contract, contractimpl, symbol_short, Address, Bytes, BytesN, Env, Map, Symbol, Vec,
};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use events::FeeAggregatorSetEvent;
pub use types::{
    InboundImplementationArgs, InboundImplementationUpdate, OutboundImplementationArgs,
    OutboundImplementationUpdate,
};

// ============================================================
// Storage Keys
// ============================================================

pub(crate) const INITIALIZED: Symbol = symbol_short!("INIT");
pub(crate) const OWNER: Symbol = symbol_short!("OWNER");
pub(crate) const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
pub(crate) const VER_INBOUND: Symbol = symbol_short!("VERINB");
pub(crate) const DEST_OUTBND: Symbol = symbol_short!("DESTOUT");
pub(crate) const SUP_VERS: Symbol = symbol_short!("SUPVERS");
pub(crate) const SUP_DESTS: Symbol = symbol_short!("SUPDEST");
pub(crate) const FEE_AGG: Symbol = symbol_short!("FEEAGG");

// ============================================================
// Contract
// ============================================================

#[contract]
pub struct VersionedVerifierResolverContract;

#[contractimpl]
impl Initializable for VersionedVerifierResolverContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for VersionedVerifierResolverContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl]
impl VersionedVerifierResolverContract {
    // ========================================
    // Initialization
    // ========================================

    /// Initialize the VersionedVerifierResolver contract.
    ///
    /// # Arguments
    /// * `owner` - The owner address
    /// * `fee_aggregator` - The fee aggregator address
    ///
    /// # Errors
    /// * `AlreadyInitialized` - If contract is already initialized
    pub fn initialize(env: Env, owner: Address, fee_aggregator: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;

        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as Initializable>::init(&env)?;

        // Initialize empty mappings
        let inbound_map: Map<BytesN<4>, Address> = Map::new(&env);
        env.storage().instance().set(&VER_INBOUND, &inbound_map);

        let outbound_map: Map<u64, Address> = Map::new(&env);
        env.storage().instance().set(&DEST_OUTBND, &outbound_map);

        let supported_versions: Vec<BytesN<4>> = Vec::new(&env);
        env.storage().instance().set(&SUP_VERS, &supported_versions);

        let supported_dests: Vec<u64> = Vec::new(&env);
        env.storage().instance().set(&SUP_DESTS, &supported_dests);

        env.storage().instance().set(&FEE_AGG, &fee_aggregator);

        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "VersionedVerifierResolver 1.0.0")
    }

    // ========================================
    // View Functions
    // ========================================

    /// Returns the inbound verifier implementation for the given verifier results.
    ///
    /// The first 4 bytes of `verifier_results` are used as the version key
    /// to look up the corresponding verifier contract address.
    ///
    /// # Arguments
    /// * `verifier_results` - The verifier results bytes (must be at least 4 bytes)
    ///
    /// # Returns
    /// The address of the inbound verifier contract
    ///
    /// # Errors
    /// * `InvalidVerifierResultsLength` - If verifier_results is shorter than 4 bytes
    /// * `InboundImplementationNotFound` - If no implementation is registered for the version
    pub fn get_inbound_implementation(
        env: Env,
        verifier_results: Bytes,
    ) -> Result<Address, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        if verifier_results.len() < 4 {
            return Err(CCIPError::InvalidVerifierResultsLength);
        }

        // Extract first 4 bytes as version
        let version: BytesN<4> = verifier_results
            .slice(0..4)
            .try_into()
            .expect("slice is 4 bytes");

        let inbound_map: Map<BytesN<4>, Address> = env
            .storage()
            .instance()
            .get(&VER_INBOUND)
            .unwrap_or(Map::new(&env));

        inbound_map
            .get(version)
            .ok_or(CCIPError::InboundImplementationNotFound)
    }

    /// Returns all registered inbound implementations.
    ///
    /// # Returns
    /// A vector of `InboundImplementationArgs` containing all version-to-verifier mappings
    pub fn get_all_inbound_implementations(env: Env) -> Vec<InboundImplementationArgs> {
        let supported_versions: Vec<BytesN<4>> = env
            .storage()
            .instance()
            .get(&SUP_VERS)
            .unwrap_or(Vec::new(&env));

        let inbound_map: Map<BytesN<4>, Address> = env
            .storage()
            .instance()
            .get(&VER_INBOUND)
            .unwrap_or(Map::new(&env));

        let mut result: Vec<InboundImplementationArgs> = Vec::new(&env);

        for version in supported_versions.iter() {
            if let Some(verifier) = inbound_map.get(version.clone()) {
                result.push_back(InboundImplementationArgs {
                    version: version.clone(),
                    verifier,
                });
            }
        }

        result
    }

    /// Returns the outbound verifier implementation for the given destination chain.
    ///
    /// # Arguments
    /// * `dest_chain_selector` - The destination chain selector
    /// * `extra_args` - Additional arguments (reserved for future use, currently ignored)
    ///
    /// # Returns
    /// The address of the outbound verifier contract
    ///
    /// # Errors
    /// * `OutboundImplementationNotFound` - If no implementation is registered for the chain
    pub fn get_outbound_implementation(
        env: Env,
        dest_chain_selector: u64,
        _extra_args: Bytes,
    ) -> Result<Address, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let outbound_map: Map<u64, Address> = env
            .storage()
            .instance()
            .get(&DEST_OUTBND)
            .unwrap_or(Map::new(&env));

        outbound_map
            .get(dest_chain_selector)
            .ok_or(CCIPError::OutboundImplementationNotFound)
    }

    /// Returns all registered outbound implementations.
    ///
    /// # Returns
    /// A vector of `OutboundImplementationArgs` containing all chain-to-verifier mappings
    pub fn get_all_outbound_implementations(env: Env) -> Vec<OutboundImplementationArgs> {
        let supported_dests: Vec<u64> = env
            .storage()
            .instance()
            .get(&SUP_DESTS)
            .unwrap_or(Vec::new(&env));

        let outbound_map: Map<u64, Address> = env
            .storage()
            .instance()
            .get(&DEST_OUTBND)
            .unwrap_or(Map::new(&env));

        let result = supported_dests.iter().filter_map(|selector| {
            outbound_map
                .get(selector)
                .map(|verifier| OutboundImplementationArgs {
                    dest_chain_selector: selector,
                    verifier,
                })
        });

        Vec::from_iter(&env, result.into_iter())
    }

    /// Returns the fee aggregator address.
    pub fn get_fee_aggregator(env: Env) -> Result<Address, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        env.storage()
            .instance()
            .get(&FEE_AGG)
            .ok_or(CCIPError::NotInitialized)
    }

    // ========================================
    // Admin Functions (Owner Only)
    // ========================================

    /// Apply a batch of inbound implementation updates atomically.
    ///
    /// For each entry in `implementations`:
    /// - If `verifier` is `None`: removes the mapping for that version
    /// - If `verifier` is `Some(address)`: sets/updates the mapping
    ///
    /// This mirrors the EVM `applyInboundImplementationUpdates` function.
    ///
    /// # Arguments
    /// * `implementations` - A vector of updates to apply
    ///
    /// # Errors
    /// * `NotInitialized` - If contract is not initialized
    /// * `Unauthorized` - If caller is not the owner
    /// * `InvalidVersion` - If version is all zeros when setting (not removing)
    pub fn apply_inbound_impl_updates(
        env: Env,
        implementations: Vec<InboundImplementationUpdate>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env).map_err(|_| CCIPError::Unauthorized)?;

        let inbound_map: Map<BytesN<4>, Address> = env
            .storage()
            .instance()
            .get(&VER_INBOUND)
            .unwrap_or(Map::new(&env));

        inbound_map.apply_updates(&env, &implementations)?;

        Ok(())
    }

    /// Apply a batch of outbound implementation updates atomically.
    ///
    /// For each entry in `implementations`:
    /// - If `verifier` is `None`: removes the mapping for that dest chain
    /// - If `verifier` is `Some(address)`: sets/updates the mapping
    ///
    /// This mirrors the EVM `applyOutboundImplementationUpdates` function.
    ///
    /// # Arguments
    /// * `implementations` - A vector of updates to apply
    ///
    /// # Errors
    /// * `NotInitialized` - If contract is not initialized
    /// * `Unauthorized` - If caller is not the owner
    /// * `InvalidChainSelector` - If dest_chain_selector is 0 when setting (not removing)
    pub fn apply_outbound_impl_updates(
        env: Env,
        implementations: Vec<OutboundImplementationUpdate>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env).map_err(|_| CCIPError::Unauthorized)?;

        let outbound_map: Map<u64, Address> = env
            .storage()
            .instance()
            .get(&DEST_OUTBND)
            .unwrap_or(Map::new(&env));

        // Note: this also stores the updates to storage based on the MapUpdater implementation
        // See `types.rs` in crate for the implementation details.
        outbound_map.apply_updates(&env, &implementations)?;

        Ok(())
    }

    /// Update the fee aggregator address.
    ///
    /// # Arguments
    /// * `fee_aggregator` - The new fee aggregator address
    ///
    /// # Errors
    /// * `NotInitialized` - If contract is not initialized
    /// * `Unauthorized` - If caller is not the owner
    pub fn set_fee_aggregator(env: Env, fee_aggregator: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        env.storage().instance().set(&FEE_AGG, &fee_aggregator);

        FeeAggregatorSetEvent { fee_aggregator }.publish(&env);

        Ok(())
    }
}

#[cfg(test)]
mod test;
