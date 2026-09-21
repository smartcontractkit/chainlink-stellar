use common_error::CCIPError;
use common_helpers::validation::Validatable;
use soroban_sdk::{contracttype, Address};

/// Per-destination-chain configuration for the executor. Mirrors EVM
/// `Executor.RemoteChainConfig { uint16 usdCentsFee; bool enabled; }`. The fee
/// is widened to `u32` (Soroban has no `u16` arg type) but carries the same
/// USD-cents semantics as `IExecutor.getFee`'s `uint16` return.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RemoteChainConfig {
    /// Flat fee the executor charges to process a message to this chain (USD cents).
    pub usd_cents_fee: u32,
    /// Whether this destination chain is enabled for the executor.
    pub enabled: bool,
}

/// Caller-facing shape for `apply_dest_chain_updates`. Mirrors EVM
/// `Executor.RemoteChainConfigArgs { uint64 destChainSelector; RemoteChainConfig config; }`.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RemoteChainConfigArgs {
    pub dest_chain_selector: u64,
    pub config: RemoteChainConfig,
}

impl Validatable for RemoteChainConfigArgs {
    fn validate(&self) -> Result<(), CCIPError> {
        // Mirrors EVM `applyDestChainUpdates` revert `InvalidDestChain(0)`.
        if self.dest_chain_selector == 0 {
            return Err(CCIPError::InvalidChainSelector);
        }
        Ok(())
    }
}

/// Dynamic configuration mirrored from EVM `Executor.DynamicConfig`.
/// `fee_aggregator` is `Option<Address>`: `None` (or the zero account) is a
/// valid config that intentionally reverts `withdraw_fee_tokens` — same as
/// EVM's zero `feeAggregator` (EVM `FeeTokenHandler` reverts on withdraw only).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DynamicConfig {
    /// Destination for withdrawn fee tokens.
    pub fee_aggregator: Option<Address>,
    /// Allowed finality config (`FinalityCodec` encoding). Enforced in `get_fee`
    /// via `finality_codec::ensure_requested_finality_allowed` — this is the
    /// executor layer of the 5-layer FTF opt-in matrix (H-8 / INV-FIN-EXEC-1/2).
    pub allowed_finality_config: u32,
    /// Whether the CCV allowlist is enforced in `get_fee`.
    pub ccv_allowlist_enabled: bool,
}
