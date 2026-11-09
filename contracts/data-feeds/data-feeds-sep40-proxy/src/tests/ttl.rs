use super::harness::*;

fn assert_aged_then_restored(a: &Adapter, call: impl Fn(&Adapter)) {
    let full = network_max_ttl(&a.env);
    age_ttl(&a.env);
    assert!(
        a.instance_ttl() < full,
        "aging the ledger drops the instance TTL below max"
    );
    call(a);
    assert_eq!(
        a.instance_ttl(),
        full,
        "the call bumps the instance TTL back to max"
    );
}

#[test]
fn constructor_pins_instance_ttl_to_max() {
    let a = Adapter::deploy();
    assert_eq!(a.instance_ttl(), network_max_ttl(&a.env));
}

mod lastprice {
    use super::*;

    #[test]
    fn extends_instance_ttl_even_for_an_unmapped_asset() {
        let a = Adapter::deploy();
        let unmapped = a.asset("BTC");
        assert_aged_then_restored(&a, |a| {
            a.sep40().lastprice(&unmapped);
        });
    }

    #[test]
    fn extends_instance_and_persistent_entry_ttls() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        let keys = [
            DataKey::Assets,
            DataKey::AssetToFeed(btc.clone()),
            DataKey::FeedToAsset(a.feed(1)),
        ];
        let full = network_max_ttl(&a.env);

        age_ttl(&a.env);
        assert!(a.instance_ttl() < full);
        for key in &keys {
            assert!(a.persistent_ttl(key) < full, "{key:?} ages with the ledger");
        }
        a.sep40().lastprice(&btc);
        assert_eq!(a.instance_ttl(), full);
        for key in &keys {
            assert_eq!(a.persistent_ttl(key), full, "{key:?} is re-pinned to max");
        }
    }

    #[test]
    fn extends_even_when_the_proxy_read_fails() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        a.mock_client().set_frozen(&a.feed(1));
        let key = DataKey::AssetToFeed(btc.clone());
        let full = network_max_ttl(&a.env);

        age_ttl(&a.env);
        assert_eq!(a.sep40().lastprice(&btc), None);
        assert_eq!(a.persistent_ttl(&key), full);
    }
}

mod writes {
    use super::*;

    #[test]
    fn unchanged_feed_preserves_mapping_ttl_renewal() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        let keys = [
            DataKey::AssetToFeed(btc.clone()),
            DataKey::FeedToAsset(a.feed(1)),
        ];
        let full = network_max_ttl(&a.env);

        age_ttl(&a.env);
        for key in &keys {
            assert!(a.persistent_ttl(key) < full);
        }

        a.client().update_feed(&btc, &a.feed(1));

        for key in &keys {
            assert_eq!(a.persistent_ttl(key), full);
        }
    }

    #[test]
    fn new_mappings_are_pinned_to_max() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        let full = network_max_ttl(&a.env);

        assert_eq!(a.persistent_ttl(&DataKey::Assets), full);
        assert_eq!(a.persistent_ttl(&DataKey::AssetToFeed(btc)), full);
        assert_eq!(a.persistent_ttl(&DataKey::FeedToAsset(a.feed(1))), full);
    }

    #[test]
    fn add_feed_extends_instance_ttl() {
        let a = Adapter::deploy();
        a.set_price(1, 100, 10);
        assert_aged_then_restored(&a, |a| a.client().add_feed(&a.asset("BTC"), &a.feed(1)));
    }

    #[test]
    fn update_feed_extends_instance_ttl() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        a.set_price(2, 100, 10);
        assert_aged_then_restored(&a, |a| a.client().update_feed(&btc, &a.feed(2)));
    }

    #[test]
    fn remove_feed_extends_instance_ttl() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        assert_aged_then_restored(&a, |a| a.client().remove_feed(&btc));
    }

    #[test]
    fn set_proxy_extends_instance_ttl() {
        let a = Adapter::deploy();
        let proxy = new_address(&a.env);
        assert_aged_then_restored(&a, |a| a.client().set_proxy(&proxy));
    }

    #[test]
    fn getters_extend_instance_ttl() {
        let a = Adapter::deploy();
        assert_aged_then_restored(&a, |a| {
            a.client().get_proxy();
        });
        let b = Adapter::deploy();
        let asset = b.asset("BTC");
        assert_aged_then_restored(&b, |b| {
            b.client().get_feed(&asset);
        });
    }
}
