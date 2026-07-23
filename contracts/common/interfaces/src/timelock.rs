#[soroban_sdk::contractargs(name = "TimelockArgs")]
#[soroban_sdk::contractclient(name = "TimelockClient")]
pub trait TimelockInterface {
    fn cancel(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        id: soroban_sdk::BytesN<32>,
    ) -> Result<(), TimelockError>;
    fn has_role(
        env: soroban_sdk::Env,
        role: soroban_sdk::Symbol,
        account: soroban_sdk::Address,
    ) -> bool;
    fn grant_role(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        role: soroban_sdk::Symbol,
        account: soroban_sdk::Address,
    ) -> Result<(), TimelockError>;
    fn initialize(
        env: soroban_sdk::Env,
        min_delay: u64,
        admin: soroban_sdk::Address,
        proposers: soroban_sdk::Vec<soroban_sdk::Address>,
        executors: soroban_sdk::Vec<soroban_sdk::Address>,
        cancellers: soroban_sdk::Vec<soroban_sdk::Address>,
        bypassers: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<(), TimelockError>;
    fn revoke_role(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        role: soroban_sdk::Symbol,
        account: soroban_sdk::Address,
    ) -> Result<(), TimelockError>;
    fn is_operation(env: soroban_sdk::Env, id: soroban_sdk::BytesN<32>) -> bool;
    fn update_delay(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        new_delay: u64,
    ) -> Result<(), TimelockError>;
    fn execute_batch(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        calls: Calls,
        predecessor: soroban_sdk::BytesN<32>,
        salt: soroban_sdk::BytesN<32>,
    ) -> Result<(), TimelockError>;
    fn get_min_delay(env: soroban_sdk::Env) -> u64;
    fn get_timestamp(env: soroban_sdk::Env, id: soroban_sdk::BytesN<32>) -> u64;
    fn renounce_role(
        env: soroban_sdk::Env,
        account: soroban_sdk::Address,
        role: soroban_sdk::Symbol,
    ) -> Result<(), TimelockError>;
    fn schedule_batch(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        calls: Calls,
        predecessor: soroban_sdk::BytesN<32>,
        salt: soroban_sdk::BytesN<32>,
        delay: u64,
    ) -> Result<(), TimelockError>;
    fn extend_all_ttls(env: soroban_sdk::Env) -> Result<(), TimelockError>;
    fn get_role_member(
        env: soroban_sdk::Env,
        role: soroban_sdk::Symbol,
        index: u32,
    ) -> Result<soroban_sdk::Address, TimelockError>;
    fn is_operation_done(env: soroban_sdk::Env, id: soroban_sdk::BytesN<32>) -> bool;
    fn extend_op_time_ttl(
        env: soroban_sdk::Env,
        id: soroban_sdk::BytesN<32>,
    ) -> Result<(), TimelockError>;
    fn is_operation_ready(env: soroban_sdk::Env, id: soroban_sdk::BytesN<32>) -> bool;
    fn hash_operation_batch(
        env: soroban_sdk::Env,
        calls: Calls,
        predecessor: soroban_sdk::BytesN<32>,
        salt: soroban_sdk::BytesN<32>,
    ) -> soroban_sdk::BytesN<32>;
    fn is_operation_pending(env: soroban_sdk::Env, id: soroban_sdk::BytesN<32>) -> bool;
    fn get_role_member_count(env: soroban_sdk::Env, role: soroban_sdk::Symbol) -> u32;
    fn bypasser_execute_batch(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        calls: Calls,
    ) -> Result<(), TimelockError>;
    fn block_function_selector(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        selector: soroban_sdk::Symbol,
    ) -> Result<(), TimelockError>;
    fn get_blocked_selector_at(
        env: soroban_sdk::Env,
        index: u32,
    ) -> Result<soroban_sdk::Symbol, TimelockError>;
    fn unblock_function_selector(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        selector: soroban_sdk::Symbol,
    ) -> Result<(), TimelockError>;
    fn get_blocked_selector_count(env: soroban_sdk::Env) -> u32;
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TokenAmount {
    pub amount: i128,
    pub token: soroban_sdk::Address,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct GenericExtraArgsV3 {
    pub block_confirmations: u32,
    pub ccv_args: soroban_sdk::Vec<soroban_sdk::Bytes>,
    pub ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub executor: soroban_sdk::Address,
    pub executor_args: soroban_sdk::Bytes,
    pub gas_limit: u32,
    pub token_args: soroban_sdk::Bytes,
    pub token_receiver: soroban_sdk::Bytes,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AnyToStellarMessage {
    pub data: soroban_sdk::Bytes,
    pub dest_token_amounts: soroban_sdk::Vec<TokenAmount>,
    pub message_id: soroban_sdk::BytesN<32>,
    pub sender: soroban_sdk::Bytes,
    pub source_chain_selector: u64,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StellarToAnyMessage {
    pub data: soroban_sdk::Bytes,
    pub extra_args: soroban_sdk::Bytes,
    pub fee_token: soroban_sdk::Address,
    pub receiver: soroban_sdk::Bytes,
    pub token_amounts: soroban_sdk::Vec<TokenAmount>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Call {
    pub data: soroban_sdk::Bytes,
    pub to: soroban_sdk::BytesN<32>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Calls {
    pub inner: soroban_sdk::Vec<Call>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum TimelockDataKey {
    OpTime(soroban_sdk::BytesN<32>),
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
    InvalidFeeCalculation = 801,
    InvalidFeeTokenConversion = 802,
    ZeroFeeAggregatorNotAllowed = 803,
}
#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum TimelockError {
    NotInitialized = 1,
    AlreadyInitialized = 2,
    NotAuthorized = 3,
    OperationAlreadyScheduled = 20,
    InsufficientDelay = 21,
    SelectorIsBlocked = 22,
    OperationNotReady = 30,
    MissingPredecessor = 31,
    CallReverted = 32,
    OperationCannotBeCancelled = 40,
    InvalidInvokeData = 50,
    IndexOutOfBounds = 51,
}
#[soroban_sdk::contractevent(topics = ["tl_Cancelled"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CancelledEvent {
    pub id: soroban_sdk::BytesN<32>,
}
#[soroban_sdk::contractevent(topics = ["tl_RoleGranted"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleGrantedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["tl_RoleRevoked"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleRevokedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["tl_CallExecuted"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CallExecutedEvent {
    pub id: soroban_sdk::BytesN<32>,
    pub index: u32,
    pub to: soroban_sdk::BytesN<32>,
    pub data: soroban_sdk::Bytes,
}
#[soroban_sdk::contractevent(topics = ["tl_CallScheduled"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CallScheduledEvent {
    pub id: soroban_sdk::BytesN<32>,
    pub index: u32,
    pub to: soroban_sdk::BytesN<32>,
    pub data: soroban_sdk::Bytes,
    pub predecessor: soroban_sdk::BytesN<32>,
    pub salt: soroban_sdk::BytesN<32>,
    pub delay: u64,
}
#[soroban_sdk::contractevent(topics = ["tl_MinDelay"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct MinDelayChangeEvent {
    pub old_duration: u64,
    pub new_duration: u64,
}
#[soroban_sdk::contractevent(topics = ["tl_BypCallExec"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct BypasserCallExecutedEvent {
    pub index: u32,
    pub to: soroban_sdk::BytesN<32>,
    pub data: soroban_sdk::Bytes,
}
#[soroban_sdk::contractevent(topics = ["tl_SelBlocked"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FunctionSelectorBlockedEvent {
    pub selector: soroban_sdk::Symbol,
}
#[soroban_sdk::contractevent(topics = ["tl_SelUnblock"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FunctionSelectorUnblockedEvent {
    pub selector: soroban_sdk::Symbol,
}
