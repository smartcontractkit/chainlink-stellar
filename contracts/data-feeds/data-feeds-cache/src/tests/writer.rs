use super::harness::*;

mod on_report {
    use super::*;

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn sender_without_auth_host_fails() {
        let cache = Cache::deploy_no_auth();
        let sender = new_address(&cache.env);
        cache.client().on_report(
            &sender,
            &mock_metadata(&cache.env),
            &report(&cache.env, &[]),
        );
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #100)")]
    fn metadata_63_bytes_is_malformed() {
        let cache = Cache::deploy();
        let sender = new_address(&cache.env);
        cache.client().on_report(
            &sender,
            &Bytes::from_array(&cache.env, &[0u8; 63]),
            &report(&cache.env, &[]),
        );
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #100)")]
    fn metadata_65_bytes_is_malformed() {
        let cache = Cache::deploy();
        let sender = new_address(&cache.env);
        cache.client().on_report(
            &sender,
            &Bytes::from_array(&cache.env, &[0u8; 65]),
            &report(&cache.env, &[]),
        );
    }

    #[test]
    #[should_panic(expected = "Error(Value, InvalidInput)")]
    fn report_with_trailing_bytes_traps_as_host_value_error() {
        let cache = Cache::deploy();
        let sender = new_address(&cache.env);
        let mut bytes = report(&cache.env, &[(mock_wire_id(&cache.env, 1, 0), 1, 1)]);
        bytes.push_back(0u8);
        cache
            .client()
            .on_report(&sender, &mock_metadata(&cache.env), &bytes);
    }

    #[test]
    #[should_panic(expected = "Error(Value, InvalidInput)")]
    fn undecodable_report_traps_as_host_value_error() {
        let cache = Cache::deploy();
        let sender = new_address(&cache.env);
        cache.client().on_report(
            &sender,
            &mock_metadata(&cache.env),
            &Bytes::from_array(&cache.env, &[0xFFu8; 7]),
        );
    }

    #[test]
    fn empty_entry_vec_is_a_valid_no_op() {
        let cache = Cache::deploy();
        let sender = new_address(&cache.env);
        cache.client().on_report(
            &sender,
            &mock_metadata(&cache.env),
            &report(&cache.env, &[]),
        );
        assert_eq!(cache.events().len(), 0);
    }

    #[test]
    fn wire_id_truncates_to_high_16_bytes() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(7);
        let env = &cache.env;
        let c = cache.client();
        c.on_report(
            &feed.sender,
            &mock_metadata(env),
            &report(env, &[(mock_wire_id(env, 7, 0xAA), 100, 5)]),
        );
        c.on_report(
            &feed.sender,
            &mock_metadata(env),
            &report(env, &[(mock_wire_id(env, 7, 0xBB), 200, 6)]),
        );
        assert_eq!(cache.history(&feed).len(), 2);
    }

    #[test]
    fn unconfigured_feed_soft_skips_with_event_and_writes_nothing() {
        let cache = Cache::deploy();
        let sender = new_address(&cache.env);
        let unknown = mock_wire_id(&cache.env, 9, 0);
        cache.client().on_report(
            &sender,
            &mock_metadata(&cache.env),
            &report(&cache.env, &[(unknown, 100, 5)]),
        );
        cache.assert_event(InvalidUpdatePermission {
            data_id: mock_feed_id(&cache.env, 9),
            sender,
            workflow_owner: mock_wf_owner(&cache.env),
            workflow_name: mock_wf_name(&cache.env),
        });
        assert_eq!(
            cache.events().len(),
            1,
            "only the skip event — no FeedUpdated alongside it"
        );
        assert_eq!(
            cache
                .client()
                .round_range(&mock_feed_id(&cache.env, 9), &0, &u64::MAX)
                .len(),
            0
        );
    }

    #[test]
    fn wrong_owner_or_name_or_sender_does_not_record() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let env = &cache.env;
        let c = cache.client();
        let wid = mock_wire_id(env, 1, 0);
        let bad_owner = metadata(
            env,
            &BytesN::from_array(env, &[0x99; 20]),
            &mock_wf_name(env),
        );
        c.on_report(
            &feed.sender,
            &bad_owner,
            &report(env, &[(wid.clone(), 1, 5)]),
        );
        let bad_name = metadata(
            env,
            &mock_wf_owner(env),
            &BytesN::from_array(env, &[0x99; 10]),
        );
        c.on_report(
            &feed.sender,
            &bad_name,
            &report(env, &[(wid.clone(), 1, 6)]),
        );
        let other = new_address(&cache.env);
        c.on_report(&other, &mock_metadata(env), &report(env, &[(wid, 1, 7)]));
        assert_eq!(cache.history(&feed).len(), 0);
    }

