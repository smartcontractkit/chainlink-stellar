#[soroban_sdk::contractargs(name = "OffRampArgs")]
#[soroban_sdk::contractclient(name = "OffRampClient")]
pub trait OffRampInterface {
    fn init(env: soroban_sdk::Env, rmn_proxy: soroban_sdk::Address) -> Result<(), CCIPError>;
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn execute(
        env: soroban_sdk::Env,
        encoded_message: soroban_sdk::Bytes,
        ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
        verifier_results: soroban_sdk::Vec<soroban_sdk::Bytes>,
        gas_limit_override: u32,
    ) -> Result<(), CCIPError>;
    fn is_owner(env: soroban_sdk::Env, addr: soroban_sdk::Address) -> bool;
    fn is_cursed(env: soroban_sdk::Env) -> Result<bool, CCIPError>;
    fn init_owner(env: soroban_sdk::Env, owner: soroban_sdk::Address) -> Result<(), CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        static_config: StaticConfig,
    ) -> Result<(), CCIPError>;
    fn require_owner(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn set_new_owner(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn get_pending_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_static_config(env: soroban_sdk::Env) -> Result<StaticConfig, CCIPError>;
    fn is_subject_cursed(
        env: soroban_sdk::Env,
        subject: soroban_sdk::BytesN<16>,
    ) -> Result<bool, CCIPError>;
    fn require_not_cursed(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn get_execution_state(
        env: soroban_sdk::Env,
        message_id: soroban_sdk::BytesN<32>,
    ) -> Result<MessageExecutionState, CCIPError>;
    fn get_source_chain_config(
        env: soroban_sdk::Env,
        source_chain_selector: u64,
    ) -> Result<SourceChainConfig, CCIPError>;
    fn require_chain_not_cursed(
        env: soroban_sdk::Env,
        chain_selector: u64,
    ) -> Result<(), CCIPError>;
    fn cancel_ownership_transfer(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn extend_execution_state_ttl(
        env: soroban_sdk::Env,
        message_id: soroban_sdk::BytesN<32>,
    ) -> Result<(), CCIPError>;
    fn require_subject_not_cursed(
        env: soroban_sdk::Env,
        subject: soroban_sdk::BytesN<16>,
    ) -> Result<(), CCIPError>;
    fn get_all_source_chain_configs(
        env: soroban_sdk::Env,
    ) -> Result<(soroban_sdk::Vec<u64>, soroban_sdk::Vec<SourceChainConfig>), CCIPError>;
    fn apply_source_chain_cfg_updates(
        env: soroban_sdk::Env,
        source_chain_config_args: soroban_sdk::Vec<SourceChainConfigArgs>,
    ) -> Result<(), CCIPError>;
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
pub struct StaticConfig {
    pub chain_selector: u64,
    pub rmn_proxy: soroban_sdk::Address,
    pub token_admin_registry: soroban_sdk::Address,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SourceChainConfig {
    pub default_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub is_enabled: bool,
    pub lane_mandated_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub on_ramps: soroban_sdk::Vec<soroban_sdk::Bytes>,
    pub router: soroban_sdk::Address,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SourceChainConfigArgs {
    pub default_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub is_enabled: bool,
    pub lane_mandated_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub on_ramps: soroban_sdk::Vec<soroban_sdk::Bytes>,
    pub router: soroban_sdk::Address,
    pub source_chain_selector: u64,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum DataKey {
    ExecState(soroban_sdk::BytesN<32>),
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum MessageExecutionState {
    Untouched = 0,
    InProgress = 1,
    Success = 2,
    Failure = 3,
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
    InvalidChainForClient = 317,
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
#[soroban_sdk::contractevent(topics = ["offramp_1_7_StaticConfigSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StaticConfigSetEvent {
    pub static_config: StaticConfig,
}
#[soroban_sdk::contractevent(topics = ["offramp_1_7_SrcChainCfgSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SourceChainConfigSetEvent {
    pub source_chain_selector: u64,
    pub source_config: SourceChainConfig,
}
#[soroban_sdk::contractevent(topics = ["offramp_1_7_ExecStateChanged"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ExecutionStateChangedEvent {
    pub source_chain_selector: u64,
    pub sequence_number: u64,
    pub message_id: soroban_sdk::BytesN<32>,
    pub state: MessageExecutionState,
    pub return_data: soroban_sdk::Bytes,
}
