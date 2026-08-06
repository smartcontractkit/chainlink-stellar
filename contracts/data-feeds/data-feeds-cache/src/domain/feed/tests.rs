use super::*;
use crate::storage::DataKey;
use soroban_sdk::testutils::storage::Persistent as _;
use soroban_sdk::testutils::{Address as _, Ledger as _};
use soroban_sdk::{BytesN, I256};

use crate::test_utils::{mock_data_id, round_ttl};
use crate::test_utils::{mock_permission, mock_wf_name, mock_wf_owner};
use data_feeds_common::test_utils::execute_as_contract;

fn permission(env: &Env) -> WorkflowPermission {
    mock_permission(env, &Address::generate(env))
}

fn config(env: &Env, perms: &[WorkflowPermission], desc: &str) -> FeedConfig {
    let mut workflow_permissions = Vec::new(env);
    for p in perms {
        workflow_permissions.push_back(p.clone());
    }
    FeedConfig {
        description: String::from_str(env, desc),
        workflow_permissions,
    }
}

fn key_bytes(env: &Env, id: &DataId) -> BytesN<16> {
    CanonicalId::new(env, id).as_bytes().clone()
}

fn config_ttl(env: &Env, id: &DataId) -> u32 {
    env.storage()
        .persistent()
        .get_ttl(&DataKey::FeedConfig(key_bytes(env, id)))
}

fn perm_ttl(env: &Env, id: &DataId, p: &WorkflowPermission) -> u32 {
    env.storage()
        .persistent()
        .get_ttl(&DataKey::Permission(key_bytes(env, id), perm_key(env, p)))
}

fn state_ttl(env: &Env, id: &DataId) -> u32 {
    env.storage()
        .persistent()
        .get_ttl(&DataKey::FeedState(key_bytes(env, id)))
}

fn configure(env: &Env, id: &DataId, cfg: &FeedConfig) -> bool {
    super::configure(env, id, cfg, decimals_of(id).expect("valid decimals byte"))
        .expect("configure")
}

#[allow(clippy::unnecessary_min_or_max)]
fn expected_round_ttl(env: &Env) -> u32 {
    crate::storage::DATA_RETENTION_TTL.min(env.storage().max_ttl())
}

fn write(env: &Env, id: &DataId, seq: u32, ts: u64) {
    env.ledger().with_mut(|li| li.sequence_number = seq);
    record(env, id, &I256::from_i128(env, ts as i128), ts);
}

fn state(env: &Env, id: &DataId) -> FeedState {
    feed_state(env, id).unwrap()
}

fn found(env: &Env, id: &DataId, ts: u64, bound: Bound) -> Option<u64> {
    find_round(env, id, ts, bound).map(|r| r.round_id)
}

fn feed_with_rounds(env: &Env, n: u64, step: u64) -> DataId {
    let id = mock_data_id(env);
    for i in 1..=n {
        write(env, &id, env.ledger().sequence() + 1, i * step);
    }
    id
}

fn expire_round(env: &Env, id: &DataId, round_id: u64) {
    env.storage()
        .temporary()
        .remove(&DataKey::Round(key_bytes(env, id), round_id));
}

mod configure {
    use super::*;

    #[test]
    fn new_feed() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p = permission(env);

            let existed = configure(env, &id, &config(env, core::slice::from_ref(&p), "BTC/USD"));

