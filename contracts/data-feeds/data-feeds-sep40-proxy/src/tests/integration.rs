//! The adapter against the real `DataFeedsProxy` and `DataFeedsCache`, with no mocks in the path.

use super::harness::*;

use data_feeds_cache::test_utils::{permit_feed, seed_round};
use data_feeds_cache::{DataFeedsCache, DataFeedsCacheClient};
use data_feeds_proxy::{DataFeedsProxy, DataFeedsProxyClient};
use soroban_sdk::{testutils::Address as _, vec};

struct Stack {
    env: Env,
    cache: Address,
    proxy: Address,
    adapter: Address,
    sender: Address,
    feed: DataId,
}

impl Stack {
    /// Cache, proxy and adapter wired together, with one feed that has a single round.
    fn deploy(answer: i128, timestamp: u64) -> Self {
        let env = Env::default();
        env.mock_all_auths();
        let owner = new_address(&env);
        let feed = mock_feed_id(&env, 1);

        let cache = env.register(DataFeedsCache, (owner.clone(),));
        let sender = permit_feed(&env, &cache, &feed);
        seed_round(&env, &cache, &feed, &sender, answer, timestamp);

        let proxy = env.register(DataFeedsProxy, (owner.clone(), cache.clone()));
        let adapter = env.register(
            DataFeedsSep40Proxy,
            (owner, proxy.clone(), Symbol::new(&env, "USD")),
        );
        Stack {
            env,
            cache,
            proxy,
            adapter,
            sender,
            feed,
        }
    }

    fn adapter(&self) -> DataFeedsSep40ProxyClient<'static> {
        DataFeedsSep40ProxyClient::new(&self.env, &self.adapter)
    }

    fn sep40(&self) -> PriceFeedClient<'static> {
        PriceFeedClient::new(&self.env, &self.adapter)
    }

    fn proxy(&self) -> DataFeedsProxyClient<'static> {
        DataFeedsProxyClient::new(&self.env, &self.proxy)
    }

    fn cache(&self) -> DataFeedsCacheClient<'static> {
        DataFeedsCacheClient::new(&self.env, &self.cache)
    }

    fn btc(&self) -> Asset {
        Asset::Other(Symbol::new(&self.env, "BTC"))
    }

    fn map_btc(&self) -> Asset {
        let btc = self.btc();
        self.adapter().add_feed(&btc, &self.feed);
        btc
    }

    /// Registers a fresh feed admin on the cache and returns it.
    fn cache_feed_admin(&self) -> Address {
        let admin = Address::generate(&self.env);
        self.cache().add_feed_admin(&admin);
        admin
    }
}

#[test]
fn reads_latest_price_and_follows_new_reports() {
    let s = Stack::deploy(67_000_000_000_000_000_000_000, 1_700_000_000);
    let btc = s.map_btc();

    let round = s.proxy().latest_round(&s.feed, &DECIMALS);
    assert_eq!(
        s.sep40().lastprice(&btc),
        Some(PriceData {
            price: round.answer.to_i128().unwrap(),
            timestamp: round.timestamp,
        })
    );
    assert_eq!(s.sep40().lastprice(&btc).unwrap().timestamp, 1_700_000_000);
    for (index, price) in [250, 0, -250, i128::MIN].into_iter().enumerate() {
        let timestamp = 1_700_000_010 + index as u64;
        seed_round(&s.env, &s.cache, &s.feed, &s.sender, price, timestamp);
        assert_eq!(
            s.sep40().lastprice(&btc),
            Some(PriceData { price, timestamp }),
        );
    }
}

#[test]
fn freezing_is_isolated_and_recovers() {
    let s = Stack::deploy(100, 10);
    let btc = s.map_btc();

    let second = mock_feed_id(&s.env, 2);
    let second_sender = permit_feed(&s.env, &s.cache, &second);
    seed_round(&s.env, &s.cache, &second, &second_sender, 200, 20);
    let eth = Asset::Other(Symbol::new(&s.env, "ETH"));
    s.adapter().add_feed(&eth, &second);

    let admin = s.cache_feed_admin();
    s.cache()
        .set_feed_frozen(&admin, &vec![&s.env, s.feed.clone()], &true);

    assert_eq!(s.sep40().lastprice(&btc), None);
    assert_eq!(s.sep40().lastprice(&eth).unwrap().price, 200);

    s.cache()
        .set_feed_frozen(&admin, &vec![&s.env, s.feed.clone()], &false);
    assert_eq!(s.sep40().lastprice(&btc).unwrap().price, 100);
    assert_eq!(s.sep40().lastprice(&eth).unwrap().price, 200);
}

#[test]
fn reads_follow_cache_and_proxy_replacements() {
    let s = Stack::deploy(100, 10);
    let btc = s.map_btc();

    let owner = s.adapter().get_owner().unwrap();
    let cache_b = s.env.register(DataFeedsCache, (owner.clone(),));
    let sender_b = permit_feed(&s.env, &cache_b, &s.feed);
    seed_round(&s.env, &cache_b, &s.feed, &sender_b, 999, 90);
    s.proxy().set_cache(&cache_b);

    assert_eq!(
        s.sep40().lastprice(&btc),
        Some(PriceData {
            price: 999,
            timestamp: 90
        })
    );

    let cache_c = s.env.register(DataFeedsCache, (owner.clone(),));
    let sender_c = permit_feed(&s.env, &cache_c, &s.feed);
    seed_round(&s.env, &cache_c, &s.feed, &sender_c, 555, 100);
    let proxy_b = s.env.register(DataFeedsProxy, (owner, cache_c));

    s.adapter().set_proxy(&proxy_b);

    assert_eq!(
        s.sep40().lastprice(&btc),
        Some(PriceData {
            price: 555,
            timestamp: 100
        })
    );
}
