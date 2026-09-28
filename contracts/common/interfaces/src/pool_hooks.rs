/// Cross-contract interface for advanced pool hooks — Stellar analogue of
/// EVM [`IAdvancedPoolHooks`](https://github.com/smartcontractkit/chainlink-ccip/blob/develop/chains/evm/contracts/interfaces/IAdvancedPoolHooks.sol).
///
/// An optional external contract that pools call before `lock_or_burn`
/// (preflight) and before `release_or_mint` (postflight) for additional
/// security checks such as:
/// - Sender allowlisting
/// - CCV (cross-chain verifier) configuration
/// - Policy engine validation
///
/// The hooks contract must authorize the calling pool via `require_auth`.
/// `caller` (the invoking pool's address) is passed explicitly and validated
/// against the hooks' `authorized_callers` set — the Soroban analogue of EVM
/// `AuthorizedCallers._validateCaller()` (no `msg.sender` on Soroban).
use crate::token_pool::{LockOrBurnIn, MessageDirection, PoolRequiredCCVs, ReleaseOrMintIn};
use common_error::CCIPError;
use soroban_sdk::contracttype;

/// Typed carrier for the operation arguments a policy engine's extractor decodes
/// into named `Parameter`s — the Soroban analogue of EVM's opaque
/// `IPolicyEngine.Payload.data` (= `msg.data[4:]`).
///
/// EVM carries the invoked method's raw calldata as opaque `bytes` because EVM
/// contracts share a raw-calldata boundary and the extractor is a separate
/// contract that re-decodes it. Soroban cross-contract calls pass typed `Val`s
/// natively, so the carrier is typed instead: one variant per hooks method,
/// holding exactly the args the extractor projects to `Parameter[]`. The policy
/// engine itself is generic over this (it passes `data` through to the extractor
/// without inspecting it — see `PolicyData` in `policy_engine.rs`).
///
/// `token_args` / `offchain_token_data` are deliberately NOT carried here: on
/// EVM they travel as `Payload.context` (the side-channel the extractor skips),
/// and Stellar mirrors that split.
#[contracttype(export = false)]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum PoolHooksPayloadData {
    /// EVM `preflightCheck(lockOrBurnIn, requestedFinalityConfig, tokenArgs[=context], amountPostFee)`.
    Preflight(PreflightPayload),
    /// EVM `postflightCheck(releaseOrMintIn, localAmount, requestedFinalityConfig)`.
    Postflight(PostflightPayload),
}

/// Args carried for a `preflight_check` policy run.
#[contracttype(export = false)]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PreflightPayload {
    pub lock_or_burn_in: LockOrBurnIn,
    pub requested_finality: u32,
    /// Amount after the pool's bps fee is deducted (EVM `amountPostFee`).
    pub amount_post_fee: i128,
}

/// Args carried for a `postflight_check` policy run.
#[contracttype(export = false)]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PostflightPayload {
    pub release_or_mint_in: ReleaseOrMintIn,
    /// Amount minted/released locally (EVM `localAmount`).
    pub local_amount: i128,
    pub requested_finality: u32,
}

#[soroban_sdk::contractclient(name = "PoolHooksClient")]
pub trait PoolHooksInterface {
    /// Called before lock_or_burn. Revert (return Err) to block the transfer.
    /// Matches EVM `IAdvancedPoolHooks.preflightCheck(lockOrBurnIn, requestedFinalityConfig, tokenArgs, amountPostFee)`.
    /// `caller` is the invoking pool, validated via `require_auth` + the
    /// `authorized_callers` set (EVM `_validateCaller`). `token_args` is the
    /// opaque, sender-supplied per-transfer payload threaded from the CCIP
    /// message (`extra_args.token_args`); hooks may use it as policy-engine
    /// context (EVM `AdvancedPoolHooks.preflightCheck` passes it as `context`
    /// to the policy engine, and USDC/CCTP pools use it for destination-domain
    /// routing).
    fn preflight_check(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        lock_or_burn_in: LockOrBurnIn,
        requested_finality: u32,
        token_args: soroban_sdk::Bytes,
        amount: i128,
    ) -> Result<(), CCIPError>;

    /// Called before release_or_mint. Revert (return Err) to block the transfer.
    /// Matches EVM `IAdvancedPoolHooks.postflightCheck`. `caller` is the invoking
    /// pool, validated via `require_auth` + the `authorized_callers` set (EVM
    /// `_validateCaller`).
    fn postflight_check(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        release_or_mint_in: ReleaseOrMintIn,
        local_amount: i128,
        requested_finality: u32,
    ) -> Result<(), CCIPError>;

    /// Returns required CCV addresses for a transfer in a given direction.
    /// Matches EVM `IAdvancedPoolHooks.getRequiredCCVs`, with `include_defaults` replacing the
    /// `address(0)` sentinel (Stellar has no zero address).
    fn get_required_ccvs(
        env: soroban_sdk::Env,
        local_token: soroban_sdk::Address,
        remote_chain_selector: u64,
        amount: i128,
        requested_finality: u32,
        extra_data: soroban_sdk::Bytes,
        direction: MessageDirection,
    ) -> PoolRequiredCCVs;
}
