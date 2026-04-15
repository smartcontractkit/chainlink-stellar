/// TODO: `lock_or_burn`'s `requested_finality` parameter is kept for interface
/// parity with EVM but will always be 0 (WAIT_FOR_FINALITY) when Stellar is the
/// source chain, since Stellar has no reorg risk and no fast confirmation rules.
/// The FTF outbound rate limiting branch in `lock_or_burn` should be simplified
/// to always use the default bucket. For `release_or_mint`, `requested_finality`
/// is meaningful — messages from EVM sources may carry FTF flags, and Stellar as
/// the destination should respect them for inbound rate limiting.
#[soroban_sdk::contractclient(name = "TokenPoolClient")]
pub trait TokenPoolInterface {
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        token: soroban_sdk::Address,
        token_decimals: u32,
        router: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    fn set_router(env: soroban_sdk::Env, router: soroban_sdk::Address) -> Result<(), CCIPError>;

    fn get_router(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;

    fn set_advanced_pool_hooks(
        env: soroban_sdk::Env,
        hooks: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    fn remove_advanced_pool_hooks(env: soroban_sdk::Env) -> Result<(), CCIPError>;

    fn get_advanced_pool_hooks(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;

    fn lock_or_burn(
        env: soroban_sdk::Env,
        input: LockOrBurnIn,
        requested_finality: u32,
    ) -> Result<LockOrBurnOut, CCIPError>;

    fn release_or_mint(
        env: soroban_sdk::Env,
        input: ReleaseOrMintIn,
        requested_finality: u32,
    ) -> Result<ReleaseOrMintOut, CCIPError>;

    fn is_supported_token(
        env: soroban_sdk::Env,
        token: soroban_sdk::Address,
    ) -> Result<bool, CCIPError>;

    fn is_supported_chain(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<bool, CCIPError>;

    fn get_token(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;

    fn get_token_decimals(env: soroban_sdk::Env) -> Result<u32, CCIPError>;

    fn get_remote_pool(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<soroban_sdk::Bytes, CCIPError>;

    fn get_remote_token(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<soroban_sdk::Bytes, CCIPError>;

    fn apply_chain_updates(
        env: soroban_sdk::Env,
        adds: soroban_sdk::Vec<ChainUpdate>,
        removes: soroban_sdk::Vec<u64>,
    ) -> Result<(), CCIPError>;

    fn set_rate_limit_config(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
        outbound_config: RateLimitConfig,
        inbound_config: RateLimitConfig,
        fast_finality: bool,
    ) -> Result<(), CCIPError>;

    fn set_rate_limit_admin(
        env: soroban_sdk::Env,
        admin: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    fn get_current_rate_limiter_state(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
        fast_finality: bool,
    ) -> RateLimiterState;

    fn get_rate_limit_admin(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;

    fn set_allowed_finality_config(
        env: soroban_sdk::Env,
        allowed_finality: u32,
    ) -> Result<(), CCIPError>;

    fn get_allowed_finality_config(env: soroban_sdk::Env) -> u32;
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct LockOrBurnIn {
    pub receiver: soroban_sdk::Bytes,
    pub remote_chain_selector: u64,
    pub original_sender: soroban_sdk::Address,
    pub amount: i128,
    pub local_token: soroban_sdk::Address,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct LockOrBurnOut {
    pub dest_token_address: soroban_sdk::Bytes,
    pub dest_pool_data: soroban_sdk::Bytes,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ReleaseOrMintIn {
    pub original_sender: soroban_sdk::Bytes,
    pub remote_chain_selector: u64,
    pub receiver: soroban_sdk::Address,
    /// Source-denominated amount (EVM `sourceDenominatedAmount`).
    pub amount: i128,
    pub local_token: soroban_sdk::Address,
    pub source_pool_address: soroban_sdk::Bytes,
    pub source_pool_data: soroban_sdk::Bytes,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ReleaseOrMintOut {
    pub destination_amount: i128,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RateLimitConfig {
    pub is_enabled: bool,
    pub capacity: u128,
    pub rate: u128,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TokenBucket {
    pub tokens: u128,
    pub last_updated: u64,
    pub is_enabled: bool,
    pub capacity: u128,
    pub rate: u128,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RateLimiterState {
    pub outbound: TokenBucket,
    pub inbound: TokenBucket,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ChainUpdate {
    pub remote_chain_selector: u64,
    pub remote_pool_addresses: soroban_sdk::Bytes,
    pub remote_token_address: soroban_sdk::Bytes,
    pub outbound_rate_limiter_config: RateLimitConfig,
    pub inbound_rate_limiter_config: RateLimitConfig,
}

#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum CCIPError {
    NotInitialized = 1,
    AlreadyInitialized = 2,
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
    InvalidVersionTag = 13,
    InvalidSignatureLength = 14,
    InvalidSignature = 15,
    InvalidSignatureCount = 16,
    InvalidSignatureThreshold = 17,
    InvalidSignaturePubkey = 18,
    SourceSignersNotConfigured = 19,
    InvalidVerifierResults = 20,
    ReentrantCall = 21,
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
    InvalidTokenAmount = 50,
    InvalidReceiverAddress = 51,
    InvalidConfig = 52,
    InvalidVerifierResultsLength = 53,
    InboundImplementationNotFound = 54,
    OutboundImplementationNotFound = 55,
    InvalidAddress = 56,
    InvalidChainSelector = 57,
    InvalidVersion = 58,
    InvalidCCVVersion = 59,
    OffRampAlreadyExists = 60,
    OffRampMismatch = 61,
    BadRMNSignal = 62,
    UnsupportedDestinationChain = 63,
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
    ReceiverDoesNotExist = 114,
    ReceiverNotWasmContract = 115,
    OnlyRegistryModuleOrOwner = 201,
    OnlyAdministrator = 202,
    OnlyPendingAdministrator = 203,
    TokenAlreadyRegistered = 204,
    InvalidTokenPoolToken = 205,
    PoolTokenMismatch = 301,
    ChainNotSupported = 302,
    CallerIsNotRamp = 303,
    InsufficientPoolLiquidity = 304,
    InvalidRemotePoolAddress = 305,
    InvalidRemoteChainConfig = 306,
    InvalidRemoteChainDecimals = 307,
    DecimalAmountOverflow = 308,
    InvalidPoolTokenDecimals = 309,
    BucketOverfilled = 310,
    TokenMaxCapacityExceeded = 311,
    TokenRateLimitReached = 312,
    InvalidRateLimitRate = 313,
    DisabledNonZeroRateLimit = 314,
    InvalidRequestedFinality = 315,
    RequestedFinalityCanOnlyHaveOneMode = 316,
    InvalidFeeCalculation = 801,
    InvalidFeeTokenConversion = 802,
}
