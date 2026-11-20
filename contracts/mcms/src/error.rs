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
    CallReverted = 45,
    InvalidSignature = 46,
    InvalidSignatureEncoding = 47,
    NonceOverflow = 48,
    InvalidUint40 = 49,
    InvalidInvokeData = 50,
    NonZeroValue = 51,
    MissingRootMetadata = 52,
    /// `valid_until` exceeds the fixed maximum root-validity policy.
    ValidUntilExceedsMaximum = 53,
    /// Reserved v1 error code. V2 has no seconds-per-ledger policy input.
    InvalidMinSecsPerLedger = 54,
    /// The downstream invocation aborted before returning a typed contract result.
    /// This includes host-level invocation failures and callee panics/traps.
    CallAborted = 55,
    UnsupportedEncodingVersion = 56,
    ConfigVersionMismatch = 57,
    InvalidTarget = 58,
    InvalidMultisig = 59,
    InvalidArgsXdr = 60,
    ConfigVersionOverflow = 61,
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
