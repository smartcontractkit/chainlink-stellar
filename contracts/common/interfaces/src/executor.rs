#[soroban_sdk::contractargs(name = "ExecutorArgs")]
#[soroban_sdk::contractclient(name = "ExecutorClient")]
pub trait ExecutorInterface {
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_fee(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        requested_finality_config: u32,
        ccv_addresses: soroban_sdk::Vec<soroban_sdk::Address>,
        extra_args: soroban_sdk::Bytes,
        fee_token: soroban_sdk::Address,
    ) -> Result<u32, CCIPError>;
    fn is_owner(env: soroban_sdk::Env, addr: soroban_sdk::Address) -> bool;
    fn init_owner(env: soroban_sdk::Env, owner: soroban_sdk::Address) -> Result<(), CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        max_ccvs_per_msg: u32,
        dynamic_config: DynamicConfig,
    ) -> Result<(), CCIPError>;
    fn require_owner(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn get_dest_chains(
        env: soroban_sdk::Env,
    ) -> Result<soroban_sdk::Vec<RemoteChainConfigArgs>, CCIPError>;
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn get_allowed_ccvs(env: soroban_sdk::Env) -> soroban_sdk::Vec<soroban_sdk::Address>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn get_pending_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_dynamic_config(env: soroban_sdk::Env) -> Result<DynamicConfig, CCIPError>;
    fn set_dynamic_config(
        env: soroban_sdk::Env,
        dynamic_config: DynamicConfig,
    ) -> Result<(), CCIPError>;
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn withdraw_fee_tokens(
        env: soroban_sdk::Env,
        fee_tokens: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<(), CCIPError>;
    fn get_dest_chain_config(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
    ) -> Result<RemoteChainConfig, CCIPError>;
    fn apply_dest_chain_updates(
        env: soroban_sdk::Env,
        dest_chain_selectors_to_remove: soroban_sdk::Vec<u64>,
        dest_chain_selectors_to_add: soroban_sdk::Vec<RemoteChainConfigArgs>,
    ) -> Result<(), CCIPError>;
    fn get_max_ccvs_per_message(env: soroban_sdk::Env) -> Result<u32, CCIPError>;
    fn apply_allowed_ccv_updates(
        env: soroban_sdk::Env,
        ccvs_to_remove: soroban_sdk::Vec<soroban_sdk::Address>,
        ccvs_to_add: soroban_sdk::Vec<soroban_sdk::Address>,
        ccv_allowlist_enabled: bool,
    ) -> Result<(), CCIPError>;
    fn cancel_ownership_transfer(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn get_allowed_finality_config(env: soroban_sdk::Env) -> Result<u32, CCIPError>;
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct DynamicConfig {
    pub allowed_finality_config: u32,
    pub ccv_allowlist_enabled: bool,
    pub fee_aggregator: Option<soroban_sdk::Address>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RemoteChainConfig {
    pub enabled: bool,
    pub usd_cents_fee: u32,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RemoteChainConfigArgs {
    pub config: RemoteChainConfig,
    pub dest_chain_selector: u64,
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
    RequiredCCVMissing = 116,
    InvalidOptionalThreshold = 117,
    OptionalCCVQuorumNotReached = 118,
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
    InvalidChainForClient = 317,
    RouterNotConfigured = 318,
    InvalidSourcePoolAddress = 319,
    DuplicateCCVNotAllowed = 320,
    InvalidFeeCalculation = 801,
    InvalidFeeTokenConversion = 802,
    ZeroFeeAggregatorNotAllowed = 803,
    ExceedsMaxCCVs = 804,
    CCVNotAllowed = 805,
}
#[soroban_sdk::contractevent(topics = ["auth_RoleGranted"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleGrantedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["auth_RoleRevoked"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleRevokedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["auth_CallerAdded"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AuthorizedCallerAddedEvent {
    pub caller: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["auth_CallerRemoved"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AuthorizedCallerRemovedEvent {
    pub caller: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["auth_OwnerTransferStart"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipTransferStartedEvent {
    pub previous_owner: soroban_sdk::Address,
    pub new_owner: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["exec_CCVAdded"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CCVAddedEvent {
    pub ccv: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["exec_ConfigSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ConfigSetEvent {
    pub dynamic_config: DynamicConfig,
}
#[soroban_sdk::contractevent(topics = ["exec_CCVRemoved"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CCVRemovedEvent {
    pub ccv: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["exec_DestChainAdded"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct DestChainAddedEvent {
    pub dest_chain_selector: u64,
    pub config: RemoteChainConfig,
}
#[soroban_sdk::contractevent(topics = ["exec_DestChainRemoved"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct DestChainRemovedEvent {
    pub dest_chain_selector: u64,
}
#[soroban_sdk::contractevent(topics = ["exec_FeeTokenWithdrawn"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FeeTokenWithdrawnEvent {
    pub receiver: soroban_sdk::Address,
    pub fee_token: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["exec_CCVAllowlistUpdated"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CCVAllowlistUpdatedEvent {
    pub enabled: bool,
}
