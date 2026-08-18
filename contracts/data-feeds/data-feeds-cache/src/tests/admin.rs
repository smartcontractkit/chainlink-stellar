use super::harness::*;

mod set_feed_configs {
    use super::*;

    #[test]
    #[should_panic(expected = "Error(Contract, #101)")]
    fn non_admin_caller_is_unauthorized() {
        let cache = Cache::deploy();
        let not_admin = new_address(&cache.env);
        let s = new_address(&cache.env);
        let entry = mock_feed_config(&cache.env, &mock_feed_id(&cache.env, 1), &s, "X");
        cache
            .client()
            .set_feed_configs(&not_admin, &vec![&cache.env, entry]);
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #103)")]
    fn empty_entries_is_empty_config() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        cache
            .client()
            .set_feed_configs(&admin, &Vec::<FeedConfigEntry>::new(&cache.env));
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #107)")]
    fn zero_data_id_is_rejected() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let s = new_address(&cache.env);
        let entry = mock_feed_config(&cache.env, &zero_feed_id(&cache.env), &s, "X");
        cache
            .client()
            .set_feed_configs(&admin, &vec![&cache.env, entry]);
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #103)")]
    fn entry_with_no_permissions_is_empty_config() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let entry = FeedConfigEntry {
            data_id: mock_feed_id(&cache.env, 1),
            config: FeedConfig {
                description: String::from_str(&cache.env, "X"),
                workflow_permissions: Vec::new(&cache.env),
            },
        };
        cache
            .client()
            .set_feed_configs(&admin, &vec![&cache.env, entry]);
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #105)")]
    fn permission_with_zero_workflow_name_is_invalid_name() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let mut perm = mock_permission(&cache.env, &new_address(&cache.env));
        perm.allowed_workflow_name = BytesN::from_array(&cache.env, &[0u8; 10]);
        let entry = feed_config(&cache.env, &mock_feed_id(&cache.env, 1), perm, "X");
        cache
            .client()
            .set_feed_configs(&admin, &vec![&cache.env, entry]);
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #106)")]
    fn duplicate_permission_in_one_config_is_rejected() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let env = &cache.env;
        let s = new_address(env);
        let entry = FeedConfigEntry {
            data_id: mock_feed_id(env, 1),
            config: FeedConfig {
                description: String::from_str(env, "X"),
                workflow_permissions: vec![env, mock_permission(env, &s), mock_permission(env, &s)],
            },
        };
        cache.client().set_feed_configs(&admin, &vec![env, entry]);
    }