            assert!(!existed);
            assert!(configured(env, &id));
            assert_eq!(
                description(env, &id),
                Some(String::from_str(env, "BTC/USD"))
            );
            assert!(permitted(env, &id, &perm_key(env, &p)));
        });
    }

    #[test]
    fn new_feed_with_multiple_permissions() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p1 = permission(env);
            let p2 = permission(env);
            let p3 = permission(env);

            configure(
                env,
                &id,
                &config(env, &[p1.clone(), p2.clone(), p3.clone()], "BTC/USD"),
            );

            assert!(permitted(env, &id, &perm_key(env, &p1)));
            assert!(permitted(env, &id, &perm_key(env, &p2)));
            assert!(permitted(env, &id, &perm_key(env, &p3)));
        });
    }

    #[test]
    fn new_feed_extends_config_and_permission_ttls() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "X"));

            let full = env.storage().max_ttl();
            assert_eq!(config_ttl(env, &id), full);
            assert_eq!(perm_ttl(env, &id, &p), full);
        });
    }

    #[test]
    fn new_feed_writes_no_latest_round() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "X"));

            assert!(
                feed_state(env, &id).is_none(),
                "no round written yet, so there is no latest-round entry to extend"
            );
        });
    }

    #[test]
    fn existing_feed_returns_true_and_updates_description() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "old"));

            let existed = configure(env, &id, &config(env, core::slice::from_ref(&p), "new"));

            assert!(existed);
            assert_eq!(description(env, &id), Some(String::from_str(env, "new")));
        });
    }

    #[test]
    fn existing_feed_replaces_permissions() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let old1 = permission(env);
            let old2 = permission(env);
            configure(env, &id, &config(env, &[old1.clone(), old2.clone()], "old"));

            let new1 = permission(env);
            let new2 = permission(env);
            configure(env, &id, &config(env, &[new1.clone(), new2.clone()], "new"));

            assert!(permitted(env, &id, &perm_key(env, &new1)));
            assert!(permitted(env, &id, &perm_key(env, &new2)));
            assert!(!permitted(env, &id, &perm_key(env, &old1)));
            assert!(!permitted(env, &id, &perm_key(env, &old2)));
        });
    }

    #[test]
    fn existing_feed_preserves_round_history() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "old"));
            record(env, &id, &I256::from_i128(env, 1), 1);
            record(env, &id, &I256::from_i128(env, 2), 2);

            configure(env, &id, &config(env, core::slice::from_ref(&p), "new"));

            assert_eq!(state(env, &id).latest_round.round_id, 2);
            assert!(round(env, &id, 1).is_some());
        });
    }

    #[test]
    fn existing_feed_does_not_extend_old_round_ttl() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "old"));
            write(env, &id, 100, 1);
            write(env, &id, 100, 2);

            let rt = expected_round_ttl(env);
            let nearly_expired_seq = 100 + rt - rt / 100;
            env.ledger()
                .with_mut(|li| li.sequence_number = nearly_expired_seq);
            let aged_round = round_ttl(env, &id, 1);

            configure(env, &id, &config(env, core::slice::from_ref(&p), "new"));

            assert_eq!(
                round_ttl(env, &id, 1),
                aged_round,
                "configure must not extend old round-data TTL; only the state is kept alive"
            );
        });
    }
}

mod remove {
    use super::*;

    #[test]
    fn removes_config_and_permissions() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "X"));

            let existed = remove(env, &id);

            assert!(existed);
            assert!(!configured(env, &id));
            assert!(!permitted(env, &id, &perm_key(env, &p)));
        });
    }

    #[test]
    fn keeps_round_history() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "X"));
            record(env, &id, &I256::from_i128(env, 1), 1);
            record(env, &id, &I256::from_i128(env, 2), 2);

            remove(env, &id);

            assert_eq!(state(env, &id).latest_round.round_id, 2);
            assert!(round(env, &id, 1).is_some());
            assert!(round(env, &id, 2).is_some());
        });
    }

    #[test]
    fn unconfigured_feed_returns_false() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            assert!(!configured(env, &id));
            assert!(!remove(env, &id));
        });
    }
}

mod record {
    use super::*;

