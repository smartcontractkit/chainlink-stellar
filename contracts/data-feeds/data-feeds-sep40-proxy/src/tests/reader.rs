use super::harness::*;

use soroban_sdk::{testutils::Address as _, vec};

mod constants {
    use super::*;

    #[test]
    fn base_is_the_constructor_argument_wrapped_as_other() {
        for name in ["USD", "GBP", "EUR"] {
            let a = Adapter::deploy_with_base(name);
            assert_eq!(
                a.sep40().base(),
                Asset::Other(Symbol::new(&a.env, name)),
                "{name}"
            );
        }
    }

    #[test]
    fn base_does_not_change_what_the_other_methods_return() {
        let usd = Adapter::deploy_with_base("USD");
        let gbp = Adapter::deploy_with_base("GBP");
        for a in [&usd, &gbp] {
            a.map("BTC", 1, 100, 10);
        }

        assert_eq!(
            usd.sep40().lastprice(&usd.asset("BTC")),
            gbp.sep40().lastprice(&gbp.asset("BTC"))
        );
        assert_eq!(usd.sep40().decimals(), gbp.sep40().decimals());
        assert_eq!(usd.sep40().resolution(), gbp.sep40().resolution());
        assert_eq!(usd.sep40().assets().len(), gbp.sep40().assets().len());
    }

    #[test]
    fn decimals_is_eighteen() {
        let a = Adapter::deploy();
        assert_eq!(a.sep40().decimals(), 18);
        assert_eq!(a.sep40().decimals(), DECIMALS);
    }

    #[test]
    fn resolution_is_one() {
        let a = Adapter::deploy();
        assert_eq!(a.sep40().resolution(), 1);
    }
}

mod assets {
    use super::*;

    #[test]
    fn empty_before_any_feed_is_added() {
        let a = Adapter::deploy();
        assert_eq!(a.sep40().assets().len(), 0);
    }

    #[test]
    fn lists_assets_in_insertion_order() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 10, 1);
        let eth = a.map("ETH", 2, 10, 1);
        let stellar = Asset::Stellar(Address::generate(&a.env));
        a.set_price(3, 10, 1);
        a.client().add_feed(&stellar, &a.feed(3));

        assert_eq!(
            a.sep40().assets(),
            vec![&a.env, btc, eth, stellar],
            "assets() returns every mapped asset in the order it was added"
        );
    }
}

mod lastprice {
    use super::*;

    #[test]
    fn returns_price_and_timestamp_unchanged() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 67_000_000_000_000_000_000_000, 1_700_000_123);

        assert_eq!(
            a.sep40().lastprice(&btc),
            Some(PriceData {
                price: 67_000_000_000_000_000_000_000,
                timestamp: 1_700_000_123,
            }),
            "timestamp is the proxy's, not floored to the resolution"
        );
    }

    #[test]
    fn works_for_stellar_assets() {
        let a = Adapter::deploy();
        let token = Asset::Stellar(Address::generate(&a.env));
        a.set_price(1, 5, 9);
        a.client().add_feed(&token, &a.feed(1));

        assert_eq!(
            a.sep40().lastprice(&token),
            Some(PriceData {
                price: 5,
                timestamp: 9
            })
        );
    }

    #[test]
    fn reads_each_asset_from_its_own_feed() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        let eth = a.map("ETH", 2, 200, 20);

        assert_eq!(a.sep40().lastprice(&btc).unwrap().price, 100);
        assert_eq!(a.sep40().lastprice(&eth).unwrap().price, 200);
    }

    #[test]
    fn asks_the_proxy_for_eighteen_decimals() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        a.sep40().lastprice(&btc);

        assert_eq!(a.mock_client().last_decimals(), Some(18));
    }

    #[test]
    fn needs_no_auth() {
        let a = Adapter::deploy_no_auth();
        let btc = a.asset("BTC");
        a.map_unchecked(&btc, &a.feed(1));
        a.set_price(1, 100, 10);

        assert!(
            a.sep40().lastprice(&btc).is_some(),
            "no mock_all_auths is installed, so any require_auth would trap"
        );
    }

    #[test]
    fn follows_new_rounds() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        a.set_price(1, 150, 20);

        assert_eq!(
            a.sep40().lastprice(&btc),
            Some(PriceData {
                price: 150,
                timestamp: 20
            })
        );
    }
}

