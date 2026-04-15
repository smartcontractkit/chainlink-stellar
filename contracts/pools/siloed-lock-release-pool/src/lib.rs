#![no_std]

//! Stellar analogue of EVM [`SiloedLockReleaseTokenPool`](https://github.com/smartcontractkit/chainlink-ccip/blob/develop/chains/evm/contracts/pools/SiloedLockReleaseTokenPool.sol).
//!
//! Like the regular lock-release pool but liquidity is held in **lockbox contracts**
//! rather than the pool's own token balance. Each remote chain selector maps to a
//! lockbox address (many-to-one allowed), enabling **shared or isolated liquidity**
//! per chain group.
//!
//! Migration story: to upgrade a pool, deploy the new pool and add it as an
//! allowed caller on the same lockbox(es) — no token sweep required.

mod events;

use soroban_sdk::{contract, contractimpl, symbol_short, token, Address, Bytes, Env, Symbol, Vec};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_interfaces::token_lock_box::TokenLockBoxClient;
use common_pool::{
    calculate_local_amount, encode_local_decimals, finality_codec, parse_remote_decimals,
    rate_limit, BaseTokenPool, ChainUpdate, FtfInboundConsumedEvent, FtfOutboundConsumedEvent,
    InboundRateLimitConsumedEvent, LockOrBurnIn, LockOrBurnOut, OutboundRateLimitConsumedEvent,
    RateLimitConfig, RateLimiterState, ReleaseOrMintIn, ReleaseOrMintOut,
};
use events::{LockBoxConfiguredEvent, LockedEvent, ReleasedEvent};

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
/// Persistent: `(LOCKBOX, remote_chain_selector) → Address` of the lockbox for that chain.
const LOCKBOX: Symbol = symbol_short!("LOCKBOX");

#[contract]
pub struct SiloedLockReleaseTokenPoolContract;

#[contractimpl]
impl Initializable for SiloedLockReleaseTokenPoolContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for SiloedLockReleaseTokenPoolContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl]
impl BaseTokenPool for SiloedLockReleaseTokenPoolContract {}

#[contractimpl]
impl SiloedLockReleaseTokenPoolContract {
    // ------------------------------------------------------------------
    // Initialization
    // ------------------------------------------------------------------

