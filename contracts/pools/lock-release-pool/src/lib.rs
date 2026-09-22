#![no_std]

mod events;

use soroban_sdk::{contract, contractimpl, symbol_short, token, Address, Bytes, Env, Symbol, Vec};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_interfaces::token_lock_box::TokenLockBoxClient;
use common_pool::{
    _get_fee, calculate_local_amount, encode_local_decimals, finality_codec, parse_remote_decimals,
    rate_limit, BaseTokenPool, ChainUpdate, FtfInboundConsumedEvent, FtfOutboundConsumedEvent,
    InboundRateLimitConsumedEvent, LockBoxConfiguredEvent, LockBoxEntry, LockOrBurnIn,
    LockOrBurnOut, MessageDirection, OutboundRateLimitConsumedEvent, PoolFeeResult,
    PoolRequiredCCVs, RateLimitConfig, RateLimiterState, ReleaseOrMintIn, ReleaseOrMintOut,
    TokenTransferFeeConfig, TokenTransferFeeConfigArgs,
};
use events::{LockedEvent, ReleasedEvent};

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
/// Persistent: `(LOCKBOX, remote_chain_selector) → Address` of the lockbox for that chain.
const LOCKBOX: Symbol = symbol_short!("LOCKBOX");
/// `approve` expiry ledger for pool→lockbox allowance: `ledger.sequence() + this`.
///
/// In the intended CCIP path (`Router::ccip_send` → `OnRamp::forward_from_router` →
/// pool `lock_or_burn`), the whole graph runs in one Stellar transaction / ledger
/// close. This buffer only caps worst-case residual allowance exposure if the
/// `approve(0)` cleanup fails; `1` is enough for SAC. See
/// `SiloedLockReleaseTokenPool` for the full rationale.
const LOCKBOX_ALLOWANCE_EXPIRY_BUFFER: u32 = 1;

#[contract]
pub struct LockReleaseTokenPoolContract;

#[contractimpl]
impl Initializable for LockReleaseTokenPoolContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for LockReleaseTokenPoolContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl]
impl BaseTokenPool for LockReleaseTokenPoolContract {}

