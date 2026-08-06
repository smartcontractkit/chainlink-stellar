use super::harness::*;

mod constructor {
    use super::*;

    #[test]
    fn sets_owner() {
        let cache = Cache::deploy();
        assert_eq!(cache.client().get_owner().unwrap(), cache.owner);
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
        let cache = [
            CacheError::MalformedReport,
            CacheError::UnauthorizedCaller,
            CacheError::EmptyConfig,
            CacheError::InvalidAddress,
            CacheError::InvalidWorkflowName,
            CacheError::DuplicatePermission,
            CacheError::InvalidDataId,
            CacheError::DuplicateFeedConfig,
            CacheError::FeedFrozen,
            CacheError::NoFeedState,
            CacheError::DecimalsMismatch,
        ];
        for e in cache {
            let expected = match e {
                CacheError::MalformedReport => 100,
                CacheError::UnauthorizedCaller => 101,
                CacheError::EmptyConfig => 103,
                CacheError::InvalidAddress => 104,
                CacheError::InvalidWorkflowName => 105,
                CacheError::DuplicatePermission => 106,
                CacheError::InvalidDataId => 107,
                CacheError::DuplicateFeedConfig => 108,
                CacheError::FeedFrozen => 109,
                CacheError::NoFeedState => 110,
                CacheError::DecimalsMismatch => 111,
            };
            let code = e as u32;
            assert_eq!(code, expected, "discriminant matches its wire value");
            assert!(
                (100..=199).contains(&code),
                "cache error {code} within [100, 199]"
            );
            assert!(
                !ownable.contains(&code),
                "cache error {code} disjoint from ownable"
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
        let cache = Cache::deploy();
        assert_eq!(cache.client().version(), 1);
        assert_eq!(
            cache.client().type_and_version(),
            String::from_str(&cache.env, "DataFeedsCache 1.0.0")
        );
    }

    #[test]
    fn two_step_ownership_is_wired() {
        let cache = Cache::deploy();
        let c = cache.client();
        let new_owner = new_address(&cache.env);
        let offer_live_until_ledger = 1_000u32;
        c.transfer_ownership(&new_owner, &offer_live_until_ledger);
        c.accept_ownership();
        assert_eq!(c.get_owner().unwrap(), new_owner);
    }

    #[test]
    fn recover_tokens_is_wired() {
        let cache = Cache::deploy();
        let admin = new_address(&cache.env);
        let token = cache
            .env
            .register_stellar_asset_contract_v2(admin)
            .address();
        let dest = new_address(&cache.env);
        let amount = 1_000i128;
        StellarAssetClient::new(&cache.env, &token).mint(&cache.id, &amount);
        cache.client().recover_tokens(&token, &dest, &amount);
        assert_eq!(TokenClient::new(&cache.env, &token).balance(&dest), amount);
    }

    #[test]
    fn upgrade_is_wired() {
        const TARGET_WASM: &[u8] = include_bytes!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../data-feeds-common/test_fixtures/upgrade_target.wasm"
        ));
        let cache = Cache::deploy();
        let hash = cache.env.deployer().upload_contract_wasm(TARGET_WASM);
        cache.client().upgrade(&hash);
        assert_eq!(peek(&cache.env, &cache.id), 0);
    }

    const SELF_WASM: &[u8] = include_bytes!(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/test_fixtures/cache_self_upgrade.wasm"
    ));

    fn assert_same_round(after: &RoundData, before: &RoundData, ctx: &str) {
        assert_eq!(after.round_id, before.round_id, "{ctx}: round_id diverged");
        assert_eq!(after.answer, before.answer, "{ctx}: answer diverged");
        assert_eq!(
            after.timestamp, before.timestamp,
            "{ctx}: timestamp diverged"
        );
        assert_eq!(
            after.ledger_seq, before.ledger_seq,
            "{ctx}: ledger_seq diverged"
        );
        assert_eq!(after.primary, before.primary, "{ctx}: primary diverged");
    }

    #[test]
    fn all_persistent_and_temporary_feed_state_survives_upgrade() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);

        cache.seed(&feed, 3);

        let big_answer = I256::from_i128(&cache.env, 123_456_789);
        let big_ts: u64 = (1u64 << 33) + 7;
        roll(&cache.env, 200);
        cache.write(&feed, 123_456_789, big_ts);

        let history_before = cache.history(&feed);
        let latest_before = cache.latest_round(&feed);
        let decimals_before = cache.client().decimals(&feed.id);
        let description_before = cache.client().description(&feed.id);
        let perms_before = cache.client().get_feed_permissions(&feed.id);

        assert_eq!(
            history_before.len(),
            4,
            "3 seeded + 1 big round must be present pre-upgrade"
        );
        assert_eq!(latest_before.answer, big_answer);
        assert_eq!(latest_before.timestamp, big_ts);
        assert_eq!(decimals_before, Some(18));
        assert_eq!(perms_before.len(), 1);
        assert!(cache.client().is_feed_admin(&feed.admin));

        let hash = cache.env.deployer().upload_contract_wasm(SELF_WASM);
        cache.client().upgrade(&hash);

        assert_eq!(
            cache.client().version(),
            1,
            "cache still serves version() after the upgrade"
        );

        assert_eq!(
            cache.history(&feed).len(),
            history_before.len(),
            "round count changed across upgrade"
        );
        for before in history_before.iter() {
            let after = cache
                .round(&feed, before.round_id)
                .expect("round lost across upgrade");
            assert_same_round(&after, &before, "history round");
        }
        assert_same_round(&cache.latest_round(&feed), &latest_before, "latest_round");

        let r2_before = history_before.iter().find(|r| r.round_id == 2).unwrap();
        assert_same_round(&cache.round(&feed, 2).unwrap(), &r2_before, "get_round(2)");

        assert_eq!(
            cache.client().decimals(&feed.id),
            decimals_before,
            "decimals lost across upgrade"
        );
        assert_eq!(
            cache.client().description(&feed.id),
            description_before,
            "description lost across upgrade"
        );
        assert_eq!(
            cache.client().get_feed_permissions(&feed.id),
            perms_before,
            "permissions lost across upgrade"
        );

        assert!(
            cache.client().is_feed_admin(&feed.admin),
            "feed admin authorization lost across upgrade"
        );
    }
}