    #[test]
    fn assigns_sequential_ids_and_accumulates_history() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);

            assert!(matches!(
                record(env, &id, &I256::from_i128(env, 10), 1),
                Recorded::Appended { round_id: 1 }
            ));
            assert_eq!(state(env, &id).latest_round.round_id, 1);
            assert_eq!(round(env, &id, 1).unwrap().answer, I256::from_i128(env, 10));

            assert!(matches!(
                record(env, &id, &I256::from_i128(env, 20), 2),
                Recorded::Appended { round_id: 2 }
            ));
            assert_eq!(state(env, &id).latest_round.round_id, 2);
            assert_eq!(round(env, &id, 2).unwrap().answer, I256::from_i128(env, 20));

            assert_eq!(
                round(env, &id, 1).unwrap().answer,
                I256::from_i128(env, 10),
                "appending round 2 leaves round 1 retrievable (history accumulates)"
            );
        });
    }

    #[test]
    fn refuses_to_overwrite_with_stale_or_equal_timestamp() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            record(env, &id, &I256::from_i128(env, 10), 5);

            assert!(
                matches!(
                    record(env, &id, &I256::from_i128(env, 99), 5),
                    Recorded::Stale { stored_ts: 5 }
                ),
                "equal timestamp is stale"
            );
            assert!(
                matches!(
                    record(env, &id, &I256::from_i128(env, 99), 4),
                    Recorded::Stale { stored_ts: 5 }
                ),
                "older timestamp is stale"
            );

            assert_eq!(state(env, &id).latest_round.round_id, 1, "no new round");
            assert_eq!(
                round(env, &id, 1).unwrap().answer,
                I256::from_i128(env, 10),
                "the original reading is untouched"
            );

            let before = state(env, &id);
            env.ledger().set_max_entry_ttl(50);
            env.ledger().with_mut(|li| li.sequence_number = 200);
            assert!(matches!(
                record(env, &id, &I256::from_i128(env, 9), 5),
                Recorded::Stale { .. }
            ));
            let after = state(env, &id);
            assert_eq!(
                (
                    after.window.shortest_ttl,
                    after.window.grow_to_ttl,
                    after.window.grow_at_ledger
                ),
                (
                    before.window.shortest_ttl,
                    before.window.grow_to_ttl,
                    before.window.grow_at_ledger
                ),
                "a stale report never touches the window"
            );
        });
    }

    #[test]
    fn extends_all_ttls() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "X"));
            record(env, &id, &I256::from_i128(env, 1), 1);

            let full = env.storage().max_ttl();
            let near_max_seq = full - full / 100 * 5;
            write(env, &id, near_max_seq, 2);

            let rt = expected_round_ttl(env);
            assert_eq!(
                config_ttl(env, &id),
                full,
                "record re-extends the config TTL"
            );
            assert_eq!(
                perm_ttl(env, &id, &p),
                full,
                "record re-extends permission TTLs"
            );
            assert_eq!(state_ttl(env, &id), full);
            assert_eq!(round_ttl(env, &id, 2), rt);
        });
    }

    #[test]
    fn stale_report_still_extends_persistent_ttls() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "X"));
            record(env, &id, &I256::from_i128(env, 1), 5);

            let full = env.storage().max_ttl();
            let near_max_seq = full - full / 100 * 5;
            env.ledger()
                .with_mut(|li| li.sequence_number = near_max_seq);

            assert!(
                matches!(
                    record(env, &id, &I256::from_i128(env, 9), 5),
                    Recorded::Stale { .. }
                ),
                "equal timestamp is stale"
            );

            assert_eq!(config_ttl(env, &id), full, "stale re-extends config TTL");
            assert_eq!(
                perm_ttl(env, &id, &p),
                full,
                "stale re-extends permission TTLs"
            );
            assert_eq!(state_ttl(env, &id), full, "stale re-extends state TTL");
        });
    }
}

mod window {
    use super::*;

    fn mock_state(
        env: &Env,
        anchor_ledger: u32,
        shortest_ttl: u32,
        grow_to_ttl: u32,
        grow_at_ledger: u32,
    ) -> FeedState {
        FeedState {
            latest_round: RoundData {
                round_id: 1,
                answer: I256::from_i128(env, 0),
                timestamp: 1,
                ledger_seq: anchor_ledger,
                primary: true,
            },
            frozen: false,
            window: Window {
                shortest_ttl,
                grow_to_ttl,
                grow_at_ledger,
            },
        }
    }

    #[test]
    fn shrinks_immediately_when_ttl_drops() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            env.ledger().set_max_entry_ttl(1_000);
            write(env, &id, 1_000, 10);
            env.ledger().set_max_entry_ttl(50);
            write(env, &id, 1_100, 20);

