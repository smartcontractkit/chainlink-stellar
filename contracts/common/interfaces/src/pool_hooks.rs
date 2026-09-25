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
