use soroban_sdk::{contractevent, Address, BytesN};

/// Emitted when an inbound implementation is set or updated.
#[contractevent(topics = ["vvr_InboundImplSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct InboundImplSetEvent {
    /// The 4-byte verifier version prefix
    pub version: BytesN<4>,
    /// The address of the verifier contract
    pub verifier: Address,
}

/// Emitted when an inbound implementation is removed.
#[contractevent(topics = ["vvr_InboundImplRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct InboundImplRemovedEvent {
    /// The 4-byte verifier version prefix that was removed
    pub version: BytesN<4>,
}

/// Emitted when an outbound implementation is set or updated.
#[contractevent(topics = ["vvr_OutboundImplSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct OutboundImplSetEvent {
    /// The destination chain selector
    pub dest_chain_selector: u64,
    /// The address of the verifier contract
    pub verifier: Address,
}

/// Emitted when an outbound implementation is removed.
#[contractevent(topics = ["vvr_OutboundImplRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct OutboundImplRemovedEvent {
    /// The destination chain selector that was removed
    pub dest_chain_selector: u64,
}

/// Emitted when the fee aggregator address is updated.
#[contractevent(topics = ["vvr_FeeAggregatorSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FeeAggregatorSetEvent {
    /// The new fee aggregator address
    pub fee_aggregator: Address,
}
