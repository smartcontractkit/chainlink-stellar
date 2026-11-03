use soroban_sdk::{contractevent, Bytes, BytesN};

use crate::types::{MessageExecutionState, SourceChainConfig, StaticConfig};

/// Emitted when a message execution state changes.
/// This is the primary event for tracking message delivery outcomes.
#[contractevent(topics = ["offramp_2_0_ExecStateChanged"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ExecutionStateChangedEvent {
    pub source_chain_selector: u64,
    pub sequence_number: u64,
    pub message_id: BytesN<32>,
    pub state: MessageExecutionState,
    pub return_data: Bytes,
}

/// Emitted when a source chain configuration is created or updated.
#[contractevent(topics = ["offramp_2_0_SrcChainCfgSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SourceChainConfigSetEvent {
    pub source_chain_selector: u64,
    pub source_config: SourceChainConfig,
}

/// Emitted once at initialization with the immutable static configuration.
#[contractevent(topics = ["offramp_2_0_StaticConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StaticConfigSetEvent {
    pub static_config: StaticConfig,
}
