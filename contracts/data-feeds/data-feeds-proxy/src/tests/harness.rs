pub(crate) use soroban_sdk::{
    token::{StellarAssetClient, TokenClient},
    vec, Address, Env, String, Vec, I256,
};

pub(crate) use data_feeds_common::test_utils::{
    age_ttl, authorize, network_max_ttl, new_address, peek, Harness,
};

pub(crate) use data_feeds_cache::test_utils::{mock_feed_id, mock_feed_id_with};
pub(crate) use data_feeds_cache::{CacheError, RoundData};

pub(crate) use super::mock_cache::{mock_round_data, MockCache, MockCacheClient};
pub(crate) use crate::events::CacheSet;
pub(crate) use crate::{DataFeedsProxy, DataFeedsProxyClient, DataId, ProxyReadError, Round};

pub(crate) const DUMMY_MOCK_FEED_ID: u8 = 1;

pub(crate) struct Proxy {
    pub(crate) env: Env,
    pub(crate) id: Address,
    pub(crate) mock: Address,
    pub(crate) owner: Address,
}

impl Harness for Proxy {
    fn env(&self) -> &Env {
        &self.env
    }

    fn id(&self) -> &Address {
        &self.id
    }
}

impl Proxy {
    pub(crate) fn deploy() -> Self {
        let proxy = Self::deploy_no_auth();
        proxy.env.mock_all_auths();
        proxy
    }

    pub(crate) fn deploy_no_auth() -> Self {
        let env = Env::default();
        let owner = new_address(&env);
        let mock = env.register(MockCache, ());
        let id = env.register(DataFeedsProxy, (owner.clone(), mock.clone()));
        Proxy {
            env,
            id,
            mock,
            owner,
        }
    }

    pub(crate) fn inject(&self, rows: &[(u64, i128, u64)]) {
        let mut v: Vec<RoundData> = Vec::new(&self.env);
        for r in rows {
            v.push_back(mock_round_data(&self.env, *r));
        }
        MockCacheClient::new(&self.env, &self.mock).inject(&self.data_id(), &v);
    }

    pub(crate) fn fail_cache(&self) {
        MockCacheClient::new(&self.env, &self.mock).set_err(&CacheError::MalformedReport);
    }

    pub(crate) fn data_id(&self) -> DataId {
        mock_feed_id(&self.env, DUMMY_MOCK_FEED_ID)
    }

    pub(crate) fn client(&self) -> DataFeedsProxyClient<'static> {
        DataFeedsProxyClient::new(&self.env, &self.id)
    }

    pub(crate) fn latest_round(&self) -> Round {
        self.client().latest_round(&self.data_id())
    }
}
