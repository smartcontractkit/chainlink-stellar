use soroban_sdk::{contractevent, Address, BytesN};

/// Same topics and data layout as the production forwarder's
/// ReportProcessedEvent, so off-chain consumers decode both identically.
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
