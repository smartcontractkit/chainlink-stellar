use soroban_sdk::{contract, contractimpl, Address, Bytes, BytesN, Env, String, Vec};

use data_feeds_common::{TokenRecoverable, Upgradeable, Versioned};
use stellar_access::ownable::{self, enforce_owner_auth, Ownable};

use crate::domain::decode::{decode_metadata, decode_report, truncate_data_id};
use crate::domain::feed;
use crate::events::{
    FeedAdminAdded, FeedAdminRemoved, FeedConfigSet, FeedFrozenSet, FeedUpdated,
    InvalidUpdatePermission, StaleReport,
};
use crate::interface::types::{RoundData, WorkflowPermission};
use crate::interface::CacheError;
use crate::interface::{
    Bound, DataFeedsCacheAdmin, DataFeedsCacheReader, DataFeedsCacheWriter, DataId, FeedConfig,
    FeedConfigEntry,
};
use crate::storage::Store;

#[contract]
pub struct DataFeedsCache;

#[contractimpl]
impl DataFeedsCache {
    pub fn __constructor(env: Env, owner: Address) {
        ownable::set_owner(&env, &owner);
        env.contract_store().extend_ttl();
    }
}

#[contractimpl]
impl DataFeedsCacheReader for DataFeedsCache {
    fn latest_round(env: Env, data_id: BytesN<16>) -> Result<Option<RoundData>, CacheError> {
        reject_frozen(&env, &data_id)?;
        Ok(feed::latest(&env, &data_id))
    }

    fn get_round(
        env: Env,
        data_id: BytesN<16>,
        round_id: u64,
    ) -> Result<Option<RoundData>, CacheError> {
        reject_frozen(&env, &data_id)?;
        Ok(feed::round(&env, &data_id, round_id))
    }

    fn round_range(
        env: Env,
        data_id: BytesN<16>,
        from: u64,
        to: u64,
    ) -> Result<Vec<RoundData>, CacheError> {
        reject_frozen(&env, &data_id)?;
        Ok(feed::range(&env, &data_id, from, to))
    }

    fn find_round(
        env: Env,
        data_id: BytesN<16>,
        timestamp: u64,
        bound: Bound,
    ) -> Result<Option<RoundData>, CacheError> {
        reject_frozen(&env, &data_id)?;
        Ok(feed::find_round(&env, &data_id, timestamp, bound))
    }

    fn decimals(env: Env, data_id: BytesN<16>) -> Result<Option<u32>, CacheError> {
        reject_frozen(&env, &data_id)?;
        Ok(feed::decimals(&env, &data_id))
    }

    fn description(env: Env, data_id: BytesN<16>) -> Result<Option<String>, CacheError> {
        reject_frozen(&env, &data_id)?;
        Ok(feed::description(&env, &data_id))
    }

    fn is_configured(env: Env, data_id: BytesN<16>) -> Result<bool, CacheError> {
        Ok(feed::configured(&env, &data_id))
    }

    fn is_frozen(env: Env, data_id: BytesN<16>) -> bool {
        feed::is_frozen(&env, &data_id)
    }
}

fn reject_frozen(env: &Env, data_id: &DataId) -> Result<(), CacheError> {
    if feed::is_frozen(env, data_id) {
        return Err(CacheError::FeedFrozen);
    }
    Ok(())
}

#[contractimpl]
impl DataFeedsCacheWriter for DataFeedsCache {
    fn on_report(
        env: Env,
        sender: Address,
        metadata: Bytes,
        report: Bytes,
    ) -> Result<(), CacheError> {
        sender.require_auth();
        env.contract_store().extend_ttl();

        let md = decode_metadata(&env, &metadata)?;
        let entries = decode_report(&env, &report)?;

        let ledger = env.ledger().sequence();
        let phash = feed::perm_hash(&env, &sender, &md.workflow_owner, &md.workflow_name);

        for entry in entries.iter() {
            let data_id = truncate_data_id(&env, &entry.data_id);

            if !feed::permitted(&env, &data_id, &phash) {
                InvalidUpdatePermission {
                    data_id,
                    sender: sender.clone(),
                    workflow_owner: md.workflow_owner.clone(),
                    workflow_name: md.workflow_name.clone(),
                }
                .publish(&env);
                continue;
            }

            match feed::record(&env, &data_id, &entry.answer, entry.timestamp) {
                feed::Recorded::Stale { stored_ts } => {
                    StaleReport {
                        data_id,
                        report_ts: entry.timestamp,
                        stored_ts,
                    }
                    .publish(&env);
                }
                feed::Recorded::Appended { round_id } => {
                    FeedUpdated {
                        data_id,
                        round_id,
                        timestamp: entry.timestamp,
                        answer: entry.answer,
                        ledger_seq: ledger,
                        primary: true,
                    }
                    .publish(&env);
                }
            }
        }
        Ok(())
    }
}

