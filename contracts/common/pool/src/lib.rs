#![no_std]

pub mod decimals;
pub mod events;
pub mod finality_codec;
pub mod rate_limit;
pub mod types;

#[cfg(test)]
mod decimals_tests;

#[cfg(test)]
mod finality_codec_tests;

#[cfg(test)]
mod rate_limit_tests;

pub use decimals::*;
pub use events::*;
pub use types::*;

use common_error::CCIPError;
use common_interfaces::pool_hooks::PoolHooksClient;
use common_interfaces::router::RouterClient;
use soroban_sdk::{contracttrait, Address, Bytes, Env, Vec};

pub use types::PoolFeeResult;

/// Base token pool trait providing shared pool configuration and chain management.
///
/// Concrete pool contracts (LockRelease, BurnMint) implement this trait
/// alongside their specific `lock_or_burn` / `release_or_mint` logic.
/// Ownership checks (via `Ownable`) are handled by the concrete contract
/// `#[contractimpl]` blocks, not enforced here.
///
/// Modeled after the EVM `TokenPool.sol` shared configuration surface.
#[contracttrait]
pub trait BaseTokenPool {
    // ------------------------------------------------------------------
    // Initialization
    // ------------------------------------------------------------------

    fn init_pool(env: &Env, token: &Address, token_decimals: u32) -> Result<(), CCIPError> {
        if token_decimals > u8::MAX as u32 {
            return Err(CCIPError::InvalidPoolTokenDecimals);
        }
        env.storage().instance().set(&PoolDataKey::Token, token);
        env.storage()
            .instance()
            .set(&PoolDataKey::TokenDecimals, &token_decimals);
        let chains: Vec<u64> = Vec::new(env);
        env.storage()
            .instance()
            .set(&PoolDataKey::SupportedChains, &chains);
        Ok(())
    }

    // ------------------------------------------------------------------
    // View Functions
    // ------------------------------------------------------------------

    fn get_token(env: &Env) -> Result<Address, CCIPError> {
        env.storage()
            .instance()
            .get(&PoolDataKey::Token)
            .ok_or(CCIPError::NotInitialized)
    }

    fn get_token_decimals(env: &Env) -> Result<u32, CCIPError> {
        env.storage()
            .instance()
            .get(&PoolDataKey::TokenDecimals)
            .ok_or(CCIPError::NotInitialized)
    }

    fn is_supported_token(env: &Env, token: &Address) -> Result<bool, CCIPError> {
        let pool_token = Self::get_token(env)?;
        Ok(pool_token == *token)
    }

    fn is_supported_chain(env: &Env, remote_chain_selector: u64) -> Result<bool, CCIPError> {
        let chains: Vec<u64> = env
            .storage()
            .instance()
            .get(&PoolDataKey::SupportedChains)
            .unwrap_or(Vec::new(env));
        for chain in chains.iter() {
            if chain == remote_chain_selector {
                return Ok(true);
            }
        }
        Ok(false)
    }

    fn get_remote_pool(env: &Env, remote_chain_selector: u64) -> Result<Bytes, CCIPError> {
        let config: RemoteChainConfig = env
            .storage()
            .persistent()
            .get(&PoolDataKey::RemoteChainConfig(remote_chain_selector))
            .ok_or(CCIPError::ChainNotSupported)?;
        Ok(config.remote_pool_address)
    }

    fn get_remote_token(env: &Env, remote_chain_selector: u64) -> Result<Bytes, CCIPError> {
        let config: RemoteChainConfig = env
            .storage()
            .persistent()
            .get(&PoolDataKey::RemoteChainConfig(remote_chain_selector))
            .ok_or(CCIPError::ChainNotSupported)?;
        Ok(config.remote_token_address)
    }

