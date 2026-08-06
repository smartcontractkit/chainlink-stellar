use soroban_sdk::{contractevent, Address, BytesN, String, Vec, I256};

use crate::interface::types::WorkflowPermission;

#[contractevent(topics = ["FeedUpdated"])]
#[derive(Clone, Debug)]
pub struct FeedUpdated {
    #[topic]
    pub data_id: BytesN<16>,
    pub round_id: u64,
    pub timestamp: u64,
    pub answer: I256,
    pub ledger_seq: u32,
    pub primary: bool,
}

#[contractevent(topics = ["StaleReport"])]
#[derive(Clone, Debug)]
pub struct StaleReport {
    #[topic]
    pub data_id: BytesN<16>,
    pub report_ts: u64,
    pub stored_ts: u64,
}

#[contractevent(topics = ["InvalidUpdatePermission"])]
#[derive(Clone, Debug)]
pub struct InvalidUpdatePermission {
    #[topic]
    pub data_id: BytesN<16>,
    pub sender: Address,
    pub workflow_owner: BytesN<20>,
    pub workflow_name: BytesN<10>,
}

#[contractevent(topics = ["NonCanonicalReport"])]
#[derive(Clone, Debug)]
pub struct NonCanonicalReport {
    #[topic]
    pub data_id: BytesN<16>,
    pub expected_decimals: u32,
}

#[contractevent(topics = ["FeedConfigSet"])]
#[derive(Clone, Debug)]
pub struct FeedConfigSet {
    #[topic]
    pub data_id: BytesN<16>,
    pub decimals: u32,
    pub description: String,
    pub workflow_permissions: Vec<WorkflowPermission>,
}

#[contractevent(topics = ["FeedFrozenSet"])]
#[derive(Clone, Debug)]
pub struct FeedFrozenSet {
    #[topic]
    pub data_id: BytesN<16>,
    pub frozen: bool,
}

#[contractevent(topics = ["FeedAdminAdded"])]
#[derive(Clone, Debug)]
pub struct FeedAdminAdded {
    #[topic]
    pub admin: Address,
}

#[contractevent(topics = ["FeedAdminRemoved"])]
#[derive(Clone, Debug)]
pub struct FeedAdminRemoved {
    #[topic]
    pub admin: Address,
}
