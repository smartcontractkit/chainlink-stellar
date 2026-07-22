use soroban_sdk::{contracttype, Address, BytesN, String, Vec, I256};

pub type DataId = BytesN<16>;

pub type WorkflowOwner = BytesN<20>;

pub type WorkflowName = BytesN<10>;

pub type WorkflowCid = BytesN<32>;

pub type ReportId = BytesN<2>;

pub type WireDataId = BytesN<32>;

#[contracttype]
#[derive(Clone, Debug)]
pub struct RoundData {
    pub round_id: u64,
    pub answer: I256,
    pub timestamp: u64,
    pub ledger_seq: u32,
    pub primary: bool,
}

#[contracttype]
#[derive(Copy, Clone, Debug)]
pub enum Bound {
    AtOrBefore,
    AtOrAfter,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct WorkflowPermission {
    pub allowed_sender: Address,
    pub allowed_workflow_owner: WorkflowOwner,
    pub allowed_workflow_name: WorkflowName,
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct FeedConfig {
    pub description: String,
    pub workflow_permissions: Vec<WorkflowPermission>,
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct FeedConfigEntry {
    pub data_id: DataId,
    pub config: FeedConfig,
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct ReportEntry {
    pub data_id: WireDataId,
    pub answer: I256,
    pub timestamp: u64,
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct Metadata {
    pub workflow_cid: WorkflowCid,
    pub workflow_name: WorkflowName,
    pub workflow_owner: WorkflowOwner,
    pub report_id: ReportId,
}