    pub fn initialize(
        env: Env,
        owner: Address,
        token: Address,
        token_decimals: u32,
        router: Address,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as BaseTokenPool>::init_pool(&env, &token, token_decimals)?;
        <Self as BaseTokenPool>::set_router(&env, &router);
        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "SiloedLockReleaseTokenPool 1.0.0")
    }

    // ------------------------------------------------------------------
    // Lock box configuration (owner-only)
    // ------------------------------------------------------------------

    /// Map remote chain selectors to lockbox addresses (EVM `configureLockBoxes`).
    /// Many selectors may point to the same lockbox (shared liquidity).
    /// Each lockbox must support this pool's token (`is_token_supported`).
    pub fn configure_lock_boxes(env: Env, configs: Vec<LockBoxEntry>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        let pool_token = <Self as BaseTokenPool>::get_token(&env)?;
        for i in 0..configs.len() {
            let entry = configs.get(i).ok_or(CCIPError::InvalidConfig)?;
            let lb_client = TokenLockBoxClient::new(&env, &entry.lock_box);
            if !lb_client.is_token_supported(&pool_token) {
                return Err(CCIPError::InvalidConfig);
            }
            let key = (LOCKBOX, entry.remote_chain_selector);
            env.storage().persistent().set(&key, &entry.lock_box);
            LockBoxConfiguredEvent {
                remote_chain_selector: entry.remote_chain_selector,
                lock_box: entry.lock_box,
            }
            .publish(&env);
        }
        Ok(())
    }

    pub fn get_lock_box(env: Env, remote_chain_selector: u64) -> Result<Address, CCIPError> {
        resolve_lock_box(&env, remote_chain_selector)
    }

    pub fn get_all_lock_box_configs(env: Env) -> Result<Vec<LockBoxEntry>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let chains = load_supported_chains(&env);
        let mut out: Vec<LockBoxEntry> = Vec::new(&env);
        for sel in chains.iter() {
            let key = (LOCKBOX, sel);
            if let Some(addr) = env
                .storage()
                .persistent()
                .get::<(Symbol, u64), Address>(&key)
            {
                out.push_back(LockBoxEntry {
                    remote_chain_selector: sel,
                    lock_box: addr,
                });
            }
        }
        Ok(out)
    }

    // ------------------------------------------------------------------
    // Pool operations
    // ------------------------------------------------------------------

    /// Lock tokens by depositing into the lockbox configured for `remote_chain_selector`.
    ///
    /// The OnRamp / Router arranges auth for `transfer(original_sender, pool, amount)`
    /// in the invocation tree. This pool then deposits into the lockbox.
    pub fn lock_or_burn(
        env: Env,
        on_ramp: Address,
        input: LockOrBurnIn,
        requested_finality: u32,
    ) -> Result<LockOrBurnOut, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as BaseTokenPool>::require_on_ramp(&env, &on_ramp, input.remote_chain_selector)?;

        let pool_token = <Self as BaseTokenPool>::get_token(&env)?;
        if pool_token != input.local_token {
            return Err(CCIPError::PoolTokenMismatch);
        }
        if !<Self as BaseTokenPool>::is_supported_chain(&env, input.remote_chain_selector)? {
            return Err(CCIPError::ChainNotSupported);
        }

        consume_outbound_rate_limit(&env, &input, requested_finality)?;

        <Self as BaseTokenPool>::preflight_check(
            &env,
            &input.original_sender,
            input.remote_chain_selector,
            input.amount,
            requested_finality,
        )?;

        let pool_address = env.current_contract_address();
        let token_client = token::Client::new(&env, &pool_token);
        token_client.transfer(&input.original_sender, &pool_address, &input.amount);

        let lock_box_addr = resolve_lock_box(&env, input.remote_chain_selector)?;
        let lb_client = TokenLockBoxClient::new(&env, &lock_box_addr);
        token_client.approve(&pool_address, &lock_box_addr, &input.amount, &1_000_000);
        lb_client.deposit(&pool_address, &input.amount);

        LockedEvent {
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

    /// Release tokens by withdrawing from the lockbox to the receiver.
    pub fn release_or_mint(
        env: Env,
        off_ramp: Address,
        input: ReleaseOrMintIn,
        requested_finality: u32,
    ) -> Result<ReleaseOrMintOut, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as BaseTokenPool>::require_off_ramp(&env, &off_ramp, input.remote_chain_selector)?;

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

        consume_inbound_rate_limit(
            &env,
            input.remote_chain_selector,
            local_amount,
            requested_finality,
        )?;

        <Self as BaseTokenPool>::postflight_check(
            &env,
            input.remote_chain_selector,
            &input.receiver,
            local_amount,
            requested_finality,
        )?;

        let lock_box_addr = resolve_lock_box(&env, input.remote_chain_selector)?;
        let pool_address = env.current_contract_address();
        let lb_client = TokenLockBoxClient::new(&env, &lock_box_addr);
        lb_client.withdraw(&pool_address, &local_amount, &input.receiver);

        ReleasedEvent {
            sender: pool_address,
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

    pub fn set_rate_limit_admin(env: Env, admin: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_rate_limit_admin(&env, &admin);
        Ok(())
    }

    /// Set the router address. Owner-only (EVM `setDynamicConfig`).
    pub fn set_router(env: Env, router: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_router(&env, &router);
        Ok(())
    }

    pub fn get_router(env: Env) -> Option<Address> {
        <Self as BaseTokenPool>::get_router(&env)
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

    // ------------------------------------------------------------------
    // View helpers
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

    pub fn set_allowed_finality_config(env: Env, allowed_finality: u32) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_allowed_finality_config(&env, allowed_finality);
        Ok(())
    }

    // ------------------------------------------------------------------
    // Internal
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

// ============================================================
// Public types (visible in ABI)
// ============================================================

/// Per-chain lockbox assignment (EVM `SiloedLockReleaseTokenPool.LockBoxConfig`).
#[soroban_sdk::contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LockBoxEntry {
    pub remote_chain_selector: u64,
    pub lock_box: Address,
}

// ============================================================
// Internal helpers
// ============================================================

fn resolve_lock_box(env: &Env, remote_chain_selector: u64) -> Result<Address, CCIPError> {
    let key = (LOCKBOX, remote_chain_selector);
    env.storage()
        .persistent()
        .get::<(Symbol, u64), Address>(&key)
        .ok_or(CCIPError::InvalidConfig)
}

fn load_supported_chains(env: &Env) -> Vec<u64> {
    use common_pool::PoolDataKey;
    env.storage()
        .instance()
        .get(&PoolDataKey::SupportedChains)
        .unwrap_or_else(|| Vec::new(env))
}

fn consume_outbound_rate_limit(
    env: &Env,
    input: &LockOrBurnIn,
    requested_finality: u32,
) -> Result<(), CCIPError> {
    if finality_codec::is_fast_finality(requested_finality) {
        let allowed =
            <SiloedLockReleaseTokenPoolContract as BaseTokenPool>::get_allowed_finality_config(env);
        finality_codec::ensure_requested_finality_allowed(requested_finality, allowed)?;
        let used_ftf =
            rate_limit::consume_ftf_outbound(env, input.remote_chain_selector, input.amount)?;
        if used_ftf {
            FtfOutboundConsumedEvent {
                remote_chain_selector: input.remote_chain_selector,
                amount: input.amount,
            }
            .publish(env);
        } else {
            OutboundRateLimitConsumedEvent {
                remote_chain_selector: input.remote_chain_selector,
                amount: input.amount,
            }
            .publish(env);
        }
    } else {
        rate_limit::consume_outbound(env, input.remote_chain_selector, input.amount)?;
        OutboundRateLimitConsumedEvent {
            remote_chain_selector: input.remote_chain_selector,
            amount: input.amount,
        }
        .publish(env);
    }
    Ok(())
}

fn consume_inbound_rate_limit(
    env: &Env,
    remote_chain_selector: u64,
    local_amount: i128,
    requested_finality: u32,
) -> Result<(), CCIPError> {
    if finality_codec::is_fast_finality(requested_finality) {
        let used_ftf = rate_limit::consume_ftf_inbound(env, remote_chain_selector, local_amount)?;
        if used_ftf {
            FtfInboundConsumedEvent {
                remote_chain_selector,
                amount: local_amount,
            }
            .publish(env);
        } else {
            InboundRateLimitConsumedEvent {
                remote_chain_selector,
                amount: local_amount,
            }
            .publish(env);
        }
    } else {
        rate_limit::consume_inbound(env, remote_chain_selector, local_amount)?;
        InboundRateLimitConsumedEvent {
            remote_chain_selector,
            amount: local_amount,
        }
        .publish(env);
    }
    Ok(())
}

#[cfg(test)]
mod mock_router;
#[cfg(test)]
mod test;
