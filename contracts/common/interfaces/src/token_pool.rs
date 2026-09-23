/// TODO: `lock_or_burn`'s `requested_finality` parameter is kept for interface
/// parity with EVM but will always be 0 (WAIT_FOR_FINALITY) when Stellar is the
/// source chain, since Stellar has no reorg risk and no fast confirmation rules.
/// The FTF outbound rate limiting branch in `lock_or_burn` should be simplified
/// to always use the default bucket. For `release_or_mint`, `requested_finality`
/// is meaningful — messages from EVM sources may carry FTF flags, and Stellar as
/// the destination should respect them for inbound rate limiting.
#[soroban_sdk::contractclient(name = "TokenPoolClient")]
pub trait TokenPoolInterface {
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        token: soroban_sdk::Address,
        token_decimals: u32,
        router: soroban_sdk::Address,
        ramp_registry: soroban_sdk::Address,
        rmn_proxy: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;

    fn lock_or_burn(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        input: LockOrBurnIn,
        requested_finality: u32,
        token_args: soroban_sdk::Bytes,
    ) -> Result<LockOrBurnOut, CCIPError>;

    fn release_or_mint(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        input: ReleaseOrMintIn,
        requested_finality: u32,
    ) -> Result<ReleaseOrMintOut, CCIPError>;

    /// Returns the pool fee parameters that will apply to a transfer
    /// (EVM `IPoolV2.getFee`). EVM's `localToken`/`feeToken` args are omitted
    /// because the base body ignores them. `is_enabled == false` signals the
    /// OnRamp to fall back to the FeeQuoter's `get_token_transfer_fee` for the
    /// flat fee + overheads (EVM `OnRamp._getReceipts` L1047-1053 parity).
    fn get_fee(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        amount: i128,
        requested_finality: u32,
        token_args: soroban_sdk::Bytes,
    ) -> Result<PoolFeeResult, CCIPError>;

    /// Returns the token-transfer fee override for a destination chain
    /// (EVM `TokenPool.getTokenTransferFeeConfig`). EVM's `localToken`/
    /// `requestedFinalityConfig`/`tokenArgs` args are omitted because the base
    /// body ignores them (lookup is by destination chain selector only).
    /// Returns a disabled config when none is stored.
    fn get_token_transfer_fee_config(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
    ) -> Result<TokenTransferFeeConfig, CCIPError>;

    /// Applies a batch of token-transfer fee config additions and disables
    /// (EVM `TokenPool.applyTokenTransferFeeConfigUpdates`). Owner-only.
    /// Adds reject `is_enabled == false` (use the disable list), bps >=
    /// `BPS_DIVIDER`, and `dest_gas_overhead == 0`; the chain must be supported.
    /// Disables delete the stored entry.
    fn apply_token_fee_config_updates(
        env: soroban_sdk::Env,
        adds: soroban_sdk::Vec<TokenTransferFeeConfigArgs>,
        disables: soroban_sdk::Vec<u64>,
    ) -> Result<(), CCIPError>;

