use common_message::{StellarToAnyMessage, TokenAmount};

#[soroban_sdk::contractargs(name = "OnRampArgs")]
#[soroban_sdk::contractclient(name = "OnRampClient")]
pub trait OnRampInterface {
    fn init(env: soroban_sdk::Env, rmn_proxy: soroban_sdk::Address) -> Result<(), CCIPError>;
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_fee(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        message: StellarToAnyMessage,
    ) -> Result<i128, CCIPError>;
    fn is_owner(env: soroban_sdk::Env, addr: soroban_sdk::Address) -> bool;
    fn is_cursed(env: soroban_sdk::Env) -> Result<bool, CCIPError>;
    fn init_owner(env: soroban_sdk::Env, owner: soroban_sdk::Address) -> Result<(), CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        static_config: StaticConfig,
        dynamic_config: DynamicConfig,
    ) -> Result<(), CCIPError>;
    fn require_owner(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn set_new_owner(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn get_pending_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_static_config(env: soroban_sdk::Env) -> Result<StaticConfig, CCIPError>;
    fn get_dynamic_config(env: soroban_sdk::Env) -> Result<DynamicConfig, CCIPError>;
    fn require_not_cursed(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn set_dynamic_config(
        env: soroban_sdk::Env,
        dynamic_config: DynamicConfig,
    ) -> Result<(), CCIPError>;
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn forward_from_router(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        message: StellarToAnyMessage,
        fee_token_amount: i128,
        original_sender: soroban_sdk::Address,
    ) -> Result<soroban_sdk::BytesN<32>, CCIPError>;
    fn withdraw_fee_tokens(
        env: soroban_sdk::Env,
        fee_tokens: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<(), CCIPError>;
    fn get_dest_chain_config(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
    ) -> Result<DestChainConfig, CCIPError>;
    fn get_pool_by_source_token(
        env: soroban_sdk::Env,
        source_token: soroban_sdk::Address,
    ) -> Result<soroban_sdk::Address, CCIPError>;
    fn cancel_ownership_transfer(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn get_all_dest_chain_configs(
        env: soroban_sdk::Env,
    ) -> Result<(soroban_sdk::Vec<u64>, soroban_sdk::Vec<DestChainConfig>), CCIPError>;
    fn apply_dest_chain_config_updates(
        env: soroban_sdk::Env,
        dest_chain_config_args: soroban_sdk::Vec<DestChainConfigArgs>,
    ) -> Result<(), CCIPError>;
    fn get_expected_next_message_number(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
    ) -> Result<u64, CCIPError>;
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
pub struct Receipt {
    pub dest_bytes_overhead: u32,
    pub dest_gas_limit: u32,
    pub extra_args: soroban_sdk::Bytes,
    pub fee_token_amount: i128,
    pub issuer: soroban_sdk::Address,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StaticConfig {
    pub chain_selector: u64,
    pub max_usd_cents_per_message: u32,
    pub rmn_proxy: soroban_sdk::Address,
    pub token_admin_registry: soroban_sdk::Address,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct DynamicConfig {
    pub fee_aggregator: soroban_sdk::Address,
    pub fee_quoter: soroban_sdk::Address,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct DestChainConfig {
    pub address_bytes_length: u32,
    pub base_execution_gas_cost: u32,
    pub default_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub default_executor: soroban_sdk::Address,
    pub lane_mandated_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub message_network_fee_usd_cents: u32,
    pub message_number: u64,
    pub off_ramp: soroban_sdk::Bytes,
    pub router: soroban_sdk::Address,
    pub token_network_fee_usd_cents: u32,
    pub token_receiver_allowed: bool,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct DestChainConfigArgs {
    pub address_bytes_length: u32,
    pub base_execution_gas_cost: u32,
    pub default_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub default_executor: soroban_sdk::Address,
    pub dest_chain_selector: u64,
    pub lane_mandated_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub message_network_fee_usd_cents: u32,
    pub off_ramp: soroban_sdk::Bytes,
    pub router: soroban_sdk::Address,
    pub token_network_fee_usd_cents: u32,
    pub token_receiver_allowed: bool,
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
#[soroban_sdk::contractevent(topics = ["onramp_1_7_ConfigSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ConfigSetEvent {
    pub static_config: StaticConfig,
    pub dynamic_config: DynamicConfig,
}
#[soroban_sdk::contractevent(topics = ["onramp_1_7_CCIPMessageSent"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CCIPMessageSentEvent {
    pub dest_chain_selector: u64,
    pub sequence_number: u64,
    pub sender: soroban_sdk::Address,
    pub message_id: soroban_sdk::BytesN<32>,
    pub fee_token: soroban_sdk::Address,
    pub token_amount_before_fees: i128,
    pub encoded_message: soroban_sdk::Bytes,
    pub receipts: soroban_sdk::Vec<Receipt>,
    pub verifier_blobs: soroban_sdk::Vec<soroban_sdk::Bytes>,
}
#[soroban_sdk::contractevent(topics = ["onramp_1_7_DestChainConfigSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct DestChainConfigSetEvent {
    pub dest_chain_selector: u64,
    pub message_number: u64,
    pub config: DestChainConfig,
}
#[soroban_sdk::contractevent(
    topics = ["onramp_1_7_OwnershipTransferred",
    ],
    export = false
)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipTransferredEvent {
    pub new_owner: soroban_sdk::Address,
}
