use soroban_sdk::{contracttype, Address, Vec};

// ============================================================
// Static Configuration
// ============================================================

/// Static configuration that cannot be changed after deployment.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StaticConfig {
    /// Maximum fee that can be charged for a message (safety guard).
    /// Denominated in LINK token's smallest unit (juels).
    pub max_fee_juels_per_msg: i128,
    /// LINK token address for fee calculations and discounts.
    pub link_token: Address,
}

// ============================================================
// Destination Chain Configuration
// ============================================================

/// Fee and validation configuration for a destination chain.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DestChainConfig {
    /// Whether this destination chain is enabled.
    pub is_enabled: bool,
    /// Maximum data payload size in bytes.
    pub max_data_bytes: u32,
    /// Maximum gas limit per message.
    pub max_per_msg_gas_limit: u32,
    /// Gas charged on top of the gasLimit to cover destination chain costs.
    pub dest_gas_overhead: u32,
    /// Default dest-chain gas charged per byte of `data` payload.
    pub dest_gas_per_payload_byte: u32,
    /// Default token fee charged per token transfer (USD cents).
    pub default_token_fee_usd: u32,
    /// Default gas charged to execute a token transfer on the destination chain.
    pub default_token_dest_gas: u32,
    /// Default gas limit for a transaction.
    pub default_tx_gas_limit: u32,
    /// Flat network fee to charge for messages (USD cents, multiples of 0.01 USD).
    pub network_fee_usd_cents: u32,
    /// Percentage multiplier when fee is paid in LINK. 90 = 10% discount.
    pub link_premium_percent: u32,
}

/// Arguments for updating destination chain configuration.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DestChainConfigArgs {
    /// Destination chain selector.
    pub dest_chain_selector: u64,
    /// Configuration to apply.
    pub config: DestChainConfig,
}

// ============================================================
// Token Transfer Fee Configuration
// ============================================================

/// Fee configuration for token transfers to a specific destination chain.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TokenTransferFeeConfig {
    /// Minimum fee to charge per token transfer (USD cents).
    pub fee_usd_cents: u32,
    /// Gas charged to execute the token transfer on the destination chain.
    pub dest_gas_overhead: u32,
    /// Data availability bytes that are returned from the source pool.
    pub dest_bytes_overhead: u32,
    /// Whether this token has custom transfer fees.
    pub is_enabled: bool,
}

/// Arguments for setting token transfer fee configuration.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TokenFeeConfigArgs {
    /// Destination chain selector.
    pub dest_chain_selector: u64,
    /// Token address.
    pub token: Address,
    /// Fee configuration for this token.
    pub config: TokenTransferFeeConfig,
}

/// Arguments for removing token transfer fee configuration.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TokenFeeConfigRemoveArgs {
    /// Destination chain selector.
    pub dest_chain_selector: u64,
    /// Token address.
    pub token: Address,
}

// ============================================================
// Price Types
// ============================================================

/// A price value with its update timestamp.
/// Prices are stored in USD with 18 decimals (1e18 = $1 USD).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TimestampedPrice {
    /// Price value in USD with 18 decimals.
    pub value: u128,
    /// Unix timestamp when the price was last updated.
    pub timestamp: u64,
}

/// Token price update for batch updates.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TokenPriceUpdate {
    /// Token address.
    pub token: Address,
    /// Price in USD with 18 decimals per 1e18 of smallest token denomination.
    pub usd_per_token: u128,
}

/// Gas price update for batch updates.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct GasPriceUpdate {
    /// Destination chain selector.
    pub dest_chain_selector: u64,
    /// Gas price in USD with 18 decimals per unit of gas.
    pub usd_per_unit_gas: u128,
}

/// Batch price updates combining token and gas prices.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PriceUpdates {
    /// Token price updates.
    pub token_price_updates: Vec<TokenPriceUpdate>,
    /// Gas price updates.
    pub gas_price_updates: Vec<GasPriceUpdate>,
}

// ============================================================
// Fee Quote Result Types
// ============================================================

/// Result from quoting gas for execution.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct GasQuoteResult {
    /// Total gas needed for the message.
    pub total_gas: u32,
    /// Gas cost in USD cents.
    pub gas_cost_usd_cents: u128,
    /// Fee token price in USD with 18 decimals.
    pub fee_token_price: u128,
    /// Premium percentage multiplier (100 = no premium, 90 = 10% discount).
    pub premium_multiplier: u32,
}

/// Result from getting token transfer fee.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TokenTransferFeeResult {
    /// Fee in USD cents.
    pub fee_usd_cents: u32,
    /// Gas overhead for destination execution.
    pub dest_gas_overhead: u32,
    /// Bytes overhead for destination.
    pub dest_bytes_overhead: u32,
}

/// Result from getting message fee.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct MessageFeeResult {
    /// Fee in USD cents.
    pub fee_usd_cents: u128,
    /// Fee in fee token.
    pub fee_token_amount: i128,
    /// Fee token price in USD with 18 decimals.
    pub fee_token_price: u128,
}