    /// Return the current outbound + inbound rate limiter state for a chain,
    /// with time-based refill applied. Mirrors EVM `getCurrentRateLimiterState`.
    ///
    /// TODO: When the FTF outbound simplification is applied, the
    /// `fast_finality=true` branch should only return FTF inbound state.
    /// FTF outbound state is meaningless on Stellar (no reorg risk).
    fn get_current_rate_limiter_state(
        env: &Env,
        remote_chain_selector: u64,
        fast_finality: bool,
    ) -> RateLimiterState {
        if fast_finality {
            RateLimiterState {
                outbound: rate_limit::current_state(
                    env,
                    &PoolDataKey::FtfOutboundRateLimit(remote_chain_selector),
                ),
                inbound: rate_limit::current_state(
                    env,
                    &PoolDataKey::FtfInboundRateLimit(remote_chain_selector),
                ),
            }
        } else {
            RateLimiterState {
                outbound: rate_limit::current_state(
                    env,
                    &PoolDataKey::OutboundRateLimit(remote_chain_selector),
                ),
                inbound: rate_limit::current_state(
                    env,
                    &PoolDataKey::InboundRateLimit(remote_chain_selector),
                ),
            }
        }
    }

    fn get_rate_limit_admin(env: &Env) -> Option<Address> {
        env.storage().instance().get(&PoolDataKey::RateLimitAdmin)
    }

    fn get_allowed_finality_config(env: &Env) -> u32 {
        env.storage()
            .instance()
            .get(&PoolDataKey::AllowedFinalityConfig)
            .unwrap_or(finality_codec::WAIT_FOR_FINALITY_FLAG)
    }

    // ------------------------------------------------------------------
    // Pool Fee
    // ------------------------------------------------------------------

    /// Returns the pool fee for transfers to the given chain. Returns 0 if no
    /// per-chain fee is configured.
    fn get_fee(env: &Env, remote_chain_selector: u64) -> Result<PoolFeeResult, CCIPError> {
        let config: Option<PoolFeeConfig> = env
            .storage()
            .persistent()
            .get(&PoolDataKey::PoolFeeConfig(remote_chain_selector));

        match config {
            Some(c) if c.is_enabled => Ok(PoolFeeResult {
                fee_usd_cents: c.fee_usd_cents,
            }),
            _ => Ok(PoolFeeResult { fee_usd_cents: 0 }),
        }
    }

    /// Set the per-chain pool fee config. Caller must enforce owner-only.
    fn set_pool_fee_config(
        env: &Env,
        remote_chain_selector: u64,
        config: &PoolFeeConfig,
    ) -> Result<(), CCIPError> {
        if !Self::is_supported_chain(env, remote_chain_selector)? {
            return Err(CCIPError::ChainNotSupported);
        }
        env.storage()
            .persistent()
            .set(&PoolDataKey::PoolFeeConfig(remote_chain_selector), config);
        Ok(())
    }

    // ------------------------------------------------------------------
    // Chain Configuration (owner check done by caller)
    // ------------------------------------------------------------------

    fn apply_chain_updates(
        env: &Env,
        adds: Vec<ChainUpdate>,
        removes: Vec<u64>,
    ) -> Result<(), CCIPError> {
        let mut chains: Vec<u64> = env
            .storage()
            .instance()
            .get(&PoolDataKey::SupportedChains)
            .unwrap_or(Vec::new(env));

        for selector in removes.iter() {
            env.storage()
                .persistent()
                .remove(&PoolDataKey::RemoteChainConfig(selector));

            rate_limit::remove_buckets(env, selector);

            let mut new_chains: Vec<u64> = Vec::new(env);
            for c in chains.iter() {
                if c != selector {
                    new_chains.push_back(c);
                }
            }
            chains = new_chains;

            ChainRemovedEvent {
                remote_chain_selector: selector,
            }
            .publish(env);
        }

        for update in adds.iter() {
            let config = RemoteChainConfig {
                remote_pool_address: update.remote_pool_addresses.clone(),
                remote_token_address: update.remote_token_address.clone(),
            };
            env.storage().persistent().set(
                &PoolDataKey::RemoteChainConfig(update.remote_chain_selector),
                &config,
            );

            rate_limit::set_config(
                env,
                &PoolDataKey::OutboundRateLimit(update.remote_chain_selector),
                &update.outbound_rate_limiter_config,
            )?;
            rate_limit::set_config(
                env,
                &PoolDataKey::InboundRateLimit(update.remote_chain_selector),
                &update.inbound_rate_limiter_config,
            )?;

            let mut already_listed = false;
            for c in chains.iter() {
                if c == update.remote_chain_selector {
                    already_listed = true;
                    break;
                }
            }
            if !already_listed {
                chains.push_back(update.remote_chain_selector);
            }

            ChainConfiguredEvent {
                remote_chain_selector: update.remote_chain_selector,
                remote_pool_address: update.remote_pool_addresses.clone(),
                remote_token_address: update.remote_token_address.clone(),
                outbound_rate_limiter_config: update.outbound_rate_limiter_config.clone(),
                inbound_rate_limiter_config: update.inbound_rate_limiter_config.clone(),
            }
            .publish(env);
        }

        env.storage()
            .instance()
            .set(&PoolDataKey::SupportedChains, &chains);

        Ok(())
    }