            let w = state(env, &id).window;
            assert_eq!(w.shortest_ttl, 50);
            assert_eq!(w.width_at(1_100), 50, "no wait on the way down");
        });
    }

    #[test]
    fn grows_at_expected_time_when_ttl_increases() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            env.ledger().set_max_entry_ttl(50);
            write(env, &id, 1_000, 10);
            env.ledger().set_max_entry_ttl(10_000);
            write(env, &id, 1_100, 20);

            let grow_at_ledger = 1_000 + 10_000 + 1;
            let w = state(env, &id).window;
            assert_eq!(
                (w.shortest_ttl, w.grow_to_ttl, w.grow_at_ledger),
                (50, 10_000, grow_at_ledger),
                "held at the old width until the short round is out of reach"
            );

            write(env, &id, 1_200, 30);
            let w = state(env, &id).window;
            assert_eq!(
                w.grow_at_ledger, grow_at_ledger,
                "another round at the same ttl keeps the clock"
            );

            assert_eq!(w.width_at(grow_at_ledger - 1), 50);
            assert_eq!(w.width_at(grow_at_ledger), 10_000);
        });
    }

    #[test]
    fn changing_ttl_again_replaces_the_pending_plan() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            env.ledger().set_max_entry_ttl(50);
            write(env, &id, 100, 10);
            env.ledger().set_max_entry_ttl(10_000);
            write(env, &id, 200, 20);
            env.ledger().set_max_entry_ttl(1_000);
            write(env, &id, 300, 30);

            let t = state(env, &id);
            assert_eq!(t.window.shortest_ttl, 50, "still held");
            assert_eq!(
                (t.window.grow_to_ttl, t.window.grow_at_ledger),
                (1_000, 200 + 1_000 + 1),
                "the 10_000 plan is gone; the new date comes from the new width"
            );
            assert_eq!(
                t.window.width_at(1_201),
                1_000,
                "recovers on the new schedule"
            );
        });
    }

    #[test]
    fn same_ledger_writes_share_the_window_through_storage() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            env.ledger().set_max_entry_ttl(1_000);
            write(env, &id, 100, 10);
            write(env, &id, 100, 20);
            env.ledger().set_max_entry_ttl(50);
            write(env, &id, 105, 30);
            write(env, &id, 105, 40);
            env.ledger().set_max_entry_ttl(1_000);
            write(env, &id, 105, 50);
            write(env, &id, 106, 60);

            let t = state(env, &id);
            assert_eq!(
                (t.window.grow_to_ttl, t.window.grow_at_ledger),
                (1_000, 105 + 1_000 + 1),
                "a same-ledger raise anchors on its own ledger"
            );
            env.ledger().with_mut(|li| li.sequence_number = 1_105);
            assert!(found(env, &id, 50, Bound::AtOrBefore).is_none());
            env.ledger().with_mut(|li| li.sequence_number = 1_106);
            assert!(
                found(env, &id, 50, Bound::AtOrBefore).is_none(),
                "the raise round leaves reach exactly one ledger after it dies"
            );
            assert_eq!(found(env, &id, 60, Bound::AtOrBefore), Some(6));
        });
    }

    #[test]
    fn short_ttl_rounds_get_their_exact_lifetime() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            env.ledger().set_max_entry_ttl(10_000);
            write(env, &id, 1_000, 10);
            env.ledger().set_max_entry_ttl(50);
            write(env, &id, 10_960, 20);
            write(env, &id, 10_970, 30);
            assert_eq!(state(env, &id).window.shortest_ttl, 50);
            assert_eq!(
                round_ttl(env, &id, 2),
                40,
                "exact extend pins the full 50-ledger TTL"
            );

            env.ledger().with_mut(|li| li.sequence_number = 10_980);
            assert_eq!(found(env, &id, 25, Bound::AtOrBefore), Some(2));
            assert_eq!(found(env, &id, 15, Bound::AtOrBefore), None, "masked");
            assert_eq!(found(env, &id, 15, Bound::AtOrAfter), Some(2));
        });
    }

    #[test]
    fn raised_ttl_reaches_only_new_rounds() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            env.ledger().set_max_entry_ttl(50);
            write(env, &id, 1_000, 10);
            env.ledger().set_max_entry_ttl(10_000);
            write(env, &id, 1_010, 20);

            assert_eq!(
                round_ttl(env, &id, 2),
                10_000,
                "a new round is minted at the raised ttl immediately"
            );
            assert_eq!(
                round_ttl(env, &id, 1),
                40,
                "the raise never reaches the old round — why reads wait until grow_at_ledger"
            );
        });
    }

    #[test]
    fn lowered_ttl_reaches_new_rounds_immediately() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            env.ledger().set_max_entry_ttl(10_000);
            write(env, &id, 1_000, 10);
            env.ledger().set_max_entry_ttl(50);
            write(env, &id, 1_010, 20);

            assert_eq!(
                round_ttl(env, &id, 2),
                50,
                "a new round is minted at the lowered ttl immediately"
            );
            assert_eq!(
                round_ttl(env, &id, 1),
                9_990,
                "nothing shortens an already-minted round"
            );
        });
    }

    #[test]
    fn max_ttl_below_fresh_minimum_stays_safe() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            env.ledger().set_max_entry_ttl(10);
            write(env, &id, 100, 10);
            assert_eq!(
                state(env, &id).window.shortest_ttl,
                10,
                "next_window uses the minted value"
            );
            assert_eq!(
                round_ttl(env, &id, 1),
                15,
                "the network floor for fresh temp entries beats the 10-ledger mint"
            );

            env.ledger().set_max_entry_ttl(1_000);
            write(env, &id, 105, 20);
            let grow_at_ledger = 100 + 1_000 + 1;
            let t = state(env, &id);
            assert_eq!(t.window.shortest_ttl, 10);
            assert_eq!(
                (t.window.grow_to_ttl, t.window.grow_at_ledger),
                (1_000, grow_at_ledger)
            );
            env.ledger().with_mut(|li| li.sequence_number = 113);
            assert_eq!(
                found(env, &id, 15, Bound::AtOrBefore),
                None,
                "round 1 is alive but outside the 10-ledger window"
            );
            assert_eq!(found(env, &id, 25, Bound::AtOrBefore), Some(2), "head");
            env.ledger()
                .with_mut(|li| li.sequence_number = grow_at_ledger);
            assert_eq!(found(env, &id, 25, Bound::AtOrBefore), Some(2));
        });
    }

    #[test]
    fn a_write_after_the_grow_date_locks_in_the_grown_width() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            env.ledger().set_max_entry_ttl(50);
            write(env, &id, 1_000, 10);
            env.ledger().set_max_entry_ttl(10_000);
            write(env, &id, 1_100, 20);

            let grow_at_ledger = 1_000 + 10_000 + 1;
            write(env, &id, grow_at_ledger, 30);
            assert_eq!(
                state(env, &id).window.shortest_ttl,
                10_000,
                "the write lands with the grown width, so the new floor is the full ttl"
            );
        });
    }

    #[test]
    fn next_window_without_state_starts_at_the_minted_ttl() {
        let w = next_window(None, 100, 7);
        assert_eq!(
            (w.shortest_ttl, w.grow_to_ttl, w.grow_at_ledger),
            (100, 100, 0)
        );
    }

    #[test]
    fn next_window_keeps_the_date_while_the_ttl_is_unchanged() {
        let env = Env::default();
        let state = mock_state(&env, 1_000, 50, 1_000, 1_701);
        let w = next_window(Some(&state), 1_000, 1_200);
        assert_eq!(
            (w.shortest_ttl, w.grow_to_ttl, w.grow_at_ledger),
            (50, 1_000, 1_701),
            "same ttl: the clock keeps running"
        );
    }

    #[test]
    fn next_window_caps_the_width_for_a_lower_ttl() {
        let env = Env::default();
        let state = mock_state(&env, 1_000, 50, 1_000, 2_001);
        let w = next_window(Some(&state), 20, 1_200);
        assert_eq!(w.shortest_ttl, 20, "a drop caps the width immediately");
        assert_eq!(
            (w.grow_to_ttl, w.grow_at_ledger),
            (20, 1_000 + 20 + 1),
            "goal follows the mint; the date is inert since width == goal"
        );
    }

    #[test]
    fn next_window_re_dates_for_a_higher_ttl() {
        let env = Env::default();
        let state = mock_state(&env, 1_000, 50, 50, 0);
        let w = next_window(Some(&state), 10_000, 1_100);
        assert_eq!(
            (w.shortest_ttl, w.grow_to_ttl, w.grow_at_ledger),
            (50, 10_000, 1_000 + 10_000 + 1),
            "a raise holds the width and dates the growth from the anchor"
        );
    }
}

