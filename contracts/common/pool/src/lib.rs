#![no_std]

pub mod decimals;
pub mod events;
pub mod rate_limit;
pub mod types;

// `finality_codec` lives in `common-helpers` so the ramps can use it without a
// hard dependency on this pool-implementation crate. Re-exported here for
// back-compat with existing pool imports (`common_pool::finality_codec`).
pub use common_helpers::finality_codec;

#[cfg(test)]
mod decimals_tests;

#[cfg(test)]
mod rate_limit_tests;

pub use decimals::*;
pub use events::*;
pub use types::*;

use common_error::CCIPError;
use common_interfaces::pool_hooks::PoolHooksClient;
use common_interfaces::ramp_registry::RampRegistryClient;
use common_interfaces::rmn_proxy::RmnProxyClient;
use common_interfaces::rmn_remote::RmnRemoteClient;
use common_interfaces::token_pool::{
    LockOrBurnIn as IfaceLockOrBurnIn, MessageDirection as IfaceMessageDirection,
    PoolRequiredCCVs as IfacePoolRequiredCCVs, ReleaseOrMintIn as IfaceReleaseOrMintIn,
};
use soroban_sdk::{contracttrait, Address, Bytes, BytesN, Env, Vec, U256};

pub use types::{
    LockBoxEntry, PoolFeeResult, PoolRequiredCCVs, TokenTransferFeeConfig,
    TokenTransferFeeConfigArgs, BPS_DIVIDER,
};

