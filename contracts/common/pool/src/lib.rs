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
use common_interfaces::token_pool::{
    LockOrBurnIn as IfaceLockOrBurnIn, MessageDirection as IfaceMessageDirection,
    ReleaseOrMintIn as IfaceReleaseOrMintIn,
};
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
    // Router / Ramp Gating (EVM `_onlyOnRamp` / `_onlyOffRamp`)
    // ------------------------------------------------------------------

    /// Store the router address. Owner-only — caller must enforce.
    /// On EVM this is part of `setDynamicConfig`.
    fn set_router(env: &Env, router: &Address) {
        env.storage().instance().set(&PoolDataKey::Router, router);
    }

    fn get_router(env: &Env) -> Option<Address> {
        env.storage().instance().get(&PoolDataKey::Router)
    }

    /// Require that the configured router has provided authorization in the
    /// current invocation's auth tree. This is the Soroban analogue of EVM's
    /// `_onlyOnRamp` / `_onlyOffRamp`: only calls originating from the
    /// router (which in turn is called by onramp/offramp) are accepted.
    ///
    /// If no router is configured the check is skipped (permissive — allows
    /// pools to work before router is wired up, matching existing behavior).
    fn require_router(env: &Env) -> Result<(), CCIPError> {
        if let Some(router) = env
            .storage()
            .instance()
            .get::<PoolDataKey, Address>(&PoolDataKey::Router)
        {
            router.require_auth();
        }
        Ok(())
    }

    // ------------------------------------------------------------------
    // Advanced Pool Hooks (EVM `IAdvancedPoolHooks`)
    // ------------------------------------------------------------------

    /// Set the advanced pool hooks contract address. Owner-only — caller must enforce.
    /// Pass a zero-like "none" to disable hooks (EVM `updateAdvancedPoolHooks`).
    fn set_advanced_pool_hooks(env: &Env, hooks: &Address) {
        let old_hooks = env
            .storage()
            .instance()
            .get::<PoolDataKey, Address>(&PoolDataKey::AdvancedPoolHooks);
        env.storage()
            .instance()
            .set(&PoolDataKey::AdvancedPoolHooks, hooks);
        AdvancedPoolHooksUpdatedEvent {
            old_hooks,
            new_hooks: Some(hooks.clone()),
        }
        .publish(env);
    }

    fn remove_advanced_pool_hooks(env: &Env) {
        let old_hooks = env
            .storage()
            .instance()
            .get::<PoolDataKey, Address>(&PoolDataKey::AdvancedPoolHooks);
        env.storage()
            .instance()
            .remove(&PoolDataKey::AdvancedPoolHooks);
        AdvancedPoolHooksUpdatedEvent {
            old_hooks,
            new_hooks: None,
        }
        .publish(env);
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
        lock_or_burn_in: &LockOrBurnIn,
        requested_finality: u32,
        amount: i128,
    ) -> Result<(), CCIPError> {
        if let Some(hooks_addr) = env
            .storage()
            .instance()
            .get::<PoolDataKey, Address>(&PoolDataKey::AdvancedPoolHooks)
        {
            let client = PoolHooksClient::new(env, &hooks_addr);
            let input = lock_or_burn_in_to_iface(lock_or_burn_in);
            // Hook failures abort the invocation at the host; the client returns `()`.
            client.preflight_check(&input, &requested_finality, &amount);
        }
        Ok(())
    }

    /// Post-flight hook: called before release_or_mint if a hooks contract is configured.
    /// Delegates to `PoolHooksClient::postflight_check`. No-op if hooks not set.
    fn postflight_check(
        env: &Env,
        release_or_mint_in: &ReleaseOrMintIn,
        local_amount: i128,
        requested_finality: u32,
    ) -> Result<(), CCIPError> {
        if let Some(hooks_addr) = env
            .storage()
            .instance()
            .get::<PoolDataKey, Address>(&PoolDataKey::AdvancedPoolHooks)
        {
            let client = PoolHooksClient::new(env, &hooks_addr);
            let input = release_or_mint_in_to_iface(release_or_mint_in);
            // Hook failures abort the invocation at the host; the client returns `()`.
            client.postflight_check(&input, &local_amount, &requested_finality);
        }
        Ok(())
    }

    /// Returns required CCV resolver addresses from advanced hooks, or empty if unset.
    fn get_required_ccvs(
        env: &Env,
        local_token: &Address,
        remote_chain_selector: u64,
        amount: i128,
        requested_finality: u32,
        extra_data: &Bytes,
        direction: &MessageDirection,
    ) -> Vec<Address> {
        if let Some(hooks_addr) = env
            .storage()
            .instance()
            .get::<PoolDataKey, Address>(&PoolDataKey::AdvancedPoolHooks)
        {
            let client = PoolHooksClient::new(env, &hooks_addr);
            let direction_iface = message_direction_to_iface(direction);
            return client.get_required_ccvs(
                local_token,
                &remote_chain_selector,
                &amount,
                &requested_finality,
                extra_data,
                &direction_iface,
            );
        }
        Vec::new(env)
    }
}

fn lock_or_burn_in_to_iface(input: &LockOrBurnIn) -> IfaceLockOrBurnIn {
    IfaceLockOrBurnIn {
        receiver: input.receiver.clone(),
        remote_chain_selector: input.remote_chain_selector,
        original_sender: input.original_sender.clone(),
        amount: input.amount,
        local_token: input.local_token.clone(),
        token_args: input.token_args.clone(),
    }
}

fn release_or_mint_in_to_iface(input: &ReleaseOrMintIn) -> IfaceReleaseOrMintIn {
    IfaceReleaseOrMintIn {
        original_sender: input.original_sender.clone(),
        remote_chain_selector: input.remote_chain_selector,
        receiver: input.receiver.clone(),
        amount: input.amount,
        local_token: input.local_token.clone(),
        source_pool_address: input.source_pool_address.clone(),
        source_pool_data: input.source_pool_data.clone(),
    }
}

fn message_direction_to_iface(d: &MessageDirection) -> IfaceMessageDirection {
    match d {
        MessageDirection::Outbound => IfaceMessageDirection::Outbound,
        MessageDirection::Inbound => IfaceMessageDirection::Inbound,
    }
}