    // ------------------------------------------------------------------
    // Rate Limit Configuration (auth check done by caller)
    // ------------------------------------------------------------------

    /// Update outbound/inbound rate limit configs for a supported chain.
    /// When `fast_finality` is true, sets the FTF buckets; otherwise the default buckets.
    /// Mirrors EVM `setRateLimitConfig`. Caller must enforce owner-or-admin.
    ///
    /// TODO: When the FTF outbound simplification is applied, the `fast_finality`
    /// path should only allow setting FTF inbound config. FTF outbound config is
    /// not meaningful on Stellar. Consider rejecting non-disabled FTF outbound
    /// config or splitting into separate inbound/outbound setters.
    fn set_rate_limit_config(
        env: &Env,
        remote_chain_selector: u64,
        outbound_config: RateLimitConfig,
        inbound_config: RateLimitConfig,
        fast_finality: bool,
    ) -> Result<(), CCIPError> {
        if !Self::is_supported_chain(env, remote_chain_selector)? {
            return Err(CCIPError::ChainNotSupported);
        }

        if fast_finality {
            rate_limit::set_config(
                env,
                &PoolDataKey::FtfOutboundRateLimit(remote_chain_selector),
                &outbound_config,
            )?;
            rate_limit::set_config(
                env,
                &PoolDataKey::FtfInboundRateLimit(remote_chain_selector),
                &inbound_config,
            )?;
        } else {
            rate_limit::set_config(
                env,
                &PoolDataKey::OutboundRateLimit(remote_chain_selector),
                &outbound_config,
            )?;
            rate_limit::set_config(
                env,
                &PoolDataKey::InboundRateLimit(remote_chain_selector),
                &inbound_config,
            )?;
        }

        RateLimitConfiguredEvent {
            remote_chain_selector,
            fast_finality,
            outbound_config,
            inbound_config,
        }
        .publish(env);

        Ok(())
    }

    /// Set the rate limit admin address. Owner-only — caller must enforce.
    fn set_rate_limit_admin(env: &Env, admin: &Address) {
        env.storage()
            .instance()
            .set(&PoolDataKey::RateLimitAdmin, admin);
    }

    /// Set the allowed finality configuration. Owner-only — caller must enforce.
    /// Mirrors EVM `setAllowedFinalityConfig`.
    fn set_allowed_finality_config(env: &Env, allowed_finality: u32) {
        env.storage()
            .instance()
            .set(&PoolDataKey::AllowedFinalityConfig, &allowed_finality);

        FinalityConfigSetEvent { allowed_finality }.publish(env);
    }

    // ------------------------------------------------------------------
    // Router & ramp gating (EVM `_onlyOnRamp` / `_onlyOffRamp`)
    // ------------------------------------------------------------------

    /// Store the router address. Owner-only — caller must enforce.
    fn set_router(env: &Env, router: &Address) {
        env.storage().instance().set(&PoolDataKey::Router, router);
    }

    fn get_router(env: &Env) -> Option<Address> {
        env.storage().instance().get(&PoolDataKey::Router)
    }

