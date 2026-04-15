#[soroban_sdk::contractargs(name = "RouterArgs")]
#[soroban_sdk::contractclient(name = "RouterClient")]
pub trait RouterInterface {
    fn get_fee(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        message: StellarToAnyMessage,
    ) -> Result<i128, CCIPError>;
    fn ccip_send(
        env: soroban_sdk::Env,
        sender: soroban_sdk::Address,
        dest_chain_selector: u64,
        message: StellarToAnyMessage,
        fee_token_amount: i128,
    ) -> Result<soroban_sdk::BytesN<32>, CCIPError>;
    fn get_config(env: soroban_sdk::Env) -> Result<RouterConfig, CCIPError>;
    fn get_onramp(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
    ) -> Result<soroban_sdk::Address, CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        rmn_proxy: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn is_offramp(
        env: soroban_sdk::Env,
        source_chain_selector: u64,
        offramp: soroban_sdk::Address,
    ) -> Result<bool, CCIPError>;
    fn set_onramp(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        onramp: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn add_offramp(
        env: soroban_sdk::Env,
        source_chain_selector: u64,
        offramp: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn get_onramps(env: soroban_sdk::Env) -> Result<soroban_sdk::Vec<OnRampEntry>, CCIPError>;
    fn get_offramps(env: soroban_sdk::Env) -> Result<soroban_sdk::Vec<OffRampEntry>, CCIPError>;
    fn route_message(
        env: soroban_sdk::Env,
        offramp: soroban_sdk::Address,
        source_chain_selector: u64,
        receiver: soroban_sdk::Address,
        message: AnyToStellarMessage,
    ) -> Result<(), CCIPError>;
    fn remove_offramp(
        env: soroban_sdk::Env,
        source_chain_selector: u64,
        offramp: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn apply_ramp_updates(
        env: soroban_sdk::Env,
        onramp_updates: soroban_sdk::Vec<OnRampEntry>,
        offramp_removes: soroban_sdk::Vec<OffRampEntry>,
        offramp_adds: soroban_sdk::Vec<OffRampEntry>,
    ) -> Result<(), CCIPError>;
    fn is_chain_supported(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
    ) -> Result<bool, CCIPError>;
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AllowListEntry {
    pub allowlist: soroban_sdk::Vec<soroban_sdk::Address>,
    pub allowlist_enabled: bool,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AllowListUpdate {
    pub added_allowlisted_senders: soroban_sdk::Vec<soroban_sdk::Address>,
    pub allowlist_enabled: bool,
    pub dest_chain_selector: u64,
    pub removed_allowlisted_senders: soroban_sdk::Vec<soroban_sdk::Address>,
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
pub struct OnRampEntry {
    pub dest_chain_selector: u64,
    pub onramp: soroban_sdk::Address,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OffRampEntry {
    pub offramp: soroban_sdk::Address,
    pub source_chain_selector: u64,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RouterConfig {
    pub rmn_proxy: soroban_sdk::Address,
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
#[soroban_sdk::contractevent(topics = ["router_OnRampSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OnRampSetEvent {
    pub dest_chain_selector: u64,
    pub onramp: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["router_OffRampAdded"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OffRampAddedEvent {
    pub source_chain_selector: u64,
    pub offramp: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["router_OffRampRemoved"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OffRampRemovedEvent {
    pub source_chain_selector: u64,
    pub offramp: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["router_MessageExecuted"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct MessageExecutedEvent {
    pub message_id: soroban_sdk::BytesN<32>,
    pub source_chain_selector: u64,
    pub offramp: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["router_CCIPSendRequested"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CCIPSendRequestedEvent {
    pub message_id: soroban_sdk::BytesN<32>,
    pub dest_chain_selector: u64,
    pub sender: soroban_sdk::Address,
}
