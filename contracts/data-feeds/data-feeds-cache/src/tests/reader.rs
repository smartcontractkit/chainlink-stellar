use super::harness::*;

fn shrunken_window(cache: &Cache) -> Feed {
    let feed = cache.add_feed(1);

    set_network_max_ttl(&cache.env, 10_000);
    roll(&cache.env, 1_000);
    cache.report_at(&feed, 10);
    roll(&cache.env, 1_010);
    cache.report_at(&feed, 20);

    set_network_max_ttl(&cache.env, 50);
    roll(&cache.env, 1_030);
    cache.report_at(&feed, 30);

    roll(&cache.env, 1_055);
    feed
}

fn grown_window(cache: &Cache) -> (Feed, u32) {
    let feed = cache.add_feed(1);

    set_network_max_ttl(&cache.env, 50);
    roll(&cache.env, 1_000);
    cache.report_at(&feed, 10);

    set_network_max_ttl(&cache.env, 10_000);
    roll(&cache.env, 1_100);
    cache.report_at(&feed, 20);
    roll(&cache.env, 1_200);
    cache.report_at(&feed, 30);

    (feed, 1_000 + 10_000 + 1)
}

mod latest_round {
    use super::*;

    #[test]
    fn absent_is_none() {
        let cache = Cache::deploy();
        let missing = mock_feed_id(&cache.env, 1);
        assert!(cache.client().latest_round(&missing).is_none());
    }

    #[test]
    fn returns_newest() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.seed(&feed, 3);
        let latest = cache.latest_round(&feed);
        assert_eq!(latest.round_id, 3);
        assert_eq!(latest.answer, I256::from_i128(&cache.env, 300));
        assert_eq!(latest.timestamp, 30);
    }

    #[test]
    fn returns_latest_after_round_expires() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.seed(&feed, 3);
        cache.expire_round(&feed, 3);
        assert_eq!(cache.latest_round(&feed).round_id, 3);
    }
}

mod get_round {
    use super::*;

    #[test]
    fn returns_by_id() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.seed(&feed, 3);
        assert_eq!(cache.round(&feed, 2).unwrap().round_id, 2);
        assert!(cache.round(&feed, 9).is_none());
    }

    #[test]
    fn returns_none_if_no_round_present() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        assert!(cache.round(&feed, 1).is_none());
    }

    #[test]
    fn explicit_id_reads_share_the_lookback_window() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        set_network_max_ttl(&cache.env, 10_000);
        roll(&cache.env, 1_000);
        cache.report_at(&feed, 10);
        set_network_max_ttl(&cache.env, 50);
        roll(&cache.env, 1_030);
        cache.report_at(&feed, 20);

        roll(&cache.env, 1_055);
        assert!(
            cache.round(&feed, 1).is_none(),
            "round 1 is alive until 11_000 but outside the 50-ledger window"
        );
        assert_eq!(
            cache.round(&feed, 2).unwrap().round_id,
            2,
            "in-window round served"
        );
    }

    #[test]
    fn tip_survives_expiry_non_tip_does_not() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.seed(&feed, 3);
        cache.expire_round(&feed, 1);
        cache.expire_round(&feed, 3);
        assert!(
            cache.round(&feed, 1).is_none(),
            "expired non-tip round is gone"
        );
        assert_eq!(
            cache.round(&feed, 3).unwrap().round_id,
            3,
            "tip survives in its own slot"
        );
    }
}

mod round_range {
    use super::*;

    #[test]
    fn empty_when_no_round_exists() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        assert_eq!(cache.history(&feed).len(), 0, "configured but unwritten");
        let unknown = mock_feed_id(&cache.env, 2);
        assert_eq!(cache.client().round_range(&unknown, &0, &u64::MAX).len(), 0);
    }

    #[test]
    fn full_history_oldest_first() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.seed(&feed, 5);
        let history = cache.history(&feed);
        assert_eq!(history.len(), 5);
        assert_eq!(history.get(0).unwrap().round_id, 1);
        assert_eq!(history.get(4).unwrap().round_id, 5);
    }

    #[test]
    fn bounded_range_inclusive() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.seed(&feed, 5);
        let range = cache.client().round_range(&feed.id, &2, &4);
        assert_eq!(range.len(), 3);
        assert_eq!(range.get(0).unwrap().round_id, 2);
        assert_eq!(range.get(2).unwrap().round_id, 4);
    }

    #[test]
    fn expired_rounds_drop_out_of_a_full_range() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.seed(&feed, 6);
        cache.expire_round(&feed, 1);
        cache.expire_round(&feed, 2);
        cache.expire_round(&feed, 3);
        let survivors = cache.history(&feed);
        assert_eq!(survivors.len(), 3);
        assert_eq!(survivors.get(0).unwrap().round_id, 4);
        assert_eq!(survivors.get(2).unwrap().round_id, 6);
    }

    #[test]
    fn window_shrinks_immediately_when_the_ttl_drops() {
        let cache = Cache::deploy();
        let feed = shrunken_window(&cache);
        let visible = cache.history(&feed);
        assert_eq!(visible.len(), 2, "round 1 masked by the shrunk window");
        assert_eq!(visible.get(0).unwrap().round_id, 2);
        assert_eq!(visible.get(1).unwrap().round_id, 3);
    }

    #[test]
    fn window_grows_at_grow_at_ledger() {
        let cache = Cache::deploy();
        let (feed, grow_at_ledger) = grown_window(&cache);
        roll(&cache.env, grow_at_ledger - 1);
        let before = cache.history(&feed);
        assert_eq!(
            before.len(),
            1,
            "only the head is in reach before the grow date"
        );
        assert_eq!(before.get(0).unwrap().round_id, 3);

        roll(&cache.env, grow_at_ledger);
        let after = cache.history(&feed);
        assert_eq!(
            after.len(),
            2,
            "round 2 re-enters the range once the window grows"
        );
        assert_eq!(after.get(0).unwrap().round_id, 2);
        assert_eq!(after.get(1).unwrap().round_id, 3);
    }
}

