use soroban_sdk::{contractclient, Address, Env, Vec};

use crate::interface::types::{
    DataId, FeedConfigEntry, WorkflowName, WorkflowOwner, WorkflowPermission,
};
use crate::interface::CacheError;

#[contractclient(name = "DataFeedsCacheAdminClient")]
pub trait DataFeedsCacheAdmin {
    fn set_feed_configs(
        env: Env,
        admin: Address,
        entries: Vec<FeedConfigEntry>,
    ) -> Result<(), CacheError>;

    fn remove_feed_configs(
        env: Env,
        admin: Address,
        data_ids: Vec<DataId>,
    ) -> Result<(), CacheError>;

    fn add_feed_admin(env: Env, new_admin: Address) -> Result<(), CacheError>;

    fn remove_feed_admin(env: Env, admin: Address) -> Result<(), CacheError>;

    fn get_feed_permissions(env: Env, data_id: DataId) -> Vec<WorkflowPermission>;

    fn has_permission(
        env: Env,
        data_id: DataId,
        sender: Address,
        workflow_owner: WorkflowOwner,
        workflow_name: WorkflowName,
    ) -> bool;

    fn is_feed_admin(env: Env, admin: Address) -> bool;
}
