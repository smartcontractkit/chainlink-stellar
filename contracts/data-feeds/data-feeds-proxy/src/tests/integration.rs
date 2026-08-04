use super::harness::*;

use data_feeds_cache::test_utils::{permit_feed, seed_round, DEFAULT_DESC};
use data_feeds_cache::{DataFeedsCache, DataFeedsCacheClient};

const PROXY_SELF_WASM: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/test_fixtures/proxy_self_upgrade.wasm"
));

const CACHE_SELF_WASM: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../data-feeds-cache/test_fixtures/cache_self_upgrade.wasm"
));

struct ProxyWithCache {
    env: Env,
    owner: Address,
    cache: Address,
    sender: Address,
    proxy: Address,
    did: DataId,
}

impl ProxyWithCache {
    fn deploy(rounds: &[(i128, u64)]) -> Self {
        let env = Env::default();
        env.mock_all_auths();
        let owner = new_address(&env);
        let did = mock_feed_id(&env, DUMMY_MOCK_FEED_ID);
        let (cache, sender) = deploy_seeded_cache(&env, &owner, &did, rounds);
        let proxy = env.register(DataFeedsProxy, (owner.clone(), cache.clone()));
        ProxyWithCache {
            env,
            owner,
            cache,
            sender,
            proxy,
            did,
        }
    }

    fn client(&self) -> DataFeedsProxyClient<'static> {
        DataFeedsProxyClient::new(&self.env, &self.proxy)
    }

    fn deploy_secondary_cache(&self, rounds: &[(i128, u64)]) -> Address {
        deploy_seeded_cache(&self.env, &self.owner, &self.did, rounds).0
    }
}

fn deploy_seeded_cache(
    env: &Env,
    owner: &Address,
    did: &DataId,
    rounds: &[(i128, u64)],
) -> (Address, Address) {
    let cache = env.register(DataFeedsCache, (owner.clone(),));
    let sender = permit_feed(env, &cache, did);
    for (answer, ts) in rounds {
        seed_round(env, &cache, did, &sender, *answer, *ts);
    }
    (cache, sender)
}

mod cache_reader_client {
    use super::*;

    #[test]
    fn real_cache_reads_end_to_end() {
        let s = ProxyWithCache::deploy(&[(12345, 7)]);
        let c = s.client();

        let latest = c.latest_round(&s.did);
        assert_eq!(latest.answer, I256::from_i128(&s.env, 12345));
        assert_eq!(latest.timestamp, 7);
        assert_eq!(
            c.get_round(&s.did, &1).answer,
            I256::from_i128(&s.env, 12345),
            "historical round resolves through the proxy"
        );
        assert_eq!(c.decimals(&s.did), 18, "decimals derive from the feed id");
        assert_eq!(
            c.description(&s.did),
            String::from_str(&s.env, DEFAULT_DESC),
            "description comes from the real cache's feed config"
        );
    }

    #[test]
    fn every_read_on_an_unconfigured_feed_is_no_data_present() {
        let s = ProxyWithCache::deploy(&[(12345, 7)]);
        let unknown = mock_feed_id(&s.env, 99);
        let c = s.client();

        assert!(matches!(
            c.try_latest_round(&unknown),
            Err(Ok(ProxyReadError::NoDataPresent))
        ));
        assert!(matches!(
            c.try_get_round(&unknown, &1),
            Err(Ok(ProxyReadError::NoDataPresent))
        ));
        assert_eq!(
            c.try_decimals(&unknown),
            Err(Ok(ProxyReadError::NoDataPresent)),
            "a derivable id with no config is absence, not decimals 18"
        );
        assert_eq!(
            c.try_description(&unknown),
            Err(Ok(ProxyReadError::NoDataPresent))
        );
    }
}

mod lifecycle {
    use super::*;

    #[test]
    fn proxy_is_upgradable() {
        let s = ProxyWithCache::deploy(&[(100, 5)]);
        let id_before = s.proxy.clone();
        let c = s.client();
        assert_eq!(c.latest_round(&s.did).answer, I256::from_i128(&s.env, 100));

        let hash = s.env.deployer().upload_contract_wasm(PROXY_SELF_WASM);
        c.upgrade(&hash);

        assert_eq!(
            s.proxy, id_before,
            "proxy address is stable across the upgrade"
        );
        assert_eq!(
            c.latest_round(&s.did).answer,
            I256::from_i128(&s.env, 100),
            "reader interface and cache routing survive the upgrade"
        );
    }

    #[test]
    fn cache_is_upgradable() {
        let s = ProxyWithCache::deploy(&[(100, 5), (110, 6)]);
        let c = s.client();
        assert_eq!(c.latest_round(&s.did).answer, I256::from_i128(&s.env, 110));

        let cache_id_before = s.cache.clone();
        let hash = s.env.deployer().upload_contract_wasm(CACHE_SELF_WASM);
        DataFeedsCacheClient::new(&s.env, &s.cache).upgrade(&hash);

        assert_eq!(
            s.cache, cache_id_before,
            "cache address is stable across the upgrade"
        );
        assert_eq!(
            c.latest_round(&s.did).answer,
            I256::from_i128(&s.env, 110),
            "latest round survives the cache upgrade"
        );
        assert_eq!(
            c.get_round(&s.did, &1).answer,
            I256::from_i128(&s.env, 100),
            "historical round survives the cache upgrade"
        );

        seed_round(&s.env, &s.cache, &s.did, &s.sender, 250, 9);
        assert_eq!(
            c.latest_round(&s.did).answer,
            I256::from_i128(&s.env, 250),
            "the upgraded cache keeps operating end-to-end"
        );
    }

    #[test]
    fn cache_is_swappable() {
        let s = ProxyWithCache::deploy(&[(100, 5)]);
        let cache_b = s.deploy_secondary_cache(&[(999, 90)]);
        let id_before = s.proxy.clone();
        let c = s.client();
        let before = c.latest_round(&s.did);
        assert_eq!(before.answer, I256::from_i128(&s.env, 100));
        assert_eq!(before.timestamp, 5);

        c.set_cache(&cache_b);

        assert_eq!(
            s.proxy, id_before,
            "proxy address is stable across the swap"
        );
        let after = c.latest_round(&s.did);
        assert_eq!(
            after.answer,
            I256::from_i128(&s.env, 999),
            "reads now resolve from the swapped-in real cache"
        );
        assert_eq!(
            after.timestamp, 90,
            "the swapped-in real cache serves its own data behind the identical interface"
        );
    }

    #[test]
    fn get_round_history_does_not_span_caches_after_swap() {
        let s = ProxyWithCache::deploy(&[(100, 5), (110, 6)]);
        let cache2 = s.deploy_secondary_cache(&[(200, 5)]);
        let c = s.client();
        assert_eq!(c.get_round(&s.did, &2).answer, I256::from_i128(&s.env, 110));
        c.set_cache(&cache2);
        assert_eq!(
            c.get_round(&s.did, &1).answer,
            I256::from_i128(&s.env, 200),
            "round 1 now resolves from the new cache"
        );
        assert!(
            matches!(
                c.try_get_round(&s.did, &2),
                Err(Ok(ProxyReadError::NoDataPresent))
            ),
            "old cache's round 2 is unreachable — round history never spans caches"
        );
    }
}
