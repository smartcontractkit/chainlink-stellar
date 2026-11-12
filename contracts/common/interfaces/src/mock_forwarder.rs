#[soroban_sdk::contractargs(name = "MockForwarderArgs")]
#[soroban_sdk::contractclient(name = "MockForwarderClient")]
pub trait MockForwarderInterface {
    fn report(
        env: soroban_sdk::Env,
        transmitter: soroban_sdk::Address,
        receiver: soroban_sdk::Address,
        raw_report: soroban_sdk::Bytes,
        report_context: soroban_sdk::Bytes,
        signatures: soroban_sdk::Vec<Ed25519Signature>,
    ) -> Result<(), MockForwarderError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn get_transmission_info(
        env: soroban_sdk::Env,
        receiver: soroban_sdk::Address,
        workflow_execution_id: soroban_sdk::BytesN<32>,
        report_id: soroban_sdk::BytesN<2>,
    ) -> TransmissionInfo;
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Ed25519Signature {
    pub public_key: soroban_sdk::BytesN<32>,
    pub signature: soroban_sdk::BytesN<64>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TransmissionInfo {
    pub state: TransmissionState,
    pub transmitter: Option<soroban_sdk::Address>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum TransmissionState {
    NotAttempted = 0,
    Succeeded = 1,
    InvalidReceiver = 2,
    Failed = 3,
}
#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum MockForwarderError {
    InvalidReport = 2,
    InvalidReportContext = 3,
    InvalidReportVersion = 4,
    AlreadyProcessed = 13,
    InvalidReceiver = 18,
}
#[soroban_sdk::contractevent(topics = ["forwarder_ReportProcessed"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ReportProcessedEvent {
    #[topic]
    pub receiver: soroban_sdk::Address,
    #[topic]
    pub workflow_execution_id: soroban_sdk::BytesN<32>,
    #[topic]
    pub report_id: soroban_sdk::BytesN<2>,
    pub success: bool,
}
