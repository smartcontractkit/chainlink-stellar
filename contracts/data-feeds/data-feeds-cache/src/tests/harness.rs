pub(crate) use soroban_sdk::{
    testutils::storage::Persistent as _,
    token::{StellarAssetClient, TokenClient},
    vec,
    xdr::ToXdr,
    Address, Bytes, BytesN, Env, Event, String, Vec, I256,
};

pub(crate) use data_feeds_common::test_utils::{
    age_ttl, authorize, network_max_ttl, new_address, peek, roll, set_network_max_ttl, Harness,
};

pub(crate) use crate::events::{
    FeedAdminAdded, FeedAdminRemoved, FeedConfigRemoved, FeedConfigSet, FeedFrozenSet, FeedUpdated,
    InvalidUpdatePermission, StaleReport,
};
pub(crate) use crate::interface::types::{
    Bound, DataId, FeedConfig, ReportEntry, RoundData, WireDataId, WorkflowName, WorkflowOwner,
    WorkflowPermission,
};
pub(crate) use crate::interface::CacheError;
pub(crate) use crate::interface::FeedConfigEntry;
pub(crate) use crate::storage::DataKey;
pub(crate) use crate::{DataFeedsCache, DataFeedsCacheClient};

use crate::domain::feed::perm_hash;

pub(crate) use crate::test_utils::*;

pub(crate) struct Cache {
    pub(crate) env: Env,
    pub(crate) id: Address,
    pub(crate) owner: Address,
}

impl Harness for Cache {
    fn env(&self) -> &Env {
        &self.env
    }

    fn id(&self) -> &Address {
        &self.id
    }
}

impl Cache {
    pub(crate) fn deploy() -> Self {
        let cache = Self::deploy_no_auth();
        cache.env.mock_all_auths();
        cache
    }

    pub(crate) fn deploy_no_auth() -> Self {
        let env = Env::default();
        let owner = new_address(&env);
        let id = env.register(DataFeedsCache, (owner.clone(),));
        Cache { env, id, owner }
    }

    pub(crate) fn add_admin(&self) -> Address {
        let admin = new_address(&self.env);
        self.client().add_feed_admin(&admin);
        admin
    }

    pub(crate) fn configure_feed(
        &self,
        admin: &Address,
        sender: &Address,
        tag: u8,
        desc: &str,
    ) -> DataId {
        let id = mock_feed_id(&self.env, tag);
        self.client().set_feed_configs(
            admin,
            &vec![&self.env, mock_feed_config(&self.env, &id, sender, desc)],
        );
        id
    }

    pub(crate) fn client(&self) -> DataFeedsCacheClient<'static> {
        DataFeedsCacheClient::new(&self.env, &self.id)
    }

    pub(crate) fn persistent_ttl(&self, key: &DataKey) -> u32 {
        self.env
            .as_contract(&self.id, || self.env.storage().persistent().get_ttl(key))
    }

    pub(crate) fn permission_ttl(&self, id: &DataId, sender: &Address) -> u32 {
        let phash = perm_hash(
            &self.env,
            sender,
            &mock_wf_owner(&self.env),
            &mock_wf_name(&self.env),
        );
        self.persistent_ttl(&DataKey::Permission(id.clone(), phash))
    }

    pub(crate) fn add_feed(&self, tag: u8) -> Feed {
        let admin = self.add_admin();
        let sender = new_address(&self.env);
        let id = self.configure_feed(&admin, &sender, tag, DEFAULT_DESC);
        Feed {
            admin,
            sender,
            id,
            tag,
        }
    }

    pub(crate) fn add_feed_pair(&self) -> (DataId, DataId, Address) {
        let admin = self.add_admin();
        let sender = new_address(&self.env);
        let a = self.configure_feed(&admin, &sender, 1, "A");
        let b = self.configure_feed(&admin, &sender, 2, "B");
        (a, b, sender)
    }

    pub(crate) fn write(&self, feed: &Feed, answer: i128, ts: u64) {
        let wid = mock_wire_id(&self.env, feed.tag, 0);
        self.client().on_report(
            &feed.sender,
            &mock_metadata(&self.env),
            &report(&self.env, &[(wid, answer, ts)]),
        );
    }

    pub(crate) fn report_at(&self, feed: &Feed, ts: u64) {
        self.write(feed, 0, ts);
    }

    pub(crate) fn seed(&self, feed: &Feed, count: u64) {
        for k in 1..=count {
            roll(&self.env, 100 + k as u32);
            self.write(feed, (k * 100) as i128, k * 10);
        }
    }

    pub(crate) fn remove(&self, feed: &Feed) {
        self.client()
            .remove_feed_configs(&feed.admin, &vec![&self.env, feed.id.clone()]);
    }

    pub(crate) fn history(&self, feed: &Feed) -> Vec<RoundData> {
        self.client().round_range(&feed.id, &0, &u64::MAX)
    }

    pub(crate) fn round(&self, feed: &Feed, round_id: u64) -> Option<RoundData> {
        self.client().get_round(&feed.id, &round_id)
    }

    pub(crate) fn find(&self, feed: &Feed, ts: u64, bound: Bound) -> Option<RoundData> {
        self.client().find_round(&feed.id, &ts, &bound)
    }

    pub(crate) fn latest_round(&self, feed: &Feed) -> RoundData {
        self.client().latest_round(&feed.id).unwrap()
    }

    pub(crate) fn expire_round(&self, feed: &Feed, round_id: u64) {
        let did = feed.id.clone();
        self.env.as_contract(&self.id, || {
            self.env
                .storage()
                .temporary()
                .remove(&DataKey::Round(did.clone(), round_id));
        });
    }
}

pub(crate) struct Feed {
    pub(crate) admin: Address,
    pub(crate) sender: Address,
    pub(crate) id: DataId,
    pub(crate) tag: u8,
}
