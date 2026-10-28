use soroban_sdk::{contracttype, Address};

// ============================================================
// Types & Structs
// ============================================================

/// Per-token configuration storing the administrator, pending administrator,
/// and the associated token pool address.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TokenConfig {
    pub administrator: Option<Address>,
    pub pending_administrator: Option<Address>,
    pub token_pool: Option<Address>,
}

// ============================================================
// Persistent Storage Keys
// ============================================================

/// Keys for per-token data stored in persistent storage.
#[contracttype]
#[derive(Clone)]
pub enum DataKey {
    /// Maps a token address to its TokenConfig.
    TokenConfig(Address),
    /// Maps an index (u32) to a token address, enabling paginated enumeration.
    TokenIndex(u32),
    /// Maps a token address to its `TokenIndex` position, giving idempotent
    /// registration an O(1) membership check (EVM `EnumerableSet` keeps the
    /// same reverse mapping alongside its values array).
    TokenIndexLookup(Address),
}
