use soroban_sdk::{contracttype, Address, BytesN, Env};

use crate::domain::data_id::CanonicalId;
use crate::interface::{types::RoundData, FeedConfig};

pub(crate) type PermissionHash = BytesN<32>;

pub(crate) const DATA_RETENTION_TTL: u32 = 518_400;

#[contracttype]
#[derive(Clone, Debug)]
pub(crate) enum DataKey {
    FeedAdmin(Address),
    FeedConfig(BytesN<16>),
    Permission(BytesN<16>, PermissionHash),
    FeedState(BytesN<16>),
    Round(BytesN<16>, u64),
}

#[contracttype]
#[derive(Clone, Debug)]
pub(crate) struct Window {
    pub(crate) shortest_ttl: u32,
    pub(crate) grow_to_ttl: u32,
    pub(crate) grow_at_ledger: u32,
}

impl Window {
    pub(crate) fn width_at(&self, now: u32) -> u32 {
        if now >= self.grow_at_ledger {
            self.grow_to_ttl
        } else {
            self.shortest_ttl
        }
    }
}

#[contracttype]
#[derive(Clone, Debug)]
pub(crate) struct FeedState {
    pub(crate) latest_round: RoundData,
    pub(crate) window: Window,
    pub(crate) frozen: bool,
}

#[contracttype]
#[derive(Clone, Debug)]
pub(crate) struct StoredConfig {
    pub(crate) config: FeedConfig,
    pub(crate) decimals: u32,
}

macro_rules! store {
    ( $( ($s:ident, $accessor:ident, $tier:ident, $key:ty, $val:ty, $ret:expr, $to_key:expr) ),+ $(,)? ) => {
        pub(crate) trait Store {
            fn contract_store(&self) -> ContractStore<'_>;
            $( fn $accessor(&self) -> $s<'_>; )+
        }
        impl Store for Env {
            fn contract_store(&self) -> ContractStore<'_> { ContractStore(self) }
            $( fn $accessor(&self) -> $s<'_> { $s(self) } )+
        }

        pub(crate) struct ContractStore<'a>(&'a Env);
        impl ContractStore<'_> {
            pub(crate) fn extend_ttl(&self) {
                let ttl = self.0.storage().max_ttl();
                self.0.storage().instance().extend_ttl(ttl.saturating_sub(1), ttl);
            }
        }

        $(
            pub(crate) struct $s<'a>(&'a Env);
            #[allow(dead_code)]
            impl $s<'_> {
                fn data_key(&self, k: &$key) -> DataKey {
                    ($to_key)(k)
                }
                pub(crate) fn max_ttl(&self) -> u32 {
                    ($ret).min(self.0.storage().max_ttl())
                }
                pub(crate) fn get(&self, k: &$key) -> Option<$val> {
                    self.0.storage().$tier().get(&self.data_key(k))
                }
                pub(crate) fn set(&self, k: &$key, v: &$val) {
                    let key = self.data_key(k);
                    let s = self.0.storage().$tier();
                    let init = !s.has(&key);
                    s.set(&key, v);
                    if init {
                        let ttl = self.max_ttl();
                        s.extend_ttl(&key, ttl, ttl);
                    }
                }
                pub(crate) fn exists(&self, k: &$key) -> bool {
                    self.0.storage().$tier().has(&self.data_key(k))
                }
                pub(crate) fn remove(&self, k: &$key) {
                    self.0.storage().$tier().remove(&self.data_key(k));
                }
                pub(crate) fn extend_ttl(&self, k: &$key) {
                    let key = self.data_key(k);
                    let s = self.0.storage().$tier();
                    if s.has(&key) {
                        let ttl = self.max_ttl();
                        s.extend_ttl(&key, ttl.saturating_sub(1), ttl);
                    }
                }
            }
        )+
    };
}

