use super::harness::*;
use super::mock_proxy::{MalformedLatestRound, MissingLatestRound};

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
    fn decimals_and_resolution_are_fixed() {
        let a = Adapter::deploy();
        assert_eq!(a.sep40().decimals(), 18);
        assert_eq!(a.sep40().decimals(), DECIMALS);
        assert_eq!(a.sep40().resolution(), 1);
    }
}

mod assets {
    use super::*;

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
    fn history_methods_return_none() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);

        assert_eq!(a.sep40().price(&btc, &10), None);
        assert_eq!(a.sep40().price(&a.asset("ETH"), &10), None);
        assert_eq!(a.sep40().prices(&btc, &1), None);
        assert_eq!(a.sep40().prices(&btc, &0), None);
        assert_eq!(a.sep40().prices(&a.asset("ETH"), &5), None);
    }
}

fn assert_invalid_proxy_is_handled(a: &Adapter, proxy: &Address) {
    let btc = a.map("BTC", 1, 100, 10);
    a.client().set_proxy(proxy);

    assert_eq!(a.sep40().lastprice(&btc), None);

    assert_eq!(
        a.client().try_add_feed(&a.asset("ETH"), &a.feed(2)),
        Err(Ok(Sep40ProxyError::FeedUnreadable)),
    );
    assert_eq!(a.client().get_feed(&a.asset("ETH")), None);
    assert_eq!(a.sep40().assets().len(), 1);

    assert_eq!(
        a.client().try_update_feed(&btc, &a.feed(2)),
        Err(Ok(Sep40ProxyError::FeedUnreadable)),
    );
    assert_eq!(a.client().get_feed(&btc), Some(a.feed(1)));
    assert_eq!(a.sep40().assets().len(), 1);
}

#[test]
fn missing_proxy_method_is_handled() {
    let a = Adapter::deploy();
    let proxy = a.env.register(MissingLatestRound, ());

    assert_invalid_proxy_is_handled(&a, &proxy);
}

#[test]
fn malformed_proxy_response_is_handled() {
    let a = Adapter::deploy();
    let proxy = a.env.register(MalformedLatestRound, ());

    assert_invalid_proxy_is_handled(&a, &proxy);
}
