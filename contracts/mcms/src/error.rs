//! MCMS contract errors. Timelock lives in [`contracts/timelock`](../../timelock); see `docs/mcms-stellar-plan.md`.

use soroban_sdk::contracterror;

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq, PartialOrd, Ord)]
#[repr(u32)]
pub enum McmsError {
    NotInitialized = 1,
    AlreadyInitialized = 2,
    NotOwner = 3,
    /// Authorization / validation failures mirroring ManyChainMultiSig Solidity errors.
    OutOfBoundsNumOfSigners = 10,
    SignerGroupsLengthMismatch = 11,
    OutOfBoundsGroup = 12,
    GroupTreeNotWellFormed = 13,
    OutOfBoundsGroupQuorum = 14,
    SignerInDisabledGroup = 15,
    SignersAddressesMustBeStrictlyIncreasing = 16,
    SignedHashAlreadySeen = 20,
    SignersAddressesMustBeStrictlyIncreasingSigs = 21,
    InvalidSigner = 22,
    MissingConfig = 23,
    InsufficientSigners = 24,
    ValidUntilHasAlreadyPassed = 25,
    ProofCannotBeVerified = 26,
    WrongChainIdMeta = 27,
    WrongMultiSigMeta = 28,
    PendingOps = 29,
    WrongPreOpCount = 30,
    WrongPostOpCount = 31,
    PostOpCountReached = 40,
    WrongChainIdOp = 41,
    WrongMultiSigOp = 42,
    RootExpired = 43,
    WrongNonce = 44,
    /// Downstream invoke failed because the callee returned a contract error
    /// (`InvokeError::Contract`). The callee's specific u32 code cannot be carried through
    /// Soroban's fixed-enum error model (`#[contracterror]` variants are unit u32 codes with
    /// no payload), so only the failure *mode* is distinguished — see [`Self::CallAborted`].
    CallReverted = 45,
    InvalidSignature = 46,
    InvalidSignatureEncoding = 47,
    NonceOverflow = 48,
    InvalidUint40 = 49,
    InvalidInvokeData = 50,
    NonZeroValue = 51,
    MissingRootMetadata = 52,
    /// `valid_until` exceeds the effective max validity (the smaller of
    /// [`crate::constants::MAX_ROOT_VALIDITY_SECS`] and the dynamic cap derived from
    /// `LEDGER_BUMP * min_secs_per_ledger - SEEN_TTL_SAFETY_MARGIN_SECS`).
    ValidUntilExceedsMaximum = 53,
    /// `set_min_secs_per_ledger` was called with a value outside
    /// [[`crate::constants::MIN_SECS_PER_LEDGER_LOWER_BOUND`],
    /// [`crate::constants::MIN_SECS_PER_LEDGER_UPPER_BOUND`]].
    InvalidMinSecsPerLedger = 54,
    /// Downstream invoke failed because the callee trapped / panicked / hit a host error
    /// (`InvokeError::Abort`), as opposed to returning a contract error ([`Self::CallReverted`]).
    CallAborted = 55,
}

impl From<common_error::CCIPError> for McmsError {
    fn from(e: common_error::CCIPError) -> Self {
        match e {
            common_error::CCIPError::NotOwner => McmsError::NotOwner,
            common_error::CCIPError::AlreadyInitialized => McmsError::AlreadyInitialized,
            common_error::CCIPError::NotInitialized => McmsError::NotInitialized,
            common_error::CCIPError::NoPendingOwner => McmsError::NotOwner,
            _ => McmsError::NotOwner,
        }
    }
}
