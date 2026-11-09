use super::harness::*;

use soroban_sdk::{symbol_short, testutils::Ledger as _, vec};

use data_feeds_common::test_utils::peek;

const UPGRADE_TARGET_WASM: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../data-feeds-common/test_fixtures/upgrade_target.wasm"
));

mod constructor {
    use super::*;

    #[test]
    fn sets_owner_proxy_and_base() {
        let a = Adapter::deploy();
        assert_eq!(a.client().get_owner(), Some(a.owner.clone()));
        assert_eq!(a.client().get_proxy(), a.mock);
        assert_eq!(a.sep40().base(), a.asset("USD"));
        assert_eq!(a.sep40().assets().len(), 0);
    }

    #[test]
    fn reports_type_and_version() {
        let a = Adapter::deploy();
        assert_eq!(a.client().version(), 1);
        assert_eq!(
            a.client().type_and_version(),
            soroban_sdk::String::from_str(&a.env, "DataFeedsSep40Proxy 1.0.0")
        );
    }
}

mod non_owner_is_rejected {
    use super::*;

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn add_feed() {
        let a = Adapter::deploy_no_auth();
        a.set_price(1, 100, 10);
        let (asset, id) = (a.asset("BTC"), a.feed(1));
        let attacker = new_address(&a.env);
        authorize(
            &a.env,
            &a.id,
            &attacker,
            "add_feed",
            (asset.clone(), id.clone()),
        );
        a.client().add_feed(&asset, &id);
    }

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn update_feed() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        a.set_price(2, 100, 10);
        let id = a.feed(2);
        let attacker = new_address(&a.env);
        authorize(
            &a.env,
            &a.id,
            &attacker,
            "update_feed",
            (btc.clone(), id.clone()),
        );
        a.client().update_feed(&btc, &id);
    }

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn remove_feed() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        let attacker = new_address(&a.env);
        authorize(&a.env, &a.id, &attacker, "remove_feed", (btc.clone(),));
        a.client().remove_feed(&btc);
    }

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn set_proxy() {
        let a = Adapter::deploy();
        let proxy = new_address(&a.env);
        let attacker = new_address(&a.env);
        authorize(&a.env, &a.id, &attacker, "set_proxy", (proxy.clone(),));
        a.client().set_proxy(&proxy);
    }

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn upgrade() {
        let a = Adapter::deploy();
        let hash = a.env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
        let attacker = new_address(&a.env);
        authorize(&a.env, &a.id, &attacker, "upgrade", (hash.clone(),));
        a.client().upgrade(&hash);
    }
}

mod add_feed {
    use super::*;

    #[test]
    fn stores_the_mapping_and_lists_the_asset() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);

        a.assert_latest_event(FeedAdded {
            asset: btc.clone(),
            data_id: a.feed(1),
        });
        assert_eq!(a.client().get_feed(&btc), Some(a.feed(1)));
        assert_eq!(a.sep40().assets(), vec![&a.env, btc]);
    }

    #[test]
    fn rejects_an_already_mapped_asset() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        a.set_price(2, 100, 10);

        assert_eq!(
            a.client().try_add_feed(&btc, &a.feed(2)),
            Err(Ok(Sep40ProxyError::AssetAlreadyMapped))
        );
        assert_eq!(
            a.client().get_feed(&btc),
            Some(a.feed(1)),
            "mapping untouched"
        );
        assert_eq!(a.sep40().assets().len(), 1, "no duplicate list entry");
    }

    #[test]
    fn rejects_a_feed_already_mapped_to_another_asset() {
        let a = Adapter::deploy();
        a.map("BTC", 1, 100, 10);

        assert_eq!(
            a.client().try_add_feed(&a.asset("WBTC"), &a.feed(1)),
            Err(Ok(Sep40ProxyError::DataIdInUse))
        );
        assert_eq!(a.sep40().assets().len(), 1);
    }

    #[test]
    fn rejects_the_zero_data_id() {
        let a = Adapter::deploy();
        let zero = BytesN::from_array(&a.env, &[0u8; 32]);

        assert_eq!(
            a.client().try_add_feed(&a.asset("BTC"), &zero),
            Err(Ok(Sep40ProxyError::InvalidDataId))
        );
    }

    #[test]
    fn rejects_a_feed_with_no_data() {
        let a = Adapter::deploy();
        assert_unreadable_and_unwritten(&a, |_| {});
    }

    #[test]
    fn rejects_a_frozen_feed() {
        let a = Adapter::deploy();
        assert_unreadable_and_unwritten(&a, |a| a.mock_client().set_frozen(&a.feed(1)));
    }

    #[test]
    fn rejects_a_feed_the_proxy_errors_on() {
        let a = Adapter::deploy();
        assert_unreadable_and_unwritten(&a, |a| {
            a.mock_client()
                .set_error(&a.feed(1), &ProxyReadError::RoundsToZero)
        });
    }

    #[test]
    fn rejects_a_feed_with_a_non_positive_answer() {
        let a = Adapter::deploy();
        assert_unreadable_and_unwritten(&a, |a| a.set_price(1, 0, 10));
        assert_unreadable_and_unwritten(&a, |a| a.set_price(1, -5, 10));
    }

    fn assert_unreadable_and_unwritten(a: &Adapter, setup: impl Fn(&Adapter)) {
        setup(a);
        let btc = a.asset("BTC");

        assert_eq!(
            a.client().try_add_feed(&btc, &a.feed(1)),
            Err(Ok(Sep40ProxyError::FeedUnreadable))
        );
        assert_eq!(a.client().get_feed(&btc), None);
        assert_eq!(a.sep40().assets().len(), 0);
        assert_eq!(
            a.client().try_add_feed(&a.asset("ETH"), &a.feed(1)),
            Err(Ok(Sep40ProxyError::FeedUnreadable)),
            "the feed id was not reserved by the failed attempt"
        );
    }
}