    #[test]
    fn invalid_entry_aborts_the_whole_batch() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let c = cache.client();
        let s = new_address(&cache.env);
        let good_id = mock_feed_id(&cache.env, 1);
        let good = mock_feed_config(&cache.env, &good_id, &s, "A");
        let mut bad_perm = mock_permission(&cache.env, &new_address(&cache.env));
        bad_perm.allowed_workflow_owner = BytesN::from_array(&cache.env, &[0u8; 20]);
        let bad = feed_config(&cache.env, &mock_feed_id(&cache.env, 2), bad_perm, "B");
        assert_eq!(
            c.try_set_feed_configs(&admin, &vec![&cache.env, good, bad]),
            Err(Ok(CacheError::InvalidAddress))
        );
        assert!(
            !c.has_permission(
                &good_id,
                &s,
                &mock_wf_owner(&cache.env),
                &mock_wf_name(&cache.env)
            ),
            "the valid first entry must not survive the failed batch"
        );
        assert_eq!(c.get_feed_permissions(&good_id).len(), 0);
    }

    #[test]
    fn first_time_config_emits_only_feed_config_set_with_derived_decimals() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let s = new_address(&cache.env);
        let did = cache.configure_feed(&admin, &s, 1, "BTC/USD");
        cache.assert_event(FeedConfigSet {
            data_id: did,
            decimals: 18,
            description: String::from_str(&cache.env, "BTC/USD"),
            workflow_permissions: vec![&cache.env, mock_permission(&cache.env, &s)],
        });
        assert_eq!(
            cache.events().len(),
            1,
            "no FeedConfigRemoved on first-time config"
        );
    }

    #[test]
    fn reconfigure_is_full_replace_removed_then_set() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let c = cache.client();
        let s1 = new_address(&cache.env);
        let s2 = new_address(&cache.env);
        let did = cache.configure_feed(&admin, &s1, 1, "old");
        cache.configure_feed(&admin, &s2, 1, "new");
        let evs = cache.events();
        let removed = FeedConfigRemoved {
            data_id: did.clone(),
        }
        .to_xdr(&cache.env, &cache.id);
        let set = FeedConfigSet {
            data_id: did.clone(),
            decimals: 18,
            description: String::from_str(&cache.env, "new"),
            workflow_permissions: vec![&cache.env, mock_permission(&cache.env, &s2)],
        }
        .to_xdr(&cache.env, &cache.id);
        let i_removed = evs
            .iter()
            .position(|e| *e == removed)
            .expect("FeedConfigRemoved emitted");
        let i_set = evs
            .iter()
            .position(|e| *e == set)
            .expect("FeedConfigSet emitted");
        assert!(i_removed < i_set, "FeedConfigRemoved before FeedConfigSet");
        assert!(!c.has_permission(
            &did,
            &s1,
            &mock_wf_owner(&cache.env),
            &mock_wf_name(&cache.env)
        ));
        assert!(c.has_permission(
            &did,
            &s2,
            &mock_wf_owner(&cache.env),
            &mock_wf_name(&cache.env)
        ));
    }

    #[test]
    fn multiple_permitted_workflows_per_single_feed() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let env = &cache.env;
        let s1 = new_address(env);
        let s2 = new_address(env);
        let id = mock_feed_id(env, 1);
        let c = cache.client();
        let entry = FeedConfigEntry {
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
        c.set_feed_configs(&admin, &vec![env, entry]);

        assert_eq!(
            c.get_feed_permissions(&id).len(),
            2,
            "both permissions stored"
        );
        assert!(c.has_permission(&id, &s1, &mock_wf_owner(env), &mock_wf_name(env)));
        assert!(c.has_permission(&id, &s2, &mock_wf_owner(env), &mock_wf_name(env)));
    }

    #[test]
    fn feed_config_set_event_carries_every_permission_not_just_the_first() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let env = &cache.env;
        let s1 = new_address(env);
        let s2 = new_address(env);
        let id = mock_feed_id(env, 1);
        let entry = FeedConfigEntry {
            data_id: id.clone(),
            config: FeedConfig {
                description: String::from_str(env, "MULTI"),
                workflow_permissions: vec![
                    env,
                    mock_permission(env, &s1),
                    mock_permission(env, &s2),
                ],
            },
        };
        cache.client().set_feed_configs(&admin, &vec![env, entry]);
        cache.assert_event(FeedConfigSet {
            data_id: id,
            decimals: 18,
            description: String::from_str(env, "MULTI"),
            workflow_permissions: vec![env, mock_permission(env, &s1), mock_permission(env, &s2)],
        });
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #108)")]
    fn set_feed_configs_duplicate_id_in_one_batch_is_rejected() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let c = cache.client();
        let env = &cache.env;
        let x = mock_feed_id(env, 1);
        let s1 = new_address(env);
        let s2 = new_address(env);
        let cfg_a = mock_feed_config(env, &x, &s1, "A");
        let cfg_b = mock_feed_config(env, &x, &s2, "B");
        c.set_feed_configs(&admin, &vec![env, cfg_a, cfg_b]);
    }

    #[test]
    fn batch_configures_multiple_distinct_feeds() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let c = cache.client();
        let env = &cache.env;
        let id_a = mock_feed_id(env, 1);
        let id_b = mock_feed_id_with(env, 0x26, 2);
        let s1 = new_address(env);
        let s2 = new_address(env);
        let cfg_a = mock_feed_config(env, &id_a, &s1, "A");
        let cfg_b = mock_feed_config(env, &id_b, &s2, "B");

        c.set_feed_configs(&admin, &vec![env, cfg_a, cfg_b]);

        assert_eq!(c.get_feed_permissions(&id_a).len(), 1);
        assert_eq!(c.get_feed_permissions(&id_b).len(), 1);
        assert!(c.has_permission(&id_a, &s1, &mock_wf_owner(env), &mock_wf_name(env)));
        assert!(c.has_permission(&id_b, &s2, &mock_wf_owner(env), &mock_wf_name(env)));
        assert!(
            !c.has_permission(&id_a, &s2, &mock_wf_owner(env), &mock_wf_name(env)),
            "each feed keeps only its own permission"
        );
    }

    #[test]
    fn set_feed_configs_extends_contract_config_and_permission_ttls() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        let s = new_address(&cache.env);

        let did = cache.configure_feed(&admin, &s, 1, "BTC/USD");

        let full = network_max_ttl(&cache.env);
        age_ttl(&cache.env);

        cache.configure_feed(&admin, &s, 1, "BTC/USD");

        assert_eq!(
            cache.instance_ttl(),
            full,
            "set_feed_configs extends the contract instance TTL"
        );
        assert_eq!(
            cache.persistent_ttl(&DataKey::FeedConfig(did.clone())),
            full,
            "set_feed_configs extends the feed config TTL"
        );
        assert_eq!(
            cache.permission_ttl(&did, &s),
            full,
            "set_feed_configs extends the permission TTL"
        );
    }

    const SELF_WASM: &[u8] = include_bytes!(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/test_fixtures/cache_self_upgrade.wasm"
    ));

    #[test]
    fn stale_feed_state_resurrects_across_remove_upgrade_and_re_add() {
        let cache = Cache::deploy();
        set_network_max_ttl(&cache.env, 1_000_000);
        roll(&cache.env, 1_000);
        let feed = cache.add_feed(1);
        let env = &cache.env;

        cache.write(&feed, 100, 10);
        cache.write(&feed, 200, 20);
        cache.write(&feed, 300, 30);
        assert_eq!(cache.latest_round(&feed.id).unwrap().round_id, 3);
        assert_eq!(cache.history(&feed).len(), 3);

        cache.remove(&feed);
        assert!(
            cache.client().get_feed_permissions(&feed.id).is_empty(),
            "permissions must be cleared by remove"
        );
        assert_eq!(
            cache.description(&feed.id),
            None,
            "config must be gone after remove"
        );

        assert_eq!(
            cache.latest_round(&feed.id).unwrap().round_id,
            3,
            "FeedState survives remove_feed_configs"
        );
        assert_eq!(
            cache.history(&feed).len(),
            3,
            "round history survives remove"
        );

        let hash = cache.env.deployer().upload_contract_wasm(SELF_WASM);
        cache.client().upgrade(&hash);
        assert_eq!(
            cache.client().version(),
            1,
            "cache still serves version() after the upgrade"
        );
        assert_eq!(
            cache.latest_round(&feed.id).unwrap().round_id,
            3,
            "FeedState survives the upgrade too"
        );

        cache.configure_feed(&feed.admin, &feed.sender, feed.tag, "BTC/USD");

        cache.write(&feed, 400, 40);
        assert_eq!(
            cache.latest_round(&feed.id).unwrap().round_id,
            4,
            "round-id counter resumed from the resurrected FeedState, not reset"
        );

        let r1 = cache
            .round(&feed, 1)
            .expect("pre-removal round 1 resurrected");
        assert_eq!(r1.answer, I256::from_i128(env, 100));
        assert_eq!(r1.timestamp, 10);
        assert_eq!(
            cache.history(&feed).len(),
            4,
            "old + new rounds all visible"
        );
    }

    fn report_as(
        env: &Env,
        owner: &WorkflowOwner,
        name: &WorkflowName,
        wid: &WireDataId,
        answer: i128,
        ts: u64,
    ) -> (Bytes, Bytes) {
        (
            metadata(env, owner, name),
            report(env, &[(wid.clone(), answer, ts)]),
        )
    }

    #[test]
    fn re_add_by_a_different_workflow_inherits_prior_history_and_counter() {
        let cache = Cache::deploy();
        set_network_max_ttl(&cache.env, 1_000_000);
        roll(&cache.env, 1_000);

        let feed = cache.add_feed(1);
        let env = &cache.env;
        let wid = mock_wire_id(env, feed.tag, 0);
        cache.write(&feed, 111, 10);
        cache.write(&feed, 222, 20);
        assert_eq!(cache.latest_round(&feed.id).unwrap().round_id, 2);

        cache.remove(&feed);

        let w2_owner: WorkflowOwner = BytesN::from_array(env, &[0x77; 20]);
        let w2_name: WorkflowName = BytesN::from_array(env, &[0x66; 10]);
        let w2_sender = new_address(env);
        let perm = WorkflowPermission {
            allowed_sender: w2_sender.clone(),
            allowed_workflow_owner: w2_owner.clone(),
            allowed_workflow_name: w2_name.clone(),
        };
        let entry = feed_config(env, &feed.id, perm, "REPURPOSED FEED");
        cache
            .client()
            .set_feed_configs(&feed.admin, &vec![env, entry]);

        let inherited = cache.latest_round(&feed.id).unwrap();
        assert_eq!(inherited.round_id, 2, "W2 inherits W1's counter position");
        assert_eq!(inherited.answer, I256::from_i128(env, 222));
        assert_eq!(inherited.timestamp, 20);

        let leaked = cache.client().get_round(&feed.id, &1).unwrap();
        assert_eq!(leaked.answer, I256::from_i128(env, 111));
        assert_eq!(leaked.timestamp, 10);
        assert_eq!(
            cache.client().round_range(&feed.id, &0, &u64::MAX).len(),
            2,
            "W1's whole history is exposed to W2"
        );

        let (md, rep) = report_as(env, &w2_owner, &w2_name, &wid, 999, 30);
        cache.client().on_report(&w2_sender, &md, &rep);
        let head = cache.latest_round(&feed.id).unwrap();
        assert_eq!(head.round_id, 3, "W2 round numbering resumes W1's sequence");
        assert_eq!(head.answer, I256::from_i128(env, 999));

        let w1_report = report(env, &[(wid, 555, 40)]);
        cache
            .client()
            .on_report(&feed.sender, &mock_metadata(env), &w1_report);
        assert_eq!(
            cache.latest_round(&feed.id).unwrap().round_id,
            3,
            "old W1 sender is not authorized under W2's config (skipped)"
        );
    }

    #[test]
    fn re_add_first_report_is_rejected_stale_against_the_resurrected_timestamp() {
        let cache = Cache::deploy();
        set_network_max_ttl(&cache.env, 1_000_000);
        roll(&cache.env, 1_000);
        let feed = cache.add_feed(1);
        let env = &cache.env;

        cache.write(&feed, 100, 1_000_000);
        assert_eq!(cache.latest_round(&feed.id).unwrap().timestamp, 1_000_000);

        cache.remove(&feed);
        cache.configure_feed(&feed.admin, &feed.sender, feed.tag, "BTC/USD");

        cache.write(&feed, 200, 500_000);
        cache.assert_event(StaleReport {
            data_id: feed.id.clone(),
            report_ts: 500_000,
            stored_ts: 1_000_000,
        });
        assert_eq!(
            cache.latest_round(&feed.id).unwrap().round_id,
            1,
            "re-added feed's first (lower-ts) report was dropped as stale"
        );
        assert_eq!(
            cache.latest_round(&feed.id).unwrap().answer,
            I256::from_i128(env, 100),
            "head is still the pre-removal round"
        );

        cache.write(&feed, 300, 1_000_001);
        assert_eq!(cache.latest_round(&feed.id).unwrap().round_id, 2);
        assert_eq!(cache.latest_round(&feed.id).unwrap().timestamp, 1_000_001);
    }
}

