use soroban_sdk::{contractevent, Address, BytesN};

// ============================================================
// Events
// ============================================================

/// Emitted when an OnRamp is configured for a destination chain.
#[contractevent(topics = ["router_OnRampSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct OnRampSetEvent {
    /// Destination chain selector
    pub dest_chain_selector: u64,
    /// OnRamp contract address
    pub onramp: Address,
}

/// Emitted when an OnRamp is removed for a destination chain, pausing the lane.
/// `ccip_send` and `get_fee` revert with `UnsupportedDestinationChain` while no OnRamp is
/// configured; re-enable via `set_onramp`. Soroban has no zero-address sentinel, so removal is
/// the source-side lane pause (EVM pauses by setting the router to `address(0)`).
#[contractevent(topics = ["router_OnRampRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct OnRampRemovedEvent {
    /// Destination chain selector
    pub dest_chain_selector: u64,
    /// OnRamp contract address that was removed
    pub onramp: Address,
}

/// Emitted when an OffRamp is added for a source chain.
#[contractevent(topics = ["router_OffRampAdded"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct OffRampAddedEvent {
    /// Source chain selector
    pub source_chain_selector: u64,
    /// OffRamp contract address
    pub offramp: Address,
}

/// Emitted when an OffRamp is removed for a source chain.
#[contractevent(topics = ["router_OffRampRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct OffRampRemovedEvent {
    /// Source chain selector
    pub source_chain_selector: u64,
    /// OffRamp contract address
    pub offramp: Address,
}

/// Emitted when a message is executed (routed to receiver).
#[contractevent(topics = ["router_MessageExecuted"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct MessageExecutedEvent {
    /// Message ID
    pub message_id: BytesN<32>,
    /// Source chain selector
    pub source_chain_selector: u64,
    /// OffRamp that delivered the message
    pub offramp: Address,
}

/// Emitted when a CCIP send request is initiated through the Router.
#[contractevent(topics = ["router_CCIPSendRequested"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CCIPSendRequestedEvent {
    /// Message ID returned by OnRamp
    pub message_id: BytesN<32>,
    /// Destination chain selector
    pub dest_chain_selector: u64,
    /// Original sender who initiated the request
    pub sender: Address,
}