mod update_feed {
    use super::*;

    #[test]
    fn replaces_the_feed_and_keeps_the_asset_list() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        let eth = a.map("ETH", 2, 200, 20);
        a.set_price(3, 300, 30);

        a.client().update_feed(&btc, &a.feed(3));

        a.assert_latest_event(FeedUpdated {
            asset: btc.clone(),
            old_data_id: a.feed(1),
            new_data_id: a.feed(3),
        });
        assert_eq!(a.client().get_feed(&btc), Some(a.feed(3)));
        assert_eq!(a.sep40().lastprice(&btc).unwrap().price, 300);
        assert_eq!(a.sep40().assets(), vec![&a.env, btc, eth]);
    }

    #[test]
    fn frees_the_old_feed_for_another_asset() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        a.set_price(2, 200, 20);
        a.client().update_feed(&btc, &a.feed(2));

        a.client().add_feed(&a.asset("WBTC"), &a.feed(1));
        assert_eq!(a.client().get_feed(&a.asset("WBTC")), Some(a.feed(1)));
    }

    #[test]
    fn rejects_an_unmapped_asset() {
        let a = Adapter::deploy();
        a.set_price(1, 100, 10);

        assert_eq!(
            a.client().try_update_feed(&a.asset("BTC"), &a.feed(1)),
            Err(Ok(Sep40ProxyError::AssetNotMapped))
        );
    }

    #[test]
    fn rejects_a_feed_mapped_to_a_different_asset() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        a.map("ETH", 2, 200, 20);

        assert_eq!(
            a.client().try_update_feed(&btc, &a.feed(2)),
            Err(Ok(Sep40ProxyError::DataIdInUse))
        );
        assert_eq!(a.client().get_feed(&btc), Some(a.feed(1)));
    }

    #[test]
    fn unchanged_feed_still_emits_update_and_preserves_uniqueness() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);

        a.client().update_feed(&btc, &a.feed(1));

        a.assert_latest_event(FeedUpdated {
            asset: btc.clone(),
            old_data_id: a.feed(1),
            new_data_id: a.feed(1),
        });

        assert_eq!(a.client().get_feed(&btc), Some(a.feed(1)));
        assert_eq!(a.sep40().assets().len(), 1);
        assert_eq!(
            a.client().try_add_feed(&a.asset("WBTC"), &a.feed(1)),
            Err(Ok(Sep40ProxyError::DataIdInUse)),
        );
    }

    #[test]
    fn unchanged_feed_still_checks_readability() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);

        a.mock_client().set_frozen(&a.feed(1));

        assert_eq!(
            a.client().try_update_feed(&btc, &a.feed(1)),
            Err(Ok(Sep40ProxyError::FeedUnreadable)),
        );
        assert_eq!(a.client().get_feed(&btc), Some(a.feed(1)));
    }

    #[test]
    fn rejects_the_zero_data_id() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        let zero = BytesN::from_array(&a.env, &[0u8; 32]);

        assert_eq!(
            a.client().try_update_feed(&btc, &zero),
            Err(Ok(Sep40ProxyError::InvalidDataId))
        );
    }

    #[test]
    fn rejects_an_unreadable_feed_and_keeps_the_old_mapping() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);

        assert_eq!(
            a.client().try_update_feed(&btc, &a.feed(2)),
            Err(Ok(Sep40ProxyError::FeedUnreadable))
        );
        assert_eq!(a.client().get_feed(&btc), Some(a.feed(1)));
        assert_eq!(a.sep40().lastprice(&btc).unwrap().price, 100);
    }
}

mod remove_feed {
    use super::*;

    #[test]
    fn removes_a_middle_asset_and_preserves_the_order_of_the_rest() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        let eth = a.map("ETH", 2, 200, 20);
        let sol = a.map("SOL", 3, 300, 30);

        a.client().remove_feed(&eth);