mod permitted {
    use super::*;

    #[test]
    fn is_scoped_to_data_id() {
        execute_as_contract(|env| {
            let id_a = mock_data_id(env);
            let id_b = crate::test_utils::mock_feed_id(env, 0x99);
            let p = permission(env);
            configure(env, &id_a, &config(env, core::slice::from_ref(&p), "A"));

            assert!(permitted(env, &id_a, &perm_key(env, &p)));
            assert!(!permitted(env, &id_b, &perm_key(env, &p)));
        });
    }
}

mod permissions {
    use super::*;

    #[test]
    fn returns_a_feeds_permissions() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p1 = permission(env);
            let p2 = permission(env);
            configure(env, &id, &config(env, &[p1.clone(), p2.clone()], "A"));

            let ps = permissions(env, &id);
            assert_eq!(ps.len(), 2);
            assert_eq!(perm_key(env, &ps.get(0).unwrap()), perm_key(env, &p1));
            assert_eq!(perm_key(env, &ps.get(1).unwrap()), perm_key(env, &p2));
        });
    }

    #[test]
    fn returns_only_current_permissions_after_reconfigure() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let old = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&old), "old"));

            let new1 = permission(env);
            let new2 = permission(env);
            configure(env, &id, &config(env, &[new1.clone(), new2.clone()], "new"));

            let ps = permissions(env, &id);
            assert_eq!(ps.len(), 2);
            assert_eq!(perm_key(env, &ps.get(0).unwrap()), perm_key(env, &new1));
            assert_eq!(perm_key(env, &ps.get(1).unwrap()), perm_key(env, &new2));
        });
    }

    #[test]
    fn does_not_leak_other_feeds_permissions() {
        execute_as_contract(|env| {
            let id_a = mock_data_id(env);
            let id_b = crate::test_utils::mock_feed_id(env, 0x99);
            let p1 = permission(env);
            let p2 = permission(env);
            let p3 = permission(env);
            configure(env, &id_a, &config(env, &[p1.clone(), p2.clone()], "A"));
            configure(env, &id_b, &config(env, core::slice::from_ref(&p3), "B"));

            let pb = permissions(env, &id_b);
            assert_eq!(pb.len(), 1);
            assert_eq!(perm_key(env, &pb.get(0).unwrap()), perm_key(env, &p3));
        });
    }

    #[test]
    fn unconfigured_feed_returns_empty() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            assert_eq!(permissions(env, &id).len(), 0);
        });
    }
}

