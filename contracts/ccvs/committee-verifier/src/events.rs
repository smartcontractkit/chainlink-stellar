//! Events emitted by the CommitteeVerifier contract.

use soroban_sdk::{contractevent, Address, Bytes, BytesN, Vec};

use crate::types::DynamicConfig;

// ============================================================
// CommitteeVerifier-specific events
// ============================================================

/// Emitted when signature signer configuration is set for a source chain.
/// Mirrors `ConfigSet(uint64 indexed sourceChainSelector, address[] signers, uint8 f)`.
#[contractevent(topics = ["ccv_SignatureConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SignatureConfigSetEvent {
    pub source_chain_selector: u64,
    pub signers: Vec<BytesN<32>>,
    pub threshold: u32,
}

/// Emitted when the dynamic configuration is set.
/// Mirrors `ConfigSet(DynamicConfig dynamicConfig)` from CommitteeVerifier.sol.
#[contractevent(topics = ["ccv_ConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ConfigSetEvent {
    /// The new dynamic configuration (fee aggregator, allowlist admin).
    pub dynamic_config: DynamicConfig,
}

/// Emitted when storage locations admin transfer is initiated (two-step transfer).
/// Mirrors `StorageLocationsAdminTransferRequested(address indexed from, address indexed to)`.
#[contractevent(topics = ["ccv_StorageAdminTransferReq"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StorageAdminTransferReqEvent {
    /// Current storage locations admin initiating the transfer.
    pub from: Address,
    /// Proposed new storage locations admin.
    pub to: Address,
}

/// Emitted when storage locations admin transfer is completed.
/// Mirrors `StorageLocationsAdminTransferred(address indexed from, address indexed to)`.
#[contractevent(topics = ["ccv_StorageAdminTransferred"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StorageAdminTransferredEvent {
    /// Previous storage locations admin.
    pub from: Address,
    /// New storage locations admin.
    pub to: Address,
}

// ============================================================
// BaseVerifier events (inherited)
// ============================================================

/// Emitted when a remote chain config is set or updated.
/// Mirrors `RemoteChainConfigSet(uint64 indexed remoteChainSelector, address router, bool allowlistEnabled)`.
#[contractevent(topics = ["ccv_RemoteChainConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RemoteChainConfigSetEvent {
    /// The remote chain selector.
    pub remote_chain_selector: u64,
    /// The router address for this chain.
    pub router: Option<Address>,
    /// Whether the allowlist is enabled for this chain.
    pub allowlist_enabled: bool,
}

/// Emitted when the verifier-global allowed finality config is set.
/// Mirrors EVM `FinalityConfigSet(bytes4 allowedFinality)` (`BaseVerifier.sol:37`).
#[contractevent(topics = ["ccv_FinalityConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FinalityConfigSetEvent {
    /// The new allowed finality config (`u32` finality-codec value; `0` = wait-for-finality).
    pub allowed_finality: u32,
}

/// Emitted when a sender is added to the allowlist.
/// Mirrors `AllowListSendersAdded(uint64 indexed destChainSelector, address senders)`.
#[contractevent(topics = ["ccv_AllowListSendersAdded"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AllowListSendersAddedEvent {
    /// The destination chain selector.
    pub dest_chain_selector: u64,
    /// The sender address that was added.
    pub sender: Address,
}

/// Emitted when a sender is removed from the allowlist.
/// Mirrors `AllowListSendersRemoved(uint64 indexed destChainSelector, address senders)`.
#[contractevent(topics = ["ccv_AllowListSendersRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AllowListSendersRemovedEvent {
    /// The destination chain selector.
    pub dest_chain_selector: u64,
    /// The sender address that was removed.
    pub sender: Address,
}

/// Emitted when the allowlist state is changed for a destination chain.
/// Mirrors `AllowListStateChanged(uint64 indexed destChainSelector, bool allowlistEnabled)`.
#[contractevent(topics = ["ccv_AllowListStateChanged"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AllowListStateChangedEvent {
    /// The destination chain selector.
    pub dest_chain_selector: u64,
    /// Whether the allowlist is now enabled.
    pub allowlist_enabled: bool,
}

/// Emitted when storage locations are updated.
/// Mirrors `StorageLocationsUpdated(string[] oldLocations, string[] newLocations)`.
#[contractevent(topics = ["ccv_StorageLocationsUpdated"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StorageLocationsUpdatedEvent {
    /// The previous storage location identifiers.
    pub old_locations: Vec<Bytes>,
    /// The new storage location identifiers.
    pub new_locations: Vec<Bytes>,
}
