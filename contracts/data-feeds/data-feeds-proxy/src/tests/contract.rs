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

fn assert_read_repins_min_decimals_ttl(read: impl Fn(&Proxy)) {
    let p = Proxy::deploy();
    p.inject(&[(1, 1_000_000_000_000_000_000, 5)]);
    p.set_latest((1, 1_000_000_000_000_000_000, 5));
    p.client().set_min_decimals(&p.data_id(), &8);
    let key = DataKey::MinDecimals(p.data_id());
    let full = network_max_ttl(&p.env);
    let ttl = || {
        p.env
            .as_contract(&p.id, || p.env.storage().persistent().get_ttl(&key))
    };
    age_ttl(&p.env);
    assert!(
        ttl() < full,
        "aging the ledger drops the entry TTL below max"
    );
    read(&p);
    assert_eq!(ttl(), full, "the call re-pins the entry TTL back to max");
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
            p.client().try_latest_round(&p.data_id(), &DECIMALS),
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
    fn extends_min_decimals_ttl() {
        assert_read_repins_min_decimals_ttl(|p| {
            p.latest_round_at(8);
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
        let r = p.client().get_round(&p.data_id(), &5, &DECIMALS);
        assert_eq!(r.round_id, 5, "exact id 5, not the newer round 6");
        assert_eq!(r.answer, I256::from_i128(&p.env, 500));
        assert_eq!(r.timestamp, 50);
    }

    #[test]
    fn absent_round_is_no_data_present() {
        let p = Proxy::deploy();
        p.inject(&[(5, 5, 50)]);
        assert!(matches!(
            p.client().try_get_round(&p.data_id(), &9u64, &DECIMALS),
            Err(Ok(ProxyReadError::NoDataPresent))
        ));
    }

    #[test]
    fn extends_instance_ttl() {
        assert_read_extends_ttl(|p| {
            p.client().get_round(&p.data_id(), &1u64, &DECIMALS);
        });
    }

    #[test]
    fn extends_min_decimals_ttl() {
        assert_read_repins_min_decimals_ttl(|p| {
            p.client().get_round(&p.data_id(), &1u64, &8);
        });
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #109)")]
    fn get_round_rejects_a_frozen_feed() {
        let p = Proxy::deploy();
        p.inject(&[(3, 300, 30)]);
        p.freeze();

        p.client().get_round(&p.data_id(), &3, &DECIMALS);
    }
}

mod decimals {
    use super::*;

    #[test]
    fn decimals_matches_the_cache_precision() {
        let p = Proxy::deploy();
        assert_eq!(p.client().decimals(&p.data_id()), DECIMALS);
    }

    #[test]
    fn extends_instance_ttl() {
        assert_read_extends_ttl(|p| {
            p.client().decimals(&p.data_id());
        });
    }

    #[test]
    fn extends_min_decimals_ttl() {
        assert_read_repins_min_decimals_ttl(|p| {
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
    fn extends_min_decimals_ttl() {
        assert_read_repins_min_decimals_ttl(|p| {
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

mod get_min_decimals {
    use super::*;

    #[test]
    fn returns_the_configured_min() {
        let p = Proxy::deploy();
        p.client().set_min_decimals(&p.data_id(), &8);
        assert_eq!(p.client().get_min_decimals(&p.data_id()), 8);
    }

    #[test]
    fn extends_instance_ttl() {
        assert_read_extends_ttl(|p| {
            p.client().get_min_decimals(&p.data_id());
        });
    }

    #[test]
    fn extends_min_decimals_ttl() {
        assert_read_repins_min_decimals_ttl(|p| {
            p.client().get_min_decimals(&p.data_id());
        });
    }
}

mod get_cache {
    use super::*;

    #[test]
    fn returns_the_configured_cache() {
        let p = Proxy::deploy();
        assert_eq!(p.client().get_cache(), p.mock);
    }

    #[test]
    fn extends_instance_ttl() {
        assert_read_extends_ttl(|p| {
            p.client().get_cache();
        });
    }
}

mod set_min_decimals {
    use super::*;

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn set_min_decimals_by_non_owner_host_fails() {
        let p = Proxy::deploy_no_auth();
        let attacker = new_address(&p.env);
        authorize(
            &p.env,
            &p.id,
            &attacker,
            "set_min_decimals",
            (p.data_id(), 8u32),
        );
        p.client().set_min_decimals(&p.data_id(), &8);
    }

    #[test]
    fn min_above_cache_precision_is_rejected() {
        let p = Proxy::deploy();
        assert!(matches!(
            p.client()
                .try_set_min_decimals(&p.data_id(), &(DECIMALS + 1)),
            Err(Ok(ProxyReadError::InvalidDecimals))
        ));
    }

    #[test]
    fn sets_entry_ttl_on_init() {
        let p = Proxy::deploy();
        p.client().set_min_decimals(&p.data_id(), &8);
        let ttl = p.env.as_contract(&p.id, || {
            p.env
                .storage()
                .persistent()
                .get_ttl(&DataKey::MinDecimals(p.data_id()))
        });
        assert_eq!(
            ttl,
            network_max_ttl(&p.env),
            "the first write pins the entry TTL to max"
        );
    }

    #[test]
    fn update_repins_entry_ttl() {
        assert_read_repins_min_decimals_ttl(|p| {
            p.client().set_min_decimals(&p.data_id(), &10);
        });
    }

    #[test]
    fn extends_instance_ttl() {
        assert_read_extends_ttl(|p| {
            p.client().set_min_decimals(&p.data_id(), &8);
        });
    }

    #[test]
    fn setting_min_decimals_emits() {
        let p = Proxy::deploy();
        p.client().set_min_decimals(&p.data_id(), &8);
        p.assert_event(MinDecimalsSet {
            data_id: p.data_id(),
            min: 8,
        });
    }

    #[test]
    fn restoring_cache_precision_relocks_the_feed() {
        let p = Proxy::deploy();
        p.set_latest((1, 1_000_000_000_000_000_000, 5));
        p.client().set_min_decimals(&p.data_id(), &8);
        assert_eq!(
            p.latest_round_at(8).answer,
            I256::from_i128(&p.env, 100_000_000)
        );
        p.client().set_min_decimals(&p.data_id(), &DECIMALS);
        assert!(matches!(
            p.client().try_latest_round(&p.data_id(), &8),
            Err(Ok(ProxyReadError::InvalidDecimals)),
        ));
    }
}

mod precision {
    use super::*;

    const ANSWER: i128 = 1_999_999_999_999_999_999;

    #[test]
    fn unset_min_locks_reads_to_full_precision() {
        let p = Proxy::deploy();
        p.set_latest((1, ANSWER, 5));
        assert_eq!(p.client().get_min_decimals(&p.data_id()), DECIMALS);
        assert!(matches!(
            p.client().try_latest_round(&p.data_id(), &(DECIMALS - 1)),
            Err(Ok(ProxyReadError::InvalidDecimals))
        ));
        assert!(matches!(
            p.client().try_latest_round(&p.data_id(), &(DECIMALS + 1)),
            Err(Ok(ProxyReadError::InvalidDecimals))
        ));
        assert_eq!(p.latest_round().answer, I256::from_i128(&p.env, ANSWER));
    }

    #[test]
    fn reads_scale_down_to_the_requested_precision() {
        let p = Proxy::deploy();
        p.set_latest((1, ANSWER, 5));
        p.client().set_min_decimals(&p.data_id(), &8);
        assert_eq!(
            p.latest_round_at(8).answer,
            I256::from_i128(&p.env, 199_999_999),
            "truncates, never rounds up"
        );
        assert_eq!(
            p.latest_round_at(14).answer,
            I256::from_i128(&p.env, 199_999_999_999_999)
        );
        assert_eq!(p.latest_round().answer, I256::from_i128(&p.env, ANSWER));
    }

    #[test]
    fn below_the_configured_min_is_rejected() {
        let p = Proxy::deploy();
        p.set_latest((1, ANSWER, 5));
        p.client().set_min_decimals(&p.data_id(), &8);
        assert!(matches!(
            p.client().try_latest_round(&p.data_id(), &7),
            Err(Ok(ProxyReadError::InvalidDecimals))
        ));
        assert!(matches!(
            p.client().try_get_round(&p.data_id(), &1u64, &7),
            Err(Ok(ProxyReadError::InvalidDecimals))
        ));
    }

    #[test]
    fn above_cache_precision_is_rejected() {
        let p = Proxy::deploy();
        p.set_latest((1, ANSWER, 5));
        p.client().set_min_decimals(&p.data_id(), &8);
        assert!(matches!(
            p.client().try_latest_round(&p.data_id(), &(DECIMALS + 1)),
            Err(Ok(ProxyReadError::InvalidDecimals))
        ));
        assert!(matches!(
            p.client()
                .try_get_round(&p.data_id(), &1u64, &(DECIMALS + 1)),
            Err(Ok(ProxyReadError::InvalidDecimals))
        ));
    }

    #[test]
    fn zero_decimals_truncates_to_the_integer_part() {
        let p = Proxy::deploy();
        p.set_latest((1, ANSWER, 5));
        p.client().set_min_decimals(&p.data_id(), &0);
        assert_eq!(
            p.latest_round_at(0).answer,
            I256::from_i128(&p.env, 1),
            "1.999... at full precision reads as 1 at zero decimals"
        );
    }

    #[test]
    fn negative_answers_truncate_toward_zero() {
        let p = Proxy::deploy();
        p.set_latest((1, -ANSWER, 5));
        p.client().set_min_decimals(&p.data_id(), &8);
        assert_eq!(
            p.latest_round_at(8).answer,
            I256::from_i128(&p.env, -199_999_999)
        );
    }

    #[test]
    fn non_zero_answer_scaling_to_zero_fails() {
        let p = Proxy::deploy();
        p.set_latest((1, 5, 5));
        p.client().set_min_decimals(&p.data_id(), &0);
        assert!(matches!(
            p.client().try_latest_round(&p.data_id(), &0),
            Err(Ok(ProxyReadError::RoundsToZero))
        ));
    }

    #[test]
    fn negative_answer_scaling_to_zero_fails() {
        let p = Proxy::deploy();
        p.set_latest((1, -5, 5));
        p.client().set_min_decimals(&p.data_id(), &0);
        assert!(matches!(
            p.client().try_latest_round(&p.data_id(), &0),
            Err(Ok(ProxyReadError::RoundsToZero))
        ));
    }

    #[test]
    fn a_genuine_zero_answer_passes_at_any_precision() {
        let p = Proxy::deploy();
        p.set_latest((1, 0, 5));
        p.client().set_min_decimals(&p.data_id(), &0);
        assert_eq!(p.latest_round_at(0).answer, I256::from_i128(&p.env, 0));
    }

    #[test]
    fn get_round_scales_like_latest_round() {
        let p = Proxy::deploy();
        p.inject(&[(1, ANSWER, 5)]);
        p.client().set_min_decimals(&p.data_id(), &8);
        assert_eq!(
            p.client().get_round(&p.data_id(), &1, &8).answer,
            I256::from_i128(&p.env, 199_999_999)
        );
    }

    #[test]
    fn configured_min_without_data_is_no_data_present() {
        let p = Proxy::deploy();
        p.client().set_min_decimals(&p.data_id(), &8);
        assert!(
            matches!(
                p.client().try_latest_round(&p.data_id(), &8),
                Err(Ok(ProxyReadError::NoDataPresent))
            ),
            "passing precision validation does not invent data"
        );
        assert!(matches!(
            p.client().try_get_round(&p.data_id(), &1u64, &8),
            Err(Ok(ProxyReadError::NoDataPresent))
        ));
    }

    #[test]
    fn reads_do_not_create_a_min_decimals_entry() {
        let p = Proxy::deploy();
        p.set_latest((1, ANSWER, 5));
        p.latest_round();
        p.client().decimals(&p.data_id());
        p.client().description(&p.data_id());
        p.client().get_min_decimals(&p.data_id());
        let absent = p.env.as_contract(&p.id, || {
            !p.env
                .storage()
                .persistent()
                .has(&DataKey::MinDecimals(p.data_id()))
        });
        assert!(
            absent,
            "reads of an unconfigured feed leave storage untouched"
        );
    }

    #[test]
    fn invalid_precision_fails_before_the_cache_read() {
        let p = Proxy::deploy();
        p.set_latest((1, ANSWER, 5));
        p.freeze();
        assert!(
            matches!(
                p.client().try_latest_round(&p.data_id(), &(DECIMALS + 1)),
                Err(Ok(ProxyReadError::InvalidDecimals))
            ),
            "the range check precedes the frozen-feed cross-call"
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
        let proxy = [
            ProxyReadError::NoDataPresent,
            ProxyReadError::InvalidDecimals,
            ProxyReadError::RoundsToZero,
        ];
        for e in proxy {
            let expected = match e {
                ProxyReadError::NoDataPresent => 50,
                ProxyReadError::InvalidDecimals => 51,
                ProxyReadError::RoundsToZero => 52,
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