mod remove_feed_configs {
    use super::*;

    #[test]
    #[should_panic(expected = "Error(Contract, #101)")]
    fn non_admin_caller_is_unauthorized() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let not_admin = new_address(&cache.env);
        cache
            .client()
            .remove_feed_configs(&not_admin, &vec![&cache.env, feed.id.clone()]);
    }

    #[test]
    fn remove_empty_batch_is_noop_success() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        cache
            .client()
            .remove_feed_configs(&admin, &Vec::<DataId>::new(&cache.env));
    }

    #[test]
    fn remove_deletes_config_and_permissions() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let c = cache.client();
        cache.remove(&feed);
        cache.assert_event(FeedConfigRemoved {
            data_id: feed.id.clone(),
        });
        assert!(!c.has_permission(
            &feed.id,
            &feed.sender,
            &mock_wf_owner(&cache.env),
            &mock_wf_name(&cache.env)
        ));
        assert_eq!(c.get_feed_permissions(&feed.id).len(), 0);
        assert_eq!(cache.description(&feed.id), None);
    }

    #[test]
    fn remove_feed_configs_containing_unconfigured_feed_aborts_atomically() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let c = cache.client();
        let unconfigured_b = mock_feed_id(&cache.env, 9);
        assert_eq!(
            c.try_remove_feed_configs(
                &feed.admin,
                &vec![&cache.env, feed.id.clone(), unconfigured_b]
            ),
            Err(Ok(CacheError::FeedNotConfigured))
        );
        assert!(c.has_permission(
            &feed.id,
            &feed.sender,
            &mock_wf_owner(&cache.env),
            &mock_wf_name(&cache.env)
        ));
        assert_eq!(c.get_feed_permissions(&feed.id).len(), 1);
        assert_eq!(
            cache.description(&feed.id),
            Some(String::from_str(&cache.env, "BTC/USD"))
        );
    }

    #[test]
    fn duplicate_id_in_one_batch_is_rejected_and_removes_nothing() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let c = cache.client();
        let env = &cache.env;

        assert_eq!(
            c.try_remove_feed_configs(&feed.admin, &vec![env, feed.id.clone(), feed.id.clone()]),
            Err(Ok(CacheError::DuplicateFeedConfig)),
        );

        assert!(c.has_permission(
            &feed.id,
            &feed.sender,
            &mock_wf_owner(env),
            &mock_wf_name(env)
        ));
        assert_eq!(c.get_feed_permissions(&feed.id).len(), 1);
        assert_eq!(
            cache.description(&feed.id),
            Some(String::from_str(env, "BTC/USD"))
        );
    }

    #[test]
    fn remove_feed_configs_extends_contract_instance_ttl() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let full = network_max_ttl(&cache.env);
        age_ttl(&cache.env);

        cache.remove(&feed);

        assert_eq!(
            cache.instance_ttl(),
            full,
            "remove_feed_configs extends the contract instance TTL"
        );
    }
}

