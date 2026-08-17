use super::harness::*;

fn assert_read_extends_ttl(read: impl Fn(&Proxy)) {
    let p = Proxy::deploy();
    p.inject(&[(1, 100, 5)]);
    p.set_latest((1, 100, 5));
    let full = network_max_ttl(&p.env);
    age_ttl(&p.env);
    assert!(
        p.instance_ttl() < full,
        "aging the ledger drops the instance TTL below max"
    );
    read(&p);
    assert_eq!(
        p.instance_ttl(),
        full,
        "the read bumps the instance TTL back to max"
    );
}

mod constructor {
    use super::*;

    #[test]
    fn constructor_stores_owner_and_routes_reads() {
        let p = Proxy::deploy();
        p.set_latest((1, 100, 5));
        assert_eq!(p.client().get_owner().unwrap(), p.owner);
        assert_eq!(p.latest_round().round_id, 1);
    }

    #[test]
    fn extends_instance_ttl() {
        let p = Proxy::deploy();
        assert_eq!(
            p.instance_ttl(),
            network_max_ttl(&p.env),
            "the constructor bumps the instance TTL to max"
        );
    }
}

mod latest_round {
    use super::*;

    #[test]
    fn latest_returns_newest() {
        let p = Proxy::deploy();
        p.set_latest((9, 900, 90));
        let r = p.latest_round();
        assert_eq!(r.round_id, 9);
        assert_eq!(r.answer, I256::from_i128(&p.env, 900));
        assert_eq!(r.timestamp, 90);
    }

    #[test]
    fn no_rounds_is_no_data_present() {
        let p = Proxy::deploy();
        assert!(matches!(
            p.client().try_latest_round(&p.data_id()),
            Err(Ok(ProxyReadError::NoDataPresent))
        ));
    }