#[contractimpl]
impl DataFeedsCacheAdmin for DataFeedsCache {
    fn set_feed_configs(
        env: Env,
        admin: Address,
        entries: Vec<FeedConfigEntry>,
    ) -> Result<(), CacheError> {
        admin.require_auth();
        env.contract_store().extend_ttl();
        if !authorize(&env, &admin) {
            return Err(CacheError::UnauthorizedCaller);
        }
        if entries.is_empty() {
            return Err(CacheError::EmptyConfig);
        }

        for (i, entry) in entries.iter().enumerate() {
            if entry.data_id.to_array() == [0u8; 16] {
                return Err(CacheError::InvalidDataId);
            }
            for j in (i as u32 + 1)..entries.len() {
                if feed::is_same_feed(&env, &entry.data_id, &entries.get_unchecked(j).data_id) {
                    return Err(CacheError::DuplicateFeedConfig);
                }
            }
            assert_valid_config(&entry.config)?;
        }

        for entry in entries.iter() {
            feed::configure(&env, &entry.data_id, &entry.config)?;
            FeedConfigSet {
                data_id: entry.data_id.clone(),
                decimals: feed::decimals(&env, &entry.data_id).expect("feed was just configured"),
                description: entry.config.description,
                workflow_permissions: entry.config.workflow_permissions,
            }
            .publish(&env);
        }
        Ok(())
    }

    fn set_feed_frozen(
        env: Env,
        admin: Address,
        data_ids: Vec<BytesN<16>>,
        frozen: bool,
    ) -> Result<(), CacheError> {
        admin.require_auth();
        env.contract_store().extend_ttl();
        if !authorize(&env, &admin) {
            return Err(CacheError::UnauthorizedCaller);
        }

        for (i, did) in data_ids.iter().enumerate() {
            for j in (i as u32 + 1)..data_ids.len() {
                if feed::is_same_feed(&env, &did, &data_ids.get_unchecked(j)) {
                    return Err(CacheError::DuplicateFeedConfig);
                }
            }
        }
        for did in data_ids.iter() {
            if !feed::set_frozen(&env, &did, frozen) {
                return Err(CacheError::NoFeedState);
            }
            FeedFrozenSet {
                data_id: did.clone(),
                frozen,
            }
            .publish(&env);
        }
        Ok(())
    }

    fn add_feed_admin(env: Env, new_admin: Address) -> Result<(), CacheError> {
        enforce_owner_auth(&env);
        env.contract_store().extend_ttl();
        env.admin_store().set(&new_admin, &());
        env.admin_store().extend_ttl(&new_admin);
        FeedAdminAdded { admin: new_admin }.publish(&env);
        Ok(())
    }

    fn remove_feed_admin(env: Env, admin: Address) -> Result<(), CacheError> {
        enforce_owner_auth(&env);
        env.contract_store().extend_ttl();
        env.admin_store().remove(&admin);
        FeedAdminRemoved { admin }.publish(&env);
        Ok(())
    }

    fn get_feed_permissions(env: Env, data_id: BytesN<16>) -> Vec<WorkflowPermission> {
        feed::permissions(&env, &data_id)
    }

    fn has_permission(
        env: Env,
        data_id: BytesN<16>,
        sender: Address,
        workflow_owner: BytesN<20>,
        workflow_name: BytesN<10>,
    ) -> bool {
        let phash = feed::perm_hash(&env, &sender, &workflow_owner, &workflow_name);
        feed::permitted(&env, &data_id, &phash)
    }

    fn is_feed_admin(env: Env, admin: Address) -> bool {
        env.admin_store().exists(&admin)
    }
}

fn assert_valid_config(cfg: &FeedConfig) -> Result<(), CacheError> {
    if cfg.workflow_permissions.is_empty() {
        return Err(CacheError::EmptyConfig);
    }
    let perms = &cfg.workflow_permissions;
    for (i, p) in perms.iter().enumerate() {
        if p.allowed_workflow_owner.to_array() == [0u8; 20] {
            return Err(CacheError::InvalidAddress);
        }
        if p.allowed_workflow_name.to_array() == [0u8; 10] {
            return Err(CacheError::InvalidWorkflowName);
        }
        for j in (i as u32 + 1)..perms.len() {
            if p == perms.get_unchecked(j) {
                return Err(CacheError::DuplicatePermission);
            }
        }
    }
    Ok(())
}

fn authorize(env: &Env, admin: &Address) -> bool {
    if env.admin_store().exists(admin) {
        env.admin_store().extend_ttl(admin);
        true
    } else {
        false
    }
}

#[contractimpl(contracttrait)]
impl Versioned for DataFeedsCache {
    fn version(_env: Env) -> u32 {
        1
    }
    fn type_and_version(env: Env) -> String {
        String::from_str(&env, "DataFeedsCache 1.0.0")
    }
}

#[contractimpl(contracttrait)]
impl Ownable for DataFeedsCache {}

#[contractimpl(contracttrait)]
impl Upgradeable for DataFeedsCache {}

#[contractimpl(contracttrait)]
impl TokenRecoverable for DataFeedsCache {}