    /// Soroban analogue of EVM `_onlyOnRamp(remoteChainSelector)`.
    ///
    /// Queries the Router for the registered OnRamp for `dest_chain_selector`,
    /// checks that the supplied `on_ramp` matches, and calls `require_auth()`
    /// to bind it into the Soroban authorization tree.  The `on_ramp` parameter
    /// is the Soroban equivalent of EVM's `msg.sender`.
    fn require_on_ramp(
        env: &Env,
        on_ramp: &Address,
        dest_chain_selector: u64,
    ) -> Result<(), CCIPError> {
        let router = Self::get_router(env).ok_or(CCIPError::NotInitialized)?;
        let router_client = RouterClient::new(env, &router);
        let expected = router_client.get_onramp(&dest_chain_selector);
        if on_ramp != &expected {
            return Err(CCIPError::CallerNotAuthorized);
        }
        on_ramp.require_auth();
        Ok(())
    }

    /// Soroban analogue of EVM `_onlyOffRamp()`.
    ///
    /// Queries the Router to verify `off_ramp` is a registered OffRamp for
    /// `source_chain_selector`, then calls `require_auth()`.
    fn require_off_ramp(
        env: &Env,
        off_ramp: &Address,
        source_chain_selector: u64,
    ) -> Result<(), CCIPError> {
        let router = Self::get_router(env).ok_or(CCIPError::NotInitialized)?;
        let router_client = RouterClient::new(env, &router);
        if !router_client.is_offramp(&source_chain_selector, off_ramp) {
            return Err(CCIPError::CallerNotAuthorized);
        }
        off_ramp.require_auth();
        Ok(())
    }

    // ------------------------------------------------------------------
    // Advanced Pool Hooks (EVM `IAdvancedPoolHooks`)
    // ------------------------------------------------------------------

    /// Set the advanced pool hooks contract address. Owner-only — caller must enforce.
    /// Pass a zero-like "none" to disable hooks (EVM `updateAdvancedPoolHooks`).
    fn set_advanced_pool_hooks(env: &Env, hooks: &Address) {
        env.storage()
            .instance()
            .set(&PoolDataKey::AdvancedPoolHooks, hooks);
    }

    fn remove_advanced_pool_hooks(env: &Env) {
        env.storage()
            .instance()
            .remove(&PoolDataKey::AdvancedPoolHooks);
    }

    fn get_advanced_pool_hooks(env: &Env) -> Option<Address> {
        env.storage()
            .instance()
            .get(&PoolDataKey::AdvancedPoolHooks)
    }

    /// Pre-flight hook: called before lock_or_burn if a hooks contract is configured.
    /// Delegates to `PoolHooksClient::preflight_check`. No-op if hooks not set.
    fn preflight_check(
        env: &Env,
        original_sender: &Address,
        remote_chain_selector: u64,
        amount: i128,
        requested_finality: u32,
    ) -> Result<(), CCIPError> {
        if let Some(hooks_addr) = env
            .storage()
            .instance()
            .get::<PoolDataKey, Address>(&PoolDataKey::AdvancedPoolHooks)
        {
            let client = PoolHooksClient::new(env, &hooks_addr);
            client.preflight_check(
                original_sender,
                &remote_chain_selector,
                &amount,
                &requested_finality,
            );
        }
        Ok(())
    }

    /// Post-flight hook: called before release_or_mint if a hooks contract is configured.
    /// Delegates to `PoolHooksClient::postflight_check`. No-op if hooks not set.
    fn postflight_check(
        env: &Env,
        source_chain_selector: u64,
        receiver: &Address,
        amount: i128,
        requested_finality: u32,
    ) -> Result<(), CCIPError> {
        if let Some(hooks_addr) = env
            .storage()
            .instance()
            .get::<PoolDataKey, Address>(&PoolDataKey::AdvancedPoolHooks)
        {
            let client = PoolHooksClient::new(env, &hooks_addr);
            client.postflight_check(
                &source_chain_selector,
                receiver,
                &amount,
                &requested_finality,
            );
        }
        Ok(())
    }
}
