#[soroban_sdk::contractargs(name = "McmsArgs")]
#[soroban_sdk::contractclient(name = "McmsClient")]
pub trait McmsInterface {
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn execute(
        env: soroban_sdk::Env,
        op: StellarOp,
        proof: MerkleProof,
    ) -> Result<(), McmsError>;
    fn get_root(
        env: soroban_sdk::Env,
    ) -> Result<(soroban_sdk::BytesN<32>, u32), McmsError>;
    fn is_owner(env: soroban_sdk::Env, addr: soroban_sdk::Address) -> bool;
    fn set_root(
        env: soroban_sdk::Env,
        root: soroban_sdk::BytesN<32>,
        valid_until: u32,
        metadata: StellarRootMetadata,
        metadata_proof: MerkleProof,
        signatures: SignatureVec,
    ) -> Result<(), McmsError>;
    fn get_config(env: soroban_sdk::Env) -> Result<Config, McmsError>;
    fn init_owner(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        chain_network_id: soroban_sdk::BytesN<32>,
    ) -> Result<(), McmsError>;
    fn set_config(
        env: soroban_sdk::Env,
        signer_addresses: SignerAddresses,
        signer_groups: SignerGroups,
        group_quorums: soroban_sdk::BytesN<32>,
        group_parents: soroban_sdk::BytesN<32>,
        clear_root: bool,
    ) -> Result<(), McmsError>;
    fn get_op_count(env: soroban_sdk::Env) -> Result<u64, McmsError>;
    fn require_owner(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn set_new_owner(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn extend_all_ttls(env: soroban_sdk::Env) -> Result<(), McmsError>;
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn chain_network_id(
        env: soroban_sdk::Env,
    ) -> Result<soroban_sdk::BytesN<32>, McmsError>;
    fn get_pending_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_root_metadata(
        env: soroban_sdk::Env,
    ) -> Result<StellarRootMetadata, McmsError>;
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn get_min_secs_per_ledger(env: soroban_sdk::Env) -> Result<u64, McmsError>;
    fn set_min_secs_per_ledger(
        env: soroban_sdk::Env,
        secs: u64,
    ) -> Result<(), McmsError>;
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
pub struct Config {
    pub group_parents: soroban_sdk::BytesN<32>,
    pub group_quorums: soroban_sdk::BytesN<32>,
    pub signers: soroban_sdk::Vec<Signer>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Signer {
    pub addr: soroban_sdk::BytesN<32>,
    pub group: u32,
    pub index: u32,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Signature {
    pub r: soroban_sdk::BytesN<32>,
    pub s: soroban_sdk::BytesN<32>,
    pub v: u32,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StellarOp {
    pub chain_id: soroban_sdk::BytesN<32>,
    pub data: soroban_sdk::Bytes,
    pub multisig: soroban_sdk::BytesN<32>,
    pub nonce: u64,
    pub to: soroban_sdk::BytesN<32>,
    pub value: soroban_sdk::BytesN<32>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct MerkleProof {
    pub inner: soroban_sdk::Vec<soroban_sdk::BytesN<32>>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SignatureVec {
    pub inner: soroban_sdk::Vec<Signature>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SignerGroups {
    pub inner: soroban_sdk::Vec<u32>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SignerAddresses {
    pub inner: soroban_sdk::Vec<soroban_sdk::BytesN<32>>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StellarRootMetadata {
    pub chain_id: soroban_sdk::BytesN<32>,
    pub multisig: soroban_sdk::BytesN<32>,
    pub override_previous_root: bool,
    pub post_op_count: u64,
    pub pre_op_count: u64,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ExpiringRootAndOpCount {
    pub op_count: u64,
    pub root: soroban_sdk::BytesN<32>,
    pub valid_until: u32,
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
pub enum McmsError {
    NotInitialized = 1,
    AlreadyInitialized = 2,
    NotOwner = 3,
    OutOfBoundsNumOfSigners = 10,
    SignerGroupsLengthMismatch = 11,
    OutOfBoundsGroup = 12,
    GroupTreeNotWellFormed = 13,
    OutOfBoundsGroupQuorum = 14,
    SignerInDisabledGroup = 15,
    SignersAddressesMustBeStrictlyIncreasing = 16,
    SignedHashAlreadySeen = 20,
    SignersAddressesMustBeStrictlyIncreasingSigs = 21,
    InvalidSigner = 22,
    MissingConfig = 23,
    InsufficientSigners = 24,
    ValidUntilHasAlreadyPassed = 25,
    ProofCannotBeVerified = 26,
    WrongChainIdMeta = 27,
    WrongMultiSigMeta = 28,
    PendingOps = 29,
    WrongPreOpCount = 30,
    WrongPostOpCount = 31,
    PostOpCountReached = 40,
    WrongChainIdOp = 41,
    WrongMultiSigOp = 42,
    RootExpired = 43,
    WrongNonce = 44,
    CallReverted = 45,
    InvalidSignature = 46,
    InvalidSignatureEncoding = 47,
    NonceOverflow = 48,
    InvalidUint40 = 49,
    InvalidInvokeData = 50,
    NonZeroValue = 51,
    MissingRootMetadata = 52,
    ValidUntilExceedsMaximum = 53,
    InvalidMinSecsPerLedger = 54,
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
#[soroban_sdk::contractevent(topics = ["mcms_NewRoot"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct NewRootEvent {
    pub root: soroban_sdk::BytesN<32>,
    pub valid_until: u32,
    pub metadata: StellarRootMetadata,
}
#[soroban_sdk::contractevent(topics = ["mcms_ConfigSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ConfigSetEvent {
    pub config: Config,
    pub is_root_cleared: bool,
}
#[soroban_sdk::contractevent(topics = ["mcms_OpExecuted"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OpExecutedEvent {
    pub nonce: u64,
    pub to: soroban_sdk::BytesN<32>,
    pub data: soroban_sdk::Bytes,
    pub value: soroban_sdk::BytesN<32>,
}
#[soroban_sdk::contractevent(topics = ["mcms_MinSecsPerLedgerSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct MinSecsPerLedgerSetEvent {
    pub min_secs_per_ledger: u64,
}
