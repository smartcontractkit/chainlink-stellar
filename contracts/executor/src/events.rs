//! Events emitted by the Executor contract. Mirror `Executor.sol` events with
//! an `exec_` topic prefix (matches `gen_interfaces.sh` event-prefix convention).

use soroban_sdk::{contractevent, Address};

use crate::types::{DynamicConfig, RemoteChainConfig};

/// Mirrors `ConfigSet(DynamicConfig dynamicConfig)`.
#[contractevent(topics = ["exec_ConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ConfigSetEvent {
    pub dynamic_config: DynamicConfig,
}

/// Mirrors `DestChainAdded(uint64 indexed destChainSelector, RemoteChainConfig config)`.
#[contractevent(topics = ["exec_DestChainAdded"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DestChainAddedEvent {
    pub dest_chain_selector: u64,
    pub config: RemoteChainConfig,
}

/// Mirrors `DestChainRemoved(uint64 indexed destChainSelector)`.
#[contractevent(topics = ["exec_DestChainRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DestChainRemovedEvent {
    pub dest_chain_selector: u64,
}

/// Mirrors `CCVAllowlistUpdated(bool enabled)`.
#[contractevent(topics = ["exec_CCVAllowlistUpdated"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CCVAllowlistUpdatedEvent {
    pub enabled: bool,
}

/// Mirrors `CCVAdded(address indexed ccv)`.
#[contractevent(topics = ["exec_CCVAdded"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CCVAddedEvent {
    pub ccv: Address,
}

/// Mirrors `CCVRemoved(address indexed ccv)`.
#[contractevent(topics = ["exec_CCVRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CCVRemovedEvent {
    pub ccv: Address,
}

/// Mirrors `FeeTokenHandler.FeeTokenWithdrawn(address indexed receiver, address
/// indexed feeToken, uint256 amount)`, emitted by EVM `Executor.withdrawFeeTokens`
/// via `FeeTokenHandler._withdrawFeeTokens` for each token swept.
#[contractevent(topics = ["exec_FeeTokenWithdrawn"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FeeTokenWithdrawnEvent {
    pub receiver: Address,
    pub fee_token: Address,
    pub amount: i128,
}
