#![no_std]

use soroban_sdk::contracterror;

/// Derive macro for generating `From<T>` implementations on error enums.
///
/// Annotate a unit variant with one or more `#[from(...)]` attributes:
///
/// ```ignore
/// use common_error::ErrorConversions;
///
/// #[derive(ErrorConversions)]
/// enum ContractError {
///     #[from(common_authorization::AuthError)]
///     Unauthorized,
/// }
/// ```

#[contracterror]
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
#[repr(u32)]
pub enum CCIPError {
    // ============================================================
    // Initializable errors
    // ============================================================
    NotInitialized = 1,
    AlreadyInitialized = 2,

    // ============================================================
    // Authorization errors
    // ============================================================
    Unauthorized = 3,
    NotOwner = 4,
    NoPendingOwner = 5,
    CallerNotAuthorized = 6,
    CallerAlreadyAuthorized = 7,
    CallerNotFound = 8,
    RoleNotGranted = 9,
    FeatureNotEnabled = 10,
    RoleAlreadyGranted = 11,
    CannotRenounceRole = 12,

    // ============================================================
    // Verifier errors
    // ============================================================
    InvalidVersionTag = 13,
    InvalidSignatureLength = 14,
    InvalidSignature = 15,
    InvalidSignatureCount = 16,
    InvalidSignatureThreshold = 17,
    InvalidSignaturePubkey = 18,
    SourceSignersNotConfigured = 19,
    InvalidVerifierResults = 20,
    ReentrantCall = 21,

    // ============================================================
    // Fee quoter errors
    // ============================================================
    TokenNotSupported = 22,
    FeeTokenNotSupported = 23,
    NoGasPriceAvailable = 24,
    DestinationChainNotEnabled = 25,
    InvalidExtraArgsTag = 26,
    InvalidExtraArgsData = 27,
    MessageGasLimitTooHigh = 28,
    MessageTooLarge = 29,
    UnsupportedNumberOfTokens = 30,
    InvalidDestChainConfig = 31,
    MessageFeeTooHigh = 32,
    InvalidStaticConfig = 33,
    InvalidTokenReceiver = 34,
    SourceTokenDataTooLarge = 35,
    InvalidDestBytesOverhead = 36,

    // ============================================================
    // Onramp errors
    // ============================================================
    DestinationChainNotSupported = 37,
    MustBeCalledByRouter = 38,
    RouterMustSetOriginalSender = 39,
    CannotSendZeroTokens = 40,
    CanOnlySendOneTokenPerMessage = 41,
    UnsupportedToken = 42,
    InvalidDestChainAddress = 43,
    FeeExceedsMaxAllowed = 44,
    InsufficientFeeTokenAmount = 45,
    TokenReceiverNotAllowed = 46,
    CursedByRMN = 47,
    RemoteChainNotSupported = 48,
    SenderNotAllowed = 49,

    // ============================================================
    // Common errors
    // ============================================================
    InvalidTokenAmount = 50,
    InvalidReceiverAddress = 51,
    InvalidConfig = 52,
    /// Verifier results data is too short (must be at least 4 bytes for version prefix)
    InvalidVerifierResultsLength = 53,
    /// No inbound implementation found for the given version
    InboundImplementationNotFound = 54,
    /// No outbound implementation found for the given destination chain
    OutboundImplementationNotFound = 55,
    /// Invalid configuration: zero address not allowed
    InvalidAddress = 56,
    /// Invalid configuration: zero chain selector not allowed
    InvalidChainSelector = 57,
    InvalidVersion = 58,
    InvalidCCVVersion = 59,

    // ============================================================
    // More Onramp errors (continued to maintain increasing order)
    // ============================================================
    OffRampAlreadyExists = 60,
    OffRampMismatch = 61,
    BadRMNSignal = 62,
    UnsupportedDestinationChain = 63,

