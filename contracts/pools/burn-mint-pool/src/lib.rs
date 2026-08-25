#![no_std]

mod events;

use soroban_sdk::{contract, contractimpl, symbol_short, token, Address, Bytes, Env, Symbol, Vec};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_pool::{
    calculate_local_amount, encode_local_decimals, finality_codec, parse_remote_decimals,
    rate_limit, BaseTokenPool, ChainUpdate, FtfInboundConsumedEvent, FtfOutboundConsumedEvent,
    InboundRateLimitConsumedEvent, LockOrBurnIn, LockOrBurnOut, MessageDirection,
    OutboundRateLimitConsumedEvent, PoolFeeConfig, PoolFeeResult, PoolRequiredCCVs,
    RateLimitConfig, RateLimiterState, ReleaseOrMintIn, ReleaseOrMintOut,
};
use events::{BurnedEvent, MintedEvent};

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");

#[contract]
pub struct BurnMintTokenPoolContract;

#[contractimpl]
impl Initializable for BurnMintTokenPoolContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for BurnMintTokenPoolContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl]
impl BaseTokenPool for BurnMintTokenPoolContract {}

#[contractimpl]
impl BurnMintTokenPoolContract {
    // ------------------------------------------------------------------
    // Initialization
    // ------------------------------------------------------------------

