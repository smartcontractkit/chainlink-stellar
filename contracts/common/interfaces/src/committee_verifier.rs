#[soroban_sdk::contractargs(name = "CommitteeVerifierArgs")]
#[soroban_sdk::contractclient(name = "CommitteeVerifierClient")]
pub trait CommitteeVerifierInterface {
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_fee(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        message: soroban_sdk::Bytes,
        extra_args: soroban_sdk::Bytes,
        block_confirmations: u32,
    ) -> Result<FeeResponse, CCIPError>;
    fn is_owner(env: soroban_sdk::Env, addr: soroban_sdk::Address) -> bool;
    fn init_owner(env: soroban_sdk::Env, owner: soroban_sdk::Address) -> Result<(), CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        dynamic_config: DynamicConfig,
        storage_locations: soroban_sdk::Vec<soroban_sdk::Bytes>,
        rmn_proxy: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn version_tag(env: soroban_sdk::Env) -> soroban_sdk::BytesN<4>;
    fn require_owner(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn set_new_owner(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn init_allowlist(
        env: soroban_sdk::Env,
        initial_allowlist: soroban_sdk::Map<u64, soroban_sdk::Vec<soroban_sdk::Address>>,
    );
    fn verify_message(
        env: soroban_sdk::Env,
        source_chain_selector: u64,
        message_hash: soroban_sdk::BytesN<32>,
        verifier_results: soroban_sdk::Bytes,
    ) -> Result<(), CCIPError>;
    fn is_in_allowlist(env: soroban_sdk::Env, key: u64, addr: soroban_sdk::Address) -> bool;
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
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
    fn forward_to_verifier(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        sender: soroban_sdk::Address,
        message_id: soroban_sdk::BytesN<32>,
        fee_token: soroban_sdk::Address,
        fee_token_amount: i128,
        verifier_args: soroban_sdk::Bytes,
    ) -> Result<soroban_sdk::Bytes, CCIPError>;
    fn get_allowlist_entry(env: soroban_sdk::Env, key: u64) -> Option<AllowListEntry>;
    fn withdraw_fee_tokens(
        env: soroban_sdk::Env,
        fee_tokens: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<(), CCIPError>;
    fn is_allowlist_enabled(env: soroban_sdk::Env, key: u64) -> bool;
    fn require_in_allowlist(
        env: soroban_sdk::Env,
        key: u64,
        address: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn get_storage_locations(
        env: soroban_sdk::Env,
    ) -> Result<soroban_sdk::Vec<soroban_sdk::Bytes>, CCIPError>;
    fn apply_allowlist_updates(
        env: soroban_sdk::Env,
        updates: soroban_sdk::Vec<AllowListUpdate>,
    ) -> Result<(), CCIPError>;
    fn get_remote_chain_config(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<RemoteChainConfig, CCIPError>;
    fn update_storage_locations(
        env: soroban_sdk::Env,
        new_locations: soroban_sdk::Vec<soroban_sdk::Bytes>,
    ) -> Result<(), CCIPError>;
    fn cancel_ownership_transfer(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn get_storage_locations_admin(
        env: soroban_sdk::Env,
    ) -> Result<soroban_sdk::Address, CCIPError>;
    fn emit_allowlist_updated_event(
        env: soroban_sdk::Env,
        key: u64,
        added_addresses: soroban_sdk::Vec<soroban_sdk::Address>,
        removed_addresses: soroban_sdk::Vec<soroban_sdk::Address>,
    );
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
    pub placeholder: u64,
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
pub struct SignatureQuorumConfig {
    pub signers: soroban_sdk::Vec<soroban_sdk::BytesN<32>>,
    pub source_chain_selector: u64,
    pub threshold: u32,
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
    SourceNotConfigured = 19,
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
    InvalidFeeCalculation = 801,
    InvalidFeeTokenConversion = 802,
}
#[soroban_sdk::contractevent(topics = ["ccv_ConfigSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ConfigSetEvent {
    pub dynamic_config: DynamicConfig,
}
#[soroban_sdk::contractevent(topics = ["ccv_RemoteChainConfigSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RemoteChainConfigSetEvent {
    pub remote_chain_selector: u64,
    pub router: Option<soroban_sdk::Address>,
    pub allowlist_enabled: bool,
}
#[soroban_sdk::contractevent(topics = ["ccv_AllowListSendersAdded"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AllowListSendersAddedEvent {
    pub dest_chain_selector: u64,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["ccv_AllowListStateChanged"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AllowListStateChangedEvent {
    pub dest_chain_selector: u64,
    pub allowlist_enabled: bool,
}
#[soroban_sdk::contractevent(topics = ["ccv_AllowListSendersRemoved"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AllowListSendersRemovedEvent {
    pub dest_chain_selector: u64,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["ccv_StorageAdminTransferred"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StorageAdminTransferredEvent {
    pub from: soroban_sdk::Address,
    pub to: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["ccv_StorageAdminTransferReq"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StorageAdminTransferReqEvent {
    pub from: soroban_sdk::Address,
    pub to: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["ccv_StorageLocationsUpdated"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StorageLocationsUpdatedEvent {
    pub old_locations: soroban_sdk::Vec<soroban_sdk::Bytes>,
    pub new_locations: soroban_sdk::Vec<soroban_sdk::Bytes>,
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