#[contractimpl]
impl LockReleaseTokenPoolContract {
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
        rmn_proxy: Address,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as BaseTokenPool>::init_pool(&env, &token, token_decimals)?;
        <Self as BaseTokenPool>::set_router(&env, &router);
        <Self as BaseTokenPool>::set_ramp_registry(&env, &ramp_registry);
        // RMN proxy is set once at initialization and never mutated afterwards —
        // mirrors EVM `TokenPool`'s `immutable i_rmnProxy` (constructor arg, no
        // setter). The pool consults it directly for curse checks in
        // `lock_or_burn` / `release_or_mint` (NOT via `Router.get_config()`, which
        // would re-enter the Router during `ccip_send`). Soroban has no
        // `immutable` keyword, so immutability is enforced by `initialize` being
        // one-shot (`require_not_initialized`) and there being no `set_rmn_proxy`
        // entrypoint.
        <Self as BaseTokenPool>::set_rmn_proxy(&env, &rmn_proxy);
        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "LockReleaseTokenPool-dev 2.0.0")
    }

    // ------------------------------------------------------------------
    // Lock box configuration (owner-only)
    // ------------------------------------------------------------------

    /// Map remote chain selectors to lockbox addresses (EVM
    /// `LockReleaseTokenPool.configureLockBoxes` parity — the canonical pool
    /// now escrows in a lockbox like its siloed sibling, so the pool's own token
    /// balance equals only accrued fees and `withdraw_fee_tokens` can safely
    /// sweep the full balance). Many selectors may point to the same lockbox
    /// (shared liquidity). Each lockbox must support this pool's token.
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
        <Self as Initializable>::require_initialized(&env)?;
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
    // Pool Operations
    // ------------------------------------------------------------------

    /// Locks tokens by transferring the source-side fee to the pool and depositing
    /// the post-fee `dest_token_amount` into the lockbox configured for
    /// `remote_chain_selector` (EVM `TokenPool.lockOrBurn` parity). Called by the
    /// OnRamp during a cross-chain send.
    ///
    /// The caller (OnRamp/Router) must have arranged for the tokens to be
    /// transferred into this contract before calling `lock_or_burn`.
    /// On Stellar, this is done via the Soroban auth tree -- the sender
    /// authorizes a `transfer(sender, pool, amount)` as a sub-invocation.
    pub fn lock_or_burn(
        env: Env,
        caller: Address,
        input: LockOrBurnIn,
        requested_finality: u32,
        _token_args: Bytes,
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

        // M-6 / INV-POOL-RMN-1: reject the operation if the RMN has cursed the
        // network globally or the remote chain specifically. Mirrors EVM
        // `TokenPool._validateLockOrBurn` (TokenPool.sol:422,
        // `IRMN(i_rmnProxy).isCursed(bytes16(uint128(remoteChainSelector)))`).
        <Self as BaseTokenPool>::require_remote_chain_not_cursed(
            &env,
            input.remote_chain_selector,
        )?;

        // Compute the source-side in-token fee BEFORE rate limiting (EVM
        // `TokenPool.lockOrBurn` L288-311). The fee accrues on the pool's own
        // balance; only `dest_token_amount` crosses to the lockbox / wire.
        // `token_args` is accepted for EVM `IPoolV2.lockOrBurn` ABI parity; the
        // base fee model keys on the destination chain only, so it is unused here.
        let fee_config = <Self as BaseTokenPool>::get_token_transfer_fee_config(
            &env,
            input.remote_chain_selector,
        )?;
        let fee_amount = _get_fee(&env, input.amount, requested_finality, &fee_config)?;
        let dest_token_amount = input
            .amount
            .checked_sub(fee_amount)
            .ok_or(CCIPError::InvalidTokenAmount)?;

        // TODO: Remove FTF outbound rate limiting from lock_or_burn. Stellar has
        // deterministic ~5s finality with no reorg risk, so there is no meaningful
        // "fast finality" concept when Stellar is the source chain. Senders on
        // Stellar will never request non-default finality (block_confirmations is
        // always 0). FTF rate limits should only apply inbound (release_or_mint),
        // where messages arriving from EVM with fast finality carry higher source-
        // chain reorg risk. This block should be simplified to always use the
        // default outbound bucket, ignoring `requested_finality`. EVM rate-limits
        // the post-fee `destTokenAmount`, so the consume + event use it here.
        if finality_codec::is_fast_finality(requested_finality) {
            let allowed = <Self as BaseTokenPool>::get_allowed_finality_config(&env);
            finality_codec::ensure_requested_finality_allowed(requested_finality, allowed)?;
            let used_ftf = rate_limit::consume_ftf_outbound(
                &env,
                input.remote_chain_selector,
                dest_token_amount,
            )?;
            if used_ftf {
                FtfOutboundConsumedEvent {
                    remote_chain_selector: input.remote_chain_selector,
                    amount: dest_token_amount,
                }
                .publish(&env);
            } else {
                OutboundRateLimitConsumedEvent {
                    remote_chain_selector: input.remote_chain_selector,
                    amount: dest_token_amount,
                }
                .publish(&env);
            }
        } else {
            rate_limit::consume_outbound(&env, input.remote_chain_selector, dest_token_amount)?;
            OutboundRateLimitConsumedEvent {
                remote_chain_selector: input.remote_chain_selector,
                amount: dest_token_amount,
            }
            .publish(&env);
        }

        <Self as BaseTokenPool>::preflight_check(
            &env,
            &input,
            requested_finality,
            dest_token_amount,
        )?;

        // Move the full amount from the sender onto the pool, then deposit only
        // `dest_token_amount` into the lockbox. `fee_amount` stays on the pool
        // balance == accrued fees, so `withdraw_fee_tokens` can sweep the full
        // pool balance without touching user liquidity (EVM
        // `LockReleaseTokenPool` lockbox parity).
        let pool_address = env.current_contract_address();
        let token_client = token::Client::new(&env, &pool_token);
        token_client.transfer(&input.original_sender, &pool_address, &input.amount);

        let lock_box_addr = resolve_lock_box(&env, input.remote_chain_selector)?;
        // A zero-amount lock transfers no value, so skip the lockbox deposit
        // entirely — the lockbox `deposit` rejects `amount <= 0`. The lockbox
        // must still be configured for the chain (`resolve_lock_box` above
        // enforces that), but no token movement occurs. Mirrors EVM, where a
        // zero-amount `lockOrBurn` is a valid no-op transfer (L-1).
        if dest_token_amount > 0 {
            let lb_client = TokenLockBoxClient::new(&env, &lock_box_addr);
            let allowance_exp = env
                .ledger()
                .sequence()
                .saturating_add(LOCKBOX_ALLOWANCE_EXPIRY_BUFFER);
            token_client.approve(
                &pool_address,
                &lock_box_addr,
                &dest_token_amount,
                &allowance_exp,
            );
            if lb_client
                .try_deposit(&pool_address, &dest_token_amount)
                .is_err()
            {
                revoke_pool_allowance_to_lockbox(&env, &pool_token, &pool_address, &lock_box_addr);
                return Err(CCIPError::TokenHandlingError);
            }
            revoke_pool_allowance_to_lockbox(&env, &pool_token, &pool_address, &lock_box_addr);
        }

        LockedEvent {
            sender: input.original_sender.clone(),
            amount: dest_token_amount,
        }
        .publish(&env);

        let remote_token =
            <Self as BaseTokenPool>::get_remote_token(&env, input.remote_chain_selector)?;

        let local_decimals = <Self as BaseTokenPool>::get_token_decimals(&env)?;
        let dest_pool_data = encode_local_decimals(&env, local_decimals)?;

        Ok(LockOrBurnOut {
            dest_token_address: remote_token,
            dest_token_amount,
            dest_pool_data,
        })
    }

    /// Releases tokens from the pool to the receiver. Called by the OffRamp
    /// on the destination chain after verifying the cross-chain message.
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

        // M-6 / INV-POOL-RMN-1: reject the operation if the RMN has cursed the
        // network globally or the remote chain specifically. Mirrors EVM
        // `TokenPool._validateReleaseOrMint` (TokenPool.sol:479,
        // `IRMN(i_rmnProxy).isCursed(bytes16(uint128(remoteChainSelector)))`),
        // which runs before the source-pool membership check.
        <Self as BaseTokenPool>::require_remote_chain_not_cursed(
            &env,
            input.remote_chain_selector,
        )?;

        // Validate the inbound source pool is configured for this remote chain.
        // Mirrors EVM `TokenPool._validateReleaseOrMint` (TokenPool.sol:480):
        // the source-pool membership check runs before the inbound rate-limit
        // consume, reverting `InvalidSourcePoolAddress` for an unknown source.
        if !<Self as BaseTokenPool>::is_remote_source_pool(
            &env,
            input.remote_chain_selector,
            &input.source_pool_address,
        )? {
            return Err(CCIPError::InvalidSourcePoolAddress);
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

        // Release from the lockbox, which holds the bridged liquidity (EVM
        // `LockReleaseTokenPool._releaseOrMint` calls `i_lockBox.withdraw`). The
        // lockbox performs its own balance check, so the pool no longer guards
        // `InsufficientPoolLiquidity` here — the pool's own balance is fees only.
        let lock_box_addr = resolve_lock_box(&env, input.remote_chain_selector)?;
        let pool_address = env.current_contract_address();
        // A zero-amount release transfers no value, so skip the lockbox
        // withdrawal — the lockbox `withdraw` rejects `amount <= 0`. The lockbox
        // must still be configured for the chain (`resolve_lock_box` above
        // enforces that). Mirrors EVM, where a zero-amount `releaseOrMint` is a
        // valid no-op transfer.
        if local_amount > 0 {
            let lb_client = TokenLockBoxClient::new(&env, &lock_box_addr);
            lb_client.withdraw(&pool_address, &local_amount, &input.receiver);
        }

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

    pub fn get_fee(
        env: Env,
        dest_chain_selector: u64,
        amount: i128,
        requested_finality: u32,
        token_args: Bytes,
    ) -> Result<PoolFeeResult, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as BaseTokenPool>::get_fee(
            &env,
            dest_chain_selector,
            amount,
            requested_finality,
            &token_args,
        )
    }

    pub fn get_token_transfer_fee_config(
        env: Env,
        dest_chain_selector: u64,
    ) -> Result<TokenTransferFeeConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as BaseTokenPool>::get_token_transfer_fee_config(&env, dest_chain_selector)
    }

    pub fn apply_token_fee_config_updates(
        env: Env,
        adds: Vec<TokenTransferFeeConfigArgs>,
        disables: Vec<u64>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::apply_token_fee_config_updates(&env, &adds, &disables)
    }

    /// Withdraws accrued fee-token balances to `recipient` (EVM
    /// `TokenPool.withdrawFeeTokens`). Callable by the owner or the fee admin.
    pub fn withdraw_fee_tokens(
        env: Env,
        fee_tokens: Vec<Address>,
        recipient: Address,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Self::require_owner_or_fee_admin(&env)?;
        <Self as BaseTokenPool>::withdraw_fee_tokens(&env, &fee_tokens, &recipient)
    }

    /// Sets the fee-admin address (EVM parity for the `feeAdmin` field of
    /// `setDynamicConfig`). Owner-only.
    pub fn set_fee_admin(env: Env, fee_admin: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_fee_admin(&env, &fee_admin);
        Ok(())
    }

    pub fn get_fee_admin(env: Env) -> Option<Address> {
        <Self as BaseTokenPool>::get_fee_admin(&env)
    }

    /// Set the allowed finality configuration. Owner-only.
    pub fn set_allowed_finality_config(env: Env, allowed_finality: u32) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_allowed_finality_config(&env, allowed_finality);
        Ok(())
    }

    /// Set the CCIP Router address (EVM `s_router`). Owner-only.
    pub fn set_router(env: Env, router: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_router(&env, &router);
        Ok(())
    }

    pub fn get_router(env: Env) -> Option<Address> {
        <Self as BaseTokenPool>::get_router(&env)
    }

    /// Set the ramp registry used for ramp caller checks.
    pub fn set_ramp_registry(env: Env, ramp_registry: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        <Self as BaseTokenPool>::set_ramp_registry(&env, &ramp_registry);
        Ok(())
    }

    pub fn get_ramp_registry(env: Env) -> Option<Address> {
        <Self as BaseTokenPool>::get_ramp_registry(&env)
    }

    /// Get the RMN proxy address the pool consults for curse checks (EVM
    /// `TokenPool.getRmnProxy`). Set once at `initialize` and immutable thereafter
    /// (mirrors EVM `immutable i_rmnProxy`).
    pub fn get_rmn_proxy(env: Env) -> Option<Address> {
        <Self as BaseTokenPool>::get_rmn_proxy(&env)
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

    /// EVM `TokenPool.withdrawFeeTokens` caller gate: owner OR fee admin.
    /// `require_owner` already calls `require_auth` on the owner; if the invoker
    /// is not the owner, fall back to the fee admin (which must authorize).
    fn require_owner_or_fee_admin(env: &Env) -> Result<(), CCIPError> {
        if <Self as Ownable>::require_owner(env).is_ok() {
            return Ok(());
        }
        if let Some(admin) = <Self as BaseTokenPool>::get_fee_admin(env) {
            admin.require_auth();
            return Ok(());
        }
        Err(CCIPError::Unauthorized)
    }
}

// ============================================================
// Internal helpers
// ============================================================

/// Clears pool→lockbox token allowance (best-effort hygiene after `deposit` or on error).
fn revoke_pool_allowance_to_lockbox(
    env: &Env,
    pool_token: &Address,
    pool_address: &Address,
    lock_box_addr: &Address,
) {
    let token_client = token::Client::new(env, pool_token);
    let seq = env.ledger().sequence();
    token_client.approve(pool_address, lock_box_addr, &0i128, &seq);
}

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

#[cfg(test)]
mod test;
