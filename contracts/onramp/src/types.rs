use common_error::CCIPError;
use common_helpers::validation::Validatable;
use soroban_sdk::{contracttype, Address, Bytes, Vec};

// ============================================================
// Types & Structs
// ============================================================

/// Static configuration that cannot be changed after deployment.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StaticConfig {
    /// Local chain selector identifying this chain
    pub chain_selector: u64,
    /// Token admin registry contract address
    pub token_admin_registry: Address,
    /// RMN remote contract address (for curse checking)
    pub rmn_proxy: Address,
    /// Maximum USD cents value per message (safety limit)
    pub max_usd_cents_per_message: u32,
}

/// Dynamic configuration that can be updated by owner.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DynamicConfig {
    /// FeeQuoter contract address for fee calculations
    pub fee_quoter: Address,
    /// Fee aggregator address (receives protocol fees)
    pub fee_aggregator: Address,
}

/// Configuration for a specific destination chain.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DestChainConfig {
    /// Router address allowed to send messages to this destination
    pub router: Address,
    /// Last used message number (incremented before use)
    pub message_number: u64,
    /// Length of addresses on the destination chain (e.g., 20 for EVM, 32 for Stellar/SVM)
    pub address_bytes_length: u32,
    /// Whether specifying a different token receiver is allowed
    pub token_receiver_allowed: bool,
    /// Network fee in USD cents for messages without tokens
    pub message_network_fee_usd_cents: u32,
    /// Network fee in USD cents for messages with tokens
    pub token_network_fee_usd_cents: u32,
    /// Base gas cost for executing a message on destination
    pub base_execution_gas_cost: u32,
    /// Default executor for this destination
    pub default_executor: Address,
    /// Lane-mandated CCVs required for all messages
    pub lane_mandated_ccvs: Vec<Address>,
    /// Default CCVs to use when user doesn't specify
    pub default_ccvs: Vec<Address>,
    /// Destination OffRamp address (raw bytes, not abi-encoded)
    pub off_ramp: Bytes,
}

/// Arguments for updating destination chain configuration.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DestChainConfigArgs {
    /// Destination chain selector
    pub dest_chain_selector: u64,
    /// Router address
    pub router: Address,
    /// Address length on destination chain
    pub address_bytes_length: u32,
    /// Whether token receiver specification is allowed
    pub token_receiver_allowed: bool,
    /// Message network fee (USD cents)
    pub message_network_fee_usd_cents: u32,
    /// Token network fee (USD cents)
    pub token_network_fee_usd_cents: u32,
    /// Base execution gas cost
    pub base_execution_gas_cost: u32,
    /// Default executor
    pub default_executor: Address,
    /// Lane-mandated CCVs
    pub lane_mandated_ccvs: Vec<Address>,
    /// Default CCVs
    pub default_ccvs: Vec<Address>,
    /// OffRamp address on destination
    pub off_ramp: Bytes,
}

impl Validatable for DestChainConfigArgs {
    fn validate(&self) -> Result<(), CCIPError> {
        if self.dest_chain_selector == 0
            || self.address_bytes_length == 0
            || self.base_execution_gas_cost == 0
        {
            return Err(CCIPError::InvalidConfig);
        }

        if self.off_ramp.len() as u32 != self.address_bytes_length {
            return Err(CCIPError::InvalidDestChainAddress);
        }

        // Ensure at least one default or mandated CCV exists
        if self.default_ccvs.is_empty() && self.lane_mandated_ccvs.is_empty() {
            return Err(CCIPError::InvalidConfig);
        }

        Ok(())
    }
}

/// Receipt structure for tracking fees and gas limits per component.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Receipt {
    /// Entity that issued this receipt (CCV, executor, pool, or router for network fee)
    pub issuer: Address,
    /// Gas limit for destination chain execution
    pub dest_gas_limit: u32,
    /// Byte overhead for destination chain
    pub dest_bytes_overhead: u32,
    /// Fee amount in fee token (smallest denomination)
    pub fee_token_amount: i128,
    /// Extra arguments passed through
    pub extra_args: Bytes,
}