mod add_feed_admin {
    use super::*;

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn add_admin_by_non_owner_host_fails() {
        let cache = Cache::deploy_no_auth();
        let subject = new_address(&cache.env);
        let attacker = new_address(&cache.env);
        authorize(
            &cache.env,
            &cache.id,
            &attacker,
            "add_feed_admin",
            (subject.clone(),),
        );
        cache.client().add_feed_admin(&subject);
    }

    #[test]
    fn add_emits_and_registers() {
        let cache = Cache::deploy();
        let c = cache.client();
        let subject = new_address(&cache.env);
        c.add_feed_admin(&subject);
        cache.assert_latest_event(FeedAdminAdded {
            admin: subject.clone(),
        });
        assert!(c.is_feed_admin(&subject));
    }

    #[test]
    fn re_add_emits_and_stays_registered() {
        let cache = Cache::deploy();
        let c = cache.client();
        let subject = new_address(&cache.env);
        c.add_feed_admin(&subject);
        c.add_feed_admin(&subject);
        cache.assert_event(FeedAdminAdded {
            admin: subject.clone(),
        });
        assert!(c.is_feed_admin(&subject));
    }

    #[test]
    fn add_feed_admin_extends_contract_and_admin_entry_ttls() {
        let cache = Cache::deploy();
        let admin = new_address(&cache.env);
        let full = network_max_ttl(&cache.env);
        age_ttl(&cache.env);

        cache.client().add_feed_admin(&admin);

        assert_eq!(
            cache.instance_ttl(),
            full,
            "add_feed_admin extends the contract instance TTL"
        );
        assert_eq!(
            cache.persistent_ttl(&DataKey::FeedAdmin(admin)),
            full,
            "add_feed_admin stamps the admin registration entry to full"
        );
    }

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn feed_admin_cannot_set_admin() {
        let cache = Cache::deploy_no_auth();
        let c = cache.client();
        let admin = new_address(&cache.env);
        let accomplice = new_address(&cache.env);

        authorize(
            &cache.env,
            &cache.id,
            &cache.owner,
            "add_feed_admin",
            (admin.clone(),),
        );
        c.add_feed_admin(&admin);

        authorize(
            &cache.env,
            &cache.id,
            &admin,
            "add_feed_admin",
            (accomplice.clone(),),
        );
        c.add_feed_admin(&accomplice);
    }
}

