pub(crate) use soroban_sdk::{
    testutils::storage::Persistent as _, Address, BytesN, Env, Symbol, I256,
};

pub(crate) use data_feeds_common::test_utils::{
    age_ttl, authorize, network_max_ttl, new_address, Harness,
};

pub(crate) use data_feeds_cache::test_utils::mock_feed_id;
pub(crate) use data_feeds_proxy::{DataId, ProxyReadError, Round};

pub(crate) use super::mock_proxy::{MockProxy, MockProxyClient};
pub(crate) use crate::events::{FeedAdded, FeedRemoved, FeedUpdated, ProxySet};
pub(crate) use crate::storage::{self, DataKey};
pub(crate) use crate::{
    Asset, DataFeedsSep40Proxy, DataFeedsSep40ProxyClient, PriceData, PriceFeedClient,
    Sep40ProxyError, DECIMALS,
};

pub(crate) struct Adapter {
    pub(crate) env: Env,
    pub(crate) id: Address,
    pub(crate) mock: Address,
    pub(crate) owner: Address,
}

impl Harness for Adapter {
    fn env(&self) -> &Env {
        &self.env
    }

    fn id(&self) -> &Address {
        &self.id
    }
}

impl Adapter {
    pub(crate) fn deploy() -> Self {
        Self::deploy_with_base("USD")
    }

    pub(crate) fn deploy_with_base(base: &str) -> Self {
        let adapter = Self::deploy_no_auth_with_base(base);
        adapter.env.mock_all_auths();
        adapter
    }

    pub(crate) fn deploy_no_auth() -> Self {
        Self::deploy_no_auth_with_base("USD")
    }

    fn deploy_no_auth_with_base(base: &str) -> Self {
        let env = Env::default();
        let owner = new_address(&env);
        let mock = env.register(MockProxy, ());
        let id = env.register(
            DataFeedsSep40Proxy,
            (owner.clone(), mock.clone(), Symbol::new(&env, base)),
        );
        Adapter {
            env,
            id,
            mock,
            owner,
        }
    }

    pub(crate) fn client(&self) -> DataFeedsSep40ProxyClient<'static> {
        DataFeedsSep40ProxyClient::new(&self.env, &self.id)
    }

    pub(crate) fn sep40(&self) -> PriceFeedClient<'static> {
        PriceFeedClient::new(&self.env, &self.id)
    }

    pub(crate) fn mock_client(&self) -> MockProxyClient<'static> {
        MockProxyClient::new(&self.env, &self.mock)
    }

    pub(crate) fn asset(&self, name: &str) -> Asset {
        Asset::Other(Symbol::new(&self.env, name))
    }

    pub(crate) fn feed(&self, tag: u8) -> DataId {
        mock_feed_id(&self.env, tag)
    }

    pub(crate) fn round(&self, answer: I256, timestamp: u64) -> Round {
        Round {
            round_id: 1,
            answer,
            timestamp,
        }
    }

    /// Makes the mock proxy return `answer` / `timestamp` for feed `tag`.
    pub(crate) fn set_price(&self, tag: u8, answer: i128, timestamp: u64) {
        self.set_answer(tag, I256::from_i128(&self.env, answer), timestamp);
    }

    pub(crate) fn set_answer(&self, tag: u8, answer: I256, timestamp: u64) {
        self.mock_client()
            .set_round(&self.feed(tag), &self.round(answer, timestamp));
    }

    /// Seeds a price for feed `tag` and maps `name` to it through the real `add_feed`.
    pub(crate) fn map(&self, name: &str, tag: u8, answer: i128, timestamp: u64) -> Asset {
        self.set_price(tag, answer, timestamp);
        let asset = self.asset(name);
        self.client().add_feed(&asset, &self.feed(tag));
        asset
    }

    /// Writes a mapping straight into storage, bypassing the `add_feed` test read.
    pub(crate) fn map_unchecked(&self, asset: &Asset, data_id: &DataId) {
        self.env
            .as_contract(&self.id, || storage::insert_feed(&self.env, asset, data_id));
    }

    pub(crate) fn persistent_ttl(&self, key: &DataKey) -> u32 {
        self.env
            .as_contract(&self.id, || self.env.storage().persistent().get_ttl(key))
    }
}