mod lastprice_returns_none {
    use super::*;

    #[test]
    fn for_an_unmapped_asset() {
        let a = Adapter::deploy();
        assert_eq!(a.sep40().lastprice(&a.asset("BTC")), None);
    }

    #[test]
    fn for_an_unmapped_asset_even_when_other_assets_are_mapped() {
        let a = Adapter::deploy();
        a.map("BTC", 1, 100, 10);
        assert_eq!(a.sep40().lastprice(&a.asset("ETH")), None);
    }

    #[test]
    fn when_the_proxy_has_no_data() {
        let a = Adapter::deploy();
        let btc = a.asset("BTC");
        a.map_unchecked(&btc, &a.feed(1));

        assert_eq!(a.sep40().lastprice(&btc), None);
    }

    #[test]
    fn for_every_proxy_read_error() {
        for error in [
            ProxyReadError::NoDataPresent,
            ProxyReadError::InvalidDecimals,
            ProxyReadError::RoundsToZero,
        ] {
            let a = Adapter::deploy();
            let btc = a.map("BTC", 1, 100, 10);
            a.mock_client().set_error(&a.feed(1), &error);

            assert_eq!(a.sep40().lastprice(&btc), None, "{error:?}");
        }
    }

    #[test]
    fn when_the_feed_is_frozen() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        a.mock_client().set_frozen(&a.feed(1));

        assert_eq!(
            a.sep40().lastprice(&btc),
            None,
            "the proxy panics with CacheError::FeedFrozen, which try_ must absorb"
        );
    }

    #[test]
    fn after_the_asset_is_removed() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);
        a.client().remove_feed(&btc);

        assert_eq!(a.sep40().lastprice(&btc), None);
    }

    #[test]
    fn for_non_positive_and_oversized_answers() {
        let a = Adapter::deploy();
        let btc = a.asset("BTC");
        a.map_unchecked(&btc, &a.feed(1));
        let i128_max = I256::from_i128(&a.env, i128::MAX);
        let one = I256::from_i128(&a.env, 1);

        let rejected = [
            ("zero", I256::from_i128(&a.env, 0)),
            ("minus one", I256::from_i128(&a.env, -1)),
            ("i128::MIN", I256::from_i128(&a.env, i128::MIN)),
            ("i128::MAX + 1", i128_max.add(&one)),
            (
                "below i128::MIN",
                I256::from_i128(&a.env, i128::MIN).sub(&one),
            ),
            (
                "i128::MAX * 2^64",
                i128_max.mul(&I256::from_i128(&a.env, 1 << 64)),
            ),
        ];
        for (label, answer) in rejected {
            a.set_answer(1, answer, 10);
            assert_eq!(a.sep40().lastprice(&btc), None, "{label}");
        }
    }
}

mod lastprice_accepts_boundary_answers {
    use super::*;

    #[test]
    fn one_and_i128_max() {
        let a = Adapter::deploy();
        let btc = a.asset("BTC");
        a.map_unchecked(&btc, &a.feed(1));

        for price in [1, i128::MAX] {
            a.set_price(1, price, 10);
            assert_eq!(
                a.sep40().lastprice(&btc),
                Some(PriceData {
                    price,
                    timestamp: 10
                })
            );
        }
    }
}

mod unsupported_in_v1 {
    use super::*;

    #[test]
    fn price_returns_none() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);

        assert_eq!(a.sep40().price(&btc, &10), None);
        assert_eq!(a.sep40().price(&a.asset("ETH"), &10), None);
    }

    #[test]
    fn prices_returns_none() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);

        assert_eq!(a.sep40().prices(&btc, &1), None);
        assert_eq!(a.sep40().prices(&btc, &0), None);
        assert_eq!(a.sep40().prices(&a.asset("ETH"), &5), None);
    }
}