mod remove_feed_admin {
    use super::*;

    #[test]
    fn remove_then_not_admin_and_idempotent_emits() {
        let cache = Cache::deploy();
        let c = cache.client();
        let subject = new_address(&cache.env);
        c.add_feed_admin(&subject);
        c.remove_feed_admin(&subject);
        cache.assert_event(FeedAdminRemoved {
            admin: subject.clone(),
        });
        assert!(!c.is_feed_admin(&subject));
        c.remove_feed_admin(&subject);
        cache.assert_event(FeedAdminRemoved { admin: subject });
    }

    #[test]
    #[should_panic(expected = "Error(Auth, InvalidAction)")]
    fn remove_admin_by_non_owner_host_fails() {
        let cache = Cache::deploy_no_auth();
        let subject = new_address(&cache.env);
        let attacker = new_address(&cache.env);
        authorize(
            &cache.env,
            &cache.id,
            &attacker,
            "remove_feed_admin",
            (subject.clone(),),
        );
        cache.client().remove_feed_admin(&subject);
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #101)")]
    fn removed_admin_can_no_longer_configure() {
        let cache = Cache::deploy();
        let c = cache.client();
        let ex = new_address(&cache.env);
        c.add_feed_admin(&ex);
        c.remove_feed_admin(&ex);
        let s = new_address(&cache.env);
        let entry = mock_feed_config(&cache.env, &mock_feed_id(&cache.env, 1), &s, "X");
        c.set_feed_configs(&ex, &vec![&cache.env, entry]);
    }

    #[test]
    fn remove_feed_admin_extends_contract_instance_ttl() {
        let cache = Cache::deploy();
        let subject = new_address(&cache.env);
        let c = cache.client();
        c.add_feed_admin(&subject);

        let full = network_max_ttl(&cache.env);
        age_ttl(&cache.env);

        c.remove_feed_admin(&subject);

        assert_eq!(
            cache.instance_ttl(),
            full,
            "remove_feed_admin extends the contract instance TTL"
        );
    }
}

mod get_feed_permissions {
    use super::*;

