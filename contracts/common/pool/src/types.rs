use soroban_sdk::{contracttype, Address, Bytes, Vec};

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
    /// Amount actually bridged (post-fee). EVM `IPoolV2.lockOrBurn` returns this
    /// as a second tuple value; on Soroban it is a struct field. The OnRamp writes
    /// this onto the wire (`CcipTokenTransferV1.amount`) instead of the full
    /// input amount, so the destination releases/mints the fee-deducted quantity.
    pub dest_token_amount: i128,
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

/// Direction of a CCIP transfer for pool hooks and CCV resolution (EVM `IPoolV2.MessageDirection`).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum MessageDirection {
    Outbound,
    Inbound,
}

/// Fee result returned by a pool's `get_fee` method (EVM `IPoolV2.getFee` parity).
/// `fee_usd_cents` is the resolved flat USD-cent fee for the requested finality
/// (finality vs fast-finality). `token_fee_bps` is the resolved bps for the
/// requested finality. `is_enabled == false` signals the caller (OnRamp) to fall
/// back to the FeeQuoter's `get_token_transfer_fee` for the flat fee + overheads
/// (EVM `OnRamp._getReceipts` L1047-1053 parity). EVM models bps as `uint16`;
/// Soroban's `Val` has no `u16` conversions, so bps fields are `u32` (values stay
/// `< BPS_DIVIDER = 10_000`, so semantics are identical — a documented widening).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PoolFeeResult {
    pub fee_usd_cents: u32,
    pub dest_gas_overhead: u32,
    pub dest_bytes_overhead: u32,
    pub token_fee_bps: u32,
    pub is_enabled: bool,
}

/// Declarative CCV requirements returned by `get_required_ccvs`, mirroring
/// `common_interfaces::token_pool::PoolRequiredCCVs`. `include_defaults` stands in for EVM's
/// `address(0)` sentinel in `_getCCVsForPool` (Stellar has no zero address).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PoolRequiredCCVs {
    pub ccvs: Vec<Address>,
    pub include_defaults: bool,
}

/// Per-chain token-transfer fee configuration (EVM `IPoolV2.TokenTransferFeeConfig`).
/// Set by the pool owner via `apply_token_fee_config_updates`. The flat
/// USD-cent fee and the bps fee are each split into finality vs fast-finality
/// values; the base `get_fee`/`_get_fee` resolve the active one from the
/// requested finality. `is_enabled == false` makes `get_fee` return zeros with
/// `is_enabled: false`, signalling the OnRamp to use the FeeQuoter fallback.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TokenTransferFeeConfig {
    pub dest_gas_overhead: u32,
    pub dest_bytes_overhead: u32,
    pub finality_fee_usd_cents: u32,
    pub fast_finality_fee_usd_cents: u32,
    pub finality_transfer_fee_bps: u32,
    pub fast_finality_transfer_fee_bps: u32,
    pub is_enabled: bool,
}

impl TokenTransferFeeConfig {
    /// Disabled/absent config: all zeros, `is_enabled: false`. The value the base
    /// returns from `get_token_transfer_fee_config` when no per-chain config is
    /// stored, and from `get_fee` when disabled (EVM `TokenPool.getFee` L1090-1094).
    pub fn disabled() -> Self {
        Self {
            dest_gas_overhead: 0,
            dest_bytes_overhead: 0,
            finality_fee_usd_cents: 0,
            fast_finality_fee_usd_cents: 0,
            finality_transfer_fee_bps: 0,
            fast_finality_transfer_fee_bps: 0,
            is_enabled: false,
        }
    }
}

/// One entry of a `apply_token_fee_config_updates` batch
/// (EVM `TokenPool.TokenTransferFeeConfigArgs`).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TokenTransferFeeConfigArgs {
    pub dest_chain_selector: u64,
    pub config: TokenTransferFeeConfig,
}

/// BPS divider for the source-side transfer fee (EVM `TokenPool.BPS_DIVIDER`).
/// `fee = amount * fee_bps / BPS_DIVIDER`.
pub const BPS_DIVIDER: u32 = 10_000;

/// Per-chain lockbox custody mapping shared by the canonical and siloed
/// lock-release pools (EVM `LockReleaseTokenPool.i_lockBox` /
/// `SiloedLockReleaseTokenPool.s_lockBoxes`). Escrowing in a lockbox keeps user
/// liquidity off the pool address so the pool's own token balance equals only
/// accrued fees, making `withdraw_fee_tokens` (full-balance sweep) safe.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LockBoxEntry {
    pub remote_chain_selector: u64,
    pub lock_box: Address,
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
    /// CCIP ramp registry — same ramp tables as the Router, readable without re-entering the Router.
    RampRegistry,
    /// Optional advanced pool hooks contract (EVM `s_advancedPoolHooks`).
    /// When set, pre-flight and post-flight checks are delegated to this address.
    AdvancedPoolHooks,
    /// Per-chain token-transfer fee config set by the pool owner via
    /// `apply_token_fee_config_updates` (EVM `TokenPool.s_tokenTransferFeeConfig`).
    TokenTransferFeeConfig(u64),
    /// Optional fee-admin address authorized to call `withdraw_fee_tokens`
    /// alongside the owner (EVM `TokenPool.s_feeAdmin`). Absent means only the
    /// owner may withdraw accrued fees.
    FeeAdmin,
    /// CCIP Router address (EVM `s_router`). Stored separately from the ramp registry;
    /// ramp authorization uses [`PoolDataKey::RampRegistry`].
    Router,
    /// RMN proxy address (EVM `i_rmnProxy`). Stored on the pool — NOT resolved via
    /// `Router.get_config()` — so curse checks during `ccip_send` never re-enter the
    /// Router (Soroban forbids ancestor re-entry; the pool is called via
    /// Router → OnRamp → Pool). Owner-set via `set_rmn_proxy` (M-6).
    RmnProxy,
}