mod round {
    use super::*;

    #[test]
    fn absent_and_out_of_range_ids_are_none() {
        execute_as_contract(|env| {
            assert!(round(env, &mock_data_id(env), 1).is_none(), "no feed state");
            let id = feed_with_rounds(env, 2, 10);
            assert!(round(env, &id, 0).is_none());
            assert!(round(env, &id, 3).is_none(), "beyond the latest round");
        });
    }

    #[test]
    fn honors_the_lookback_mask() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            env.ledger().set_max_entry_ttl(10_000);
            write(env, &id, 1_000, 10);
            env.ledger().set_max_entry_ttl(50);
            write(env, &id, 10_960, 20);
            write(env, &id, 10_970, 30);
            env.ledger().with_mut(|li| li.sequence_number = 10_980);
            assert!(
                round(env, &id, 1).is_none(),
                "alive but outside the 50-ledger window"
            );
            assert!(round(env, &id, 3).is_some(), "the head is always readable");
        });
    }
}

mod range {
    use super::*;

    fn assert_round_ids(rounds: &Vec<RoundData>, expected: &[u64]) {
        assert_eq!(rounds.len(), expected.len() as u32, "length");
        for (i, &rid) in expected.iter().enumerate() {
            assert_eq!(rounds.get(i as u32).unwrap().round_id, rid, "index {i}");
        }
    }

    #[test]
    fn full_range_oldest_first() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 5, 1);
            assert_round_ids(&range(env, &id, 1, 5), &[1, 2, 3, 4, 5]);
        });
    }

    #[test]
    fn lower_end_out_of_bound() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 5, 1);
            assert_round_ids(&range(env, &id, 0, 5), &[1, 2, 3, 4, 5]);
        });
    }

    #[test]
    fn upper_end_out_of_bound() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 5, 1);
            assert_round_ids(&range(env, &id, 3, 999), &[3, 4, 5]);
        });
    }

    #[test]
    fn from_greater_than_to() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 5, 1);
            assert_eq!(range(env, &id, 4, 2).len(), 0);
        });
    }

    #[test]
    fn unknown_dataid_is_noop() {
        execute_as_contract(|env| {
            let unknown = crate::test_utils::mock_feed_id(env, 0x77);
            assert_eq!(range(env, &unknown, 1, 5).len(), 0);
        });
    }

    #[test]
    fn with_one_element() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 5, 1);
            assert_round_ids(&range(env, &id, 3, 3), &[3]);
        });
    }

    #[test]
    fn expired_tip_only_appears_in_ranges_that_include_it() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 5, 1);
            for r in 1..=5 {
                expire_round(env, &id, r);
            }

            assert_eq!(range(env, &id, 1, 3).len(), 0);
            assert_round_ids(&range(env, &id, 1, 5), &[5]);
        });
    }

    #[test]
    fn masks_alive_but_out_of_window_rounds() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            env.ledger().set_max_entry_ttl(10_000);
            write(env, &id, 1_000, 10);
            env.ledger().set_max_entry_ttl(50);
            write(env, &id, 10_960, 20);
            write(env, &id, 10_970, 30);
            env.ledger().with_mut(|li| li.sequence_number = 10_980);
            assert_round_ids(&range(env, &id, 1, 3), &[2, 3]);
        });
    }
}

mod find_round {
    use super::*;