        a.assert_latest_event(FeedRemoved {
            asset: eth.clone(),
            data_id: a.feed(2),
        });
        assert_eq!(a.sep40().assets(), vec![&a.env, btc, sol]);
        assert_eq!(a.client().get_feed(&eth), None);
        assert_eq!(a.sep40().lastprice(&eth), None);
    }

    #[test]
    fn frees_both_the_asset_and_the_feed() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        a.client().remove_feed(&btc);

        a.client().add_feed(&btc, &a.feed(1));
        assert_eq!(a.sep40().lastprice(&btc).unwrap().price, 100);
        assert_eq!(a.sep40().assets().len(), 1);
    }

    #[test]
    fn rejects_an_unmapped_asset() {
        let a = Adapter::deploy();
        assert_eq!(
            a.client().try_remove_feed(&a.asset("BTC")),
            Err(Ok(Sep40ProxyError::AssetNotMapped))
        );
    }

    #[test]
    fn leaves_other_mappings_readable() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        let eth = a.map("ETH", 2, 200, 20);
        a.client().remove_feed(&btc);

        assert_eq!(a.sep40().lastprice(&eth).unwrap().price, 200);
    }
}

mod set_proxy {
    use super::*;

    #[test]
    fn reads_follow_the_new_proxy() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        let second = a.env.register(MockProxy, ());
        MockProxyClient::new(&a.env, &second)
            .set_round(&a.feed(1), &a.round(I256::from_i128(&a.env, 999), 77));

        a.client().set_proxy(&second);

        a.assert_latest_event(ProxySet {
            old_proxy: a.mock.clone(),
            new_proxy: second.clone(),
        });
        assert_eq!(a.client().get_proxy(), second);
        assert_eq!(
            a.sep40().lastprice(&btc),
            Some(PriceData {
                price: 999,
                timestamp: 77
            })
        );
    }
}

mod ownership {
    use super::*;

    fn propose(a: &Adapter, new_owner: &Address) {
        let until = a.env.ledger().sequence() + 100;
        a.client().transfer_ownership(new_owner, &until);
    }

    #[test]
    fn two_step_transfer_hands_over_control() {
        let a = Adapter::deploy();
        let next = new_address(&a.env);

        propose(&a, &next);
        assert_eq!(a.client().get_owner(), Some(a.owner.clone()), "not yet");
        a.client().accept_ownership();
        assert_eq!(a.client().get_owner(), Some(next.clone()));

        a.set_price(1, 100, 10);
        authorize(
            &a.env,
            &a.id,
            &next,
            "add_feed",
            (a.asset("BTC"), a.feed(1)),
        );
        a.client().add_feed(&a.asset("BTC"), &a.feed(1));
    }

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn old_owner_loses_access_after_the_transfer() {
        let a = Adapter::deploy();
        let next = new_address(&a.env);
        propose(&a, &next);
        a.client().accept_ownership();

        a.set_price(1, 100, 10);
        authorize(
            &a.env,
            &a.id,
            &a.owner,
            "add_feed",
            (a.asset("BTC"), a.feed(1)),
        );
        a.client().add_feed(&a.asset("BTC"), &a.feed(1));
    }

    #[test]
    fn accept_without_a_pending_transfer_fails() {
        let a = Adapter::deploy();
        assert!(a.client().try_accept_ownership().is_err());
    }

    #[test]
    fn accept_after_the_window_closes_fails() {
        let a = Adapter::deploy();
        let next = new_address(&a.env);
        let until = a.env.ledger().sequence() + 5;
        a.client().transfer_ownership(&next, &until);
        a.env.ledger().with_mut(|li| li.sequence_number += 10);

        assert!(a.client().try_accept_ownership().is_err());
        assert_eq!(a.client().get_owner(), Some(a.owner.clone()));
    }

    #[test]
    fn a_zero_window_cancels_the_pending_transfer() {
        let a = Adapter::deploy();
        let next = new_address(&a.env);
        propose(&a, &next);
        a.client().transfer_ownership(&next, &0);

        assert!(a.client().try_accept_ownership().is_err());
    }
}

mod upgrade {
    use super::*;

    #[test]
    fn swaps_the_wasm_keeps_the_address_and_preserves_storage() {
        let a = Adapter::deploy_with_base("EUR");
        let btc = a.map("BTC", 1, 100, 10);
        a.env.as_contract(&a.id, || {
            a.env
                .storage()
                .instance()
                .set(&symbol_short!("slot"), &42u32);
        });
        let id_before = a.id.clone();
        let hash = a.env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);

        a.client().upgrade(&hash);

        assert_eq!(a.id, id_before);
        assert_eq!(
            peek(&a.env, &a.id),
            42,
            "the fixture's peek exists only in the new wasm and reads the slot the old code wrote"
        );
        a.env.as_contract(&a.id, || {
            assert_eq!(storage::feed_for(&a.env, &btc), Some(a.feed(1)));
            assert_eq!(storage::get_proxy(&a.env), a.mock);
            assert_eq!(storage::get_base_asset(&a.env), a.asset("EUR"));
        });
    }

    #[test]
    fn emits_upgraded() {
        let a = Adapter::deploy();
        let hash = a.env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
        a.client().upgrade(&hash);

        a.assert_event(data_feeds_common::Upgraded {
            new_wasm_hash: hash,
        });
    }
}
