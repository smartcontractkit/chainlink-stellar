#[soroban_sdk::contractargs(name = "ExampleCcipReceiverArgs")]
#[soroban_sdk::contractclient(name = "ExampleCcipReceiverClient")]
pub trait ExampleCcipReceiverInterface {
    fn get_router(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        router: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn ccip_receive(env: soroban_sdk::Env, message: AnyToStellarMessage) -> Result<(), CCIPError>;
    fn get_ccv_config(
        env: soroban_sdk::Env,
        source_chain_selector: u64,
    ) -> Result<CcvChainConfig, CCIPError>;
    fn last_message_id(env: soroban_sdk::Env) -> Result<soroban_sdk::BytesN<32>, CCIPError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn enable_remote_chain(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        dest_chain_selector: u64,
        extra_args: soroban_sdk::Bytes,
        allowed_finality_config: u32,
    ) -> Result<(), CCIPError>;
    fn disable_remote_chain(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        dest_chain_selector: u64,
    ) -> Result<(), CCIPError>;
    fn get_remote_chain_config(
        env: soroban_sdk::Env,
        chain_selector: u64,
    ) -> Result<RemoteChainConfig, CCIPError>;
    fn send_data_pay_fee_token(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        dest_chain_selector: u64,
        receiver: soroban_sdk::Bytes,
        data: soroban_sdk::Bytes,
        fee_token: soroban_sdk::Address,
        fee_token_amount: i128,
    ) -> Result<soroban_sdk::BytesN<32>, CCIPError>;
    fn apply_ccv_config_updates(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        updates: soroban_sdk::Vec<CcvConfigUpdate>,
    ) -> Result<(), CCIPError>;
    fn get_remote_chain_selectors(
        env: soroban_sdk::Env,
    ) -> Result<soroban_sdk::Vec<u64>, CCIPError>;
    fn get_remote_chain_extra_args(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
    ) -> Result<soroban_sdk::Bytes, CCIPError>;
    fn get_ccvs_and_finality_config(
        env: soroban_sdk::Env,
        source_chain_selector: u64,
        unused: soroban_sdk::Bytes,
    ) -> Result<CcvsAndFinalityConfig, CCIPError>;
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CcvConfigUpdate {
    pub source_chain_selector: u64,
    pub required_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub optional_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub optional_threshold: u32,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CcvChainConfig {
    pub required_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub optional_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub optional_threshold: u32,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RemoteChainConfig {
    pub extra_args: soroban_sdk::Bytes,
    pub allowed_finality_config: u32,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CcvsAndFinalityConfig {
    pub required_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub optional_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub optional_threshold: u32,
    pub allowed_finality_config: u32,
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
#[soroban_sdk::contractevent(topics = ["example_CcvCfg"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CcipCcvConfigSetEvent {
    pub source_chain_selector: u64,
    pub required_len: u32,
    pub optional_len: u32,
    pub optional_threshold: u32,
}
#[soroban_sdk::contractevent(topics = ["example_CcipMessageReceived"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CcipMessageReceivedEvent {
    pub message_id: soroban_sdk::BytesN<32>,
    pub source_chain_selector: u64,
    pub data_len: u32,
    pub sender_len: u32,
    pub dest_token_transfers: u32,
}
#[soroban_sdk::contractevent(topics = ["example_RemChCfg"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CcipRemoteChainConfiguredEvent {
    pub dest_chain_selector: u64,
    pub extra_args_len: u32,
    pub allowed_finality_config: u32,
}