    #[test]
    fn between_two_rounds() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 5, 10);
            assert_eq!(found(env, &id, 35, Bound::AtOrBefore), Some(3));
            assert_eq!(found(env, &id, 35, Bound::AtOrAfter), Some(4));
        });
    }

    #[test]
    fn with_no_records() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            assert_eq!(found(env, &id, 35, Bound::AtOrBefore), None);
            assert_eq!(found(env, &id, 35, Bound::AtOrAfter), None);
        });
    }

    #[test]
    fn with_one_record() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 1, 10);
            assert_eq!(found(env, &id, 10, Bound::AtOrBefore), Some(1));
            assert_eq!(found(env, &id, 10, Bound::AtOrAfter), Some(1));
            assert_eq!(found(env, &id, 5, Bound::AtOrBefore), None);
            assert_eq!(found(env, &id, 5, Bound::AtOrAfter), Some(1));
            assert_eq!(found(env, &id, 15, Bound::AtOrBefore), Some(1));
            assert_eq!(found(env, &id, 15, Bound::AtOrAfter), None);
        });
    }

    #[test]
    fn before_earliest() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 5, 10);
            assert_eq!(found(env, &id, 5, Bound::AtOrBefore), None);
            assert_eq!(found(env, &id, 5, Bound::AtOrAfter), Some(1));
        });
    }

    #[test]
    fn after_latest() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 5, 10);
            assert_eq!(found(env, &id, 55, Bound::AtOrBefore), Some(5));
            assert_eq!(found(env, &id, 55, Bound::AtOrAfter), None);
        });
    }

    #[test]
    fn exact_timestamp_match() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 5, 10);
            assert_eq!(found(env, &id, 30, Bound::AtOrBefore), Some(3));
            assert_eq!(found(env, &id, 30, Bound::AtOrAfter), Some(3));
            assert_eq!(found(env, &id, 10, Bound::AtOrBefore), Some(1));
            assert_eq!(found(env, &id, 10, Bound::AtOrAfter), Some(1));
            assert_eq!(found(env, &id, 50, Bound::AtOrBefore), Some(5));
            assert_eq!(found(env, &id, 50, Bound::AtOrAfter), Some(5));
        });
    }

    #[test]
    fn returns_the_tip_after_history_expires() {
        execute_as_contract(|env| {
            let id = feed_with_rounds(env, 2, 10);
            expire_round(env, &id, 1);
            expire_round(env, &id, 2);

            assert_eq!(found(env, &id, 20, Bound::AtOrBefore), Some(2));
            assert_eq!(found(env, &id, 10, Bound::AtOrBefore), None);
            assert_eq!(found(env, &id, 10, Bound::AtOrAfter), Some(2));
        });
    }
}

mod decimals {
    use super::*;

    fn id_with_byte7(env: &Env, b: u8) -> DataId {
        let mut raw = [0xFFu8; 16];
        raw[7] = b;
        BytesN::from_array(env, &raw)
    }

    #[test]
    fn reports_the_stored_scale_for_every_addressable_scale() {
        execute_as_contract(|env| {
            let id = id_with_byte7(env, 0x32);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "BTC/USD"));

            for byte in [0x32u8, 0x28, 0x26, 0x20] {
                assert_eq!(
                    decimals(env, &id_with_byte7(env, byte)),
                    Some(18),
                    "byte {byte:#x} addresses a feed stored at 18 decimals"
                );
            }
        });
    }

    #[test]
    fn gated_on_the_config() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            assert_eq!(decimals(env, &id), None, "derivable but not configured");

            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "BTC/USD"));
            assert_eq!(decimals(env, &id), decimals_of(&id));

            remove(env, &id);
            assert_eq!(decimals(env, &id), None);
        });
    }
}

mod description {
    use super::*;

    #[test]
    fn unconfigured_feed_is_none() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            assert_eq!(description(env, &id), None);
        });
    }

    #[test]
    fn can_be_empty() {
        execute_as_contract(|env| {
            let id = mock_data_id(env);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), ""));
            assert_eq!(
                description(env, &id),
                Some(String::from_str(env, "")),
                "nothing rejects an empty description, and it reads back as present"
            );
        });
    }
}

mod perm_hash {
    use super::*;

    #[test]
    fn is_deterministic_for_same_inputs() {
        let env = Env::default();
        let sender = Address::generate(&env);
        let a = perm_hash(&env, &sender, &mock_wf_owner(&env), &mock_wf_name(&env));
        let b = perm_hash(&env, &sender, &mock_wf_owner(&env), &mock_wf_name(&env));
        assert_eq!(a, b);
    }

    #[test]
    fn changes_when_sender_owner_or_name_changes() {
        let env = Env::default();
        let sender = Address::generate(&env);
        let base = perm_hash(&env, &sender, &mock_wf_owner(&env), &mock_wf_name(&env));

        let other_sender = Address::generate(&env);
        assert_ne!(
            perm_hash(
                &env,
                &other_sender,
                &mock_wf_owner(&env),
                &mock_wf_name(&env)
            ),
            base
        );

        let other_owner = BytesN::from_array(&env, &[0x33; 20]);
        assert_ne!(
            perm_hash(&env, &sender, &other_owner, &mock_wf_name(&env)),
            base
        );

        let other_name = BytesN::from_array(&env, &[0x44; 10]);
        assert_ne!(
            perm_hash(&env, &sender, &mock_wf_owner(&env), &other_name),
            base
        );
    }
}