    #[test]
    fn configured_feed_returns_its_whole_permission_list() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let c = cache.client();
        let perms = c.get_feed_permissions(&feed.id);
        assert_eq!(
            perms.len(),
            1,
            "configured feed exposes exactly its one permission"
        );
        let got = perms.get(0).unwrap();
        assert_eq!(
            got.allowed_sender, feed.sender,
            "returned permission must carry the configured sender, not a default or a sibling's"
        );
        assert_eq!(
            got.allowed_workflow_owner,
            mock_wf_owner(&cache.env),
            "returned permission must carry the configured workflow owner"
        );
        assert_eq!(
            got.allowed_workflow_name,
            mock_wf_name(&cache.env),
            "returned permission must carry the configured workflow name"
        );
    }

    #[test]
    fn unconfigured_feed_returns_empty() {
        let cache = Cache::deploy();
        cache.add_feed(1);
        let c = cache.client();
        assert_eq!(
            c.get_feed_permissions(&mock_feed_id(&cache.env, 9)).len(),
            0
        );
    }

    #[test]
    fn get_feed_permissions_zero_id_is_empty_not_rejected() {
        let cache = Cache::deploy();
        assert_eq!(
            cache
                .client()
                .get_feed_permissions(&zero_feed_id(&cache.env))
                .len(),
            0
        );
    }
}

