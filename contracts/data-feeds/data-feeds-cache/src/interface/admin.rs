use soroban_sdk::{contractclient, Address, BytesN, Env, Vec};

use crate::interface::types::{FeedConfigEntry, WorkflowPermission};
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
        data_ids: Vec<BytesN<32>>,
    ) -> Result<(), CacheError>;

    fn set_feed_frozen(
        env: Env,
        admin: Address,
        data_ids: Vec<BytesN<32>>,
        frozen: bool,
    ) -> Result<(), CacheError>;

    fn add_feed_admin(env: Env, new_admin: Address) -> Result<(), CacheError>;

    fn remove_feed_admin(env: Env, admin: Address) -> Result<(), CacheError>;

    fn get_feed_permissions(env: Env, data_id: BytesN<32>) -> Vec<WorkflowPermission>;

    fn has_permission(
        env: Env,
        data_id: BytesN<32>,
        sender: Address,
        workflow_owner: BytesN<20>,
        workflow_name: BytesN<10>,
    ) -> bool;

    fn is_feed_admin(env: Env, admin: Address) -> bool;
}
