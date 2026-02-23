use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_helpers::validation::Validatable;
use soroban_sdk::{Address, Bytes, Env, IntoVal, Map, Symbol, TryFromVal, Val, Vec};

use common_authorization::allowlist::AllowListable;

pub trait RemoteChainConfigInterface: Validatable {
    /// Returns the fee data for the remote chain.
    ///
    /// # Returns
    ///
    /// A tuple containing the fee in USD cents, the gas for verification, and the payload size in bytes.
    fn get_fee_data(&self) -> (u32, u32, u32);

    fn remote_chain_selector(&self) -> u64;
}

pub trait BaseVerifier: Initializable + AllowListable {
    const STORAGE_LOCATIONS: Symbol;
    const REMOTE_CHAINS: Symbol;

    type RemoteChainConfig: RemoteChainConfigInterface
        + TryFromVal<Env, Val>
        + IntoVal<Env, Val>
        + Clone;

    fn init(env: &Env, storage_locations: &Vec<Bytes>) -> Result<(), CCIPError> {
        env.storage()
            .instance()
            .set(&Self::STORAGE_LOCATIONS, storage_locations);

        let remote_chains: Map<u64, Self::RemoteChainConfig> = Map::new(env);
        env.storage()
            .instance()
            .set(&Self::REMOTE_CHAINS, &remote_chains);

        Ok(())
    }

    // ========================================
    // ... methods implemented by concrete contracts
    // ========================================

    fn emit_remote_chain_config_set_event(env: &Env, remote_chain_config: &Self::RemoteChainConfig);

    // ========================================
    // ... method with default implementations
    // ========================================

    fn apply_remote_chain_config_updates(
        env: &Env,
        remote_chain_updates: &Vec<Self::RemoteChainConfig>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(env)?;
        // TODO: check if the caller is owner or authorized

        let mut remote_chains: Map<u64, Self::RemoteChainConfig> = env
            .storage()
            .instance()
            .get(&Self::REMOTE_CHAINS)
            .unwrap_or(Map::new(env));

        for update in remote_chain_updates.iter() {
            update.validate()?;

            remote_chains.set(update.remote_chain_selector(), update.clone());
            Self::emit_remote_chain_config_set_event(env, &update);
        }

        env.storage()
            .instance()
            .set(&Self::REMOTE_CHAINS, &remote_chains);

        Ok(())
    }

    fn get_remote_chain_config(
        env: &Env,
        remote_chain_selector: u64,
    ) -> Result<Self::RemoteChainConfig, CCIPError> {
        let remote_chains: Map<u64, Self::RemoteChainConfig> = env
            .storage()
            .instance()
            .get(&Self::REMOTE_CHAINS)
            .unwrap_or(Map::new(env));

        remote_chains
            .get(remote_chain_selector)
            .ok_or(CCIPError::RemoteChainNotSupported)
    }

    fn get_fee(env: &Env, dest_chain_selector: u64) -> Result<(u32, u32, u32), CCIPError> {
        let cfg = Self::get_remote_chain_config(env, dest_chain_selector)?;
        Ok(cfg.get_fee_data())
    }
}