    #[test]
    fn extends_instance_ttl() {
        assert_read_extends_ttl(|p| {
            p.latest_round();
        });
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #100)")]
    fn cache_error_traps_the_read() {
        let p = Proxy::deploy();
        p.set_latest((1, 1, 10));
        p.fail_cache();
        p.latest_round();
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #109)")]
    fn latest_round_rejects_a_frozen_feed() {
        let p = Proxy::deploy();
        p.freeze();
        p.latest_round();
    }
}

mod get_round {
    use super::*;

    #[test]
    fn exact_round_projected() {
        let p = Proxy::deploy();
        p.inject(&[(6, 600, 60), (5, 500, 50)]);
        let r = p.client().get_round(&p.data_id(), &5);
        assert_eq!(r.round_id, 5, "exact id 5, not the newer round 6");
        assert_eq!(r.answer, I256::from_i128(&p.env, 500));
        assert_eq!(r.timestamp, 50);
    }

    #[test]
    fn absent_round_is_no_data_present() {
        let p = Proxy::deploy();
        p.inject(&[(5, 5, 50)]);
        assert!(matches!(
            p.client().try_get_round(&p.data_id(), &9u64),
            Err(Ok(ProxyReadError::NoDataPresent))
        ));
    }

    #[test]
    fn extends_instance_ttl() {
        assert_read_extends_ttl(|p| {
            p.client().get_round(&p.data_id(), &1u64);
        });
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #109)")]
    fn get_round_rejects_a_frozen_feed() {
        let p = Proxy::deploy();
        p.inject(&[(3, 300, 30)]);
        p.freeze();

        p.client().get_round(&p.data_id(), &3);
    }
}

mod decimals {
    use super::*;

    #[test]
    fn decimals_passes_through() {
        let p = Proxy::deploy();
        let c = p.client();
        let id_with_decimals = |dec: u8| mock_feed_id_with(&p.env, 0x20 + dec, 0);
        assert_eq!(c.decimals(&id_with_decimals(1)), 1);
        assert_eq!(
            c.decimals(&id_with_decimals(3)),
            3,
            "distinct ids yield distinct values, so the answer comes from the cache"
        );
    }

    #[test]
    fn extends_instance_ttl() {
        assert_read_extends_ttl(|p| {
            p.client().decimals(&p.data_id());
        });
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #109)")]
    fn decimals_rejects_a_frozen_feed() {
        let p = Proxy::deploy();
        p.freeze();
        p.client().decimals(&p.data_id());
    }
}

mod description {
    use super::*;

    #[test]
    fn description_passes_through() {
        let p = Proxy::deploy();
        assert_eq!(
            p.client().description(&p.data_id()),
            String::from_str(&p.env, "MOCK")
        );
    }

    #[test]
    fn extends_instance_ttl() {
        assert_read_extends_ttl(|p| {
            p.client().description(&p.data_id());
        });
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #109)")]
    fn description_rejects_a_frozen_feed() {
        let p = Proxy::deploy();
        p.freeze();
        p.client().description(&p.data_id());
    }
}

mod set_cache {
    use super::*;

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn set_cache_by_non_owner_host_fails() {
        let p = Proxy::deploy_no_auth();
        let attacker = new_address(&p.env);
        authorize(&p.env, &p.id, &attacker, "set_cache", (p.mock.clone(),));
        p.client().set_cache(&p.mock);
    }

    #[test]
    fn set_cache_swaps_routing_and_emits() {
        let p = Proxy::deploy();
        p.set_latest((1, 111, 5));
        let mock2 = p.env.register(MockCache, ());
        MockCacheClient::new(&p.env, &mock2)
            .set_latest(&p.data_id(), &mock_round_data(&p.env, (7, 222, 9)));
        assert_eq!(p.latest_round().round_id, 1);
        p.client().set_cache(&mock2);
        p.assert_event(CacheSet {
            old_cache: p.mock.clone(),
            new_cache: mock2.clone(),
        });
        assert_eq!(
            p.latest_round().round_id,
            7,
            "reads now hit the swapped cache"
        );
    }

    #[test]
    fn extends_instance_ttl() {
        let p = Proxy::deploy();
        let full = network_max_ttl(&p.env);
        age_ttl(&p.env);
        assert!(
            p.instance_ttl() < full,
            "aging the ledger drops the instance TTL below max"
        );
        p.client().set_cache(&p.mock);
        assert_eq!(
            p.instance_ttl(),
            full,
            "set_cache bumps the instance TTL back to max"
        );
    }
}

mod invariants {
    use super::*;

    #[test]
    fn error_discriminants_are_range_disjoint() {
        let ownable = [
            stellar_access::ownable::OwnableError::OwnerNotSet as u32,
            stellar_access::ownable::OwnableError::TransferInProgress as u32,
            stellar_access::ownable::OwnableError::OwnerAlreadySet as u32,
        ];
        let proxy = [ProxyReadError::NoDataPresent];
        for e in proxy {
            let expected = match e {
                ProxyReadError::NoDataPresent => 50,
            };
            let code = e as u32;
            assert_eq!(code, expected, "discriminant matches its wire value");
            assert!(
                (50..=99).contains(&code),
                "proxy error {code} within [50, 99]"
            );
            assert!(
                !ownable.contains(&code),
                "proxy error {code} disjoint from ownable"
            );
        }
        for o in ownable {
            assert!(
                (2100..=2199).contains(&o),
                "ownable error {o} within [2100, 2199]"
            );
        }
    }
}

mod lifecycle {
    use super::*;

    #[test]
    fn version_is_wired() {
        let p = Proxy::deploy();
        assert_eq!(p.client().version(), 1);
        assert_eq!(
            p.client().type_and_version(),
            String::from_str(&p.env, "DataFeedsProxy 1.0.0")
        );
    }

    #[test]
    fn two_step_ownership_is_wired() {
        let p = Proxy::deploy();
        let c = p.client();
        let new_owner = new_address(&p.env);
        let offer_live_until_ledger = 1_000u32;
        c.transfer_ownership(&new_owner, &offer_live_until_ledger);
        c.accept_ownership();
        assert_eq!(c.get_owner().unwrap(), new_owner);
    }

    #[test]
    fn recover_tokens_is_wired() {
        let p = Proxy::deploy();
        let admin = new_address(&p.env);
        let token = p.env.register_stellar_asset_contract_v2(admin).address();
        let dest = new_address(&p.env);
        let amount = 1_000i128;
        StellarAssetClient::new(&p.env, &token).mint(&p.id, &amount);
        p.client().recover_tokens(&token, &dest, &amount);
        assert_eq!(TokenClient::new(&p.env, &token).balance(&dest), amount);
    }

    #[test]
    fn upgrade_is_wired() {
        const TARGET_WASM: &[u8] = include_bytes!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../data-feeds-common/test_fixtures/upgrade_target.wasm"
        ));
        let p = Proxy::deploy();
        let hash = p.env.deployer().upload_contract_wasm(TARGET_WASM);
        p.client().upgrade(&hash);
        assert_eq!(peek(&p.env, &p.id), 0);
    }
}