mod find_round {
    use super::*;

    #[test]
    fn empty_feed_is_none() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        assert!(cache.find(&feed, 100, Bound::AtOrBefore).is_none());
        assert!(cache.find(&feed, 100, Bound::AtOrAfter).is_none());
    }

    #[test]
    fn at_or_before_picks_newest() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.seed(&feed, 5);
        assert_eq!(
            cache.find(&feed, 35, Bound::AtOrBefore).unwrap().round_id,
            3
        );
        assert_eq!(
            cache.find(&feed, 999, Bound::AtOrBefore).unwrap().round_id,
            5
        );
        assert!(
            cache.find(&feed, 5, Bound::AtOrBefore).is_none(),
            "below oldest"
        );
    }

    #[test]
    fn at_or_after_picks_oldest() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.seed(&feed, 5);
        assert_eq!(cache.find(&feed, 35, Bound::AtOrAfter).unwrap().round_id, 4);
        assert_eq!(cache.find(&feed, 5, Bound::AtOrAfter).unwrap().round_id, 1);
        assert!(
            cache.find(&feed, 999, Bound::AtOrAfter).is_none(),
            "above newest"
        );
    }

    #[test]
    fn window_shrinks_immediately_when_the_ttl_drops() {
        let cache = Cache::deploy();
        let feed = shrunken_window(&cache);
        assert!(
            cache.find(&feed, 15, Bound::AtOrBefore).is_none(),
            "round 1 is outside the 50-ledger window even while still alive"
        );
        assert_eq!(cache.find(&feed, 25, Bound::AtOrAfter).unwrap().round_id, 3);

        let round_3_dies = 1_030 + 50;
        roll(&cache.env, round_3_dies + 1);
        assert_eq!(
            cache.find(&feed, 30, Bound::AtOrBefore).unwrap().round_id,
            3
        );
        assert!(
            cache.find(&feed, 15, Bound::AtOrBefore).is_none(),
            "a death never reopens the window"
        );
    }

    #[test]
    fn window_grows_at_grow_at_ledger() {
        let cache = Cache::deploy();
        let (feed, grow_at_ledger) = grown_window(&cache);
        roll(&cache.env, grow_at_ledger - 1);
        assert!(
            cache.find(&feed, 25, Bound::AtOrBefore).is_none(),
            "one ledger early: round 2 is still beyond the 50-ledger reach"
        );
        roll(&cache.env, grow_at_ledger);
        assert_eq!(
            cache.find(&feed, 25, Bound::AtOrBefore).unwrap().round_id,
            2,
            "the window is 10_000 wide from the grow date"
        );
    }
}

mod decimals {
    use super::*;

    #[test]
    fn derived_from_id_byte7_alone() {
        let cache = Cache::deploy();
        let c = cache.client();
        assert_eq!(
            c.decimals(&mock_feed_id(&cache.env, 123)),
            18,
            "stateless on unconfigured id"
        );
        assert_eq!(
            c.decimals(&mock_feed_id_with(&cache.env, 0x60, 0)),
            64,
            "top of the encodable range"
        );
        assert_eq!(
            c.decimals(&mock_feed_id_with(&cache.env, 0x1F, 0)),
            0,
            "one below the 0x20 floor falls back to 0"
        );
    }
}

mod description {
    use super::*;

    #[test]
    fn returns_stored_description() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        assert_eq!(
            cache.client().description(&feed.id),
            String::from_str(&cache.env, "BTC/USD")
        );
    }

    #[test]
    fn unconfigured_feed_returns_empty() {
        let cache = Cache::deploy();
        let unknown = mock_feed_id(&cache.env, 99);
        assert_eq!(
            cache.client().description(&unknown),
            String::from_str(&cache.env, "")
        );
    }
}
