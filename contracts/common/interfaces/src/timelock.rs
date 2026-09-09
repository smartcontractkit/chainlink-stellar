#[soroban_sdk::contractargs(name = "TimelockArgs")]
#[soroban_sdk::contractclient(name = "TimelockClient")]
pub trait TimelockInterface {
    fn cancel(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        id: soroban_sdk::BytesN<32>,
    ) -> Result<(), TimelockError>;
    fn has_role(
        env: soroban_sdk::Env,
        role: soroban_sdk::Symbol,
        account: soroban_sdk::Address,
    ) -> Result<bool, TimelockError>;
    fn grant_role(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        role: soroban_sdk::Symbol,
        account: soroban_sdk::Address,
    ) -> Result<(), TimelockError>;
    fn initialize(
        env: soroban_sdk::Env,
        min_delay: u64,
        proposers: soroban_sdk::Vec<soroban_sdk::Address>,
        cancellers: soroban_sdk::Vec<soroban_sdk::Address>,
        bypassers: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<(), TimelockError>;
    fn revoke_role(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        role: soroban_sdk::Symbol,
        account: soroban_sdk::Address,
    ) -> Result<(), TimelockError>;
    fn is_operation(env: soroban_sdk::Env, id: soroban_sdk::BytesN<32>) -> bool;
    fn update_delay(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        new_delay: u64,
    ) -> Result<(), TimelockError>;
    fn execute_batch(
        env: soroban_sdk::Env,
        calls: Calls,
        predecessor: soroban_sdk::BytesN<32>,
        salt: soroban_sdk::BytesN<32>,
    ) -> Result<(), TimelockError>;
    fn get_min_delay(env: soroban_sdk::Env) -> u64;
    fn get_timestamp(env: soroban_sdk::Env, id: soroban_sdk::BytesN<32>) -> u64;
    fn renounce_role(
        env: soroban_sdk::Env,
        account: soroban_sdk::Address,
        role: soroban_sdk::Symbol,
    ) -> Result<(), TimelockError>;
    fn block_function(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        target: soroban_sdk::Address,
        function: soroban_sdk::Symbol,
    ) -> Result<(), TimelockError>;
    fn schedule_batch(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        calls: Calls,
        predecessor: soroban_sdk::BytesN<32>,
        salt: soroban_sdk::BytesN<32>,
        delay: u64,
    ) -> Result<(), TimelockError>;
    fn extend_all_ttls(env: soroban_sdk::Env) -> Result<(), TimelockError>;
    fn get_role_member(
        env: soroban_sdk::Env,
        role: soroban_sdk::Symbol,
        index: u32,
    ) -> Result<soroban_sdk::Address, TimelockError>;
    fn unblock_function(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        target: soroban_sdk::Address,
        function: soroban_sdk::Symbol,
    ) -> Result<(), TimelockError>;
    fn is_operation_done(env: soroban_sdk::Env, id: soroban_sdk::BytesN<32>) -> bool;
    fn extend_op_time_ttl(
        env: soroban_sdk::Env,
        id: soroban_sdk::BytesN<32>,
    ) -> Result<(), TimelockError>;
    fn is_operation_ready(env: soroban_sdk::Env, id: soroban_sdk::BytesN<32>) -> bool;
    fn is_function_blocked(
        env: soroban_sdk::Env,
        target: soroban_sdk::Address,
        function: soroban_sdk::Symbol,
    ) -> Result<bool, TimelockError>;
    fn hash_operation_batch(
        env: soroban_sdk::Env,
        calls: Calls,
        predecessor: soroban_sdk::BytesN<32>,
        salt: soroban_sdk::BytesN<32>,
    ) -> Result<soroban_sdk::BytesN<32>, TimelockError>;
    fn is_operation_pending(env: soroban_sdk::Env, id: soroban_sdk::BytesN<32>) -> bool;
    fn get_role_member_count(
        env: soroban_sdk::Env,
        role: soroban_sdk::Symbol,
    ) -> Result<u32, TimelockError>;
    fn bypasser_execute_batch(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        calls: Calls,
    ) -> Result<(), TimelockError>;
    fn get_blocked_function_at(
        env: soroban_sdk::Env,
        index: u32,
    ) -> Result<BlockedFunction, TimelockError>;
    fn get_blocked_function_count(env: soroban_sdk::Env) -> u32;
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Call {
    pub args_xdr: soroban_sdk::Bytes,
    pub function: soroban_sdk::Symbol,
    pub target: soroban_sdk::Address,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Calls {
    pub inner: soroban_sdk::Vec<Call>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct BlockedFunction {
    pub function: soroban_sdk::Symbol,
    pub target: soroban_sdk::Address,
}
#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum TimelockError {
    NotInitialized = 1,
    AlreadyInitialized = 2,
    NotAuthorized = 3,
    OperationAlreadyScheduled = 20,
    InsufficientDelay = 21,
    FunctionIsBlocked = 22,
    OperationNotReady = 30,
    MissingPredecessor = 31,
    CallReverted = 32,
    CallAborted = 33,
    OperationCannotBeCancelled = 40,
    InvalidInvokeData = 50,
    IndexOutOfBounds = 51,
    UnknownRole = 52,
    InvalidTarget = 53,
    InvalidArgsXdr = 54,
    EmptyBatch = 55,
    UnsupportedSelfCall = 56,
}
#[soroban_sdk::contractevent(topics = ["tl_Cancelled"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CancelledEvent {
    pub id: soroban_sdk::BytesN<32>,
}
#[soroban_sdk::contractevent(topics = ["tl_RoleGranted"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleGrantedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["tl_RoleRevoked"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleRevokedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["tl_CallExecuted"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CallExecutedEvent {
    pub id: soroban_sdk::BytesN<32>,
    pub index: u32,
    pub target: soroban_sdk::Address,
    pub function: soroban_sdk::Symbol,
    pub args_hash: soroban_sdk::BytesN<32>,
}
#[soroban_sdk::contractevent(topics = ["tl_CallScheduled"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CallScheduledEvent {
    pub id: soroban_sdk::BytesN<32>,
    pub index: u32,
    pub target: soroban_sdk::Address,
    pub function: soroban_sdk::Symbol,
    pub args_hash: soroban_sdk::BytesN<32>,
    pub predecessor: soroban_sdk::BytesN<32>,
    pub salt: soroban_sdk::BytesN<32>,
    pub delay: u64,
}
#[soroban_sdk::contractevent(topics = ["tl_MinDelay"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct MinDelayChangeEvent {
    pub old_duration: u64,
    pub new_duration: u64,
}
#[soroban_sdk::contractevent(topics = ["tl_FnBlocked"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FunctionBlockedEvent {
    pub target: soroban_sdk::Address,
    pub function: soroban_sdk::Symbol,
}
#[soroban_sdk::contractevent(topics = ["tl_FnUnblocked"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FunctionUnblockedEvent {
    pub target: soroban_sdk::Address,
    pub function: soroban_sdk::Symbol,
}
#[soroban_sdk::contractevent(topics = ["tl_BypCallExec"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct BypasserCallExecutedEvent {
    pub index: u32,
    pub target: soroban_sdk::Address,
    pub function: soroban_sdk::Symbol,
    pub args_hash: soroban_sdk::BytesN<32>,
}