    pub fn initialize(
        env: Env,
        owner: Address,
        token: Address,
        token_decimals: u32,
        router: Address,
        ramp_registry: Address,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as BaseTokenPool>::init_pool(&env, &token, token_decimals)?;
        <Self as BaseTokenPool>::set_router(&env, &router);
        <Self as BaseTokenPool>::set_ramp_registry(&env, &ramp_registry);
        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "BurnMintTokenPool-dev 2.0.0")
    }

    // ------------------------------------------------------------------
    // Pool Operations
    // ------------------------------------------------------------------

    /// Burns tokens on the source chain. Called by the OnRamp during a
    /// cross-chain send.
    ///
    /// Uses the SAC `burn` functionality. The caller must have arranged
    /// Soroban auth for the burn (the sender authorizes `burn(sender, amount)`
    /// as a sub-invocation in the auth tree).
    pub fn lock_or_burn(
        env: Env,
        caller: Address,
        input: LockOrBurnIn,
        requested_finality: u32,
    ) -> Result<LockOrBurnOut, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as BaseTokenPool>::require_authorized_onramp(
            &env,
            &caller,
            input.remote_chain_selector,
        )?;

        let pool_token = <Self as BaseTokenPool>::get_token(&env)?;
        if pool_token != input.local_token {
            return Err(CCIPError::PoolTokenMismatch);
        }

        if !<Self as BaseTokenPool>::is_supported_chain(&env, input.remote_chain_selector)? {
            return Err(CCIPError::ChainNotSupported);
        }

        // TODO: Remove FTF outbound rate limiting from lock_or_burn. Stellar has
        // deterministic ~5s finality with no reorg risk, so there is no meaningful
        // "fast finality" concept when Stellar is the source chain. Senders on
        // Stellar will never request non-default finality (block_confirmations is
        // always 0). FTF rate limits should only apply inbound (release_or_mint),
        // where messages arriving from EVM with fast finality carry higher source-
        // chain reorg risk. This block should be simplified to always use the
        // default outbound bucket, ignoring `requested_finality`.
        if finality_codec::is_fast_finality(requested_finality) {
            let allowed = <Self as BaseTokenPool>::get_allowed_finality_config(&env);
            finality_codec::ensure_requested_finality_allowed(requested_finality, allowed)?;
            let used_ftf =
                rate_limit::consume_ftf_outbound(&env, input.remote_chain_selector, input.amount)?;
            if used_ftf {
                FtfOutboundConsumedEvent {
                    remote_chain_selector: input.remote_chain_selector,
                    amount: input.amount,
                }
                .publish(&env);
            } else {
                OutboundRateLimitConsumedEvent {
                    remote_chain_selector: input.remote_chain_selector,
                    amount: input.amount,
                }
                .publish(&env);
            }
        } else {
            rate_limit::consume_outbound(&env, input.remote_chain_selector, input.amount)?;
            OutboundRateLimitConsumedEvent {
                remote_chain_selector: input.remote_chain_selector,
                amount: input.amount,
            }
            .publish(&env);
        }

        <Self as BaseTokenPool>::preflight_check(&env, &input, requested_finality, input.amount)?;

        let token_client = token::Client::new(&env, &pool_token);
        token_client.burn(&input.original_sender, &input.amount);

        BurnedEvent {
            sender: input.original_sender.clone(),
            amount: input.amount,
        }
        .publish(&env);

        let remote_token =
            <Self as BaseTokenPool>::get_remote_token(&env, input.remote_chain_selector)?;

        let local_decimals = <Self as BaseTokenPool>::get_token_decimals(&env)?;
        let dest_pool_data = encode_local_decimals(&env, local_decimals)?;

        Ok(LockOrBurnOut {
            dest_token_address: remote_token,
            dest_pool_data,
        })
    }

    /// Mints tokens to the receiver on the destination chain. Called by the
    /// OffRamp after verifying the cross-chain message.
    ///
    /// The pool must be the token admin (issuer) or an authorized minter
    /// for the SAC / custom Soroban token.
    pub fn release_or_mint(
        env: Env,
        caller: Address,
        input: ReleaseOrMintIn,
        requested_finality: u32,
    ) -> Result<ReleaseOrMintOut, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as BaseTokenPool>::require_authorized_offramp(
            &env,
            input.remote_chain_selector,
            &caller,
        )?;

        let pool_token = <Self as BaseTokenPool>::get_token(&env)?;
        if pool_token != input.local_token {
            return Err(CCIPError::PoolTokenMismatch);
        }

        if !<Self as BaseTokenPool>::is_supported_chain(&env, input.remote_chain_selector)? {
            return Err(CCIPError::ChainNotSupported);
        }

        let local_decimals = <Self as BaseTokenPool>::get_token_decimals(&env)?;
        let remote_decimals = parse_remote_decimals(&input.source_pool_data, local_decimals)?;
        let local_amount = calculate_local_amount(input.amount, remote_decimals, local_decimals)?;

        // FTF inbound rate limiting: when Stellar is the destination, messages
        // from EVM sources may carry fast-finality flags (e.g. WAIT_FOR_SAFE or
        // block depth). These transfers have higher source-chain reorg risk, so
        // separate FTF inbound buckets are appropriate here.
        if finality_codec::is_fast_finality(requested_finality) {
            let used_ftf =
                rate_limit::consume_ftf_inbound(&env, input.remote_chain_selector, local_amount)?;
            if used_ftf {
                FtfInboundConsumedEvent {
                    remote_chain_selector: input.remote_chain_selector,
                    amount: local_amount,
                }
                .publish(&env);
            } else {
                InboundRateLimitConsumedEvent {
                    remote_chain_selector: input.remote_chain_selector,
                    amount: local_amount,
                }
                .publish(&env);
            }
        } else {
            rate_limit::consume_inbound(&env, input.remote_chain_selector, local_amount)?;
            InboundRateLimitConsumedEvent {
                remote_chain_selector: input.remote_chain_selector,
                amount: local_amount,
            }
            .publish(&env);
        }

        <Self as BaseTokenPool>::postflight_check(&env, &input, local_amount, requested_finality)?;

        let admin_client = token::StellarAssetClient::new(&env, &pool_token);
        admin_client.mint(&input.receiver, &local_amount);

        MintedEvent {
            sender: env.current_contract_address(),
            recipient: input.receiver.clone(),
            amount: local_amount,
        }
        .publish(&env);

        Ok(ReleaseOrMintOut {
            destination_amount: local_amount,
        })
    }

    // ------------------------------------------------------------------
    // Admin (owner-gated wrappers around BaseTokenPool)
    // ------------------------------------------------------------------

    pub fn apply_chain_updates(
        env: Env,
        adds: Vec<ChainUpdate>,
        removes: Vec<u64>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::apply_chain_updates(&env, adds, removes)
    }

    /// Update rate limit configs for a chain. Callable by owner or rate limit admin.
    /// When `fast_finality` is true, sets the FTF buckets; otherwise the default buckets.
    pub fn set_rate_limit_config(
        env: Env,
        remote_chain_selector: u64,
        outbound_config: RateLimitConfig,
        inbound_config: RateLimitConfig,
        fast_finality: bool,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Self::require_owner_or_rate_limit_admin(&env)?;
        <Self as BaseTokenPool>::set_rate_limit_config(
            &env,
            remote_chain_selector,
            outbound_config,
            inbound_config,
            fast_finality,
        )
    }

    /// Set the rate limit admin address. Owner-only.
    pub fn set_rate_limit_admin(env: Env, admin: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_rate_limit_admin(&env, &admin);
        Ok(())
    }

    // ------------------------------------------------------------------
    // View helpers (re-export for contract ABI)
    // ------------------------------------------------------------------

    pub fn get_token(env: Env) -> Result<Address, CCIPError> {
        <Self as BaseTokenPool>::get_token(&env)
    }

    pub fn get_token_decimals(env: Env) -> Result<u32, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as BaseTokenPool>::get_token_decimals(&env)
    }

    pub fn is_supported_token(env: Env, token: Address) -> Result<bool, CCIPError> {
        <Self as BaseTokenPool>::is_supported_token(&env, &token)
    }

    pub fn is_supported_chain(env: Env, remote_chain_selector: u64) -> Result<bool, CCIPError> {
        <Self as BaseTokenPool>::is_supported_chain(&env, remote_chain_selector)
    }

    pub fn get_remote_pool(env: Env, remote_chain_selector: u64) -> Result<Bytes, CCIPError> {
        <Self as BaseTokenPool>::get_remote_pool(&env, remote_chain_selector)
    }

    pub fn get_remote_token(env: Env, remote_chain_selector: u64) -> Result<Bytes, CCIPError> {
        <Self as BaseTokenPool>::get_remote_token(&env, remote_chain_selector)
    }

    pub fn get_current_rate_limiter_state(
        env: Env,
        remote_chain_selector: u64,
        fast_finality: bool,
    ) -> RateLimiterState {
        <Self as BaseTokenPool>::get_current_rate_limiter_state(
            &env,
            remote_chain_selector,
            fast_finality,
        )
    }

    pub fn get_rate_limit_admin(env: Env) -> Option<Address> {
        <Self as BaseTokenPool>::get_rate_limit_admin(&env)
    }

    pub fn get_allowed_finality_config(env: Env) -> u32 {
        <Self as BaseTokenPool>::get_allowed_finality_config(&env)
    }

    pub fn get_fee(env: Env, remote_chain_selector: u64) -> Result<PoolFeeResult, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as BaseTokenPool>::get_fee(&env, remote_chain_selector)
    }

    pub fn set_pool_fee_config(
        env: Env,
        remote_chain_selector: u64,
        config: PoolFeeConfig,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_pool_fee_config(&env, remote_chain_selector, &config)
    }

    /// Set the allowed finality configuration. Owner-only.
    pub fn set_allowed_finality_config(env: Env, allowed_finality: u32) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_allowed_finality_config(&env, allowed_finality);
        Ok(())
    }

    /// Set the router address used for inbound `release_or_mint` caller checks (EVM `setDynamicConfig`).
    pub fn set_router(env: Env, router: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_router(&env, &router);
        Ok(())
    }

    pub fn get_router(env: Env) -> Option<Address> {
        <Self as BaseTokenPool>::get_router(&env)
    }

    pub fn set_ramp_registry(env: Env, ramp_registry: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_ramp_registry(&env, &ramp_registry);
        Ok(())
    }

    pub fn get_ramp_registry(env: Env) -> Option<Address> {
        <Self as BaseTokenPool>::get_ramp_registry(&env)
    }

    /// Set the advanced pool hooks contract (EVM `updateAdvancedPoolHooks`). Owner-only.
    pub fn set_advanced_pool_hooks(env: Env, hooks: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_advanced_pool_hooks(&env, &hooks);
        Ok(())
    }

    /// Remove the hooks contract, disabling pre/post-flight checks. Owner-only.
    pub fn remove_advanced_pool_hooks(env: Env) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::remove_advanced_pool_hooks(&env);
        Ok(())
    }

    pub fn get_advanced_pool_hooks(env: Env) -> Option<Address> {
        <Self as BaseTokenPool>::get_advanced_pool_hooks(&env)
    }

    /// Returns required CCV verifier resolver addresses (EVM `TokenPool.getRequiredCCVs`).
    pub fn get_required_ccvs(
        env: Env,
        local_token: Address,
        remote_chain_selector: u64,
        amount: i128,
        requested_finality: u32,
        extra_data: Bytes,
        direction: MessageDirection,
    ) -> PoolRequiredCCVs {
        <Self as BaseTokenPool>::get_required_ccvs(
            &env,
            &local_token,
            remote_chain_selector,
            amount,
            requested_finality,
            &extra_data,
            &direction,
        )
    }

    // ------------------------------------------------------------------
    // Internal helpers
    // ------------------------------------------------------------------

    fn require_owner_or_rate_limit_admin(env: &Env) -> Result<(), CCIPError> {
        if <Self as Ownable>::require_owner(env).is_ok() {
            return Ok(());
        }
        if let Some(admin) = <Self as BaseTokenPool>::get_rate_limit_admin(env) {
            admin.require_auth();
            return Ok(());
        }
        Err(CCIPError::Unauthorized)
    }
}

#[cfg(test)]
mod test;
