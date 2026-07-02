use soroban_sdk::{contractevent, Address, BytesN, Vec};

#[contractevent(topics = ["forwarder_ForwarderAdded"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ForwarderAddedEvent {
    pub forwarder: Address,
}

#[contractevent(topics = ["forwarder_ForwarderRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ForwarderRemovedEvent {
    pub forwarder: Address,
}

#[contractevent(topics = ["forwarder_ConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ConfigSetEvent {
    #[topic]
    pub don_id: u32,
    #[topic]
    pub config_version: u32,
    pub f: u32,
    pub signers: Vec<BytesN<32>>,
}

#[contractevent(topics = ["forwarder_ReportProcessed"], data_format = "single-value")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ReportProcessedEvent {
    #[topic]
    pub receiver: Address,
    #[topic]
    pub workflow_execution_id: BytesN<32>,
    #[topic]
    pub report_id: BytesN<2>,
    pub success: bool,
}