store! {
    (AdminStore,       admin_store,        persistent, Address,                       (),           u32::MAX, |a: &Address| DataKey::FeedAdmin(a.clone())),
    (ConfigStore,      config_store,       persistent, CanonicalId,                   StoredConfig, u32::MAX, |c: &CanonicalId| DataKey::FeedConfig(c.as_bytes().clone())),
    (PermissionStore,  permission_store,   persistent, (CanonicalId, PermissionHash), (),           u32::MAX, |(c, ph): &(CanonicalId, PermissionHash)| DataKey::Permission(c.as_bytes().clone(), ph.clone())),
    (FeedStateStore,   feed_state_store,   persistent, CanonicalId,                   FeedState,    u32::MAX, |c: &CanonicalId| DataKey::FeedState(c.as_bytes().clone())),
    (RoundStore,       round_store,        temporary,  (CanonicalId, u64),            RoundData,    DATA_RETENTION_TTL, |(c, r): &(CanonicalId, u64)| DataKey::Round(c.as_bytes().clone(), *r)),
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::interface::types::RoundData;
    use soroban_sdk::testutils::Ledger as _;
    use soroban_sdk::I256;
    use soroban_sdk::{String, Vec};

    use crate::test_utils::{mock_data_id, round_ttl};
    use data_feeds_common::test_utils::execute_as_contract;

    fn store_round(env: &Env) -> (CanonicalId, u64) {
        let key = (CanonicalId::new(env, &mock_data_id(env)), 1u64);
        let round = RoundData {
            round_id: 1,
            answer: I256::from_i128(env, 1),
            timestamp: 1,
            ledger_seq: env.ledger().sequence(),
            primary: true,
        };
        env.round_store().set(&key, &round);
        key
    }

    #[test]
    fn get_and_exists_are_false_for_an_absent_key() {
        execute_as_contract(|env| {
            let key = (CanonicalId::new(env, &mock_data_id(env)), 1u64);
            assert!(!env.round_store().exists(&key));
            assert!(env.round_store().get(&key).is_none());
        });
    }

    #[test]
    fn set_then_get_round_trips_the_value() {
        execute_as_contract(|env| {
            let key = store_round(env);
            assert!(env.round_store().exists(&key));
            let got = env
                .round_store()
                .get(&key)
                .expect("stored round is readable");
            assert_eq!(got.round_id, 1);
            assert_eq!(got.answer, I256::from_i128(env, 1));
            assert_eq!(got.timestamp, 1);
            assert!(got.primary);
        });
    }

    #[test]
    fn remove_deletes_the_entry() {
        execute_as_contract(|env| {
            let key = store_round(env);
            assert!(env.round_store().exists(&key));
            env.round_store().remove(&key);
            assert!(!env.round_store().exists(&key));
            assert!(env.round_store().get(&key).is_none());
        });
    }

    #[test]
    fn extend_ttl_on_an_absent_key_is_a_noop() {
        execute_as_contract(|env| {
            let key = (CanonicalId::new(env, &mock_data_id(env)), 1u64);
            env.round_store().extend_ttl(&key);
            assert!(
                !env.round_store().exists(&key),
                "extend_ttl must not create the entry"
            );
        });
    }

    #[test]
    fn a_new_round_is_pinned_to_min_of_retention_and_network_cap() {
        execute_as_contract(|env| {
            env.ledger().set_max_entry_ttl(1_000);
            let key = store_round(env);
            assert_eq!(
                round_ttl(env, &mock_data_id(env), key.1),
                1_000,
                "network cap wins below the retention constant"
            );
        });
        execute_as_contract(|env| {
            env.ledger().set_max_entry_ttl(600_000);
            let key = store_round(env);
            assert_eq!(
                round_ttl(env, &mock_data_id(env), key.1),
                DATA_RETENTION_TTL,
                "retention wins below the network cap"
            );
        });
    }

    #[test]
    fn overwriting_an_existing_entry_keeps_its_ttl() {
        execute_as_contract(|env| {
            env.ledger().set_max_entry_ttl(10_000);
            env.ledger().with_mut(|li| li.sequence_number = 1_000);
            let key = store_round(env);
            let stored = env.round_store().get(&key).unwrap();
            env.ledger().with_mut(|li| li.sequence_number = 2_000);
            env.round_store().set(&key, &stored);
            assert_eq!(
                round_ttl(env, &mock_data_id(env), key.1),
                9_000,
                "set never re-pins a live entry"
            );
        });
    }

    #[test]
    fn extend_ttl_always_restores_to_full() {
        execute_as_contract(|env| {
            env.ledger().set_max_entry_ttl(10_000);
            env.ledger().with_mut(|li| li.sequence_number = 1_000);
            let key = store_round(env);
            env.ledger().with_mut(|li| li.sequence_number = 2_000);
            env.round_store().extend_ttl(&key);
            assert_eq!(
                round_ttl(env, &mock_data_id(env), key.1),
                10_000,
                "extend_ttl bumps to full regardless of remaining"
            );
        });
    }

    #[test]
    fn width_at_switches_exactly_at_the_grow_date() {
        let window = Window {
            shortest_ttl: 50,
            grow_to_ttl: 10_000,
            grow_at_ledger: 1_051,
        };
        assert_eq!(window.width_at(1_050), 50);
        assert_eq!(window.width_at(1_051), 10_000);
    }

    #[test]
    fn round_retention_is_pinned() {
        assert_eq!(
            DATA_RETENTION_TTL, 518_400,
            "round-history retention is 30 days at 5s ledgers. Correctness does not depend on \
             the value: reads only see rounds written within the 30 day window, which tracks the \
             minted TTL downward instantly and upward once the shorter cohort has aged out. \
             Treat any change as deliberate, and update this canary."
        );
    }

    #[test]
    fn distinct_datakey_variants_sharing_one_data_id_never_alias() {
        execute_as_contract(|env| {
            let d = CanonicalId::new(env, &mock_data_id(env));

            let mut hb = [0xAAu8; 32];
            hb[..16].copy_from_slice(&[0x32; 16]);
            let ph: PermissionHash = BytesN::from_array(env, &hb);
            let ph2: PermissionHash = BytesN::from_array(env, &[0xBB; 32]);

            let config = StoredConfig {
                config: FeedConfig {
                    description: String::from_str(env, "CONFIG"),
                    workflow_permissions: Vec::new(env),
                },
                decimals: 18,
            };
            let state = FeedState {
                latest_round: RoundData {
                    round_id: 777,
                    answer: I256::from_i128(env, -99),
                    timestamp: 555,
                    ledger_seq: 44,
                    primary: false,
                },
                frozen: false,
                window: Window {
                    shortest_ttl: 11,
                    grow_to_ttl: 22,
                    grow_at_ledger: 33,
                },
            };
            let round = RoundData {
                round_id: 1,
                answer: I256::from_i128(env, 42),
                timestamp: 7,
                ledger_seq: env.ledger().sequence(),
                primary: true,
            };

            env.config_store().set(&d, &config);
            env.permission_store().set(&(d.clone(), ph.clone()), &());
            env.permission_store().set(&(d.clone(), ph2.clone()), &());
            env.round_store().set(&(d.clone(), 1u64), &round);
            env.feed_state_store().set(&d, &state);

            let got_cfg = env.config_store().get(&d).expect("config present");
            assert_eq!(
                got_cfg.config.description,
                String::from_str(env, "CONFIG"),
                "FeedConfig(d) survived a same-id FeedState write"
            );
            let got_state = env.feed_state_store().get(&d).expect("state present");
            assert_eq!(got_state.latest_round.round_id, 777);
            assert_eq!(got_state.window.grow_at_ledger, 33);
            assert!(env.permission_store().exists(&(d.clone(), ph.clone())));
            let got_round = env
                .round_store()
                .get(&(d.clone(), 1u64))
                .expect("round present");
            assert_eq!(got_round.round_id, 1);
            assert_eq!(got_round.timestamp, 7);

            env.permission_store().remove(&(d.clone(), ph.clone()));
            assert!(!env.permission_store().exists(&(d.clone(), ph.clone())));
            assert!(
                env.permission_store().exists(&(d.clone(), ph2.clone())),
                "removing Permission(d, ph) left Permission(d, ph2) intact"
            );

            env.config_store().set(
                &d,
                &StoredConfig {
                    config: FeedConfig {
                        description: String::from_str(env, "REWRITTEN"),
                        workflow_permissions: Vec::new(env),
                    },
                    decimals: 18,
                },
            );
            assert_eq!(
                env.feed_state_store()
                    .get(&d)
                    .unwrap()
                    .latest_round
                    .round_id,
                777,
                "FeedState(d) survived a FeedConfig(d) overwrite"
            );
        });
    }
}
