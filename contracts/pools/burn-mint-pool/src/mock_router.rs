//! Minimal Router mock for pool unit tests.
//!
//! Implements only the subset of `RouterInterface` that the pool's ramp-gating
//! logic calls: `get_onramp` and `is_offramp`.  Test setup registers OnRamp /
//! OffRamp addresses via `set_onramp` / `add_offramp`.
#![cfg(test)]

use common_error::CCIPError;
use soroban_sdk::{contract, contractimpl, contracttype, Address, Env};

#[contracttype]
enum Key {
    OnRamp(u64),
    OffRamp(u64, Address),
}

#[contract]
pub struct MockRouterContract;

#[contractimpl]
impl MockRouterContract {
    pub fn set_onramp(env: Env, dest_chain_selector: u64, onramp: Address) {
        env.storage()
            .instance()
            .set(&Key::OnRamp(dest_chain_selector), &onramp);
    }

    pub fn get_onramp(env: Env, dest_chain_selector: u64) -> Result<Address, CCIPError> {
        env.storage()
            .instance()
            .get(&Key::OnRamp(dest_chain_selector))
            .ok_or(CCIPError::UnsupportedDestinationChain)
    }

    pub fn add_offramp(env: Env, source_chain_selector: u64, offramp: Address) {
        env.storage()
            .instance()
            .set(&Key::OffRamp(source_chain_selector, offramp), &true);
    }

    pub fn is_offramp(
        env: Env,
        source_chain_selector: u64,
        offramp: Address,
    ) -> Result<bool, CCIPError> {
        Ok(env
            .storage()
            .instance()
            .get::<Key, bool>(&Key::OffRamp(source_chain_selector, offramp))
            .unwrap_or(false))
    }
}
