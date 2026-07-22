use soroban_sdk::{contractevent, Address, String, Vec, I256};

use crate::interface::DataId;

use crate::interface::types::{WorkflowName, WorkflowOwner, WorkflowPermission};

#[contractevent(topics = ["FeedUpdated"])]
#[derive(Clone, Debug)]
pub struct FeedUpdated {
    #[topic]
    pub data_id: DataId,
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
    pub data_id: DataId,
    pub report_ts: u64,
    pub stored_ts: u64,
}

#[contractevent(topics = ["InvalidUpdatePermission"])]
#[derive(Clone, Debug)]
pub struct InvalidUpdatePermission {
    #[topic]
    pub data_id: DataId,
    pub sender: Address,
    pub workflow_owner: WorkflowOwner,
    pub workflow_name: WorkflowName,
}

#[contractevent(topics = ["FeedConfigSet"])]
#[derive(Clone, Debug)]
pub struct FeedConfigSet {
    #[topic]
    pub data_id: DataId,
    pub decimals: u32,
    pub description: String,
    pub workflow_permissions: Vec<WorkflowPermission>,
}

#[contractevent(topics = ["FeedConfigRemoved"])]
#[derive(Clone, Debug)]
pub struct FeedConfigRemoved {
    #[topic]
    pub data_id: DataId,
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
