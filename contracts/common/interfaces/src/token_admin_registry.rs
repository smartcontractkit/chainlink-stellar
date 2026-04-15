#[soroban_sdk::contractclient(name = "TokenAdminRegistryClient")]
pub trait TokenAdminRegistryInterface {
    fn initialize(env: soroban_sdk::Env, owner: soroban_sdk::Address) -> Result<(), CCIPError>;

    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;

    fn get_pool(
        env: soroban_sdk::Env,
        token: soroban_sdk::Address,
    ) -> Result<Option<soroban_sdk::Address>, CCIPError>;

    fn get_pools(
        env: soroban_sdk::Env,
        tokens: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<soroban_sdk::Vec<Option<soroban_sdk::Address>>, CCIPError>;

    fn get_token_config(
        env: soroban_sdk::Env,
        token: soroban_sdk::Address,
    ) -> Result<TokenConfig, CCIPError>;

    fn get_all_configured_tokens(
        env: soroban_sdk::Env,
        start_index: u32,
        max_count: u32,
    ) -> Result<soroban_sdk::Vec<soroban_sdk::Address>, CCIPError>;

    fn is_administrator(
        env: soroban_sdk::Env,
        local_token: soroban_sdk::Address,
        administrator: soroban_sdk::Address,
    ) -> Result<bool, CCIPError>;

    fn set_pool(
        env: soroban_sdk::Env,
        local_token: soroban_sdk::Address,
        pool: Option<soroban_sdk::Address>,
    ) -> Result<(), CCIPError>;

    fn propose_administrator(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        local_token: soroban_sdk::Address,
        administrator: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    fn accept_admin_role(
        env: soroban_sdk::Env,
        local_token: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    fn transfer_admin_role(
        env: soroban_sdk::Env,
        local_token: soroban_sdk::Address,
        new_admin: Option<soroban_sdk::Address>,
    ) -> Result<(), CCIPError>;

    fn is_registry_module(
        env: soroban_sdk::Env,
        module: soroban_sdk::Address,
    ) -> Result<bool, CCIPError>;

    fn add_registry_module(
        env: soroban_sdk::Env,
        module: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    fn remove_registry_module(
        env: soroban_sdk::Env,
        module: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TokenConfig {
    pub administrator: Option<soroban_sdk::Address>,
    pub pending_administrator: Option<soroban_sdk::Address>,
    pub token_pool: Option<soroban_sdk::Address>,
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
    InvalidFeeCalculation = 801,
    InvalidFeeTokenConversion = 802,
}
