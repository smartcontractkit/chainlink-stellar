#![no_std]

mod events;
pub mod types;

use common_interfaces::{
    committee_verifier::FeeResponse,
    fee_quoter::{FeeQuoterClient, MessageFeeResult, TokenTransferFeeResult},
    token_admin_registry::TokenAdminRegistryClient,
    token_pool::{LockOrBurnIn, MessageDirection, PoolRequiredCCVs, TokenPoolClient},
    versioned_verifier_resolver::VersionedVerifierResolverClient,
};
use soroban_sdk::{
    contract, contractimpl, symbol_short, token, vec,
    xdr::{FromXdr, ToXdr},
    Address, Bytes, BytesN, Env, IntoVal, Map, Symbol, Vec,
};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::{initializable::Initializable, ReentrancyGuard};
use common_helpers::{
    curse_checkable::CurseCheckable, fee_math, finality_codec, validation::Validatable,
};
use common_message::{
    CcipMessageV1, CcipTokenTransferV1, GenericExtraArgsV3, MessageIdCompute, StellarToAnyMessage,
    ToBytes, MESSAGE_V1_VERSION,
};
use events::{CCIPMessageSentEvent, ConfigSetEvent, DestChainConfigSetEvent};
use types::{DestChainConfig, DestChainConfigArgs, DynamicConfig, Receipt, StaticConfig};

// ============================================================
// Fee breakdown (shared by get_fee and forward_from_router)
// ============================================================

/// Result of [`OnRampContract::compute_outbound_fee_breakdown`]. Carries the
/// total required fee plus the executor slice (flat fee from `Executor::get_fee`
/// + priced execution gas) so `forward_from_router` can build the executor
/// receipt and distribute the executor fee to the executor contract (H-3),
/// without recomputing anything.
struct FeeBreakdown {
    /// Total required fee in fee-token smallest units (message fee + additional).
    total_fee: i128,
    /// FeeQuoter message-fee result (carries `fee_token_price` for conversions).
    message_fee: MessageFeeResult,
    /// Per-CCV fee responses (used for receipts + execution-gas-limit sum).
    ccv_fee_responses: Vec<FeeResponse>,
    /// Executor flat fee in USD cents (from `Executor::get_fee`; 0 for no-exec).
    executor_flat_usd_cents: u128,
    /// Priced execution-gas cost in USD cents (via `quote_gas_for_exec`; 0 for no-exec).
    exec_cost_usd_cents: u128,
    /// Executor flat fee + exec cost, converted to fee-token units, for H-3
    /// distribution transfer to the executor contract (0 for no-exec).
    executor_fee_tokens: i128,
    /// True iff the executor field is the no-execution sentinel (zero executor
    /// fees, no distribution, receipt still emitted with the sentinel issuer).
    is_no_exec: bool,
    /// Total destination execution gas (Σ CCV `dest_gas_limit` + base + user
    /// `gas_limit`). A message property — computed always, priced only when
    /// auto-executing (H-5 / INV-FEE-10).
    execution_gas_limit: u32,
    /// Token-pool fee slice for the first token transfer, resolved EVM-style
    /// (`OnRamp._getReceipts` L1028-1053): from `IPoolV2.getFee` when the pool's
    /// config is enabled, else from `FeeQuoter.get_token_transfer_fee`. Carried
    /// so `forward_from_router` builds the pool receipt + wire amount without a
    /// second `get_fee` call (the fee config is unchanged by `lock_or_burn`).
    pool_dest_gas_limit: u32,
    pool_dest_bytes_overhead: u32,
    pool_fee_usd_cents: u128,
    /// Fee-quoter premium percent (100 = no premium, <100 = LINK discount). EVM
    /// `percentMultiplier` (`FeeQuoter.sol` L337). Carried for the per-receipt
    /// distribution conversions in `forward_from_router`.
    premium_multiplier: u32,
}

/// True iff `addr` is the zero Stellar account (EVM `address(0)` parity).
fn is_zero_account(env: &Env, addr: &Address) -> bool {
    addr == &Address::from_str(
        env,
        "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
    )
}

/// Reject a lane `default_executor` that is either executor sentinel or the
/// zero account — the default executor must be a real, auto-executing contract.
/// Mirrors EVM `OnRamp.sol:653-656`. (Lives here, not in
/// `DestChainConfigArgs::validate`, because sentinel/zero recognition requires
/// `Env`, which the env-less `validate(&self)` does not have.)
fn validate_default_executor(env: &Env, default_executor: &Address) -> Result<(), CCIPError> {
    if GenericExtraArgsV3::is_no_execution_address(env, default_executor)
        || GenericExtraArgsV3::is_use_default_executor_address(env, default_executor)
        || is_zero_account(env, default_executor)
    {
        return Err(CCIPError::InvalidAddress);
    }
    Ok(())
}

// ============================================================
// Storage Keys
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const STATIC_CONFIG: Symbol = symbol_short!("STATIC");
const DYNAMIC_CONFIG: Symbol = symbol_short!("DYNAMIC");
const DEST_CHAINS: Symbol = symbol_short!("DESTCHNS");
const RMN_PROXY: Symbol = symbol_short!("RMN_PROXY");

// ============================================================
// Contract
// ============================================================

#[contract]
pub struct OnRampContract;

#[contractimpl]
impl Initializable for OnRampContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for OnRampContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl(contracttrait)]
impl CurseCheckable for OnRampContract {
    const RMN_PROXY: Symbol = RMN_PROXY;
}

#[contractimpl]
impl OnRampContract {
    // ========================================
    // Initialization
    // ========================================