    /// Withdraws accrued fee-token balances to `recipient`
    /// (EVM `TokenPool.withdrawFeeTokens`). Callable by the owner or the fee
    /// admin. Sweeps the pool's full token balance, which equals accrued fees
    /// because user liquidity is escrowed in a lockbox (lock-release pools) or
    /// burned (burn-mint pools), never held on the pool address.
    fn withdraw_fee_tokens(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        fee_tokens: soroban_sdk::Vec<soroban_sdk::Address>,
        recipient: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    /// Sets the fee-admin address authorized to call `withdraw_fee_tokens`
    /// alongside the owner (EVM parity for the `feeAdmin` field of
    /// `setDynamicConfig`). EVM folds this into `setDynamicConfig` alongside
    /// router/rateLimitAdmin; only `feeAdmin` is needed for withdrawal gating,
    /// so this is a surgical slice rather than full dynamic-config parity.
    /// Owner-only.
    fn set_fee_admin(
        env: soroban_sdk::Env,
        fee_admin: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    fn is_supported_token(
        env: soroban_sdk::Env,
        token: soroban_sdk::Address,
    ) -> Result<bool, CCIPError>;

    fn is_supported_chain(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<bool, CCIPError>;

    fn get_token(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;

    fn get_token_decimals(env: soroban_sdk::Env) -> Result<u32, CCIPError>;

    /// Adds a single remote pool address to a chain's set
    /// (EVM `TokenPool.addRemotePool`). Owner-only.
    fn add_remote_pool(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
        remote_pool_address: soroban_sdk::Bytes,
    ) -> Result<(), CCIPError>;

    /// Removes a single remote pool address from a chain's set
    /// (EVM `TokenPool.removeRemotePool`). Owner-only.
    fn remove_remote_pool(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
        remote_pool_address: soroban_sdk::Bytes,
    ) -> Result<(), CCIPError>;

    /// Returns the full set of remote pool addresses for a chain
    /// (EVM `TokenPool.getRemotePools`). H-14: multiple remote pools per chain.
    fn get_remote_pools(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<soroban_sdk::Vec<soroban_sdk::Bytes>, CCIPError>;

    fn get_remote_token(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
    ) -> Result<soroban_sdk::Bytes, CCIPError>;

    fn apply_chain_updates(
        env: soroban_sdk::Env,
        adds: soroban_sdk::Vec<ChainUpdate>,
        removes: soroban_sdk::Vec<u64>,
    ) -> Result<(), CCIPError>;

    fn set_rate_limit_config(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        remote_chain_selector: u64,
        outbound_config: RateLimitConfig,
        inbound_config: RateLimitConfig,
        fast_finality: bool,
    ) -> Result<(), CCIPError>;

    fn set_rate_limit_admin(
        env: soroban_sdk::Env,
        admin: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    fn get_current_rate_limiter_state(
        env: soroban_sdk::Env,
        remote_chain_selector: u64,
        fast_finality: bool,
    ) -> RateLimiterState;

    fn get_rate_limit_admin(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;

    fn set_allowed_finality_config(
        env: soroban_sdk::Env,
        allowed_finality: u32,
    ) -> Result<(), CCIPError>;

    fn get_allowed_finality_config(env: soroban_sdk::Env) -> u32;

    /// Returns the configured advanced pool hooks contract, if any (EVM `getAdvancedPoolHooks`).
    fn get_advanced_pool_hooks(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;

    /// Sets the advanced pool hooks contract (EVM `setAdvancedPoolHooks`). Owner-only.
    fn set_advanced_pool_hooks(
        env: soroban_sdk::Env,
        hooks: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    /// Removes advanced pool hooks (EVM `setAdvancedPoolHooks` to zero). Owner-only.
    fn remove_advanced_pool_hooks(env: soroban_sdk::Env) -> Result<(), CCIPError>;

    /// Returns required CCV verifier resolver addresses for a transfer (EVM `TokenPool.getRequiredCCVs`).
    ///
    /// Returns [`PoolRequiredCCVs`] so a pool can ask the OnRamp to append its custom CCVs
    /// **alongside** the lane defaults (`include_defaults = true`), matching EVM's
    /// `address(0)` sentinel in `_getCCVsForPool`. Stellar has no zero address, so parity is
    /// expressed as an explicit boolean instead of an in-band placeholder.
    ///
    /// Pools without hooks SHOULD return `{ ccvs: [], include_defaults: true }` to preserve
    /// the existing "no pool requirements, use lane defaults" behavior.
    fn get_required_ccvs(
        env: soroban_sdk::Env,
        local_token: soroban_sdk::Address,
        remote_chain_selector: u64,
        amount: i128,
        requested_finality: u32,
        extra_data: soroban_sdk::Bytes,
        direction: MessageDirection,
    ) -> PoolRequiredCCVs;

    /// Update CCIP Router address (EVM `setDynamicConfig` / `s_router`). Owner-only.
    fn set_router(env: soroban_sdk::Env, router: soroban_sdk::Address) -> Result<(), CCIPError>;

    fn get_router(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;

    /// Update ramp registry used for ramp caller checks. Owner-only.
    fn set_ramp_registry(
        env: soroban_sdk::Env,
        ramp_registry: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;

    fn get_ramp_registry(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;

    /// RMN proxy set once at `initialize` and immutable thereafter (mirrors EVM
    /// `TokenPool`'s `immutable i_rmnProxy` constructor arg — there is NO
    /// `set_rmn_proxy` entrypoint). Consulted by `lock_or_burn` / `release_or_mint`
    /// for remote-chain curse checks.
    fn get_rmn_proxy(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
}

/// Declarative CCV requirements returned by a pool (EVM parity for the `address(0)` sentinel
/// used in `_getCCVsForPool`). `include_defaults = true` means "append lane default CCVs on top
/// of `ccvs`"; `false` means "use only `ccvs` (skipping lane defaults unless user / lane
/// sources add them)".
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct PoolRequiredCCVs {
    pub ccvs: soroban_sdk::Vec<soroban_sdk::Address>,
    pub include_defaults: bool,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct PoolFeeResult {
    /// Flat fee in USD cents for the requested finality (EVM `feeUSDCents`).
    pub fee_usd_cents: u32,
    /// Destination gas overhead charged for accounting (EVM `destGasOverhead`).
    pub dest_gas_overhead: u32,
    /// Destination data-availability bytes overhead (EVM `destBytesOverhead`).
    pub dest_bytes_overhead: u32,
    /// Bps charged in token units for the requested finality (EVM `tokenFeeBps`).
    /// Zero implies no in-token fee. EVM models this as `uint16`; Soroban's `Val`
    /// has no `u16` conversions, so it is `u32` (values stay `< BPS_DIVIDER`).
    pub token_fee_bps: u32,
    /// Whether the pool's fee config is enabled. If false, the OnRamp should use
    /// FeeQuoter defaults (EVM `isEnabled`).
    pub is_enabled: bool,
}

/// Per-chain token-transfer fee configuration (EVM `IPoolV2.TokenTransferFeeConfig`).
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TokenTransferFeeConfig {
    pub dest_gas_overhead: u32,
    pub dest_bytes_overhead: u32,
    pub finality_fee_usd_cents: u32,
    pub fast_finality_fee_usd_cents: u32,
    pub finality_transfer_fee_bps: u32,
    pub fast_finality_transfer_fee_bps: u32,
    pub is_enabled: bool,
}

/// One entry of an `apply_token_fee_config_updates` batch
/// (EVM `TokenPool.TokenTransferFeeConfigArgs`).
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TokenTransferFeeConfigArgs {
    pub dest_chain_selector: u64,
    pub config: TokenTransferFeeConfig,
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
pub struct LockOrBurnOut {
    pub dest_token_address: soroban_sdk::Bytes,
    /// Amount actually bridged (post-fee), written onto the wire as
    /// `CcipTokenTransferV1.amount` (EVM `lockOrBurn`'s second return value
    /// `destTokenAmount`).
    pub dest_token_amount: i128,
    pub dest_pool_data: soroban_sdk::Bytes,
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
pub struct ReleaseOrMintOut {
    pub destination_amount: i128,
}

/// Direction of a CCIP transfer (EVM `IPoolV2.MessageDirection`).
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum MessageDirection {
    Outbound,
    Inbound,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RateLimitConfig {
    pub is_enabled: bool,
    pub capacity: u128,
    pub rate: u128,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TokenBucket {
    pub tokens: u128,
    pub last_updated: u64,
    pub is_enabled: bool,
    pub capacity: u128,
    pub rate: u128,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RateLimiterState {
    pub outbound: TokenBucket,
    pub inbound: TokenBucket,
}

#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ChainUpdate {
    pub remote_chain_selector: u64,
    /// Initial set of remote pool addresses for the chain (EVM
    /// `bytes[] remotePoolAddresses`, `TokenPool.sol:95`). H-14: multiple
    /// remote pools per chain; each element is seeded into the per-chain set
    /// by `apply_chain_updates`, with further pools added/removed individually
    /// via `add_remote_pool`/`remove_remote_pool`.
    pub remote_pool_addresses: soroban_sdk::Vec<soroban_sdk::Bytes>,
    pub remote_token_address: soroban_sdk::Bytes,
    pub outbound_rate_limiter_config: RateLimitConfig,
    pub inbound_rate_limiter_config: RateLimitConfig,
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
    RouterNotConfigured = 318,
    InvalidSourcePoolAddress = 319,
    /// A token-transfer fee config add is invalid: `is_enabled == false` or
    /// `dest_gas_overhead == 0` (EVM `TokenPool.InvalidTokenTransferFeeConfig`).
    InvalidTokenTransferFeeConfig = 321,
    /// A token-transfer fee config bps is >= `BPS_DIVIDER` (EVM `TokenPool.InvalidTransferFeeBps`).
    InvalidTransferFeeBps = 322,
    InvalidFeeCalculation = 801,
    InvalidFeeTokenConversion = 802,
}