/// Maps the interface `ramp_registry::CCIPError` to `common_error::CCIPError`.
///
/// The interface re-exports `common_error::CCIPError`, so this is the identity conversion in
/// practice; it is kept as a guard in case the generated interface ever diverges again.
#[inline]
fn ramp_registry_ccip_error_to_common(e: common_error::CCIPError) -> CCIPError {
    let d = e as u32;
    // SAFETY: interface `CCIPError` discriminants are generated to match `common_error::CCIPError`
    // (`#[repr(u32)]` on both). Invalid values cannot be produced by an on-chain contract return.
    unsafe { core::mem::transmute::<u32, CCIPError>(d) }
}

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

    /// True iff `source_pool_address` is the configured remote pool for
    /// `remote_chain_selector`. Mirrors EVM `TokenPool.isRemotePool`
    /// (`pools/TokenPool.sol:601`), used by `_validateReleaseOrMint` to reject
    /// inbound messages whose claimed source pool is not configured here.
    ///
    /// Today the store holds a single pool per chain, so this is an equality
    /// check. When H-14 widens `RemoteChainConfig.remote_pool_address` to a
    /// per-chain set (`Vec<Bytes>`), only this body changes to set membership —
    /// the call sites and the `InvalidSourcePoolAddress` revert stay identical.
    fn is_remote_source_pool(
        env: &Env,
        remote_chain_selector: u64,
        source_pool_address: &Bytes,
    ) -> Result<bool, CCIPError> {
        let configured = Self::get_remote_pool(env, remote_chain_selector)?;
        Ok(configured == *source_pool_address)
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
    // Pool Fee (EVM TokenPool fee-model parity)
    // ------------------------------------------------------------------

    /// Returns the pool fee parameters for a transfer to `dest_chain_selector`
    /// (EVM `TokenPool.getFee`). Resolves the flat USD-cent fee and the bps fee
    /// for the requested finality (finality vs fast-finality). When no config is
    /// stored or it is disabled, returns all-zeros with `is_enabled: false` so
    /// the OnRamp falls back to the FeeQuoter's `get_token_transfer_fee` (EVM
    /// `OnRamp._getReceipts` L1047-1053 parity). EVM's `localToken`/`feeToken`
    /// args are omitted because the base body ignores them. Concrete wrappers
    /// expose this as the `get_fee` entrypoint.
    fn get_fee(
        env: &Env,
        dest_chain_selector: u64,
        _amount: i128,
        requested_finality: u32,
        _token_args: &Bytes,
    ) -> Result<PoolFeeResult, CCIPError> {
        finality_codec::ensure_requested_finality_allowed(
            requested_finality,
            Self::get_allowed_finality_config(env),
        )?;

        let config = Self::get_token_transfer_fee_config(env, dest_chain_selector)?;
        if !config.is_enabled {
            return Ok(PoolFeeResult {
                fee_usd_cents: 0,
                dest_gas_overhead: 0,
                dest_bytes_overhead: 0,
                token_fee_bps: 0,
                is_enabled: false,
            });
        }

        if finality_codec::is_fast_finality(requested_finality) {
            Ok(PoolFeeResult {
                fee_usd_cents: config.fast_finality_fee_usd_cents,
                dest_gas_overhead: config.dest_gas_overhead,
                dest_bytes_overhead: config.dest_bytes_overhead,
                token_fee_bps: config.fast_finality_transfer_fee_bps,
                is_enabled: true,
            })
        } else {
            Ok(PoolFeeResult {
                fee_usd_cents: config.finality_fee_usd_cents,
                dest_gas_overhead: config.dest_gas_overhead,
                dest_bytes_overhead: config.dest_bytes_overhead,
                token_fee_bps: config.finality_transfer_fee_bps,
                is_enabled: true,
            })
        }
    }

    /// Returns the stored token-transfer fee config for a destination chain, or
    /// a disabled config when none is set (EVM `TokenPool.getTokenTransferFeeConfig`).
    fn get_token_transfer_fee_config(
        env: &Env,
        dest_chain_selector: u64,
    ) -> Result<TokenTransferFeeConfig, CCIPError> {
        Ok(env
            .storage()
            .persistent()
            .get(&PoolDataKey::TokenTransferFeeConfig(dest_chain_selector))
            .unwrap_or_else(TokenTransferFeeConfig::disabled))
    }

    /// Applies a batch of token-transfer fee config additions and disables
    /// (EVM `TokenPool.applyTokenTransferFeeConfigUpdates`). Owner-only — the
    /// concrete wrapper enforces that. Adds reject `is_enabled == false` (use
    /// the disable list), bps >= `BPS_DIVIDER`, and `dest_gas_overhead == 0`,
    /// and require the chain to be supported. Disables delete the stored entry.
    fn apply_token_fee_config_updates(
        env: &Env,
        adds: &Vec<TokenTransferFeeConfigArgs>,
        disables: &Vec<u64>,
    ) -> Result<(), CCIPError> {
        for args in adds.iter() {
            if !Self::is_supported_chain(env, args.dest_chain_selector)? {
                return Err(CCIPError::ChainNotSupported);
            }
            let c = &args.config;
            // Reject configs with isEnabled: false - use the disable list instead.
            if !c.is_enabled {
                return Err(CCIPError::InvalidTokenTransferFeeConfig);
            }
            if c.finality_transfer_fee_bps >= BPS_DIVIDER {
                return Err(CCIPError::InvalidTransferFeeBps);
            }
            if c.fast_finality_transfer_fee_bps >= BPS_DIVIDER {
                return Err(CCIPError::InvalidTransferFeeBps);
            }
            // Gas overhead must be non-zero for proper fee accounting.
            if c.dest_gas_overhead == 0 {
                return Err(CCIPError::InvalidTokenTransferFeeConfig);
            }
            env.storage().persistent().set(
                &PoolDataKey::TokenTransferFeeConfig(args.dest_chain_selector),
                c,
            );
            TokenFeeCfgUpdatedEvent {
                remote_chain_selector: args.dest_chain_selector,
                config: c.clone(),
            }
            .publish(env);
        }
        for selector in disables.iter() {
            env.storage()
                .persistent()
                .remove(&PoolDataKey::TokenTransferFeeConfig(selector));
            TokenFeeCfgDeletedEvent {
                remote_chain_selector: selector,
            }
            .publish(env);
        }
        Ok(())
    }

    /// Sweeps the pool's full balance of each `fee_token` to `recipient`
    /// (EVM `TokenPool.withdrawFeeTokens`). The owner-or-feeAdmin gate is
    /// enforced by the concrete wrapper. Safe to sweep the full balance because
    /// user liquidity is escrowed in a lockbox (lock-release) or burned
    /// (burn-mint), so the pool's own token balance equals only accrued fees.
    fn withdraw_fee_tokens(
        env: &Env,
        fee_tokens: &Vec<Address>,
        recipient: &Address,
    ) -> Result<(), CCIPError> {
        let pool_address = env.current_contract_address();
        for token in fee_tokens.iter() {
            let token_client = soroban_sdk::token::Client::new(env, &token);
            let balance = token_client.balance(&pool_address);
            if balance > 0 {
                token_client.transfer(&pool_address, recipient, &balance);
            }
        }
        Ok(())
    }

    /// Sets the fee-admin address (EVM parity for the `feeAdmin` field of
    /// `setDynamicConfig`). Owner-only — the concrete wrapper enforces that.
    fn set_fee_admin(env: &Env, fee_admin: &Address) {
        env.storage()
            .instance()
            .set(&PoolDataKey::FeeAdmin, fee_admin);
    }

    /// Returns the fee-admin address, if any.
    fn get_fee_admin(env: &Env) -> Option<Address> {
        env.storage().instance().get(&PoolDataKey::FeeAdmin)
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
            // M-14 / INV-POOL-ENC-2/4, INV-PCFG-1: reject empty remote pool and token
            // addresses at config time. EVM `TokenPool._validateTokenPoolConfig` requires
            // a non-empty `remoteTokenAddress`, and the remote pool is only ever set
            // (never emptied) via `setRemotePool`. An empty address here would silently
            // create a degenerate lane whose source-pool validation (C-3) and
            // release/mint destination can never match.
            if update.remote_pool_addresses.len() == 0 || update.remote_token_address.len() == 0 {
                return Err(CCIPError::InvalidConfig);
            }

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
    // Router + ramp registry (EVM `s_router` / ramp lookups)
    // ------------------------------------------------------------------

    /// Store the CCIP Router address. Owner-only — caller must enforce.
    fn set_router(env: &Env, router: &Address) {
        env.storage().instance().set(&PoolDataKey::Router, router);
    }

    fn get_router(env: &Env) -> Option<Address> {
        env.storage().instance().get(&PoolDataKey::Router)
    }

    /// Store the ramp registry address. Owner-only — caller must enforce.
    /// Used for on/off ramp authorization so pools never re-enter the Router on the outbound path.
    fn set_ramp_registry(env: &Env, registry: &Address) {
        env.storage()
            .instance()
            .set(&PoolDataKey::RampRegistry, registry);
    }

    fn get_ramp_registry(env: &Env) -> Option<Address> {
        env.storage().instance().get(&PoolDataKey::RampRegistry)
    }

    /// Store the RMN proxy address. Internal helper called once from each pool's
    /// `initialize` (mirrors EVM `TokenPool`'s `immutable i_rmnProxy` constructor
    /// arg — there is NO public `set_rmn_proxy` entrypoint, so the value is
    /// immutable after the one-shot `initialize`). The pool stores this directly —
    /// like the ramp registry — rather than resolving it via `Router.get_config()`,
    /// because `lock_or_burn` runs inside `ccip_send` (Router → OnRamp → Pool) and
    /// Soroban forbids re-entering an ancestor contract on the call stack.
    fn set_rmn_proxy(env: &Env, rmn_proxy: &Address) {
        env.storage()
            .instance()
            .set(&PoolDataKey::RmnProxy, rmn_proxy);
    }

    fn get_rmn_proxy(env: &Env) -> Option<Address> {
        env.storage().instance().get(&PoolDataKey::RmnProxy)
    }

    /// Require that neither the RMN network globally nor `remote_chain_selector`
    /// specifically is cursed, mirroring EVM `TokenPool._validateLockOrBurn`
    /// (`TokenPool.sol:422`) and `_validateReleaseOrMint` (`:479`): both call
    /// `IRMN(i_rmnProxy).isCursed(bytes16(uint128(remoteChainSelector)))`, which
    /// checks the global curse *and* the per-subject curse in one call.
    ///
    /// The RMN proxy is read from pool storage (set via `set_rmn_proxy`), NOT
    /// resolved via `Router.get_config()` — see `set_rmn_proxy` for the re-entry
    /// rationale. Reuses `CursedByRMN=47` (binding-neutral).
    fn require_remote_chain_not_cursed(
        env: &Env,
        remote_chain_selector: u64,
    ) -> Result<(), CCIPError> {
        let rmn_proxy = Self::get_rmn_proxy(env).ok_or(CCIPError::RouterNotConfigured)?;
        let rmn_proxy_client = RmnProxyClient::new(env, &rmn_proxy);

        // Global curse.
        if rmn_proxy_client.is_cursed() {
            return Err(CCIPError::CursedByRMN);
        }

        // Per-chain (subject) curse: bytes16(uint128(remoteChainSelector)) — the
        // selector occupies the low 8 bytes of the 16-byte subject. Mirrors
        // `CurseCheckable::require_chain_not_cursed`.
        let selector_bytes = remote_chain_selector.to_be_bytes();
        let mut subject_array = [0u8; 16];
        subject_array[8..16].copy_from_slice(&selector_bytes);
        let subject = BytesN::<16>::from_array(env, &subject_array);

        let rmn_remote = rmn_proxy_client.get_rmn();
        let rmn_remote_client = RmnRemoteClient::new(env, &rmn_remote);
        if rmn_remote_client.is_cursed_by_subject(&subject) {
            return Err(CCIPError::CursedByRMN);
        }

        Ok(())
    }

    /// Require `caller` to be the configured OnRamp for `dest_chain_selector` on the ramp
    /// registry (EVM `TokenPool._onlyOnRamp`). `caller` must authorize this call.
    fn require_authorized_onramp(
        env: &Env,
        caller: &Address,
        dest_chain_selector: u64,
    ) -> Result<(), CCIPError> {
        caller.require_auth();
        let reg = Self::get_ramp_registry(env).ok_or(CCIPError::RouterNotConfigured)?;
        let reg_client = RampRegistryClient::new(env, &reg);
        let expected = match reg_client.try_get_onramp(&dest_chain_selector) {
            Ok(Ok(addr)) => addr,
            Ok(Err(_)) => return Err(CCIPError::MessageDecodingError),
            Err(Ok(e)) => return Err(ramp_registry_ccip_error_to_common(e)),
            Err(Err(_)) => return Err(CCIPError::UnsupportedDestinationChain),
        };
        if expected != *caller {
            return Err(CCIPError::CallerNotAuthorized);
        }
        Ok(())
    }

    /// Require `caller` to be a registered OffRamp for `source_chain_selector` on the ramp
    /// registry (EVM `TokenPool._onlyOffRamp`). `caller` must match the direct invoker and
    /// authorize this call (`caller.require_auth()`).
    fn require_authorized_offramp(
        env: &Env,
        source_chain_selector: u64,
        caller: &Address,
    ) -> Result<(), CCIPError> {
        caller.require_auth();
        let reg = Self::get_ramp_registry(env).ok_or(CCIPError::RouterNotConfigured)?;
        let reg_client = RampRegistryClient::new(env, &reg);
        if !reg_client.is_offramp(&source_chain_selector, caller) {
            return Err(CCIPError::CallerNotAuthorized);
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
    /// `token_args` is forwarded to the hooks contract as opaque policy-engine context
    /// (EVM `IAdvancedPoolHooks.preflightCheck` parity).
    fn preflight_check(
        env: &Env,
        lock_or_burn_in: &LockOrBurnIn,
        requested_finality: u32,
        token_args: &Bytes,
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
            client.preflight_check(&input, &requested_finality, token_args, &amount);
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

    /// Returns required CCV resolver addresses + `include_defaults` flag from advanced hooks.
    ///
    /// When no advanced hooks contract is configured, returns
    /// `{ ccvs: [], include_defaults: true }`, i.e. "no pool-specific CCVs, fall back to
    /// lane defaults" — matching the pre-hooks behavior and EVM's `_getCCVsForPool` default.
    fn get_required_ccvs(
        env: &Env,
        local_token: &Address,
        remote_chain_selector: u64,
        amount: i128,
        requested_finality: u32,
        extra_data: &Bytes,
        direction: &MessageDirection,
    ) -> PoolRequiredCCVs {
        if let Some(hooks_addr) = env
            .storage()
            .instance()
            .get::<PoolDataKey, Address>(&PoolDataKey::AdvancedPoolHooks)
        {
            let client = PoolHooksClient::new(env, &hooks_addr);
            let direction_iface = message_direction_to_iface(direction);
            let iface_result: IfacePoolRequiredCCVs = client.get_required_ccvs(
                local_token,
                &remote_chain_selector,
                &amount,
                &requested_finality,
                extra_data,
                &direction_iface,
            );
            return PoolRequiredCCVs {
                ccvs: iface_result.ccvs,
                include_defaults: iface_result.include_defaults,
            };
        }
        PoolRequiredCCVs {
            ccvs: Vec::new(env),
            include_defaults: true,
        }
    }
}

/// Calculates the source-side in-token fee for a lock/burn
/// (EVM `TokenPool._getFee`, `internal`). `fee = amount * fee_bps / BPS_DIVIDER`,
/// computed in `U256` to mirror EVM `uint256` and avoid i128 overflow on large
/// amounts. The caller passes the already-fetched config so this stays pure and
/// testable. `amount` must be non-negative; the result fits in u128 (< 2^141).
pub fn _get_fee(
    env: &Env,
    amount: i128,
    requested_finality: u32,
    config: &TokenTransferFeeConfig,
) -> Result<i128, CCIPError> {
    if !config.is_enabled {
        return Ok(0);
    }
    let bps = if finality_codec::is_fast_finality(requested_finality) {
        config.fast_finality_transfer_fee_bps
    } else {
        config.finality_transfer_fee_bps
    };
    if bps == 0 {
        return Ok(0);
    }
    let amount_u: u128 = amount
        .try_into()
        .map_err(|_| CCIPError::InvalidTokenAmount)?;
    let amount_u256 = U256::from_u128(env, amount_u);
    let fee_u = amount_u256
        .checked_mul(&U256::from_u32(env, bps))
        .ok_or(CCIPError::InvalidFeeCalculation)?
        .checked_div(&U256::from_u32(env, BPS_DIVIDER))
        .ok_or(CCIPError::InvalidFeeCalculation)?;
    let fee: u128 = fee_u.to_u128().ok_or(CCIPError::InvalidFeeCalculation)?;
    Ok(fee as i128)
}

fn lock_or_burn_in_to_iface(input: &LockOrBurnIn) -> IfaceLockOrBurnIn {
    IfaceLockOrBurnIn {
        receiver: input.receiver.clone(),
        remote_chain_selector: input.remote_chain_selector,
        original_sender: input.original_sender.clone(),
        amount: input.amount,
        local_token: input.local_token.clone(),
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
