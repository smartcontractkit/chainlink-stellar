#[soroban_sdk::contractargs(name = "AdvancedPoolHooksArgs")]
#[soroban_sdk::contractclient(name = "AdvancedPoolHooksClient")]
pub trait AdvancedPoolHooksInterface {
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn upgrade(
        env: soroban_sdk::Env,
        new_wasm_hash: soroban_sdk::BytesN<32>,
    ) -> Result<(), CCIPError>;
    fn is_owner(env: soroban_sdk::Env, addr: soroban_sdk::Address) -> bool;
    fn init_owner(env: soroban_sdk::Env, owner: soroban_sdk::Address) -> Result<(), CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        allowlist: soroban_sdk::Vec<soroban_sdk::Address>,
        threshold_amount: i128,
        authorized_callers: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<(), CCIPError>;
    fn get_allowlist(env: soroban_sdk::Env) -> soroban_sdk::Vec<soroban_sdk::Address>;
    fn require_owner(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn get_ccv_config(env: soroban_sdk::Env, remote_chain_selector: u64) -> Option<CCVConfig>;
    fn preflight_check(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        lock_or_burn_in: LockOrBurnIn,
        requested_finality: u32,
        token_args: soroban_sdk::Bytes,
        amount: i128,
    ) -> Result<(), CCIPError>;
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn postflight_check(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        release_or_mint_in: ReleaseOrMintIn,
        local_amount: i128,
        requested_finality: u32,
    ) -> Result<(), CCIPError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn get_pending_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_required_ccvs(
        env: soroban_sdk::Env,
        local_token: soroban_sdk::Address,
        remote_chain_selector: u64,
        amount: i128,
        requested_finality: u32,
        extra_data: soroban_sdk::Bytes,
        direction: MessageDirection,
    ) -> PoolRequiredCCVs;
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn get_all_ccv_configs(env: soroban_sdk::Env) -> soroban_sdk::Vec<CCVConfigArg>;
    fn get_threshold_amount(env: soroban_sdk::Env) -> i128;
    fn set_threshold_amount(env: soroban_sdk::Env, threshold_amount: i128)
        -> Result<(), CCIPError>;
    fn get_allowlist_enabled(env: soroban_sdk::Env) -> bool;
    fn apply_allowlist_updates(
        env: soroban_sdk::Env,
        removes: soroban_sdk::Vec<soroban_sdk::Address>,
        adds: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<(), CCIPError>;
    fn apply_ccv_config_updates(
        env: soroban_sdk::Env,
        configs: soroban_sdk::Vec<CCVConfigArg>,
    ) -> Result<(), CCIPError>;
    fn cancel_ownership_transfer(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn get_all_authorized_callers(env: soroban_sdk::Env) -> soroban_sdk::Vec<soroban_sdk::Address>;
    fn apply_authorized_callers_updates(
        env: soroban_sdk::Env,
        removes: soroban_sdk::Vec<soroban_sdk::Address>,
        adds: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<(), CCIPError>;
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct PoolRequiredCCVs {
    pub ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub include_defaults: bool,
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
pub enum MessageDirection {
    Outbound,
    Inbound,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CCVConfig {
    pub inbound_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub inbound_include_defaults: bool,
    pub outbound_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub outbound_include_defaults: bool,
    pub threshold_inbound_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub threshold_outbound_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CCVConfigArg {
    pub inbound_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub inbound_include_defaults: bool,
    pub outbound_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub outbound_include_defaults: bool,
    pub remote_chain_selector: u64,
    pub threshold_inbound_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub threshold_outbound_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
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
    InvalidTokenTransferFeeConfig = 321,
    InvalidTransferFeeBps = 322,
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
#[soroban_sdk::contractevent(topics = ["aph_AllowListAdd"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AllowListAddEvent {
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["aph_AllowListRemove"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AllowListRemoveEvent {
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["aph_CCVConfigUpdated"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CCVConfigUpdatedEvent {
    pub remote_chain_selector: u64,
    pub config: CCVConfig,
}
#[soroban_sdk::contractevent(topics = ["aph_ThresholdAmountSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ThresholdAmountSetEvent {
    pub threshold_amount: i128,
}