mod has_permission {
    use super::*;

    #[test]
    fn true_for_configured_sender_owner_name() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let env = &cache.env;
        assert!(cache.client().has_permission(
            &feed.id,
            &feed.sender,
            &mock_wf_owner(env),
            &mock_wf_name(env)
        ));
    }

    #[test]
    fn false_for_unknown_sender_or_feed() {
        let cache = Cache::deploy();
        let feed = cache.add_feed(1);
        let c = cache.client();
        let env = &cache.env;
        assert!(
            !c.has_permission(
                &feed.id,
                &new_address(env),
                &mock_wf_owner(env),
                &mock_wf_name(env)
            ),
            "right feed, wrong sender"
        );
        assert!(
            !c.has_permission(
                &mock_feed_id(env, 9),
                &feed.sender,
                &mock_wf_owner(env),
                &mock_wf_name(env)
            ),
            "right sender, unconfigured feed"
        );
    }

    #[test]
    fn has_permission_zero_id_is_false_not_rejected() {
        let cache = Cache::deploy();
        assert!(!cache.client().has_permission(
            &zero_feed_id(&cache.env),
            &new_address(&cache.env),
            &mock_wf_owner(&cache.env),
            &mock_wf_name(&cache.env),
        ));
    }
}

mod set_feed_frozen {
    use super::*;

    fn live_feed(cache: &Cache, rounds: u64) -> Feed {
        let feed = cache.add_feed(1);
        cache.seed(&feed, rounds);
        feed
    }

    fn freeze(cache: &Cache, feed: &Feed, frozen: bool) {
        cache
            .client()
            .set_feed_frozen(&feed.admin, &vec![&cache.env, feed.id.clone()], &frozen);
    }

    #[test]
    fn every_read_stays_raw_while_frozen() {
        let cache = Cache::deploy();
        let feed = live_feed(&cache, 3);
        freeze(&cache, &feed, true);
        let c = cache.client();

        assert!(cache.is_frozen(&feed.id));
        assert!(cache.is_frozen(&feed.id));
        assert_eq!(cache.latest_round(&feed.id).unwrap().round_id, 3);
        assert_eq!(cache.round(&feed, 1).unwrap().round_id, 1);
        assert_eq!(
            cache.find(&feed, 10, Bound::AtOrBefore).unwrap().round_id,
            1
        );
        assert_eq!(cache.history(&feed).len(), 3);
        assert_eq!(cache.decimals(&feed.id), Some(18));
        assert!(cache.description(&feed.id).is_some());
        assert!(
            cache.is_configured(&feed.id),
            "freeze is consumer policy enforced at the proxy; the cache serves \
             raw data and only reports the flag"
        );
    }

    #[test]
    fn unfreezing_restores_every_read() {
        let cache = Cache::deploy();
        let feed = live_feed(&cache, 3);
        freeze(&cache, &feed, true);
        freeze(&cache, &feed, false);
        let c = cache.client();

        assert!(!cache.is_frozen(&feed.id));
        assert_eq!(cache.latest_round(&feed.id).unwrap().round_id, 3);
        assert_eq!(cache.round(&feed, 1).unwrap().round_id, 1);
        assert_eq!(
            cache.find(&feed, 10, Bound::AtOrBefore).unwrap().round_id,
            1
        );
        assert_eq!(cache.history(&feed).len(), 3);
        assert_eq!(cache.decimals(&feed.id), Some(18));
        assert_eq!(
            cache.description(&feed.id),
            Some(String::from_str(&cache.env, "BTC/USD"))
        );
    }

