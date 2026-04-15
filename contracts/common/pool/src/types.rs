use soroban_sdk::{contracttype, Address, Bytes};

// ============================================================
// Rate Limit Types (EVM RateLimiter.sol parity)
// ============================================================

/// Static configuration for a rate limit bucket (EVM `RateLimiter.Config`).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RateLimitConfig {
    pub is_enabled: bool,
    /// Maximum number of tokens that can be in the bucket.
    pub capacity: u128,
    /// Tokens per second the bucket is refilled.
    pub rate: u128,
}

impl RateLimitConfig {
    pub fn disabled() -> Self {
        Self {
            is_enabled: false,
            capacity: 0,
            rate: 0,
        }
    }
}

/// Live token bucket state (EVM `RateLimiter.TokenBucket`).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TokenBucket {
    pub tokens: u128,
    /// Ledger timestamp (seconds) of the last refill.
    pub last_updated: u64,
    pub is_enabled: bool,
    pub capacity: u128,
    pub rate: u128,
}

impl TokenBucket {
    pub fn disabled() -> Self {
        Self {
            tokens: 0,
            last_updated: 0,
            is_enabled: false,
            capacity: 0,
            rate: 0,
        }
    }
}

/// Paired outbound + inbound bucket state returned by view functions.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RateLimiterState {
    pub outbound: TokenBucket,
    pub inbound: TokenBucket,
}

// ============================================================
// Pool Operation Types
// ============================================================

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LockOrBurnIn {
    pub receiver: Bytes,
    pub remote_chain_selector: u64,
    pub original_sender: Address,
    pub amount: i128,
    pub local_token: Address,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LockOrBurnOut {
    pub dest_token_address: Bytes,
    pub dest_pool_data: Bytes,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ReleaseOrMintIn {
    pub original_sender: Bytes,
    pub remote_chain_selector: u64,
    pub receiver: Address,
    /// Amount in **source** token minimal units (EVM `sourceDenominatedAmount`).
    pub amount: i128,
    pub local_token: Address,
    pub source_pool_address: Bytes,
    pub source_pool_data: Bytes,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ReleaseOrMintOut {
    pub destination_amount: i128,
}

/// Fee result returned by a pool's `get_fee` method.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PoolFeeResult {
    pub fee_usd_cents: u32,
}

/// Per-chain fee configuration set by the pool owner.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PoolFeeConfig {
    /// Whether the pool charges a fee for transfers to this chain.
    pub is_enabled: bool,
    /// Fee in USD cents.
    pub fee_usd_cents: u32,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RemoteChainConfig {
    pub remote_pool_address: Bytes,
    pub remote_token_address: Bytes,
}

/// Parameters for adding a remote chain, including initial rate limit configs
/// (EVM `TokenPool.ChainUpdate`).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ChainUpdate {
    pub remote_chain_selector: u64,
    pub remote_pool_addresses: Bytes,
    pub remote_token_address: Bytes,
    pub outbound_rate_limiter_config: RateLimitConfig,
    pub inbound_rate_limiter_config: RateLimitConfig,
}

// ============================================================
// Storage Keys
// ============================================================

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum PoolDataKey {
    Token,
    RemoteChainConfig(u64),
    SupportedChains,
    /// Local token decimals (`uint8` on EVM), stored at init.
    TokenDecimals,
    /// Outbound token bucket for a remote chain.
    OutboundRateLimit(u64),
    /// Inbound token bucket for a remote chain.
    InboundRateLimit(u64),
    /// Optional rate limit admin address (EVM `s_rateLimitAdmin`).
    RateLimitAdmin,
    /// Fast-finality outbound token bucket for a remote chain (EVM `s_fastFinalityOutboundRateLimiterConfig`).
    /// TODO: Likely removable for Stellar — outbound FTF is not meaningful since
    /// Stellar has no reorg risk and senders will never request fast finality.
    FtfOutboundRateLimit(u64),
    /// Fast-finality inbound token bucket for a remote chain (EVM `s_fastFinalityInboundRateLimiterConfig`).
    FtfInboundRateLimit(u64),
    /// Allowed finality configuration (EVM `s_allowedFinalityConfig`). Stored as `u32` matching `bytes4`.
    AllowedFinalityConfig,
    /// Router address (EVM `s_router`). Used at runtime to query registered
    /// OnRamp/OffRamp addresses for ramp-gating (Soroban analogue of
    /// EVM `_onlyOnRamp` / `_onlyOffRamp` which call `IRouter(router)`).
    Router,
    /// Optional advanced pool hooks contract (EVM `s_advancedPoolHooks`).
    /// When set, pre-flight and post-flight checks are delegated to this address.
    AdvancedPoolHooks,
    /// Per-chain fee config set by pool owner. Allows pools to charge additional
    /// fees on top of the protocol fee.
    PoolFeeConfig(u64),
}
