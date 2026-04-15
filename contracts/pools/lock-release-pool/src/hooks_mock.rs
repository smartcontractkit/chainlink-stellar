//! Test-only pool hooks contracts (see `pool_hooks` interface).
#![cfg(test)]

use soroban_sdk::{contract, contractimpl, Address, Env};

use common_interfaces::pool_hooks::CCIPError as HooksError;

/// `preflight_check` always returns `SenderNotAllowed`.
#[contract]
pub struct RejectingPreflightHooksContract;

#[contractimpl]
impl RejectingPreflightHooksContract {
    pub fn preflight_check(
        _env: Env,
        _original_sender: Address,
        _remote_chain_selector: u64,
        _amount: i128,
        _requested_finality: u32,
    ) -> Result<(), HooksError> {
        Err(HooksError::SenderNotAllowed)
    }

    pub fn postflight_check(
        _env: Env,
        _source_chain_selector: u64,
        _receiver: Address,
        _amount: i128,
        _requested_finality: u32,
    ) -> Result<(), HooksError> {
        Ok(())
    }
}

/// `postflight_check` always returns `InvalidConfig`.
#[contract]
pub struct RejectingPostflightHooksContract;

#[contractimpl]
impl RejectingPostflightHooksContract {
    pub fn preflight_check(
        _env: Env,
        _original_sender: Address,
        _remote_chain_selector: u64,
        _amount: i128,
        _requested_finality: u32,
    ) -> Result<(), HooksError> {
        Ok(())
    }

    pub fn postflight_check(
        _env: Env,
        _source_chain_selector: u64,
        _receiver: Address,
        _amount: i128,
        _requested_finality: u32,
    ) -> Result<(), HooksError> {
        Err(HooksError::InvalidConfig)
    }
}

/// No-op hooks (both checks succeed).
#[contract]
pub struct AcceptingPoolHooksContract;

#[contractimpl]
impl AcceptingPoolHooksContract {
    pub fn preflight_check(
        _env: Env,
        _original_sender: Address,
        _remote_chain_selector: u64,
        _amount: i128,
        _requested_finality: u32,
    ) -> Result<(), HooksError> {
        Ok(())
    }

    pub fn postflight_check(
        _env: Env,
        _source_chain_selector: u64,
        _receiver: Address,
        _amount: i128,
        _requested_finality: u32,
    ) -> Result<(), HooksError> {
        Ok(())
    }
}
