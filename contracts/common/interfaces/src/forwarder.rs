#[soroban_sdk::contractargs(name = "creArgs")]
#[soroban_sdk::contractclient(name = "creClient")]
pub trait creInterface {
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn route(
        env: soroban_sdk::Env,
        transmission_id: soroban_sdk::BytesN<32>,
        transmitter: soroban_sdk::Address,
        receiver: soroban_sdk::Address,
        metadata: soroban_sdk::Bytes,
        validated_report: soroban_sdk::Bytes,
    ) -> Result<bool, ForwarderError>;
    fn report(
        env: soroban_sdk::Env,
        transmitter: soroban_sdk::Address,
        receiver: soroban_sdk::Address,
        raw_report: soroban_sdk::Bytes,
        report_context: soroban_sdk::Bytes,
        signatures: soroban_sdk::Vec<Ed25519Signature>,
    ) -> Result<(), ForwarderError>;
    fn is_owner(env: soroban_sdk::Env, addr: soroban_sdk::Address) -> bool;
    fn init_owner(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
    ) -> Result<(), ForwarderError>;
    fn set_config(
        env: soroban_sdk::Env,
        don_id: u32,
        config_version: u32,
        f: u32,
        signers: soroban_sdk::Vec<soroban_sdk::BytesN<32>>,
    ) -> Result<(), ForwarderError>;
    fn clear_config(
        env: soroban_sdk::Env,
        don_id: u32,
        config_version: u32,
    ) -> Result<(), ForwarderError>;
    fn add_forwarder(
        env: soroban_sdk::Env,
        forwarder: soroban_sdk::Address,
    ) -> Result<(), ForwarderError>;
    fn require_owner(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn set_new_owner(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn remove_forwarder(
        env: soroban_sdk::Env,
        forwarder: soroban_sdk::Address,
    ) -> Result<(), ForwarderError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn get_pending_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn get_transmission_info(
        env: soroban_sdk::Env,
        receiver: soroban_sdk::Address,
        workflow_execution_id: soroban_sdk::BytesN<32>,
        report_id: soroban_sdk::BytesN<2>,
    ) -> Result<TransmissionInfo, ForwarderError>;
    fn cancel_ownership_transfer(env: soroban_sdk::Env) -> Result<(), CCIPError>;
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
pub struct SignatureConfig {
    pub signers: soroban_sdk::Vec<soroban_sdk::BytesN<32>>,
    pub verification: SignatureVerificationConfig,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Config {
    pub f: u32,
    pub signers: soroban_sdk::Vec<soroban_sdk::BytesN<32>>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Transmission {
    pub state: TransmissionState,
    pub transmitter: soroban_sdk::Address,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Ed25519Signature {
    pub public_key: soroban_sdk::BytesN<32>,
    pub signature: soroban_sdk::BytesN<64>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TransmissionInfo {
    pub state: TransmissionState,
    pub transmitter: Option<soroban_sdk::Address>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum SignatureVerificationConfig {
    Threshold(u32),
    Failure(u32),
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum DataKey {
    Forwarder(soroban_sdk::Address),
    Config(u64),
    Transmission(soroban_sdk::BytesN<32>),
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum TransmissionState {
    NotAttempted = 0,
    Succeeded = 1,
    InvalidReceiver = 2,
    Failed = 3,
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
pub enum ForwarderError {
    AlreadyInitialized = 1,
    InvalidReport = 2,
    InvalidReportContext = 3,
    InvalidReportVersion = 4,
    FaultToleranceMustBePositive = 5,
    ExcessSigners = 6,
    InsufficientSigners = 7,
    InvalidConfig = 8,
    InvalidSignatureCount = 9,
    DuplicateSigner = 10,
    InvalidSignature = 11,
    InvalidSignerOrder = 12,
    AlreadyProcessed = 13,
    NotOwner = 14,
    NotProposedOwner = 15,
    Uninitialized = 16,
    UnauthorizedForwarder = 17,
    InvalidReceiver = 18,
    InvalidSigner = 19,
    CannotRemoveSelf = 20,
}
#[soroban_sdk::contractevent(export = false, topics = ["auth_RoleGranted"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleGrantedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["auth_RoleRevoked"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleRevokedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["auth_CallerAdded"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AuthorizedCallerAddedEvent {
    pub caller: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["auth_CallerRemoved"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AuthorizedCallerRemovedEvent {
    pub caller: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["auth_OwnerTransferStart"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipTransferStartedEvent {
    pub previous_owner: soroban_sdk::Address,
    pub new_owner: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["forwarder_ConfigSet"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ConfigSetEvent {
    #[topic]
    pub don_id: u32,
    #[topic]
    pub config_version: u32,
    pub f: u32,
    pub signers: soroban_sdk::Vec<soroban_sdk::BytesN<32>>,
}
#[soroban_sdk::contractevent(export = false, topics = ["forwarder_ForwarderAdded"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ForwarderAddedEvent {
    pub forwarder: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["forwarder_ReportProcessed"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ReportProcessedEvent {
    #[topic]
    pub receiver: soroban_sdk::Address,
    #[topic]
    pub workflow_execution_id: soroban_sdk::BytesN<32>,
    #[topic]
    pub report_id: soroban_sdk::BytesN<2>,
    pub success: bool,
}
#[soroban_sdk::contractevent(export = false, topics = ["forwarder_ForwarderRemoved"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ForwarderRemovedEvent {
    pub forwarder: soroban_sdk::Address,
}
