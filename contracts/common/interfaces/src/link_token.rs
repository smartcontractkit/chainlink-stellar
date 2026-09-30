#[soroban_sdk::contractargs(name = "LinkTokenArgs")]
#[soroban_sdk::contractclient(name = "LinkTokenClient")]
pub trait LinkTokenInterface {
    fn burn(env: soroban_sdk::Env, from: soroban_sdk::Address, amount: i128);
    fn mint(env: soroban_sdk::Env, to: soroban_sdk::Address, amount: i128);
    fn name(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn admin(env: soroban_sdk::Env) -> soroban_sdk::Address;
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn trust(env: soroban_sdk::Env, addr: soroban_sdk::Address);
    fn symbol(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn approve(
        env: soroban_sdk::Env,
        from: soroban_sdk::Address,
        spender: soroban_sdk::Address,
        amount: i128,
        expiration_ledger: u32,
    );
    fn balance(env: soroban_sdk::Env, id: soroban_sdk::Address) -> i128;
    fn mint_as(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        to: soroban_sdk::Address,
        amount: i128,
    );
    fn clawback(env: soroban_sdk::Env, from: soroban_sdk::Address, amount: i128);
    fn decimals(env: soroban_sdk::Env) -> u32;
    fn is_owner(env: soroban_sdk::Env, addr: soroban_sdk::Address) -> bool;
    fn transfer(
        env: soroban_sdk::Env,
        from: soroban_sdk::Address,
        to: soroban_sdk::MuxedAddress,
        amount: i128,
    );
    fn allowance(
        env: soroban_sdk::Env,
        from: soroban_sdk::Address,
        spender: soroban_sdk::Address,
    ) -> i128;
    fn burn_from(
        env: soroban_sdk::Env,
        spender: soroban_sdk::Address,
        from: soroban_sdk::Address,
        amount: i128,
    );
    fn set_admin(env: soroban_sdk::Env, new_admin: soroban_sdk::Address);
    fn add_minter(env: soroban_sdk::Env, minter: soroban_sdk::Address) -> Result<(), LinkError>;
    fn authorized(env: soroban_sdk::Env, id: soroban_sdk::Address) -> bool;
    fn init_owner(env: soroban_sdk::Env, owner: soroban_sdk::Address) -> Result<(), CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        admin: soroban_sdk::Address,
        name: soroban_sdk::String,
        symbol: soroban_sdk::String,
        decimals: u32,
    ) -> Result<(), LinkError>;
    fn get_minters(env: soroban_sdk::Env) -> soroban_sdk::Vec<soroban_sdk::Address>;
    fn remove_minter(env: soroban_sdk::Env, minter: soroban_sdk::Address) -> Result<(), LinkError>;
    fn require_owner(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn transfer_from(
        env: soroban_sdk::Env,
        spender: soroban_sdk::Address,
        from: soroban_sdk::Address,
        to: soroban_sdk::Address,
        amount: i128,
    );
    fn set_authorized(env: soroban_sdk::Env, id: soroban_sdk::Address, authorize: bool);
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn get_pending_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn cancel_ownership_transfer(env: soroban_sdk::Env) -> Result<(), CCIPError>;
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
    InvalidFeeCalculation = 801,
    InvalidFeeTokenConversion = 802,
    ZeroFeeAggregatorNotAllowed = 803,
    ExceedsMaxCCVs = 804,
    CCVNotAllowed = 805,
    PolicyEngineDetachReverted = 806,
    UnsupportedSelector = 807,
    ZeroAddressNotAllowed = 808,
}
#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum LinkError {
    AlreadyInitialized = 1,
    NotAdmin = 2,
    InsufficientBalance = 3,
    InsufficientAllowance = 4,
    NegativeAmount = 5,
    NotInitialized = 6,
    UnsupportedOperation = 7,
    NotMinter = 8,
    MinterAlreadyExists = 9,
    MinterNotFound = 10,
    CannotRemovePrimaryMinter = 11,
}
#[soroban_sdk::contractevent(topics = ["Upgraded"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Upgraded {
    pub new_wasm_hash: soroban_sdk::BytesN<32>,
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
#[soroban_sdk::contractevent(topics = ["burn"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct BurnEvent {
    pub from: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["mint"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct MintEvent {
    pub to: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["approve"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ApproveEvent {
    pub from: soroban_sdk::Address,
    pub spender: soroban_sdk::Address,
    pub amount: i128,
    pub expiration_ledger: u32,
}
#[soroban_sdk::contractevent(topics = ["set_admin"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SetAdminEvent {
    pub admin: soroban_sdk::Address,
    pub new_admin: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["transfer"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TransferEvent {
    pub from: soroban_sdk::Address,
    pub to: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["set_authorized"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SetAuthorizedEvent {
    pub id: soroban_sdk::Address,
    pub authorize: bool,
}