mod canonical {
    use super::*;

    const STORED: i128 = 1_500_000_000_000_000_000;

    fn feed_at(env: &Env, byte7: u8) -> DataId {
        let id = crate::test_utils::mock_feed_id_with(env, byte7, 0x01);
        let p = permission(env);
        configure(env, &id, &config(env, core::slice::from_ref(&p), "BTC/USD"));
        id
    }

    fn at_scale(env: &Env, id: &DataId, byte7: u8) -> DataId {
        let mut bytes = id.to_array();
        bytes[7] = byte7;
        BytesN::from_array(env, &bytes)
    }

    #[test]
    fn configured_is_true_for_every_scale() {
        execute_as_contract(|env| {
            let id = feed_at(env, 0x32);

            for byte7 in [0x32u8, 0x28, 0x26] {
                assert!(
                    configured(env, &at_scale(env, &id, byte7)),
                    "byte {byte7:#x} addresses the configured feed"
                );
            }
        });
    }

    #[test]
    fn rounds_are_served_as_stored_whichever_scale_is_addressed() {
        execute_as_contract(|env| {
            let id = feed_at(env, 0x32);
            record(env, &id, &I256::from_i128(env, STORED), 10);

            for byte7 in [0x32u8, 0x28, 0x20] {
                let answer = latest(env, &at_scale(env, &id, byte7))
                    .expect("round present")
                    .answer;
                assert_eq!(
                    answer.to_i128(),
                    Some(STORED),
                    "the cache never scales; byte {byte7:#x}"
                );
            }
        });
    }

    #[test]
    fn permissions_are_shared_by_every_scale() {
        execute_as_contract(|env| {
            let id = crate::test_utils::mock_feed_id_with(env, 0x32, 0x02);
            let p = permission(env);
            configure(env, &id, &config(env, core::slice::from_ref(&p), "BTC/USD"));

            assert!(
                permitted(env, &at_scale(env, &id, 0x28), &perm_key(env, &p)),
                "a derived scale must not need its own permission"
            );
        });
    }

    #[test]
    fn freezing_a_feed_freezes_every_scale() {
        execute_as_contract(|env| {
            let id = feed_at(env, 0x32);
            record(env, &id, &I256::from_i128(env, STORED), 10);
            set_frozen(env, &id, true);

            assert!(is_frozen(env, &at_scale(env, &id, 0x28)));
        });
    }

    #[test]
    fn a_report_at_another_scale_is_not_recorded() {
        execute_as_contract(|env| {
            let id = feed_at(env, 0x32);
            record(env, &id, &I256::from_i128(env, STORED), 10);

            let outcome = record(env, &at_scale(env, &id, 0x28), &I256::from_i128(env, 1), 20);

            assert!(
                matches!(outcome, Recorded::NonCanonicalDecimals { expected: 18 }),
                "writing at 8 decimals would silently rescale the feed"
            );
            assert_eq!(
                state(env, &id).latest_round.timestamp,
                10,
                "the rejected report must not append a round"
            );
        });
    }

    #[test]
    fn re_registering_the_feed_at_another_scale_is_rejected() {
        execute_as_contract(|env| {
            let id = feed_at(env, 0x32);
            let p = permission(env);
            let cfg = config(env, core::slice::from_ref(&p), "BTC/USD");

            let rescaled = at_scale(env, &id, 0x28);
            assert_eq!(
                super::super::configure(env, &rescaled, &cfg, 8),
                Err(CacheError::DecimalsMismatch),
                "the scale a feed is stored at is immutable"
            );
            assert_eq!(
                decimals(env, &id),
                Some(18),
                "the stored scale is unchanged"
            );
        });
    }

    #[test]
    fn re_registering_the_feed_at_the_same_scale_is_allowed() {
        execute_as_contract(|env| {
            let id = feed_at(env, 0x32);
            let p = permission(env);

            let existed = configure(env, &id, &config(env, core::slice::from_ref(&p), "new"));

            assert!(existed, "the config is replaced, not rejected");
            assert_eq!(description(env, &id), Some(String::from_str(env, "new")));
        });
    }

    #[test]
    fn a_zero_decimal_feed_is_configurable() {
        execute_as_contract(|env| {
            let id = feed_at(env, 0x20);
            record(env, &id, &I256::from_i128(env, 4_270), 10);

            assert_eq!(decimals(env, &id), Some(0));
            assert_eq!(
                latest(env, &id).expect("round present").answer.to_i128(),
                Some(4_270)
            );
        });
    }
}
