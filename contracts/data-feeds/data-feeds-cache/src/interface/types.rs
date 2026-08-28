use soroban_sdk::{contracttype, Address, BytesN, String, Vec, I256};

pub type DataId = BytesN<32>;

pub const DECIMALS: u32 = 18;

pub type WorkflowOwner = BytesN<20>;

pub type WorkflowName = BytesN<10>;

pub type WorkflowCid = BytesN<32>;

pub type ReportId = BytesN<2>;

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
    AtOrBefore = 0,
    AtOrAfter = 1,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct WorkflowPermission {
    pub allowed_sender: Address,
    pub allowed_workflow_owner: BytesN<20>,
    pub allowed_workflow_name: BytesN<10>,
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
    pub data_id: BytesN<32>,
    pub config: FeedConfig,
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct ReportEntry {
    pub data_id: BytesN<32>,
    pub answer: I256,
    pub timestamp: u64,
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct Metadata {
    pub workflow_cid: BytesN<32>,
    pub workflow_name: BytesN<10>,
    pub workflow_owner: BytesN<20>,
    pub report_id: BytesN<2>,
}