    #[test]
    fn updates_still_land_and_stay_frozen() {
        let cache = Cache::deploy();
        let feed = live_feed(&cache, 1);
        freeze(&cache, &feed, true);

        cache.write(&feed, 500, 99);
        assert!(cache.is_frozen(&feed.id), "a report must not thaw the feed");

        freeze(&cache, &feed, false);
        let latest = cache.latest_round(&feed.id).unwrap();
        assert_eq!(latest.round_id, 2);
        assert_eq!(latest.answer, I256::from_i128(&cache.env, 500));
    }

    #[test]
    fn outlives_config_removal() {
        let cache = Cache::deploy();
        let feed = live_feed(&cache, 1);
        freeze(&cache, &feed, true);
        cache.remove(&feed);

        let c = cache.client();
        assert!(!cache.is_configured(&feed.id));
        assert!(cache.is_frozen(&feed.id));
        assert!(cache.is_frozen(&feed.id));
        assert!(cache.latest_round(&feed.id).is_some());
    }

    #[test]
    fn freezing_one_feed_leaves_its_sibling_readable() {
        let cache = Cache::deploy();
        let a = live_feed(&cache, 1);
        let b = cache.add_feed(2);
        cache.seed(&b, 1);
        freeze(&cache, &a, true);

        assert!(cache.is_frozen(&a.id));
        assert!(cache.latest_round(&a.id).is_some());
        assert_eq!(cache.latest_round(&b.id).unwrap().round_id, 1);
    }

    #[test]
    fn every_call_emits_even_without_a_change() {
        let cache = Cache::deploy();
        let feed = live_feed(&cache, 1);

        freeze(&cache, &feed, true);
        cache.assert_event(FeedFrozenSet {
            data_id: feed.id.clone(),
            frozen: true,
        });

        freeze(&cache, &feed, true);
        cache.assert_event(FeedFrozenSet {
            data_id: feed.id.clone(),
            frozen: true,
        });
    }

    #[test]
    fn a_feed_without_state_aborts_the_whole_batch() {
        let cache = Cache::deploy();
        let feed = live_feed(&cache, 1);
        let unreported = cache.configure_feed(&feed.admin, &feed.sender, 2, "X");
        let c = cache.client();

        for other in [unreported, mock_feed_id(&cache.env, 9)] {
            assert_eq!(
                c.try_set_feed_frozen(
                    &feed.admin,
                    &vec![&cache.env, feed.id.clone(), other],
                    &true
                ),
                Err(Ok(CacheError::NoFeedState))
            );
            assert!(!cache.is_frozen(&feed.id), "the valid id must not survive");
        }
    }

    #[test]
    fn duplicate_ids_are_rejected_and_change_nothing() {
        let cache = Cache::deploy();
        let feed = live_feed(&cache, 1);
        let c = cache.client();
        assert_eq!(
            c.try_set_feed_frozen(
                &feed.admin,
                &vec![&cache.env, feed.id.clone(), feed.id.clone()],
                &true
            ),
            Err(Ok(CacheError::DuplicateFeedConfig))
        );
        assert!(!cache.is_frozen(&feed.id));
    }

    #[test]
    fn empty_batch_is_a_no_op() {
        let cache = Cache::deploy();
        let admin = cache.add_admin();
        cache
            .client()
            .set_feed_frozen(&admin, &Vec::<DataId>::new(&cache.env), &true);
    }

    #[test]
    fn non_admin_caller_is_unauthorized() {
        let cache = Cache::deploy();
        let feed = live_feed(&cache, 1);
        assert_eq!(
            cache.client().try_set_feed_frozen(
                &new_address(&cache.env),
                &vec![&cache.env, feed.id.clone()],
                &true
            ),
            Err(Ok(CacheError::UnauthorizedCaller))
        );
    }
}