    /// Initialize the OnRamp contract with static and dynamic configuration.
    ///
    /// # Arguments
    /// * `owner` - The owner address (typically MCMS, can be the deployer initially)
    /// * `static_config` - Immutable configuration
    /// * `dynamic_config` - Mutable configuration
    ///
    /// # Errors
    /// * `AlreadyInitialized` - If contract is already initialized
    /// * `InvalidConfig` - If configuration is invalid
    pub fn initialize(
        env: Env,
        owner: Address,
        static_config: StaticConfig,
        dynamic_config: DynamicConfig,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;

        // Store owner
        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as Initializable>::init(&env)?;
        <Self as CurseCheckable>::init(&env, &static_config.rmn_proxy)?;

        // Validate static config
        if static_config.chain_selector == 0 || static_config.max_usd_cents_per_message == 0 {
            return Err(CCIPError::InvalidConfig);
        }

        // Validate dynamic config (fee_quoter cannot be zero address equivalent check)
        // Note: In Soroban, we check for valid address by ensuring it's set

        // Store static config (immutable after init)
        env.storage().instance().set(&STATIC_CONFIG, &static_config);

        // Store dynamic config
        env.storage()
            .instance()
            .set(&DYNAMIC_CONFIG, &dynamic_config);

        // Initialize empty destination chains map
        let dest_chains: Map<u64, DestChainConfig> = Map::new(&env);
        env.storage().persistent().set(&DEST_CHAINS, &dest_chains);

        // Emit config set event
        ConfigSetEvent {
            static_config,
            dynamic_config,
        }
        .publish(&env);

        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "OnRamp-dev 2.0.0")
    }

    // ========================================
    // Core Messaging Functions
    // ========================================

    /// Computes total required fee (fee token base units) plus FeeQuoter message fee and
    /// per-CCV [`FeeResponse`] values. Shared by [`Self::get_fee`] and [`Self::forward_from_router`]
    /// so `Router::ccip_send` does not need a separate top-level `get_fee` call (Track A).
    ///
    /// `merged_ccvs` and `merged_ccv_args` MUST be the final outbound plan (user + lane +
    /// pool-required + default fallback), produced by [`Self::build_merged_outbound_ccv_lists`].
    /// Iterating that plan here matches EVM `OnRamp._getReceipts`, which sees the fully merged
    /// list including pool-required CCVs.
    fn compute_outbound_fee_breakdown(
        env: &Env,
        dest_chain_selector: u64,
        message: &StellarToAnyMessage,
        dest_config: &DestChainConfig,
        dynamic_config: &DynamicConfig,
        static_config: &StaticConfig,
        extra_args: &GenericExtraArgsV3,
        merged_ccvs: &Vec<Address>,
        merged_ccv_args: &Vec<Bytes>,
    ) -> Result<FeeBreakdown, CCIPError> {
        if merged_ccvs.len() != merged_ccv_args.len() {
            return Err(CCIPError::CCVLengthMismatch);
        }

        let message_bytes = message.to_bytes(env)?;

        let fee_quoter = FeeQuoterClient::new(env, &dynamic_config.fee_quoter);
        let message_fee = fee_quoter.get_message_fee(&dest_chain_selector, message);

        let mut ccv_fee_responses: Vec<FeeResponse> = Vec::new(env);
        let mut ccv_fees_usd_cents: u128 = 0;

        for i in 0..merged_ccvs.len() {
            let ccv = merged_ccvs.get(i).ok_or(CCIPError::CCVLengthMismatch)?;
            let ccv_args = merged_ccv_args.get(i).ok_or(CCIPError::CCVLengthMismatch)?;

            let vvr = VersionedVerifierResolverClient::new(env, &ccv);
            let verifier_address = vvr.get_outbound_implementation(&dest_chain_selector, &ccv_args);

            let ccv_fee_response = Self::get_ccv_fee_internal(
                env,
                &verifier_address,
                dest_chain_selector,
                &message_bytes,
                &ccv_args,
                extra_args,
            )?;
            ccv_fees_usd_cents = ccv_fees_usd_cents
                .checked_add(ccv_fee_response.fee as u128)
                .ok_or(CCIPError::InvalidFeeCalculation)?;
            ccv_fee_responses.push_back(ccv_fee_response);
        }

        let mut additional_usd_cents: u128 = ccv_fees_usd_cents;

        // Token-pool fee slice, resolved exactly once EVM-style
        // (`OnRamp._getReceipts` L1028-1053): `IPoolV2.getFee` when the pool's
        // config is enabled, else `FeeQuoter.get_token_transfer_fee`. The chosen
        // USD-cent fee joins `additional` once (no double-count — H-13); the
        // overheads + fee are carried on the breakdown for `forward_from_router`'s
        // pool receipt. `get_message_fee` no longer bundles the token fee, so this
        // is the sole assembly point.
        let mut pool_dest_gas_limit: u32 = 0;
        let mut pool_dest_bytes_overhead: u32 = 0;
        let mut pool_fee_usd_cents: u128 = 0;

        if !message.token_amounts.is_empty() {
            let token_amount = message.token_amounts.get(0).unwrap();
            let pool_address =
                Self::get_pool_by_source_token_internal(env, static_config, &token_amount.token)?;
            let pool_client = TokenPoolClient::new(env, &pool_address);
            let pool_fee = pool_client.get_fee(
                &dest_chain_selector,
                &token_amount.amount,
                &extra_args.block_confirmations,
                &extra_args.token_args,
            );
            if pool_fee.is_enabled {
                pool_fee_usd_cents = pool_fee.fee_usd_cents as u128;
                pool_dest_gas_limit = pool_fee.dest_gas_overhead;
                pool_dest_bytes_overhead = pool_fee.dest_bytes_overhead;
            } else {
                let fq_fee: TokenTransferFeeResult =
                    fee_quoter.get_token_transfer_fee(&dest_chain_selector, &token_amount.token);
                pool_fee_usd_cents = fq_fee.fee_usd_cents as u128;
                pool_dest_gas_limit = fq_fee.dest_gas_overhead;
                pool_dest_bytes_overhead = fq_fee.dest_bytes_overhead;
            }
            additional_usd_cents = additional_usd_cents
                .checked_add(pool_fee_usd_cents)
                .ok_or(CCIPError::InvalidFeeCalculation)?;
        }

        // H-5 / INV-FEE-10: execution_gas_limit = Σ CCV dest_gas_limit +
        // pool dest_gas_limit + base execution gas + user gas_limit (EVM
        // `OnRamp._getReceipts` L1009/1055/1065: CCV gas → pool gas → executor
        // gas, in that order). The pool's `dest_gas_overhead` is the gas the
        // destination `release_or_mint` will consume; omitting it both
        // under-prices the execution-gas cost (priced below via
        // `quote_gas_for_exec`) and under-advertises the on-wire gas limit,
        // stranding auto-executed token transfers with an out-of-gas
        // destination call. This is a *message property* (it goes into
        // MessageV1), so it is always computed; `pool_dest_gas_limit` is 0
        // when there is no token transfer. It is *priced* only when
        // auto-executing (executor ≠ no-exec sentinel); see below.
        let mut execution_gas_limit: u32 = 0;
        for i in 0..ccv_fee_responses.len() {
            if let Some(r) = ccv_fee_responses.get(i) {
                execution_gas_limit = execution_gas_limit.saturating_add(r.dest_gas_limit);
            }
        }
        execution_gas_limit = execution_gas_limit.saturating_add(pool_dest_gas_limit);
        let executor_dest_gas = dest_config
            .base_execution_gas_cost
            .saturating_add(extra_args.gas_limit);
        execution_gas_limit = execution_gas_limit.saturating_add(executor_dest_gas);

        // Executor slice. The "use default" sentinel is resolved to the lane's
        // concrete `default_executor` by the caller before reaching here, so
        // `extra_args.executor` is either a concrete contract or the no-execution
        // sentinel (M-7 / INV-NOEXEC-1/2).
        let is_no_exec = GenericExtraArgsV3::is_no_execution_address(env, &extra_args.executor);

        // H-8 / INV-FIN-EXEC-1/2 + INV-FEE-8: the executor flat USD-cent fee comes
        // from `Executor::get_fee` (a view cross-contract call). That call also
        // enforces the executor layer of the 5-layer FTF opt-in matrix — it
        // reverts (`InvalidRequestedFinality`) on a disallowed requested finality,
        // so the OnRamp needs no separate executor-layer finality check.
        //
        // H-5 / INV-FEE-10: the execution-gas *cost* is priced via the fee
        // quoter with NO premium (mirror EVM `OnRamp.sol:1095-1097`: exec cost is
        // not multiplied by `percentMultiplier`; the message-fee portion already
        // carries `get_message_fee`'s internal premium). `calldata_size = 0`
        // because payload bytes are already priced inside `get_message_fee`.
        // H-5 / INV-FEE-10 + M-10 / INV-FEE-13: price the execution gas once via
        // `quote_gas_for_exec`, unconditionally — EVM `_getReceipts` calls
        // `quoteGasForExec` for every message (OnRamp.sol L1075-1077) and only
        // adds the exec cost to the executor receipt when the executor isn't the
        // no-exec sentinel (L1094). Calling it here regardless yields
        // `premium_multiplier` for the CCV/pool/executor-flat conversions below
        // even on the no-exec path (those fees still need the LINK discount).
        let calldata_size: u32 = 0;
        let gas_quote = fee_quoter.quote_gas_for_exec(
            &dest_chain_selector,
            &execution_gas_limit,
            &calldata_size,
            &message.fee_token,
        );
        let premium_multiplier = gas_quote.premium_multiplier;

        let (executor_flat_usd_cents, exec_cost_usd_cents) = if is_no_exec {
            // No-execution sentinel: zero executor fee, zero exec-gas cost. The
            // sentinel is left in place (EVM leaves `NO_EXECUTION_ADDRESS`).
            (0u128, 0u128)
        } else {
            let executor_flat = Self::get_executor_fee_internal(
                env,
                &extra_args.executor,
                dest_chain_selector,
                extra_args.block_confirmations,
                merged_ccvs,
                &extra_args.executor_args,
                &message.fee_token,
            )? as u128;
            (executor_flat, gas_quote.gas_cost_usd_cents)
        };

        // `additional_usd_cents` accumulates the premium-eligible slice
        // (CCV + pool + executor flat). Exec-gas cost is kept separate: EVM adds
        // it to the executor receipt AFTER the `feeMultiplier` (OnRamp.sol
        // L1095-1097), so it must NOT be discounted.
        additional_usd_cents = additional_usd_cents
            .checked_add(executor_flat_usd_cents)
            .ok_or(CCIPError::InvalidFeeCalculation)?;

        // M-10 / INV-FEE-13: convert the premium-eligible slice with the EVM
        // `feeMultiplier` (`usd_cents_to_fee_token_with_premium`) and the exec-cost
        // slice with the bare helper, then sum — matching EVM's per-receipt
        // `feeTokenAmount = usdCents * feeMultiplier` plus the non-multiplied
        // exec-cost addend. `premium_multiplier = 100` (non-LINK) makes the
        // premium helper bit-identical to the bare one, so non-LINK fees are
        // unchanged.
        let exec_cost_in_fee_token =
            fee_math::usd_cents_to_fee_token(exec_cost_usd_cents, message_fee.fee_token_price)?;
        let additional_in_fee_token = fee_math::usd_cents_to_fee_token_with_premium(
            additional_usd_cents,
            premium_multiplier,
            message_fee.fee_token_price,
        )?
        .checked_add(exec_cost_in_fee_token)
        .ok_or(CCIPError::InvalidFeeCalculation)?;

        // H-3 / INV-FEE-19 + M-10 / INV-FEE-13: executor fee in fee-token units
        // (flat discounted + exec cost not), for the receipt and the H-3
        // distribution transfer to the executor contract.
        let executor_fee_tokens = fee_math::usd_cents_to_fee_token_with_premium(
            executor_flat_usd_cents,
            premium_multiplier,
            message_fee.fee_token_price,
        )?
        .checked_add(exec_cost_in_fee_token)
        .ok_or(CCIPError::InvalidFeeCalculation)?;

        let total_fee = message_fee
            .fee_token_amount
            .checked_add(additional_in_fee_token)
            .ok_or(CCIPError::InvalidFeeCalculation)?;

        // Enforce the per-message fee cap. Mirrors EVM `OnRamp.sol:1104`:
        //   if (feeTokenAmount > (maxUSDCentsPerMsg * 1e34) / feeTokenPrice)
        //       revert FeeExceedsMaxAllowed(...)
        // The cap is denominated in USD cents (chain-agnostic) and converted to
        // the fee token's units with the same 1e34 helper, so it is dimensionally
        // correct for any fee token — unlike the legacy fee-quoter `juels` cap it
        // supersedes (see fee-quoter `get_message_fee`). EVM enforces this in the
        // shared fee computation used by both `getFee` and `forwardFromRouter`;
        // enforcing it here covers both Stellar call paths (`get_fee` and
        // `forward_from_router`) identically. `max_usd_cents_per_message` is
        // validated `!= 0` at init (`initialize`), matching EVM's
        // `maxUSDCentsPerMessage == 0` config rejection.
        let max_fee_token = fee_math::usd_cents_to_fee_token(
            static_config.max_usd_cents_per_message as u128,
            message_fee.fee_token_price,
        )?;
        if total_fee > max_fee_token {
            return Err(CCIPError::FeeExceedsMaxAllowed);
        }

        Ok(FeeBreakdown {
            total_fee,
            message_fee,
            ccv_fee_responses,
            executor_flat_usd_cents,
            exec_cost_usd_cents,
            executor_fee_tokens,
            is_no_exec,
            execution_gas_limit,
            pool_dest_gas_limit,
            pool_dest_bytes_overhead,
            pool_fee_usd_cents,
            premium_multiplier,
        })
    }

    /// Enforces EVM parity for `destChainConfig.tokenReceiverAllowed`
    /// (`OnRamp._parseExtraArgsWithDefaults` revert branch). Callers that opt into a
    /// non-default `token_receiver` must do so on a lane that permits it. The EVM variant
    /// includes `destChainSelector` in the revert data; `CCIPError` here is a plain enum so
    /// the selector is not propagated.
    fn validate_token_receiver_allowed(
        dest_config: &DestChainConfig,
        extra_args: &GenericExtraArgsV3,
    ) -> Result<(), CCIPError> {
        if extra_args.token_receiver.len() != 0 && !dest_config.token_receiver_allowed {
            return Err(CCIPError::TokenReceiverNotAllowed);
        }
        Ok(())
    }

    /// INV-MSG-8 / INV-LCFG-3: the destination `receiver` must be exactly
    /// `dest_config.address_bytes_length` bytes long. Mirrors EVM
    /// `OnRamp._validateDestChainAddress` (`if (len != addressBytesLength) revert
    /// InvalidDestChainAddress`, OnRamp.sol:471-503, invoked at :250), which the Stellar side was
    /// missing — the check was commented out in `StellarToAnyMessage::validate`. The dest chain's
    /// `address_bytes_length` lives on `DestChainConfig`, which `validate()` cannot see, so the
    /// check belongs here on the ramp where the config is in scope.
    fn validate_dest_address(
        dest_config: &DestChainConfig,
        receiver: &Bytes,
    ) -> Result<(), CCIPError> {
        if receiver.len() as u32 != dest_config.address_bytes_length {
            return Err(CCIPError::InvalidDestChainAddress);
        }
        Ok(())
    }

    /// Build the final outbound CCV plan (addresses + parallel args) used for both
    /// [`Self::get_fee`] and [`Self::forward_from_router`].
    ///
    /// Mirrors EVM `OnRamp.forwardFromRouter`'s merge step: user + lane-mandated are combined
    /// (with default fallback when both are empty), then pool-required CCVs are appended. Slots
    /// without user-provided args (lane/pool/default) carry empty `Bytes` to keep the two Vecs
    /// parallel.
    fn build_merged_outbound_ccv_lists(
        env: &Env,
        dest_chain_selector: u64,
        message: &StellarToAnyMessage,
        dest_config: &DestChainConfig,
        static_config: &StaticConfig,
        extra_args: &GenericExtraArgsV3,
    ) -> Result<(Vec<Address>, Vec<Bytes>), CCIPError> {
        // Token-only transfer: no receiver callback, no data, token present. EVM
        // `_parseExtraArgsWithDefaults` skips the user-fallback default CCVs in this case so
        // pools can run with only their own required CCVs (e.g. CCTP-only).
        let is_token_only_transfer = message.data.len() == 0
            && !message.token_amounts.is_empty()
            && extra_args.gas_limit == 0;

        // User-fallback defaults: when the user provides no CCVs and this is not a token-only
        // transfer, fall back to lane defaults. Stellar has no zero-address sentinel to expand
        // placeholders inside a non-empty user CCV list, so only the fallback branch applies.
        let user_fallback_defaults = if is_token_only_transfer {
            Vec::new(env)
        } else {
            dest_config.default_ccvs.clone()
        };

        let (mut merged_ccvs, mut merged_ccv_args) = Self::merge_ccv_lists_with_ccv_args(
            env,
            &extra_args.ccvs,
            &extra_args.ccv_args,
            &dest_config.lane_mandated_ccvs,
            &user_fallback_defaults,
        )?;

        if !message.token_amounts.is_empty() {
            let token_amount = message.token_amounts.get(0).unwrap();
            let pool_req = Self::get_outbound_pool_required_ccvs(
                env,
                dest_chain_selector,
                &token_amount.token,
                token_amount.amount,
                extra_args.block_confirmations,
                extra_args.token_args.clone(),
                static_config,
            )?;

            // Pool-specified CCVs (deduped).
            Self::append_unique_pool_ccvs(
                env,
                &mut merged_ccvs,
                &mut merged_ccv_args,
                &pool_req.ccvs,
            );

            // `include_defaults = true` is Stellar's equivalent of EVM's `address(0)` sentinel
            // in the pool-returned CCV list: it asks the OnRamp to append lane defaults on top
            // of the pool's custom CCVs. Dedup naturally avoids double-listing defaults if they
            // were already pulled in by the user-fallback path.
            if pool_req.include_defaults {
                Self::append_unique_pool_ccvs(
                    env,
                    &mut merged_ccvs,
                    &mut merged_ccv_args,
                    &dest_config.default_ccvs,
                );
            }
        }

        // H-1 / INV-CC-1: an outbound message must carry at least one CCV. EVM
        // `OnRamp.forwardFromRouter` guarantees this by construction: an empty
        // pool-returned CCV list is treated as "use lane defaults", so the merged
        // set is never empty when the lane has defaults. Stellar pools instead
        // return an explicit `include_defaults` flag, and token-only transfers
        // skip the user-fallback defaults path (`user_fallback_defaults` above) —
        // so a token-only message on a lane whose pool returns
        // `{ccvs:[], include_defaults:false}` (with no lane-mandated CCVs) would
        // otherwise be emitted with zero CCVs, bypassing verification entirely.
        // Reject that here, at the single merge point both `get_fee` and
        // `forward_from_router` route through. Reuses `CCVQuorumNotMet` (#108) —
        // no new error variant, no schema break.
        if merged_ccvs.is_empty() {
            return Err(CCIPError::CCVQuorumNotMet);
        }

        Ok((merged_ccvs, merged_ccv_args))
    }

    /// Get the fee for sending a message to a destination chain.
    ///
    /// This function calculates the total fee including:
    /// - Verifier fees from all CCVs
    /// - Token pool fees (if applicable)
    /// - Executor fees
    /// - Network protocol fee
    ///
    /// # Arguments
    /// * `dest_chain_selector` - The destination chain identifier
    /// * `message` - The message to be sent
    ///
    /// # Returns
    /// The total fee amount in the fee token's smallest denomination
    ///
    /// # Errors
    /// * `NotInitialized` - If contract is not initialized
    /// * `DestinationChainNotSupported` - If destination chain is not configured
    /// * `CanOnlySendOneTokenPerMessage` - If more than one token is specified
    pub fn get_fee(
        env: Env,
        dest_chain_selector: u64,
        message: StellarToAnyMessage,
    ) -> Result<i128, CCIPError> {
        Self::require_initialized(&env)?;
        <Self as CurseCheckable>::require_not_cursed(&env)?;

        message.validate()?;

        let dest_config = Self::get_dest_chain_config_internal(&env, dest_chain_selector)?;
        let dynamic_config = Self::get_dynamic_config_internal(&env)?;
        let static_config = Self::get_static_config_internal(&env)?;

        Self::validate_dest_address(&dest_config, &message.receiver)?;

        // Parse extra args with defaults
        let mut extra_args = if message.extra_args.len() == 0 {
            GenericExtraArgsV3::new(&env, dest_config.default_executor.clone())
        } else {
            GenericExtraArgsV3::from_xdr(&env, &message.extra_args.clone())
                .map_err(|_| CCIPError::InvalidExtraArgsData)?
        };

        // Resolve the "use default" executor sentinel (M-5 / INV-ENC-5) to the lane's
        // concrete `default_executor` before hashing and before `Executor::get_fee`.
        // Mirrors EVM `_parseExtraArgsWithDefaults` `address(0)→default`. The
        // empty-extra-args branch above already substitutes the concrete default, so this
        // is a no-op there; it only matters for the non-empty branch where a sender sets
        // the executor field to the use-default sentinel to customize gas/CCVs while
        // still requesting the default executor. The no-execution sentinel (M-7) is left
        // in place and handled in `compute_outbound_fee_breakdown`.
        if GenericExtraArgsV3::is_use_default_executor_address(&env, &extra_args.executor) {
            extra_args.executor = dest_config.default_executor.clone();
        }

        // M-8 / INV-FIN-SRC-1/3: reject malformed requested finality (a flag combined with
        // a block depth, or multiple flags) before it is committed verbatim into the
        // message ID for data-only messages. Mirrors EVM `FinalityCodec
        // ._validateRequestedFinality`, invoked on the parsed extraArgs. On Stellar the
        // finality value is carried in `extra_args.block_confirmations` (see `finality:`
        // field assignment below); `WAIT_FOR_FINALITY_FLAG` (0) and any single-mode value
        // (pure depth or a lone flag) pass.
        finality_codec::validate_requested_finality(extra_args.block_confirmations)?;

        Self::validate_token_receiver_allowed(&dest_config, &extra_args)?;

        let (merged_ccvs, merged_ccv_args) = Self::build_merged_outbound_ccv_lists(
            &env,
            dest_chain_selector,
            &message,
            &dest_config,
            &static_config,
            &extra_args,
        )?;

        let breakdown = Self::compute_outbound_fee_breakdown(
            &env,
            dest_chain_selector,
            &message,
            &dest_config,
            &dynamic_config,
            &static_config,
            &extra_args,
            &merged_ccvs,
            &merged_ccv_args,
        )?;

        Ok(breakdown.total_fee)
    }

    /// Forward a message from the Router to be sent cross-chain.
    ///
    /// This is the main entry point for sending CCIP messages. It:
    /// 1. Validates the message and caller
    /// 2. Parses extra args and applies defaults
    /// 3. Merges CCV lists (user, lane-mandated, pool-required)
    /// 4. Calculates and distributes fees
    /// 5. Locks or burns tokens (if applicable)
    /// 6. Encodes the message and computes message ID
    /// 7. Calls each verifier
    /// 8. Emits CCIPMessageSent event
    ///
    /// # Arguments
    /// * `dest_chain_selector` - Destination chain identifier
    /// * `message` - The message to send
    /// * `fee_token_amount` - Amount of fee token provided by router
    /// * `original_sender` - The original initiator of the CCIP request
    ///
    /// # Returns
    /// The unique message ID (32-byte hash)
    ///
    /// # Errors
    /// Various errors for validation failures
    ///
    /// # Panics
    ///
    /// * If the configured router did not authorize this call (`require_auth` on `dest_config.router`).
    /// * If `original_sender` did not authorize this exact invocation (see `require_auth_for_args`
    ///   with the same `(dest_chain_selector, message, fee_token_amount, original_sender)` tuple).
    pub fn forward_from_router(
        env: Env,
        dest_chain_selector: u64,
        message: StellarToAnyMessage,
        fee_token_amount: i128,
        original_sender: Address,
    ) -> Result<BytesN<32>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as CurseCheckable>::require_not_cursed(&env)?;
        message.validate()?;

        // Enter reentrancy guard (uses temporary storage)
        ReentrancyGuard::enter(&env)?;

        // Get configs
        let mut dest_config = Self::get_dest_chain_config_internal(&env, dest_chain_selector)?;
        let dynamic_config = Self::get_dynamic_config_internal(&env)?;
        let static_config = Self::get_static_config_internal(&env)?;

        Self::validate_dest_address(&dest_config, &message.receiver)?;

        // Verify caller is the router
        dest_config.router.require_auth();

        // Bind `original_sender` to this invocation so it cannot be forged by an intermediary.
        // Args must match this function's parameter list (after `env`) exactly — same rule as
        // `Router::ccip_send` → `sender.require_auth()` for the outer call.
        let auth_args = vec![
            &env,
            dest_chain_selector.into_val(&env),
            message.clone().into_val(&env),
            fee_token_amount.into_val(&env),
            original_sender.clone().into_val(&env),
        ];
        original_sender.require_auth_for_args(auth_args);

        // Parse extra args; use default when empty (common for simple messages)
        let mut extra_args = if message.extra_args.len() == 0 {
            GenericExtraArgsV3::new(&env, dest_config.default_executor.clone())
        } else {
            GenericExtraArgsV3::from_xdr(&env, &message.extra_args.clone())
                .map_err(|_| CCIPError::InvalidExtraArgsData)?
        };

        // Resolve the "use default" executor sentinel (M-5 / INV-ENC-5) to the lane's
        // concrete `default_executor` before hashing and before `Executor::get_fee`
        // (EVM `_parseExtraArgsWithDefaults` `address(0)→default`). The no-execution
        // sentinel (M-7) is left in place and handled in `compute_outbound_fee_breakdown`.
        if GenericExtraArgsV3::is_use_default_executor_address(&env, &extra_args.executor) {
            extra_args.executor = dest_config.default_executor.clone();
        }

        // M-8 / INV-FIN-SRC-1/3: reject malformed requested finality (a flag combined with
        // a block depth, or multiple flags) before it is committed verbatim into the
        // message ID for data-only messages. Mirrors EVM `FinalityCodec
        // ._validateRequestedFinality`, invoked on the parsed extraArgs. On Stellar the
        // finality value is carried in `extra_args.block_confirmations` (see `finality:`
        // field assignment below); `WAIT_FOR_FINALITY_FLAG` (0) and any single-mode value
        // (pure depth or a lone flag) pass.
        finality_codec::validate_requested_finality(extra_args.block_confirmations)?;

        Self::validate_token_receiver_allowed(&dest_config, &extra_args)?;

        // Build the final outbound CCV plan (user + lane + pool-required, with defaults as
        // fallback) ONCE so fee breakdown, `ccv_and_executor_hash`, receipts, and verifier
        // invocations all see the same list. Mirrors EVM `OnRamp.forwardFromRouter`.
        let (merged_ccvs, merged_ccv_args) = Self::build_merged_outbound_ccv_lists(
            &env,
            dest_chain_selector,
            &message,
            &dest_config,
            &static_config,
            &extra_args,
        )?;

        // Track A: single fee breakdown for this send (Router no longer calls `get_fee` first).
        // Validate fee before any token lock or sequence bump.
        let breakdown = Self::compute_outbound_fee_breakdown(
            &env,
            dest_chain_selector,
            &message,
            &dest_config,
            &dynamic_config,
            &static_config,
            &extra_args,
            &merged_ccvs,
            &merged_ccv_args,
        )?;
        if fee_token_amount < breakdown.total_fee {
            return Err(CCIPError::InsufficientFeeTokenAmount);
        }

        let message_fee = breakdown.message_fee.clone();
        let ccv_fee_responses = breakdown.ccv_fee_responses.clone();

        // Lock or burn tokens via the pool (if token transfer). When tokens are present,
        // also record pool fee for the token receipt emitted after CCV receipts (EVM
        // `OnRamp._getReceipts` ordering; see chainlink-ccv `ParseReceiptStructure`).
        let mut token_pool_receipt: Option<Receipt> = None;
        let token_transfer_bytes = if !message.token_amounts.is_empty() {
            let token_amount = message.token_amounts.get(0).unwrap();
            let pool_address =
                Self::get_pool_by_source_token_internal(&env, &static_config, &token_amount.token)?;
            let pool_client = TokenPoolClient::new(&env, &pool_address);

            // TODO: On Stellar as the source chain, `block_confirmations` will
            // always be 0 (WAIT_FOR_FINALITY) since Stellar has deterministic ~5s
            // finality and no fast confirmation rules. The pool's FTF outbound
            // branch is unreachable in practice. Consider asserting this invariant
            // or hardcoding 0 instead of threading the extra_args value.
            let lock_result = pool_client.lock_or_burn(
                &env.current_contract_address(),
                &LockOrBurnIn {
                    receiver: message.receiver.clone(),
                    remote_chain_selector: dest_chain_selector,
                    original_sender: original_sender.clone(),
                    amount: token_amount.amount,
                    local_token: token_amount.token.clone(),
                },
                &extra_args.block_confirmations,
                &extra_args.token_args,
            );

            // H-13: reuse the breakdown's resolved pool-fee slice — no second
            // `get_fee` call (the fee config is unchanged by `lock_or_burn`).
            // The wire amount is the post-fee `dest_token_amount` returned by the
            // pool (INV-POOL-10), not the full `token_amount.amount`.
            token_pool_receipt = Some(Receipt {
                issuer: pool_address.clone(),
                dest_gas_limit: breakdown.pool_dest_gas_limit,
                dest_bytes_overhead: breakdown.pool_dest_bytes_overhead,
                fee_token_amount: breakdown.pool_fee_usd_cents as i128,
                extra_args: extra_args.token_args.clone(),
            });

            let token_transfer = CcipTokenTransferV1 {
                version: MESSAGE_V1_VERSION,
                amount: Self::i128_to_bytes32(&env, lock_result.dest_token_amount),
                source_pool_address: pool_address.to_xdr(&env),
                source_token_address: token_amount.token.clone().to_xdr(&env),
                dest_token_address: lock_result.dest_token_address,
                // EVM parity (OnRamp.sol:311): an unspecified tokenReceiver defaults to the
                // message receiver, so the destination pool releases/mints to the same account
                // that receives `ccipReceive`. Lanes that disallow a *non-default* receiver still
                // accept this — the empty case is the default, gated only by
                // `validate_token_receiver_allowed` above (INV-TR-3).
                token_receiver: if extra_args.token_receiver.len() != 0 {
                    extra_args.token_receiver.clone()
                } else {
                    message.receiver.clone()
                },
                extra_data: lock_result.dest_pool_data,
            };
            token_transfer.to_bytes(&env)?
        } else {
            Bytes::new(&env)
        };

        // Compute sequence number before building the canonical message
        dest_config.message_number += 1;
        let sequence_number = dest_config.message_number;

        // EVM parity (OnRamp.sol): `ccipReceiveGasLimit` is the user callback gas, and
        // `executionGasLimit` is the total destination-chain execution gas (Σ each
        // receipt's `destGasLimit` + base + user `gasLimit`). Computed once in the shared
        // `compute_outbound_fee_breakdown` (H-5 / INV-FEE-10) and reused here so the
        // priced gas and the on-wire `execution_gas_limit` can never diverge.
        let execution_gas_limit = breakdown.execution_gas_limit;

        // Build canonical MessageV1 for message ID computation and event encoding
        let ccip_msg = CcipMessageV1 {
            source_chain_selector: static_config.chain_selector,
            dest_chain_selector,
            sequence_number,
            execution_gas_limit,
            ccip_receive_gas_limit: extra_args.gas_limit,
            finality: extra_args.block_confirmations,
            ccv_and_executor_hash: CcipMessageV1::compute_ccv_and_executor_hash(
                &env,
                &merged_ccvs,
                &extra_args.executor,
            ),
            onramp_address: env.current_contract_address().to_xdr(&env),
            offramp_address: dest_config.off_ramp.clone().to_xdr(&env),
            sender: original_sender.clone().to_xdr(&env),
            receiver: message.receiver.clone(),
            dest_blob: Bytes::new(&env),
            token_transfer: token_transfer_bytes,
            data: message.data.clone(),
        };

        let message_id = ccip_msg.compute_message_id(&env)?;

        // TODO: check if message ID already exists in storage for idempotency

        // Receipt ordering matches EVM `OnRamp._getReceipts` / chainlink-ccv `ParseReceiptStructure`:
        // [CCV_0, ..., CCV_N, TokenPool? , Executor, NetworkFee]
        // Token pool receipt is present iff `token_transfer` is non-empty (same condition as
        // `message.TokenTransferLength` on the canonical MessageV1).

        // Invoke verifiers to get verification blobs and generate receipts
        let (verifier_blobs, mut receipts) = Self::get_ccv_blobs_and_receipts_internal(
            &env,
            dest_chain_selector,
            &message_id,
            &original_sender,
            &message,
            &extra_args,
            &merged_ccvs,
            &merged_ccv_args,
            &ccv_fee_responses,
            fee_token_amount,
        )?;

        if let Some(r) = token_pool_receipt {
            receipts.push_back(r);
        }

        // Executor receipt (always before the network fee receipt). The issuer is
        // the (possibly sentinel) executor address — the no-execution sentinel is
        // left in place (M-7 / INV-NOEXEC-2). `fee_token_amount` stores USD cents
        // (the receipt convention used by every receipt); the executor slice is
        // the flat `Executor::get_fee` fee + the priced execution-gas cost (both 0
        // for the no-execution sentinel).
        receipts.push_back(Receipt {
            issuer: extra_args.executor.clone(),
            dest_gas_limit: dest_config
                .base_execution_gas_cost
                .saturating_add(extra_args.gas_limit),
            dest_bytes_overhead: 0,
            fee_token_amount: (breakdown
                .executor_flat_usd_cents
                .checked_add(breakdown.exec_cost_usd_cents)
                .ok_or(CCIPError::InvalidFeeCalculation)?) as i128,
            extra_args: extra_args.executor_args.clone(),
        });

        // TODO: Confirm with EVM reference whether message vs token network fees
        // are mutually exclusive or additive (base + surcharge). Currently treated
        // as mutually exclusive.
        let network_fee_usd_cents = if message.token_amounts.is_empty() {
            dest_config.message_network_fee_usd_cents
        } else {
            dest_config.token_network_fee_usd_cents
        };

        // Network fee receipt (always last)
        receipts.push_back(Receipt {
            issuer: dest_config.router.clone(),
            dest_gas_limit: 0,
            dest_bytes_overhead: 0,
            fee_token_amount: network_fee_usd_cents as i128,
            extra_args: Bytes::new(&env),
        });

        // Persist updated sequence number
        Self::set_dest_chain_config(&env, dest_chain_selector, &dest_config);

        // Total required fee is computed once, premium-aware, in
        // `compute_outbound_fee_breakdown` (M-10 / INV-FEE-13: the CCV/pool/
        // executor-flat slices carry the fee-quoter `premium_multiplier`, the
        // exec-cost slice does not). Reusing it here — instead of re-summing the
        // receipts and re-converting — makes the send-path total identical to the
        // `get_fee` quote by construction and guarantees the per-receipt
        // distribution below never exceeds the funded additional.
        let total_fee = breakdown.total_fee;

        if fee_token_amount < total_fee {
            return Err(CCIPError::InsufficientFeeTokenAmount);
        }

        // Distribute fee tokens at send time (H-3 / INV-FEE-18..21, EVM
        // `OnRamp._distributeFees` parity). CCV fees → each CCV's resolver (the
        // receipt `issuer` is the VVR, which custodies/sweeps via its own
        // `withdraw_fee_tokens`); the pool fee → the token pool; the executor fee →
        // the executor. The network fee is intentionally LEFT on the OnRamp — it is
        // swept to `fee_aggregator` by the permissionless `withdraw_fee_tokens`
        // (EVM: "network fee receipt which must remain in the onRamp"). The network
        // fee receipt is still emitted above, unchanged.
        //
        // Receipt ordering: [CCV_0..CCV_N, TokenPool?, Executor, NetworkFee], so the
        // first `n_ccvs` receipts are CCVs and the pool receipt (if any) sits at
        // index `n_ccvs`. M-10 / INV-FEE-13: each CCV/pool slice is converted
        // per-receipt with the EVM `feeMultiplier` (`usd_cents_to_fee_token_with_
        // premium`, `breakdown.premium_multiplier`) — the same multiplier
        // `compute_outbound_fee_breakdown` applied to the charged total — so for a
        // LINK fee token (discount) the distributed sum stays ≤
        // `breakdown.additional_in_fee_token` and the OnRamp (funded with
        // `fee_token_amount ≥ total_fee`) is never over-drawn. The executor slice
        // is transferred as `breakdown.executor_fee_tokens` (already premium-aware:
        // flat discounted, exec cost not). Floor division keeps
        // `Σ premium_convert(each) ≤ premium_convert(Σ)`.
        if fee_token_amount > 0 {
            let fee_token_client = token::Client::new(&env, &message.fee_token);
            let onramp_address = env.current_contract_address();
            let n_ccvs = merged_ccvs.len();

            // H-3: CCV fees → each CCV's resolver (receipt issuer = the VVR). Skip
            // when the CCV charged no fee or the converted amount rounds to 0.
            for i in 0..n_ccvs {
                let receipt = receipts.get(i).ok_or(CCIPError::CCVLengthMismatch)?;
                let ccv_usd_cents = receipt.fee_token_amount as u128;
                if ccv_usd_cents == 0 {
                    continue;
                }
                let ccv_fee_tokens = fee_math::usd_cents_to_fee_token_with_premium(
                    ccv_usd_cents,
                    breakdown.premium_multiplier,
                    message_fee.fee_token_price,
                )?;
                if ccv_fee_tokens > 0 {
                    fee_token_client.transfer(&onramp_address, &receipt.issuer, &ccv_fee_tokens);
                }
            }

            // H-3: pool fee → the token pool (receipt issuer). The pool receipt sits
            // at index `n_ccvs` and is present iff this is a token transfer. Stellar
            // pools are all V2 post-H-13, so the pool fee is always transferred
            // (EVM's V1 leave-it-for-sweep branch is N/A). Skip when it rounds to 0.
            if !message.token_amounts.is_empty() && breakdown.pool_fee_usd_cents > 0 {
                let pool_receipt = receipts.get(n_ccvs).ok_or(CCIPError::CCVLengthMismatch)?;
                let pool_fee_tokens = fee_math::usd_cents_to_fee_token_with_premium(
                    breakdown.pool_fee_usd_cents,
                    breakdown.premium_multiplier,
                    message_fee.fee_token_price,
                )?;
                if pool_fee_tokens > 0 {
                    fee_token_client.transfer(
                        &onramp_address,
                        &pool_receipt.issuer,
                        &pool_fee_tokens,
                    );
                }
            }

            // Executor fee → the executor contract (unchanged). Skipped for the
            // no-execution sentinel (M-7) and when the priced amount rounds to 0.
            if !breakdown.is_no_exec && breakdown.executor_fee_tokens > 0 {
                fee_token_client.transfer(
                    &onramp_address,
                    &extra_args.executor,
                    &breakdown.executor_fee_tokens,
                );
            }
        }

        // Emit CCIPMessageSent event with canonical MessageV1 encoding
        CCIPMessageSentEvent {
            dest_chain_selector,
            sequence_number,
            sender: original_sender,
            message_id: message_id.clone(),
            fee_token: message.fee_token.clone(),
            token_amount_before_fees: message
                .token_amounts
                .get(0)
                .map(|token_amount| token_amount.amount)
                .unwrap_or(0),
            encoded_message: ccip_msg.to_bytes(&env)?,
            receipts,
            verifier_blobs,
        }
        .publish(&env);

        // Exit reentrancy guard
        ReentrancyGuard::exit(&env);

        // TODO: keep track of message IDs in storage for idempotency?

        Ok(message_id)
    }

    /// Get the expected next message number for a destination chain.
    ///
    /// # Arguments
    /// * `dest_chain_selector` - The destination chain identifier
    ///
    /// # Returns
    /// The next message number that will be used
    pub fn get_expected_next_message_number(
        env: Env,
        dest_chain_selector: u64,
    ) -> Result<u64, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let dest_config = Self::get_dest_chain_config_internal(&env, dest_chain_selector)?;
        Ok(dest_config.message_number + 1)
    }

    // ========================================
    // Token Pool Functions
    // ========================================

    /// Get the pool address for a specific source token.
    ///
    /// # Arguments
    /// * `source_token` - The token address on this chain
    ///
    /// # Returns
    /// The pool address that handles this token
    pub fn get_pool_by_source_token(env: Env, source_token: Address) -> Result<Address, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let static_config = Self::get_static_config_internal(&env)?;
        let registry = TokenAdminRegistryClient::new(&env, &static_config.token_admin_registry);
        let pool = registry
            .get_pool(&source_token)
            .ok_or(CCIPError::UnsupportedToken)?;

        Ok(pool)
    }

    // ========================================
    // Configuration Functions
    // ========================================

    /// Get the static configuration.
    pub fn get_static_config(env: Env) -> Result<StaticConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Self::get_static_config_internal(&env)
    }

    /// Get the dynamic configuration.
    pub fn get_dynamic_config(env: Env) -> Result<DynamicConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Self::get_dynamic_config_internal(&env)
    }

    /// Set the dynamic configuration. Only callable by owner.
    ///
    /// # Arguments
    /// * `dynamic_config` - New dynamic configuration
    ///
    /// # Errors
    /// * `Unauthorized` - If caller is not owner
    /// * `InvalidConfig` - If configuration is invalid
    pub fn set_dynamic_config(env: Env, dynamic_config: DynamicConfig) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        env.storage()
            .instance()
            .set(&DYNAMIC_CONFIG, &dynamic_config);

        // Emit event
        let static_config: StaticConfig = env
            .storage()
            .instance()
            .get(&STATIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)?;

        ConfigSetEvent {
            static_config,
            dynamic_config,
        }
        .publish(&env);

        Ok(())
    }

    /// Get configuration for a specific destination chain.
    ///
    /// # Arguments
    /// * `dest_chain_selector` - The destination chain identifier
    pub fn get_dest_chain_config(
        env: Env,
        dest_chain_selector: u64,
    ) -> Result<DestChainConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Self::get_dest_chain_config_internal(&env, dest_chain_selector)
    }

    /// Get all destination chain configurations.
    ///
    /// # Returns
    /// Tuple of (chain selectors, configurations)
    pub fn get_all_dest_chain_configs(
        env: Env,
    ) -> Result<(Vec<u64>, Vec<DestChainConfig>), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let dest_chains: Map<u64, DestChainConfig> = env
            .storage()
            .persistent()
            .get(&DEST_CHAINS)
            .unwrap_or(Map::new(&env));

        let mut selectors: Vec<u64> = Vec::new(&env);
        let mut configs: Vec<DestChainConfig> = Vec::new(&env);

        for (selector, config) in dest_chains.iter() {
            selectors.push_back(selector);
            configs.push_back(config);
        }

        Ok((selectors, configs))
    }

    /// Apply destination chain configuration updates. Only callable by owner.
    ///
    /// # Arguments
    /// * `dest_chain_config_args` - Array of destination chain configurations to apply
    pub fn apply_dest_chain_config_updates(
        env: Env,
        dest_chain_config_args: Vec<DestChainConfigArgs>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let static_config: StaticConfig = env
            .storage()
            .instance()
            .get(&STATIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)?;

        let mut dest_chains: Map<u64, DestChainConfig> = env
            .storage()
            .persistent()
            .get(&DEST_CHAINS)
            .unwrap_or(Map::new(&env));

        for args in dest_chain_config_args.iter() {
            // Basic validation for non-zero configs and offramp address
            args.validate()?;

            // Env-aware executor-sentinel/zero check (EVM `OnRamp.sol:653-656`):
            // a lane's `default_executor` must be a real, auto-executing contract.
            // This cannot live in the env-less `DestChainConfigArgs::validate`
            // because recognizing the sentinel/zero `Address` requires `Env`.
            validate_default_executor(&env, &args.default_executor)?;

            // Validate that the message is not to self
            if args.dest_chain_selector == static_config.chain_selector {
                return Err(CCIPError::InvalidConfig);
            }

            // Get existing config or create new one
            let existing_message_number = dest_chains
                .get(args.dest_chain_selector)
                .map(|c| c.message_number)
                .unwrap_or(0);

            let new_config = DestChainConfig {
                router: args.router.clone(),
                message_number: existing_message_number,
                address_bytes_length: args.address_bytes_length,
                token_receiver_allowed: args.token_receiver_allowed,
                message_network_fee_usd_cents: args.message_network_fee_usd_cents,
                token_network_fee_usd_cents: args.token_network_fee_usd_cents,
                base_execution_gas_cost: args.base_execution_gas_cost,
                execution_fee_usd_cents: args.execution_fee_usd_cents,
                default_executor: args.default_executor.clone(),
                lane_mandated_ccvs: args.lane_mandated_ccvs.clone(),
                default_ccvs: args.default_ccvs.clone(),
                off_ramp: args.off_ramp.clone(),
            };

            dest_chains.set(args.dest_chain_selector, new_config.clone());

            // Emit event
            DestChainConfigSetEvent {
                dest_chain_selector: args.dest_chain_selector,
                message_number: existing_message_number,
                config: new_config,
            }
            .publish(&env);
        }

        env.storage().persistent().set(&DEST_CHAINS, &dest_chains);

        Ok(())
    }

    // ========================================
    // Fee Functions
    // ========================================

    /// Withdraw accumulated fee tokens to the fee aggregator.
    /// This function is permissionless as it only sends to the trusted fee aggregator.
    ///
    /// # Arguments
    /// * `fee_tokens` - List of fee token addresses to withdraw
    pub fn withdraw_fee_tokens(env: Env, fee_tokens: Vec<Address>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let dynamic_config: DynamicConfig = env
            .storage()
            .instance()
            .get(&DYNAMIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)?;

        let onramp_address = env.current_contract_address();
        for i in 0..fee_tokens.len() {
            if let Some(fee_token) = fee_tokens.get(i) {
                let token_client = token::Client::new(&env, &fee_token);
                let balance = token_client.balance(&onramp_address);
                if balance > 0 {
                    token_client.transfer(
                        &onramp_address,
                        &dynamic_config.fee_aggregator,
                        &balance,
                    );
                }
            }
        }

        Ok(())
    }

    // ========================================
    // Internal Helper Functions
    // ========================================

    fn get_dest_chain_config_internal(
        env: &Env,
        dest_chain_selector: u64,
    ) -> Result<DestChainConfig, CCIPError> {
        let dest_chains: Map<u64, DestChainConfig> =
            env.storage()
                .persistent()
                .get(&DEST_CHAINS)
                .ok_or(CCIPError::DestinationChainNotSupported)?;

        dest_chains
            .get(dest_chain_selector)
            .ok_or(CCIPError::DestinationChainNotSupported)
    }

    fn get_dynamic_config_internal(env: &Env) -> Result<DynamicConfig, CCIPError> {
        env.storage()
            .instance()
            .get(&DYNAMIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)
    }

    fn get_static_config_internal(env: &Env) -> Result<StaticConfig, CCIPError> {
        env.storage()
            .instance()
            .get(&STATIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)
    }

    fn set_dest_chain_config(env: &Env, dest_chain_selector: u64, config: &DestChainConfig) {
        let mut dest_chains: Map<u64, DestChainConfig> = env
            .storage()
            .persistent()
            .get(&DEST_CHAINS)
            .unwrap_or(Map::new(env));

        dest_chains.set(dest_chain_selector, config.clone());
        env.storage().persistent().set(&DEST_CHAINS, &dest_chains);
    }

    fn get_pool_by_source_token_internal(
        env: &Env,
        static_config: &StaticConfig,
        source_token: &Address,
    ) -> Result<Address, CCIPError> {
        let registry = TokenAdminRegistryClient::new(env, &static_config.token_admin_registry);
        registry
            .get_pool(source_token)
            .ok_or(CCIPError::UnsupportedToken)
    }

    fn i128_to_bytes32(env: &Env, value: i128) -> BytesN<32> {
        let be_bytes = value.to_be_bytes();
        let mut padded = [0u8; 32];
        padded[16..].copy_from_slice(&be_bytes);
        BytesN::from_array(env, &padded)
    }

    /// Reject duplicate addresses within a single CCV list. Mirrors EVM
    /// `CCVConfigValidation._assertNoDuplicates` (used for user-supplied CCVs at
    /// `OnRamp.sol:812`).
    fn assert_no_duplicate_ccvs(ccvs: &Vec<Address>) -> Result<(), CCIPError> {
        let len = ccvs.len();
        for i in 0..len {
            for j in (i + 1)..len {
                if ccvs.get(i) == ccvs.get(j) {
                    return Err(CCIPError::DuplicateCCVNotAllowed);
                }
            }
        }
        Ok(())
    }

    /// Merge CCV address lists (user + lane-mandated + defaults) and build parallel
    /// `ccv_args` (empty bytes for lane-only and default-fallback entries), matching
    /// EVM `OnRamp._mergeCCVLists` empty-arg slots for non-user CCVs.
    fn merge_ccv_lists_with_ccv_args(
        env: &Env,
        user_ccvs: &Vec<Address>,
        user_ccv_args: &Vec<Bytes>,
        lane_mandated_ccvs: &Vec<Address>,
        default_ccvs: &Vec<Address>,
    ) -> Result<(Vec<Address>, Vec<Bytes>), CCIPError> {
        if user_ccvs.len() != user_ccv_args.len() {
            return Err(CCIPError::CCVLengthMismatch);
        }

        // M-16 / INV-SRC-1/17: user-supplied CCVs (from ExtraArgsV3) must not contain
        // duplicates, otherwise duplicate fee receipts are emitted and the
        // `ccv_and_executor_hash` committed to the message won't match offchain
        // expectations. Mirrors EVM `CCVConfigValidation._assertNoDuplicates(userCCVs)`
        // (OnRamp.sol:812), invoked before the merge. Lane-mandated and pool-required
        // CCVs are deduped against the running list below; only the user list needs this
        // explicit rejection since it is cloned verbatim.
        Self::assert_no_duplicate_ccvs(user_ccvs)?;

        if user_ccvs.is_empty() && lane_mandated_ccvs.is_empty() {
            let merged = default_ccvs.clone();
            let mut args = Vec::new(env);
            for _ in 0..merged.len() {
                args.push_back(Bytes::new(env));
            }
            return Ok((merged, args));
        }

        let mut merged = user_ccvs.clone();
        let mut args = user_ccv_args.clone();

        for i in 0..lane_mandated_ccvs.len() {
            if let Some(ccv) = lane_mandated_ccvs.get(i) {
                let mut already_present = false;
                for j in 0..merged.len() {
                    if merged.get(j) == Some(ccv.clone()) {
                        already_present = true;
                        break;
                    }
                }
                if !already_present {
                    merged.push_back(ccv.clone());
                    args.push_back(Bytes::new(env));
                }
            }
        }

        if merged.is_empty() {
            let merged = default_ccvs.clone();
            let mut args = Vec::new(env);
            for _ in 0..merged.len() {
                args.push_back(Bytes::new(env));
            }
            return Ok((merged, args));
        }

        Ok((merged, args))
    }

    /// Pool-required CCVs for an outbound transfer (EVM `OnRamp._getCCVsForPool`).
    /// Empty hook output falls back to destination `default_ccvs`.
    /// Resolves pool-required CCVs for an outbound transfer (EVM `_getCCVsForPool` parity).
    ///
    /// Returns the pool's requested `ccvs` plus an `include_defaults` flag that the caller
    /// uses to decide whether to append lane `default_ccvs` on top. This is the Stellar
    /// analogue of EVM's `address(0)` sentinel inside the pool-returned list.
    fn get_outbound_pool_required_ccvs(
        env: &Env,
        dest_chain_selector: u64,
        token: &Address,
        amount: i128,
        requested_finality: u32,
        token_args: Bytes,
        static_config: &StaticConfig,
    ) -> Result<PoolRequiredCCVs, CCIPError> {
        let pool_address = Self::get_pool_by_source_token_internal(env, static_config, token)?;
        let pool_client = TokenPoolClient::new(env, &pool_address);
        let required = pool_client.get_required_ccvs(
            token,
            &dest_chain_selector,
            &amount,
            &requested_finality,
            &token_args,
            &MessageDirection::Outbound,
        );
        Ok(required)
    }

    fn append_unique_pool_ccvs(
        env: &Env,
        merged_ccvs: &mut Vec<Address>,
        merged_ccv_args: &mut Vec<Bytes>,
        pool_ccvs: &Vec<Address>,
    ) {
        for i in 0..pool_ccvs.len() {
            if let Some(ccv) = pool_ccvs.get(i) {
                let mut present = false;
                for j in 0..merged_ccvs.len() {
                    if merged_ccvs.get(j) == Some(ccv.clone()) {
                        present = true;
                        break;
                    }
                }
                if !present {
                    merged_ccvs.push_back(ccv.clone());
                    merged_ccv_args.push_back(Bytes::new(env));
                }
            }
        }
    }

    fn get_ccv_blobs_and_receipts_internal(
        env: &Env,
        dest_chain_selector: u64,
        message_id: &BytesN<32>,
        original_sender: &Address,
        message: &StellarToAnyMessage,
        _extra_args: &GenericExtraArgsV3,
        merged_ccvs: &Vec<Address>,
        merged_ccv_args: &Vec<Bytes>,
        ccv_fee_responses: &Vec<FeeResponse>,
        fee_token_amount: i128,
    ) -> Result<(Vec<Bytes>, Vec<Receipt>), CCIPError> {
        if merged_ccvs.len() != merged_ccv_args.len()
            || ccv_fee_responses.len() != merged_ccvs.len()
        {
            return Err(CCIPError::CCVLengthMismatch);
        }

        let mut receipts = Vec::new(env);
        let mut verification_blobs = Vec::new(env);

        for i in 0..merged_ccvs.len() {
            let ccv = merged_ccvs.get(i).ok_or(CCIPError::CCVLengthMismatch)?;
            let ccv_args = merged_ccv_args.get(i).ok_or(CCIPError::CCVLengthMismatch)?;
            let ccv_fee_response = ccv_fee_responses
                .get(i)
                .ok_or(CCIPError::CCVLengthMismatch)?;

            let vvr = VersionedVerifierResolverClient::new(env, &ccv);
            let verifier_address = vvr.get_outbound_implementation(&dest_chain_selector, &ccv_args);

            receipts.push_back(Receipt {
                issuer: ccv,
                dest_gas_limit: ccv_fee_response.dest_gas_limit,
                dest_bytes_overhead: ccv_fee_response.dest_bytes_overhead,
                // fee is in USD cents
                fee_token_amount: ccv_fee_response.fee as i128,
                extra_args: ccv_args.clone(),
            });

            let mut verifier_args = Vec::new(&env);
            verifier_args.push_back(dest_chain_selector.into_val(env));
            verifier_args.push_back(original_sender.into_val(env));
            verifier_args.push_back(message_id.into_val(env));
            verifier_args.push_back(message.fee_token.into_val(env));
            verifier_args.push_back(fee_token_amount.into_val(env));
            verifier_args.push_back(ccv_args.into_val(env));

            let verification_blob = env.invoke_contract::<Result<Bytes, CCIPError>>(
                &verifier_address,
                &Symbol::new(&env, "forward_to_verifier"),
                verifier_args,
            )?;

            verification_blobs.push_back(verification_blob);
        }

        Ok((verification_blobs, receipts))
    }

    fn get_ccv_fee_internal(
        env: &Env,
        ccv_address: &Address,
        dest_chain_selector: u64,
        message_bytes: &Bytes,
        ccv_args: &Bytes,
        extra_args: &GenericExtraArgsV3,
    ) -> Result<FeeResponse, CCIPError> {
        let mut fee_args = Vec::new(env);
        fee_args.push_back(dest_chain_selector.into_val(env));
        fee_args.push_back(message_bytes.clone().into_val(env));
        fee_args.push_back(ccv_args.clone().into_val(env));
        fee_args.push_back(extra_args.block_confirmations.into_val(env));

        env.invoke_contract::<Result<FeeResponse, CCIPError>>(
            ccv_address,
            &Symbol::new(env, "get_fee"),
            fee_args,
        )
    }

    /// Cross-contract call to `Executor::get_fee` (H-8 / INV-FEE-8). Uses a raw
    /// `invoke_contract` (not `ExecutorClient`) so the `Result<u32, CCIPError>`
    /// return propagates through `?` — the interface crate redeclares `CCIPError`
    /// as a separate type, so a typed client would raise a type mismatch. Mirrors
    /// [`Self::get_ccv_fee_internal`]. The executor view enforces the executor
    /// layer of the 5-layer FTF opt-in matrix (reverting on disallowed finality),
    /// so the OnRamp needs no separate executor-layer finality check.
    fn get_executor_fee_internal(
        env: &Env,
        executor_address: &Address,
        dest_chain_selector: u64,
        requested_finality_config: u32,
        ccv_addresses: &Vec<Address>,
        executor_args: &Bytes,
        fee_token: &Address,
    ) -> Result<u32, CCIPError> {
        let mut fee_args = Vec::new(env);
        fee_args.push_back(dest_chain_selector.into_val(env));
        fee_args.push_back(requested_finality_config.into_val(env));
        fee_args.push_back(ccv_addresses.clone().into_val(env));
        fee_args.push_back(executor_args.clone().into_val(env));
        fee_args.push_back(fee_token.clone().into_val(env));

        env.invoke_contract::<Result<u32, CCIPError>>(
            executor_address,
            &Symbol::new(env, "get_fee"),
            fee_args,
        )
    }
}

mod test;
