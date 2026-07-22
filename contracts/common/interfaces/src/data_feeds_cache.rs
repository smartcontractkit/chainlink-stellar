pub type DataId = soroban_sdk::BytesN<16>;
pub type WorkflowOwner = soroban_sdk::BytesN<20>;
pub type WorkflowName = soroban_sdk::BytesN<10>;
pub type WasmHash = soroban_sdk::BytesN<32>;

#[soroban_sdk::contractargs(name = "DataFeedsCacheArgs")]
#[soroban_sdk::contractclient(name = "DataFeedsCacheClient")]
pub trait DataFeedsCacheInterface {
    fn upgrade(env: soroban_sdk::Env, new_wasm_hash: WasmHash);
    fn version(env: soroban_sdk::Env) -> u32;
    fn decimals(env: soroban_sdk::Env, data_id: DataId) -> Result<u32, CacheError>;
    fn get_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_round(
        env: soroban_sdk::Env,
        data_id: DataId,
        round_id: u64,
    ) -> Result<Option<RoundData>, CacheError>;
    fn on_report(
        env: soroban_sdk::Env,
        sender: soroban_sdk::Address,
        metadata: soroban_sdk::Bytes,
        report: soroban_sdk::Bytes,
    ) -> Result<(), CacheError>;
    fn find_round(
        env: soroban_sdk::Env,
        data_id: DataId,
        timestamp: u64,
        bound: Bound,
    ) -> Result<Option<RoundData>, CacheError>;
    fn description(
        env: soroban_sdk::Env,
        data_id: DataId,
    ) -> Result<soroban_sdk::String, CacheError>;
    fn round_range(
        env: soroban_sdk::Env,
        data_id: DataId,
        from: u64,
        to: u64,
    ) -> Result<soroban_sdk::Vec<RoundData>, CacheError>;
    fn latest_round(
        env: soroban_sdk::Env,
        data_id: DataId,
    ) -> Result<Option<RoundData>, CacheError>;
    fn __constructor(env: soroban_sdk::Env, owner: soroban_sdk::Address);
    fn is_feed_admin(env: soroban_sdk::Env, admin: soroban_sdk::Address) -> bool;
    fn add_feed_admin(
        env: soroban_sdk::Env,
        new_admin: soroban_sdk::Address,
    ) -> Result<(), CacheError>;
    fn has_permission(
        env: soroban_sdk::Env,
        data_id: DataId,
        sender: soroban_sdk::Address,
        workflow_owner: WorkflowOwner,
        workflow_name: WorkflowName,
    ) -> bool;
    fn recover_tokens(
        env: soroban_sdk::Env,
        token: soroban_sdk::Address,
        to: soroban_sdk::Address,
        amount: i128,
    );
    fn accept_ownership(env: soroban_sdk::Env);
    fn set_feed_configs(
        env: soroban_sdk::Env,
        admin: soroban_sdk::Address,
        entries: soroban_sdk::Vec<FeedConfigEntry>,
    ) -> Result<(), CacheError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn remove_feed_admin(
        env: soroban_sdk::Env,
        admin: soroban_sdk::Address,
    ) -> Result<(), CacheError>;
    fn renounce_ownership(env: soroban_sdk::Env);
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
        live_until_ledger: u32,
    );
    fn remove_feed_configs(
        env: soroban_sdk::Env,
        admin: soroban_sdk::Address,
        data_ids: soroban_sdk::Vec<DataId>,
    ) -> Result<(), CacheError>;
    fn get_feed_permissions(
        env: soroban_sdk::Env,
        data_id: DataId,
    ) -> soroban_sdk::Vec<WorkflowPermission>;
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoundData {
    pub answer: soroban_sdk::I256,
    pub ledger_seq: u32,
    pub primary: bool,
    pub round_id: u64,
    pub timestamp: u64,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FeedConfig {
    pub description: soroban_sdk::String,
    pub workflow_permissions: soroban_sdk::Vec<WorkflowPermission>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FeedConfigEntry {
    pub config: FeedConfig,
    pub data_id: DataId,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct WorkflowPermission {
    pub allowed_sender: soroban_sdk::Address,
    pub allowed_workflow_name: WorkflowName,
    pub allowed_workflow_owner: WorkflowOwner,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum Bound {
    AtOrBefore,
    AtOrAfter,
}
#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum CacheError {
    MalformedReport = 100,
    UnauthorizedCaller = 101,
    FeedNotConfigured = 102,
    EmptyConfig = 103,
    InvalidAddress = 104,
    InvalidWorkflowName = 105,
    DuplicatePermission = 106,
    InvalidDataId = 107,
    DuplicateFeedConfig = 108,
}
#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum RoleTransferError {
    NoPendingTransfer = 2200,
    InvalidLiveUntilLedger = 2201,
    InvalidPendingAccount = 2202,
    TransferExpired = 2203,
}
#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum OwnableError {
    OwnerNotSet = 2100,
    TransferInProgress = 2101,
    OwnerAlreadySet = 2102,
}
#[soroban_sdk::contractevent(export = false, topics = ["FeedUpdated"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FeedUpdated {
    #[topic]
    pub data_id: DataId,
    pub round_id: u64,
    pub timestamp: u64,
    pub answer: soroban_sdk::I256,
    pub ledger_seq: u32,
    pub primary: bool,
}
#[soroban_sdk::contractevent(export = false, topics = ["StaleReport"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StaleReport {
    #[topic]
    pub data_id: DataId,
    pub report_ts: u64,
    pub stored_ts: u64,
}
#[soroban_sdk::contractevent(export = false, topics = ["FeedConfigSet"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FeedConfigSet {
    #[topic]
    pub data_id: DataId,
    pub decimals: u32,
    pub description: soroban_sdk::String,
    pub workflow_permissions: soroban_sdk::Vec<WorkflowPermission>,
}
#[soroban_sdk::contractevent(export = false, topics = ["FeedAdminAdded"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FeedAdminAdded {
    #[topic]
    pub admin: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["FeedAdminRemoved"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FeedAdminRemoved {
    #[topic]
    pub admin: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["FeedConfigRemoved"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct FeedConfigRemoved {
    #[topic]
    pub data_id: DataId,
}
#[soroban_sdk::contractevent(export = false, topics = ["InvalidUpdatePermission"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct InvalidUpdatePermission {
    #[topic]
    pub data_id: DataId,
    pub sender: soroban_sdk::Address,
    pub workflow_owner: WorkflowOwner,
    pub workflow_name: WorkflowName,
}
#[soroban_sdk::contractevent(export = false, topics = ["Upgraded"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Upgraded {
    pub new_wasm_hash: WasmHash,
}
#[soroban_sdk::contractevent(export = false, topics = ["TokenRecovered"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TokenRecovered {
    pub token: soroban_sdk::Address,
    pub to: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(export = false, topics = ["ownership_transfer"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipTransfer {
    pub old_owner: soroban_sdk::Address,
    pub new_owner: soroban_sdk::Address,
    pub live_until_ledger: u32,
}
#[soroban_sdk::contractevent(export = false, topics = ["ownership_renounced"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipRenounced {
    pub old_owner: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["ownership_transfer_completed"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipTransferCompleted {
    pub new_owner: soroban_sdk::Address,
}
