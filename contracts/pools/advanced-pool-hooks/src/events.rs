//! Events emitted by the AdvancedPoolHooks contract. Mirror `AdvancedPoolHooks.sol`
//! events with an `aph_` topic prefix.

use soroban_sdk::{contractevent, Address};

use crate::types::CCVConfig;

/// Mirrors `AllowListAdd(address sender)`.
#[contractevent(topics = ["aph_AllowListAdd"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AllowListAddEvent {
    pub sender: Address,
}

/// Mirrors `AllowListRemove(address sender)`.
#[contractevent(topics = ["aph_AllowListRemove"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AllowListRemoveEvent {
    pub sender: Address,
}

/// Mirrors `CCVConfigUpdated(uint64 indexed remoteChainSelector, ...)` carrying
/// the full resolved `CCVConfig`.
#[contractevent(topics = ["aph_CCVConfigUpdated"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CCVConfigUpdatedEvent {
    pub remote_chain_selector: u64,
    pub config: CCVConfig,
}

/// Mirrors `ThresholdAmountSet(uint256 thresholdAmount)`.
#[contractevent(topics = ["aph_ThresholdAmountSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ThresholdAmountSetEvent {
    pub threshold_amount: i128,
}
