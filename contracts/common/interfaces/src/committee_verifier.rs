#[soroban_sdk::contractargs(name = "CommitteeVerifierArgs")]
#[soroban_sdk::contractclient(name = "CommitteeVerifierClient")]
pub trait CommitteeVerifierInterface {
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_fee(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        message: soroban_sdk::Bytes,
        extra_args: soroban_sdk::Bytes,
        requested_finality: u32,
        fee_token: soroban_sdk::Address,
    ) -> Result<FeeResponse, CCIPError>;
    fn upgrade(
        env: soroban_sdk::Env,
        new_wasm_hash: soroban_sdk::BytesN<32>,
    ) -> Result<(), CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        dynamic_config: DynamicConfig,
        storage_locations: soroban_sdk::Vec<soroban_sdk::Bytes>,
        rmn_proxy: soroban_sdk::Address,
        version_tag: soroban_sdk::BytesN<4>,
    ) -> Result<(), CCIPError>;
    fn version_tag(env: soroban_sdk::Env) -> soroban_sdk::BytesN<4>;
    fn verify_message(
        env: soroban_sdk::Env,
        source_chain_selector: u64,
        message_hash: soroban_sdk::BytesN<32>,
        verifier_results: soroban_sdk::Bytes,
    ) -> Result<(), CCIPError>;
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn get_dynamic_config(env: soroban_sdk::Env) -> Result<DynamicConfig, CCIPError>;
    fn set_dynamic_config(
        env: soroban_sdk::Env,
        dynamic_config: DynamicConfig,
    ) -> Result<(), CCIPError>;
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn forward_to_verifier(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        sender: soroban_sdk::Address,
        message_id: soroban_sdk::BytesN<32>,
        fee_token: soroban_sdk::Address,
        fee_token_amount: i128,
        verifier_args: soroban_sdk::Bytes,
    ) -> Result<soroban_sdk::Bytes, CCIPError>;
    fn withdraw_fee_tokens(
        env: soroban_sdk::Env,
        fee_tokens: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<(), CCIPError>;
    fn get_signature_config(
        env: soroban_sdk::Env,
        source_chain_selector: u64,
    ) -> Result<SignatureQuorumConfig, CCIPError>;
    fn get_storage_locations(
        env: soroban_sdk::Env,
    ) -> Result<soroban_sdk::Vec<soroban_sdk::Bytes>, CCIPError>;
    fn apply_allowlist_updates(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        updates: soroban_sdk::Vec<AllowListUpdate>,
    ) -> Result<(), CCIPError>;
    fn apply_signature_configs(
        env: soroban_sdk::Env,
        source_chains_to_remove: soroban_sdk::Vec<u64>,
        signature_configs: soroban_sdk::Vec<SignatureQuorumConfig>,
    ) -> Result<(), CCIPError>;
    fn get_remote_chain_config(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<RemoteChainConfig, CCIPError>;
    fn update_storage_locations(
        env: soroban_sdk::Env,
        new_locations: soroban_sdk::Vec<soroban_sdk::Bytes>,
    ) -> Result<(), CCIPError>;
    fn get_allowed_finality_config(env: soroban_sdk::Env) -> Result<u32, CCIPError>;
    fn get_storage_locations_admin(
        env: soroban_sdk::Env,
    ) -> Result<soroban_sdk::Address, CCIPError>;
    fn set_allowed_finality_config(
        env: soroban_sdk::Env,
        allowed_finality: u32,
    ) -> Result<(), CCIPError>;
    fn get_pending_storage_loc_admin(
        env: soroban_sdk::Env,
    ) -> Result<Option<soroban_sdk::Address>, CCIPError>;
    fn accept_storage_locations_admin(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn apply_remote_chain_cfg_updates(
        env: soroban_sdk::Env,
        remote_chain_config_args: soroban_sdk::Vec<RemoteChainConfig>,
    ) -> Result<(), CCIPError>;
    fn transfer_storage_locations_admin(
        env: soroban_sdk::Env,
        to: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FeeResponse {
    pub dest_bytes_overhead: u32,
    pub dest_gas_limit: u32,
    pub fee: u32,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct DynamicConfig {
    pub allowlist_admin: Option<soroban_sdk::Address>,
    pub fee_aggregator: Option<soroban_sdk::Address>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RemoteChainConfig {
    pub allowlist_enabled: bool,
    pub fee_usd_cents: u32,
    pub gas_for_verification: u32,
    pub payload_size_bytes: u32,
    pub remote_chain_selector: u64,
    pub router: Option<soroban_sdk::Address>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SignatureQuorumConfig {
    pub signers: soroban_sdk::Vec<soroban_sdk::BytesN<32>>,
    pub source_chain_selector: u64,
    pub threshold: u32,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AllowListUpdate {
    pub added_allowlisted_senders: soroban_sdk::Vec<soroban_sdk::Address>,
    pub allowlist_enabled: bool,
    pub dest_chain_selector: u64,
    pub removed_allowlisted_senders: soroban_sdk::Vec<soroban_sdk::Address>,
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
    NoStateProgressMade = 119,
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
    InvalidAllowListRequest = 323,
    InvalidFeeCalculation = 801,
    InvalidFeeTokenConversion = 802,
    ZeroFeeAggregatorNotAllowed = 803,
    ExceedsMaxCCVs = 804,
    CCVNotAllowed = 805,
    PolicyEngineDetachReverted = 806,
    UnsupportedSelector = 807,
    ZeroAddressNotAllowed = 808,
}
#[soroban_sdk::contractevent(topics = ["ccv_ConfigSet", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ConfigSetEvent {
    pub dynamic_config: DynamicConfig,
}
#[soroban_sdk::contractevent(topics = ["ccv_FinalityConfigSet", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FinalityConfigSetEvent {
    pub allowed_finality: u32,
}
#[soroban_sdk::contractevent(topics = ["ccv_SignatureConfigSet", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SignatureConfigSetEvent {
    pub source_chain_selector: u64,
    pub signers: soroban_sdk::Vec<soroban_sdk::BytesN<32>>,
    pub threshold: u32,
}
#[soroban_sdk::contractevent(topics = ["ccv_RemoteChainConfigSet", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RemoteChainConfigSetEvent {
    pub remote_chain_selector: u64,
    pub router: Option<soroban_sdk::Address>,
    pub allowlist_enabled: bool,
}
#[soroban_sdk::contractevent(topics = ["ccv_AllowListSendersAdded", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AllowListSendersAddedEvent {
    pub dest_chain_selector: u64,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["ccv_AllowListStateChanged", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AllowListStateChangedEvent {
    pub dest_chain_selector: u64,
    pub allowlist_enabled: bool,
}
#[soroban_sdk::contractevent(topics = ["ccv_AllowListSendersRemoved", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AllowListSendersRemovedEvent {
    pub dest_chain_selector: u64,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["ccv_StorageAdminTransferred", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StorageAdminTransferredEvent {
    pub from: soroban_sdk::Address,
    pub to: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["ccv_StorageAdminTransferReq", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StorageAdminTransferReqEvent {
    pub from: soroban_sdk::Address,
    pub to: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["ccv_StorageLocationsUpdated", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StorageLocationsUpdatedEvent {
    pub old_locations: soroban_sdk::Vec<soroban_sdk::Bytes>,
    pub new_locations: soroban_sdk::Vec<soroban_sdk::Bytes>,
}
#[soroban_sdk::contractevent(topics = ["auth_RoleGranted", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleGrantedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["auth_RoleRevoked", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleRevokedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["auth_CallerAdded", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AuthorizedCallerAddedEvent {
    pub caller: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["auth_CallerRemoved", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AuthorizedCallerRemovedEvent {
    pub caller: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["auth_OwnerTransferStart", "map"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipTransferStartedEvent {
    pub previous_owner: soroban_sdk::Address,
    pub new_owner: soroban_sdk::Address,
}