    // ============================================================
    // RMN Remote errors (mirrors RMNRemote.sol error set)
    // ============================================================
    AlreadyCursed = 64,
    ConfigNotSet = 65,
    DuplicateOnchainPublicKey = 66,
    InvalidSignerOrder = 67,
    NotEnoughSigners = 68,
    NotCursed = 69,
    OutOfOrderSignatures = 70,
    ThresholdNotMet = 71,
    UnexpectedSigner = 72,
    ZeroValueNotAllowed = 73,

    // ============================================================
    // OffRamp errors
    // ============================================================
    SourceChainNotEnabled = 100,
    InvalidSourceChainConfig = 101,
    InvalidOnRampAddress = 102,
    InvalidOffRampAddress = 103,
    InvalidMessageDestination = 104,
    MessageAlreadyExecuted = 105,
    InvalidExecutionState = 106,
    CCVLengthMismatch = 107,
    CCVQuorumNotMet = 108,
    ReceiverError = 109,
    GasLimitOverrideTooLow = 110,
    InvalidReceiverLength = 111,
    TokenHandlingError = 112,
    MessageDecodingError = 113,
    /// Inbound receiver address has no ledger entry (see `Address::executable()`).
    ReceiverDoesNotExist = 114,
    /// `ccip_receive` may only be invoked on a Soroban Wasm contract, not a plain account / SAC.
    ReceiverNotWasmContract = 115,

    // ============================================================
    // TokenAdminRegistry errors
    // ============================================================
    OnlyRegistryModuleOrOwner = 201,
    OnlyAdministrator = 202,
    OnlyPendingAdministrator = 203,
    TokenAlreadyRegistered = 204,
    InvalidTokenPoolToken = 205,

    // ============================================================
    // TokenPool errors
    // ============================================================
    PoolTokenMismatch = 301,
    ChainNotSupported = 302,
    CallerIsNotRamp = 303,
    InsufficientPoolLiquidity = 304,
    InvalidRemotePoolAddress = 305,
    InvalidRemoteChainConfig = 306,
    /// `source_pool_data` is not a valid 32-byte ABI-encoded decimals scalar (EVM `InvalidRemoteChainDecimals`).
    InvalidRemoteChainDecimals = 307,
    /// Local amount conversion overflow (EVM `OverflowDetected` / rate math).
    DecimalAmountOverflow = 308,
    /// Pool `token_decimals` init argument out of range (must fit `uint8` / EVM `i_tokenDecimals`).
    InvalidPoolTokenDecimals = 309,
    /// Token bucket current tokens exceed capacity (EVM `BucketOverfilled`).
    BucketOverfilled = 310,
    /// Requested amount exceeds bucket capacity (EVM `TokenMaxCapacityExceeded`).
    TokenMaxCapacityExceeded = 311,
    /// Insufficient bucket tokens; wait for refill (EVM `TokenRateLimitReached`).
    TokenRateLimitReached = 312,
    /// Rate limit config has rate > capacity (EVM `InvalidRateLimitRate`).
    InvalidRateLimitRate = 313,
    /// Disabled config must have rate=0 and capacity=0 (EVM `DisabledNonZeroRateLimit`).
    DisabledNonZeroRateLimit = 314,
    /// Requested finality is not permitted by the pool's allowed finality config
    /// (EVM `FinalityCodec.InvalidRequestedFinality`).
    InvalidRequestedFinality = 315,
    /// Requested finality combines a flag with a block depth — exactly one mode allowed
    /// (EVM `FinalityCodec.RequestedFinalityCanOnlyHaveOneMode`).
    RequestedFinalityCanOnlyHaveOneMode = 316,
    /// Inbound source chain not allowlisted for this client (EVM `CCIPClientExample.InvalidChain` /
    /// `validChain` when `s_chains[source].extraArgs` is empty).
    InvalidChainForClient = 317,

    // ============================================================
    // Calculation errors
    // ============================================================
    InvalidFeeCalculation = 801,
    InvalidFeeTokenConversion = 802,
}
