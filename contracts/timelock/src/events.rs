use soroban_sdk::{contractevent, Address, BytesN, Symbol};

#[contractevent(topics = ["tl_CallScheduled"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CallScheduledEvent {
    pub id: BytesN<32>,
    pub index: u32,
    pub target: Address,
    pub function: Symbol,
    pub args_hash: BytesN<32>,
    pub predecessor: BytesN<32>,
    pub salt: BytesN<32>,
    pub delay: u64,
}

#[contractevent(topics = ["tl_CallExecuted"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CallExecutedEvent {
    pub id: BytesN<32>,
    pub index: u32,
    pub target: Address,
    pub function: Symbol,
    pub args_hash: BytesN<32>,
}

#[contractevent(topics = ["tl_BypCallExec"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BypasserCallExecutedEvent {
    pub index: u32,
    pub target: Address,
    pub function: Symbol,
    pub args_hash: BytesN<32>,
}

#[contractevent(topics = ["tl_Cancelled"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CancelledEvent {
    pub id: BytesN<32>,
}

#[contractevent(topics = ["tl_MinDelay"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct MinDelayChangeEvent {
    pub old_duration: u64,
    pub new_duration: u64,
}

#[contractevent(topics = ["tl_FnBlocked"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FunctionBlockedEvent {
    pub target: Address,
    pub function: Symbol,
}

#[contractevent(topics = ["tl_FnUnblocked"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FunctionUnblockedEvent {
    pub target: Address,
    pub function: Symbol,
}

#[contractevent(topics = ["tl_RoleGranted"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RoleGrantedEvent {
    pub role: Symbol,
    pub account: Address,
    pub sender: Address,
}

#[contractevent(topics = ["tl_RoleRevoked"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RoleRevokedEvent {
    pub role: Symbol,
    pub account: Address,
    pub sender: Address,
}