    #[test]
    fn revoked_sender_on_report_is_soft_skipped_after_reconfigure() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let env = &cache.env;
        let s1 = new_address(&cache.env);
        let s2 = new_address(&cache.env);
        let wid = mock_wire_id(env, 1, 0);
        let c = cache.client();

        let did = cache.configure_feed(&admin, &s1, 1, "old");
        c.on_report(
            &s1,
            &mock_metadata(env),
            &report(env, &[(wid.clone(), 100, 5)]),
        );
        assert_eq!(
            c.round_range(&did, &0, &u64::MAX).len(),
            1,
            "s1 round landed pre-revoke"
        );

        cache.configure_feed(&admin, &s2, 1, "new");

        c.on_report(
            &s1,
            &mock_metadata(env),
            &report(env, &[(wid.clone(), 200, 6)]),
        );
        cache.assert_event(InvalidUpdatePermission {
            data_id: did.clone(),
            sender: s1.clone(),
            workflow_owner: mock_wf_owner(env),
            workflow_name: mock_wf_name(env),
        });
        assert_eq!(
            c.round_range(&did, &0, &u64::MAX).len(),
            1,
            "revoked s1 wrote nothing — still exactly the one pre-revoke round"
        );

        c.on_report(&s2, &mock_metadata(env), &report(env, &[(wid, 200, 6)]));
        assert_eq!(
            c.round_range(&did, &0, &u64::MAX).len(),
            2,
            "s2 round landed post-reconfigure"
        );
    }

    #[test]
    fn revoked_sender_on_report_is_soft_skipped() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let env = &cache.env;
        cache.write(&feed, 100, 5);
        assert_eq!(
            cache.history(&feed).len(),
            1,
            "round landed while configured"
        );

        cache.configure_feed(&feed.admin, &new_address(env), feed.tag, "BTC/USD");

        cache.write(&feed, 200, 6);
        cache.assert_event(InvalidUpdatePermission {
            data_id: feed.id.clone(),
            sender: feed.sender.clone(),
            workflow_owner: mock_wf_owner(env),
            workflow_name: mock_wf_name(env),
        });
        assert_eq!(
            cache.history(&feed).len(),
            1,
            "the revoked sender wrote nothing — still just the earlier round"
        );
    }

    #[test]
    fn on_report_extends_all_in_scope_ttls() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.write(&feed, 100, 5);

        let full = network_max_ttl(&cache.env);
        age_ttl(&cache.env);

        cache.write(&feed, 200, 6);

        assert_eq!(
            cache.instance_ttl(),
            full,
            "on_report extends the contract instance TTL"
        );
        assert_eq!(
            cache.persistent_ttl(&DataKey::FeedConfig(cache.key_bytes(&feed.id))),
            full,
            "on_report extends the config TTL"
        );
        assert_eq!(
            cache.permission_ttl(&feed.id, &feed.sender),
            full,
            "on_report extends the permission TTL"
        );
        assert_eq!(
            cache.persistent_ttl(&DataKey::FeedState(cache.key_bytes(&feed.id))),
            full,
            "on_report extends the latest-tip TTL"
        );
    }

    #[test]
    fn on_report_refreshes_all_permissions() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let env = &cache.env;
        let s1 = new_address(&cache.env);
        let s2 = new_address(&cache.env);
        let id = mock_feed_id(env, 1);

        let cfg = FeedConfigEntry {
            data_id: id.clone(),
            config: FeedConfig {
                description: String::from_str(env, "BTC/USD"),
                workflow_permissions: vec![
                    env,
                    mock_permission(env, &s1),
                    mock_permission(env, &s2),
                ],
            },
        };
        cache.client().set_feed_configs(&admin, &vec![env, cfg]);

        let full = network_max_ttl(&cache.env);
        age_ttl(&cache.env);

        let wid = mock_wire_id(env, 1, 0);
        cache
            .client()
            .on_report(&s1, &mock_metadata(env), &report(env, &[(wid, 100, 5)]));

        assert_eq!(
            cache.permission_ttl(&id, &s1),
            full,
            "primary permission refreshed"
        );
        assert_eq!(
            cache.permission_ttl(&id, &s2),
            full,
            "backup permission must also be refreshed, else it evicts while the feed lives \
             and the honest backup reporter is silently de-authorized"
        );
    }

    #[test]
    fn equal_or_older_timestamp_is_stale_and_emits_event() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.write(&feed, 100, 5);
        cache.write(&feed, 999, 5);
        cache.assert_event(StaleReport {
            data_id: feed.id.clone(),
            report_ts: 5,
            stored_ts: 5,
        });
        cache.write(&feed, 999, 4);
        assert_eq!(cache.history(&feed).len(), 1);
    }

    #[test]
    fn absent_latest_means_stored_ts_zero() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.write(&feed, 7, 0);
        cache.assert_event(StaleReport {
            data_id: feed.id.clone(),
            report_ts: 0,
            stored_ts: 0,
        });
        cache.write(&feed, 7, 1);
        assert_eq!(cache.latest_round(&feed).round_id, 1);
    }

    #[test]
    fn in_batch_stale_entries_use_running_ts_and_do_not_block_later_accepts() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let env = &cache.env;
        let wid = mock_wire_id(env, 1, 0);
        cache.write(&feed, 10, 10);
        cache.client().on_report(
            &feed.sender,
            &mock_metadata(env),
            &report(env, &[(wid.clone(), 100, 11), (wid.clone(), 200, 11)]),
        );
        cache.assert_event(StaleReport {
            data_id: feed.id.clone(),
            report_ts: 11,
            stored_ts: 11,
        });
        assert_eq!(
            cache.events().len(),
            2,
            "one FeedUpdated and one StaleReport: the stale entry emits no FeedUpdated"
        );
        assert_eq!(
            cache.history(&feed).len(),
            2,
            "only the first ts=11 entry accepted"
        );

        cache.client().on_report(
            &feed.sender,
            &mock_metadata(env),
            &report(env, &[(wid.clone(), 300, 11), (wid, 400, 12)]),
        );
        assert_eq!(
            cache.history(&feed).len(),
            3,
            "the ts=12 accept lands after the stale ts=11 skip"
        );
        assert_eq!(cache.latest_round(&feed).timestamp, 12);
    }

    #[test]
    fn multiple_reports_for_one_feed_advance_the_counter_twice() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let env = &cache.env;
        let wid = mock_wire_id(env, 1, 0);
        cache.client().on_report(
            &feed.sender,
            &mock_metadata(env),
            &report(env, &[(wid.clone(), 100, 5), (wid, 200, 6)]),
        );
        let history = cache.history(&feed);
        assert_eq!(history.len(), 2, "both ascending-ts entries accepted");
        assert_eq!(history.get(0).unwrap().round_id, 1);
        assert_eq!(history.get(1).unwrap().round_id, 2);
        assert_eq!(history.get(1).unwrap().answer, I256::from_i128(env, 200));
        assert_eq!(cache.latest_round(&feed).timestamp, 6);
    }

    #[test]
    fn first_accept_assigns_round_one_and_stores_provenance() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        roll(&cache.env, 4242);
        cache.write(&feed, 100, 5);
        let rd = cache.latest_round(&feed);
        assert_eq!(rd.round_id, 1);
        assert_eq!(rd.answer, I256::from_i128(&cache.env, 100));
        assert_eq!(rd.timestamp, 5);
        assert_eq!(
            rd.ledger_seq, 4242,
            "ledger_seq stamped from env.ledger().sequence()"
        );
        assert!(rd.primary, "day-1 writes are stamped primary");
    }

    #[test]
    fn round_id_increments_per_feed_independently() {
        let cache = Cache::deploy();
        let (a, b, sender) = cache.add_feed_pair();
        let c = cache.client();
        let md = mock_metadata(&cache.env);
        c.on_report(
            &sender,
            &md,
            &report(&cache.env, &[(mock_wire_id(&cache.env, 1, 0), 1, 5)]),
        );
        c.on_report(
            &sender,
            &md,
            &report(&cache.env, &[(mock_wire_id(&cache.env, 1, 0), 2, 6)]),
        );
        c.on_report(
            &sender,
            &md,
            &report(&cache.env, &[(mock_wire_id(&cache.env, 2, 0), 9, 5)]),
        );
        assert_eq!(c.latest_round(&a).unwrap().round_id, 2);
        assert_eq!(c.latest_round(&b).unwrap().round_id, 1);
    }

    #[test]
    fn i256_answer_fidelity_across_full_range() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        cache.write(&feed, i128::MAX, 5);
        assert_eq!(
            cache.latest_round(&feed).answer,
            I256::from_i128(&cache.env, i128::MAX)
        );
        cache.write(&feed, i128::MIN, 6);
        assert_eq!(
            cache.latest_round(&feed).answer,
            I256::from_i128(&cache.env, i128::MIN)
        );
    }

    #[test]
    fn i256_answer_beyond_i128_survives_on_report() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let env = &cache.env;

        let big = I256::from_parts(env, i64::MAX, u64::MAX, u64::MAX, u64::MAX);
        let wid = mock_wire_id(env, feed.tag, 0);
        let report = vec![
            env,
            ReportEntry {
                data_id: wid,
                answer: big.clone(),
                timestamp: 5,
            },
        ]
        .to_xdr(env);
        cache
            .client()
            .on_report(&feed.sender, &mock_metadata(env), &report);

        let got = cache.latest_round(&feed).answer;
        assert_eq!(got, big);
        assert!(got.to_i128().is_none(), "answer coerced into i128 range");
    }

    #[test]
    fn mixed_batch_lands_valid_skips_soft_and_returns_ok() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let env = &cache.env;
        let c = cache.client();
        let md = mock_metadata(env);
        cache.write(&feed, 10, 10);
        c.on_report(
            &feed.sender,
            &md,
            &report(
                env,
                &[
                    (mock_wire_id(env, 1, 0), 11, 11),
                    (mock_wire_id(env, 9, 0), 99, 99),
                    (mock_wire_id(env, 1, 0), 5, 5),
                    (mock_wire_id(env, 1, 0), 12, 12),
                ],
            ),
        );
        assert_eq!(cache.history(&feed).len(), 3);
        let latest = cache.latest_round(&feed);
        assert_eq!(latest.round_id, 3, "skips do not advance the counter");
        assert_eq!(latest.timestamp, 12);
    }

    #[test]
    fn two_accept_batch_emits_exactly_two_feed_updated_and_no_legacy_round_events() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let env = &cache.env;
        let wid = mock_wire_id(env, 1, 0);
        cache.client().on_report(
            &feed.sender,
            &mock_metadata(env),
            &report(env, &[(wid.clone(), 1, 5), (wid, 2, 6)]),
        );
        assert_eq!(
            cache.events().len(),
            2,
            "two FeedUpdated, no NewRound/AnswerUpdated"
        );
    }

    #[test]
    fn mixed_batch_two_feeds_land_independently_with_correct_event_fields() {
        let cache = Cache::deploy();
        let (a, b, sender) = cache.add_feed_pair();
        roll(&cache.env, 100);
        let c = cache.client();
        c.on_report(
            &sender,
            &mock_metadata(&cache.env),
            &report(
                &cache.env,
                &[
                    (mock_wire_id(&cache.env, 1, 0), 111, 10),
                    (mock_wire_id(&cache.env, 2, 0), 222, 20),
                    (mock_wire_id(&cache.env, 1, 0), 333, 11),
                ],
            ),
        );
        cache.assert_event(FeedUpdated {
            data_id: b.clone(),
            round_id: 1,
            timestamp: 20,
            answer: I256::from_i128(&cache.env, 222),
            ledger_seq: 100,
            primary: true,
        });
        assert_eq!(
            c.latest_round(&a).unwrap().round_id,
            2,
            "feed A's own counter, not a batch-global one"
        );
        let b_latest = c.latest_round(&b).unwrap();
        assert_eq!(
            b_latest.round_id, 1,
            "feed B starts its own round sequence at 1"
        );
        assert_eq!(b_latest.answer, I256::from_i128(&cache.env, 222));
    }

    #[test]
    fn stale_skip_on_one_feed_does_not_corrupt_a_sibling_accept_in_same_batch() {
        let cache = Cache::deploy();
        let (a, b, sender) = cache.add_feed_pair();
        let c = cache.client();
        let md = mock_metadata(&cache.env);
        c.on_report(
            &sender,
            &md,
            &report(&cache.env, &[(mock_wire_id(&cache.env, 1, 0), 999, 20)]),
        );
        c.on_report(
            &sender,
            &md,
            &report(
                &cache.env,
                &[
                    (mock_wire_id(&cache.env, 1, 0), 777, 15),
                    (mock_wire_id(&cache.env, 2, 0), 222, 5),
                ],
            ),
        );
        cache.assert_event(StaleReport {
            data_id: a.clone(),
            report_ts: 15,
            stored_ts: 20,
        });
        let b_latest = c.latest_round(&b).unwrap();
        assert_eq!(b_latest.round_id, 1);
        assert_eq!(b_latest.answer, I256::from_i128(&cache.env, 222));
        assert_eq!(b_latest.timestamp, 5);
        let a_latest = c.latest_round(&a).unwrap();
        assert_eq!(
            a_latest.round_id, 1,
            "A's stale entry skipped; no second round"
        );
        assert_eq!(a_latest.timestamp, 20);
        assert_eq!(a_latest.answer, I256::from_i128(&cache.env, 999));
    }
}
