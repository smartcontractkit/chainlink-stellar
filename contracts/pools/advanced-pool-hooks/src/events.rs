//! Events emitted by the AdvancedPoolHooks contract. Mirror `AdvancedPoolHooks.sol`
//! events with an `aph_` topic prefix.

use soroban_sdk::{contractevent, Address};

use crate::types::CCVConfig;

/// Mirrors `AuthorizedCallerAdded(address caller)` (EVM `AuthorizedCallers`).
#[contractevent(topics = ["auth_CallerAdded"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorizedCallerAddedEvent {
    pub caller: Address,
}

/// Mirrors `AuthorizedCallerRemoved(address caller)` (EVM `AuthorizedCallers`).
#[contractevent(topics = ["auth_CallerRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorizedCallerRemovedEvent {
    pub caller: Address,
}

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

/// Mirrors `PolicyEngineAttached(address indexed policyEngine)`. `None` means
/// the engine was cleared (EVM `address(0)` — policy checks disabled).
#[contractevent(topics = ["aph_PolicyEngineAttached"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PolicyEngineAttachedEvent {
    pub policy_engine: Option<Address>,
}

/// Mirrors `PolicyEngineDetachFailed(address indexed policyEngine, bytes reason)`.
/// EVM carries the revert reason bytes; Soroban's typed `try_` call API does not
/// surface them cheaply, so only the failing engine address is carried.
#[contractevent(topics = ["aph_PolicyEngineDetachFailed"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PolicyEngineDetachFailedEvent {
    pub policy_engine: Address,
}
