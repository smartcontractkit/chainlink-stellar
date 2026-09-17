use soroban_sdk::{contractevent, Address, Bytes, Vec};

use crate::types::RateLimitConfig;

#[contractevent(topics = ["pool_Locked"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LockedEvent {
    pub sender: Address,
    pub amount: i128,
}

#[contractevent(topics = ["pool_Released"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ReleasedEvent {
    pub sender: Address,
    pub recipient: Address,
    pub amount: i128,
}

#[contractevent(topics = ["pool_Burned"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BurnedEvent {
    pub sender: Address,
    pub amount: i128,
}

#[contractevent(topics = ["pool_Minted"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct MintedEvent {
    pub sender: Address,
    pub recipient: Address,
    pub amount: i128,
}

#[contractevent(topics = ["pool_ChainConfigured"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ChainConfiguredEvent {
    pub remote_chain_selector: u64,
    /// Initial set of remote pool addresses seeded for the chain (EVM
    /// `bytes[] remotePoolAddresses`). H-14: was a single `Bytes`.
    pub remote_pool_addresses: Vec<Bytes>,
    pub remote_token_address: Bytes,
    pub outbound_rate_limiter_config: RateLimitConfig,
    pub inbound_rate_limiter_config: RateLimitConfig,
}

#[contractevent(topics = ["pool_ChainRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ChainRemovedEvent {
    pub remote_chain_selector: u64,
}

/// EVM `RemotePoolAdded(uint64 indexed remoteChainSelector, bytes remotePoolAddress)`
/// (`pools/TokenPool.sol:75`). Emitted by `add_remote_pool` when a new remote
/// pool is appended to a chain's configured set (H-14).
#[contractevent(topics = ["pool_RemotePoolAdded"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RemotePoolAddedEvent {
    pub remote_chain_selector: u64,
    pub remote_pool_address: Bytes,
}

/// EVM `RemotePoolRemoved(uint64 indexed remoteChainSelector, bytes remotePoolAddress)`
/// (`pools/TokenPool.sol:76`). Emitted by `remove_remote_pool` when a remote
/// pool is removed from a chain's configured set (H-14).
#[contractevent(topics = ["pool_RemotePoolRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RemotePoolRemovedEvent {
    pub remote_chain_selector: u64,
    pub remote_pool_address: Bytes,
}

#[contractevent(topics = ["pool_RateLimitConfigured"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RateLimitConfiguredEvent {
    pub remote_chain_selector: u64,
    pub fast_finality: bool,
    pub outbound_config: RateLimitConfig,
    pub inbound_config: RateLimitConfig,
}

#[contractevent(topics = ["pool_OutboundRateLimitConsumed"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct OutboundRateLimitConsumedEvent {
    pub remote_chain_selector: u64,
    pub amount: i128,
}

#[contractevent(topics = ["pool_InboundRateLimitConsumed"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct InboundRateLimitConsumedEvent {
    pub remote_chain_selector: u64,
    pub amount: i128,
}

/// TODO: Likely removable for Stellar — outbound FTF is not meaningful since
/// Stellar has deterministic finality (no reorg risk). See `lock_or_burn` TODO.
#[contractevent(topics = ["pool_FtfOutboundConsumed"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FtfOutboundConsumedEvent {
    pub remote_chain_selector: u64,
    pub amount: i128,
}

#[contractevent(topics = ["pool_FtfInboundConsumed"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FtfInboundConsumedEvent {
    pub remote_chain_selector: u64,
    pub amount: i128,
}

#[contractevent(topics = ["pool_FinalityConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FinalityConfigSetEvent {
    pub allowed_finality: u32,
}

#[contractevent(topics = ["pool_HooksUpdated"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AdvancedPoolHooksUpdatedEvent {
    pub old_hooks: Option<Address>,
    pub new_hooks: Option<Address>,
}
