#[soroban_sdk::contractargs(name = "LockReleasePoolArgs")]
#[soroban_sdk::contractclient(name = "LockReleasePoolClient")]
pub trait LockReleasePoolInterface {
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_fee(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<PoolFeeResult, CCIPError>;
    fn is_owner(env: soroban_sdk::Env, addr: soroban_sdk::Address) -> bool;
    fn get_token(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn get_router(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn init_owner(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        token: soroban_sdk::Address,
        token_decimals: u32,
        router: soroban_sdk::Address,
        ramp_registry: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn set_router(
        env: soroban_sdk::Env,
        router: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn lock_or_burn(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        input: LockOrBurnIn,
        requested_finality: u32,
    ) -> Result<LockOrBurnOut, CCIPError>;
    fn require_owner(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn set_new_owner(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn get_remote_pool(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<soroban_sdk::Bytes, CCIPError>;
    fn release_or_mint(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        input: ReleaseOrMintIn,
        requested_finality: u32,
    ) -> Result<ReleaseOrMintOut, CCIPError>;
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn get_remote_token(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<soroban_sdk::Bytes, CCIPError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn get_pending_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_ramp_registry(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_required_ccvs(
        env: soroban_sdk::Env,
        local_token: soroban_sdk::Address,
        remote_chain_selector: u64,
        amount: i128,
        requested_finality: u32,
        extra_data: soroban_sdk::Bytes,
        direction: MessageDirection,
    ) -> PoolRequiredCCVs;
    fn set_ramp_registry(
        env: soroban_sdk::Env,
        ramp_registry: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn get_token_decimals(env: soroban_sdk::Env) -> Result<u32, CCIPError>;
    fn is_supported_chain(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<bool, CCIPError>;
    fn is_supported_token(
        env: soroban_sdk::Env,
        token: soroban_sdk::Address,
    ) -> Result<bool, CCIPError>;
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn apply_chain_updates(
        env: soroban_sdk::Env,
        adds: soroban_sdk::Vec<ChainUpdate>,
        removes: soroban_sdk::Vec<u64>,
    ) -> Result<(), CCIPError>;
    fn set_pool_fee_config(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
        config: PoolFeeConfig,
    ) -> Result<(), CCIPError>;
    fn get_rate_limit_admin(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn set_rate_limit_admin(
        env: soroban_sdk::Env,
        admin: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn set_rate_limit_config(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
        outbound_config: RateLimitConfig,
        inbound_config: RateLimitConfig,
        fast_finality: bool,
    ) -> Result<(), CCIPError>;
    fn get_advanced_pool_hooks(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn set_advanced_pool_hooks(
        env: soroban_sdk::Env,
        hooks: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn cancel_ownership_transfer(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn remove_advanced_pool_hooks(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn get_allowed_finality_config(env: soroban_sdk::Env) -> u32;
    fn set_allowed_finality_config(
        env: soroban_sdk::Env,
        allowed_finality: u32,
    ) -> Result<(), CCIPError>;
    fn get_current_rate_limiter_state(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
        fast_finality: bool,
    ) -> RateLimiterState;
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
pub struct ChainUpdate {
    pub inbound_rate_limiter_config: RateLimitConfig,
    pub outbound_rate_limiter_config: RateLimitConfig,
    pub remote_chain_selector: u64,
    pub remote_pool_addresses: soroban_sdk::Bytes,
    pub remote_token_address: soroban_sdk::Bytes,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TokenBucket {
    pub capacity: u128,
    pub is_enabled: bool,
    pub last_updated: u64,
    pub rate: u128,
    pub tokens: u128,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct LockOrBurnIn {
    pub amount: i128,
    pub local_token: soroban_sdk::Address,
    pub original_sender: soroban_sdk::Address,
    pub receiver: soroban_sdk::Bytes,
    pub remote_chain_selector: u64,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct LockOrBurnOut {
    pub dest_pool_data: soroban_sdk::Bytes,
    pub dest_token_address: soroban_sdk::Bytes,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct PoolFeeConfig {
    pub fee_usd_cents: u32,
    pub is_enabled: bool,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct PoolFeeResult {
    pub fee_usd_cents: u32,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RateLimitConfig {
    pub capacity: u128,
    pub is_enabled: bool,
    pub rate: u128,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ReleaseOrMintIn {
    pub amount: i128,
    pub local_token: soroban_sdk::Address,
    pub original_sender: soroban_sdk::Bytes,
    pub receiver: soroban_sdk::Address,
    pub remote_chain_selector: u64,
    pub source_pool_address: soroban_sdk::Bytes,
    pub source_pool_data: soroban_sdk::Bytes,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct PoolRequiredCCVs {
    pub ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub include_defaults: bool,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RateLimiterState {
    pub inbound: TokenBucket,
    pub outbound: TokenBucket,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ReleaseOrMintOut {
    pub destination_amount: i128,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RemoteChainConfig {
    pub remote_pool_address: soroban_sdk::Bytes,
    pub remote_token_address: soroban_sdk::Bytes,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum PoolDataKey {
    Token,
    RemoteChainConfig(u64),
    SupportedChains,
    TokenDecimals,
    OutboundRateLimit(u64),
    InboundRateLimit(u64),
    RateLimitAdmin,
    FtfOutboundRateLimit(u64),
    FtfInboundRateLimit(u64),
    AllowedFinalityConfig,
    RampRegistry,
    AdvancedPoolHooks,
    PoolFeeConfig(u64),
    Router,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum MessageDirection {
    Outbound,
    Inbound,
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
#[soroban_sdk::contractevent(topics = ["pool_Burned"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct BurnedEvent {
    pub sender: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["pool_Locked"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct LockedEvent {
    pub sender: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["pool_Minted"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct MintedEvent {
    pub sender: soroban_sdk::Address,
    pub recipient: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["pool_Released"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ReleasedEvent {
    pub sender: soroban_sdk::Address,
    pub recipient: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["pool_ChainRemoved"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ChainRemovedEvent {
    pub remote_chain_selector: u64,
}
#[soroban_sdk::contractevent(topics = ["pool_ChainConfigured"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ChainConfiguredEvent {
    pub remote_chain_selector: u64,
    pub remote_pool_address: soroban_sdk::Bytes,
    pub remote_token_address: soroban_sdk::Bytes,
    pub outbound_rate_limiter_config: RateLimitConfig,
    pub inbound_rate_limiter_config: RateLimitConfig,
}
#[soroban_sdk::contractevent(topics = ["pool_FinalityConfigSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FinalityConfigSetEvent {
    pub allowed_finality: u32,
}
#[soroban_sdk::contractevent(topics = ["pool_FtfInboundConsumed"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FtfInboundConsumedEvent {
    pub remote_chain_selector: u64,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["pool_FtfOutboundConsumed"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FtfOutboundConsumedEvent {
    pub remote_chain_selector: u64,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["pool_RateLimitConfigured"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RateLimitConfiguredEvent {
    pub remote_chain_selector: u64,
    pub fast_finality: bool,
    pub outbound_config: RateLimitConfig,
    pub inbound_config: RateLimitConfig,
}
#[soroban_sdk::contractevent(topics = ["pool_HooksUpdated"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AdvancedPoolHooksUpdatedEvent {
    pub old_hooks: Option<soroban_sdk::Address>,
    pub new_hooks: Option<soroban_sdk::Address>,
}
#[soroban_sdk::contractevent(topics = ["pool_InboundRateLimitConsumed"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct InboundRateLimitConsumedEvent {
    pub remote_chain_selector: u64,
    pub amount: i128,
}
#[soroban_sdk::contractevent(
    topics = ["pool_OutboundRateLimitConsumed",
    ],
    export = false
)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OutboundRateLimitConsumedEvent {
    pub remote_chain_selector: u64,
    pub amount: i128,
}
