#![cfg(test)]

extern crate std;

use soroban_sdk::{
    symbol_short, testutils::storage::Instance as _, testutils::storage::Persistent as _,
    testutils::Address as _, testutils::Events as _, vec, Address, Bytes, BytesN, Env, Map, Symbol,
    TryFromVal, TryIntoVal, Val, Vec,
};

use crate::{CcvChainConfig, CcvConfigUpdate, ExampleCcipReceiver, ExampleCcipReceiverClient};

use common_message::AnyToStellarMessage;

#[test]
fn ccip_receive_accepts_router_auth() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());

    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    // EVM `validChain`: inbound `source_chain_selector` must match an `enable_remote_chain` entry.
    let src: u64 = 1;
    let extra = Bytes::from_slice(&env, &[0x01]);
    client.enable_remote_chain(&owner, &src, &extra, &0u32);

    let msg = AnyToStellarMessage {
        message_id: BytesN::from_array(&env, &[7u8; 32]),
        source_chain_selector: src,
        sender: Bytes::from_array(&env, &[0u8; 32]),
        data: Bytes::from_slice(&env, &[1, 2, 3]),
        dest_token_amounts: vec![&env],
    };

    // With `mock_all_auths`, `router.require_auth_for_args` is satisfied without `as_contract`.
    client.ccip_receive(&msg);

    let stored = client.last_message_id();
    assert_eq!(stored, msg.message_id);
}

#[test]
fn ccip_receive_rejects_unknown_source_chain() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    let msg = AnyToStellarMessage {
        message_id: BytesN::from_array(&env, &[9u8; 32]),
        source_chain_selector: 404,
        sender: Bytes::from_array(&env, &[0u8; 32]),
        data: Bytes::from_slice(&env, &[1]),
        dest_token_amounts: vec![&env],
    };

    let r = client.try_ccip_receive(&msg);
    assert!(r.is_err());
}

#[test]
fn get_remote_chain_selectors_tracks_enable_disable() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    let extra = Bytes::from_slice(&env, &[0x01]);
    client.enable_remote_chain(&owner, &100u64, &extra, &0u32);
    client.enable_remote_chain(&owner, &200u64, &extra, &0u32);

    let sels = client.get_remote_chain_selectors();
    assert_eq!(sels.len(), 2);
    assert_eq!(sels.get(0).unwrap(), 100u64);
    assert_eq!(sels.get(1).unwrap(), 200u64);

    client.disable_remote_chain(&owner, &100u64);
    let sels2 = client.get_remote_chain_selectors();
    assert_eq!(sels2.len(), 1);
    assert_eq!(sels2.get(0).unwrap(), 200u64);
}

#[test]
fn enable_remote_chain_stores_extra_args() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    let dest: u64 = 9_876;
    let extra = Bytes::from_slice(&env, &[0xde, 0xad, 0xbe, 0xef]);
    let fin: u32 = 0x0001_0000; // WAIT_FOR_SAFE-style flag (FinalityCodec)
    client.enable_remote_chain(&owner, &dest, &extra, &fin);

    let got = client.get_remote_chain_extra_args(&dest);
    assert_eq!(got, extra);
    let cfg = client.get_remote_chain_config(&dest);
    assert_eq!(cfg.extra_args, extra);
    assert_eq!(cfg.allowed_finality_config, fin);
}

#[test]
fn get_ccvs_and_finality_config_combines_ccv_and_remote_chain() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    let sel: u64 = 7_777;
    let extra = Bytes::from_slice(&env, &[1, 2, 3]);
    let allowed: u32 = 0; // full finality (EVM default)
    client.enable_remote_chain(&owner, &sel, &extra, &allowed);

    let ccv1 = Address::generate(&env);
    let ccv2 = Address::generate(&env);
    client.apply_ccv_config_updates(
        &owner,
        &vec![
            &env,
            CcvConfigUpdate {
                source_chain_selector: sel,
                required_ccvs: vec![&env, ccv1.clone()],
                optional_ccvs: vec![&env, ccv2.clone()],
                optional_threshold: 0,
            },
        ],
    );

    let combined = client.get_ccvs_and_finality_config(&sel, &Bytes::new(&env));
    assert_eq!(combined.required_ccvs.len(), 1);
    assert_eq!(combined.optional_ccvs.len(), 1);
    assert_eq!(combined.optional_threshold, 0);
    assert_eq!(combined.allowed_finality_config, allowed);
    assert_eq!(combined.required_ccvs.get(0).unwrap(), ccv1);
}

#[test]
fn apply_ccv_config_updates_stores_per_source_chain() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    let a1 = Address::generate(&env);
    let a2 = Address::generate(&env);
    let o1 = Address::generate(&env);
    let o2 = Address::generate(&env);
    let src: u64 = 5_001;
    let upd = CcvConfigUpdate {
        source_chain_selector: src,
        required_ccvs: vec![&env, a1.clone(), a2.clone()],
        optional_ccvs: vec![&env, o1.clone(), o2.clone()],
        optional_threshold: 1,
    };
    client.apply_ccv_config_updates(&owner, &vec![&env, upd]);

    let cfg = client.get_ccv_config(&src);
    assert_eq!(cfg.required_ccvs.len(), 2);
    assert_eq!(cfg.optional_ccvs.len(), 2);
    assert_eq!(cfg.optional_threshold, 1);
}

#[test]
fn apply_ccv_config_rejects_invalid_optional_threshold() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    let o1 = Address::generate(&env);
    let o2 = Address::generate(&env);
    // optional_threshold > optional.len() is invalid (EVM parity). threshold == len
    // (require-all-optionals) is valid — see validate_ccv_config_update.
    let upd = CcvConfigUpdate {
        source_chain_selector: 42,
        required_ccvs: vec![&env],
        optional_ccvs: vec![&env, o1, o2],
        optional_threshold: 3, // 3 > len(2) ⇒ rejected
    };
    let r = client.try_apply_ccv_config_updates(&owner, &vec![&env, upd]);
    assert!(r.is_err());
}

#[test]
fn apply_ccv_config_accepts_threshold_equal_to_optional_len() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    let o1 = Address::generate(&env);
    let o2 = Address::generate(&env);
    // threshold == optional.len() (N-of-N, require all optionals) is valid (EVM parity).
    let upd = CcvConfigUpdate {
        source_chain_selector: 42,
        required_ccvs: vec![&env],
        optional_ccvs: vec![&env, o1, o2],
        optional_threshold: 2, // 2 == len(2) ⇒ accepted
    };
    client.apply_ccv_config_updates(&owner, &vec![&env, upd]);
    let cfg = client.get_ccv_config(&42);
    assert_eq!(cfg.optional_ccvs.len(), 2);
    assert_eq!(cfg.optional_threshold, 2);
}

#[test]
fn apply_ccv_config_rejects_duplicate_ccv() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    let dup = Address::generate(&env);
    let upd = CcvConfigUpdate {
        source_chain_selector: 99,
        required_ccvs: vec![&env, dup.clone()],
        optional_ccvs: vec![&env, dup.clone()],
        optional_threshold: 0,
    };
    let r = client.try_apply_ccv_config_updates(&owner, &vec![&env, upd]);
    assert!(r.is_err());
}

#[test]
fn get_ccv_config_returns_empty_when_unset() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    let cfg = client.get_ccv_config(&12345u64);
    let empty = CcvChainConfig {
        required_ccvs: vec![&env],
        optional_ccvs: vec![&env],
        optional_threshold: 0,
    };
    assert_eq!(cfg, empty);
}

// --- in-place upgradeability (shared `common_authorization::Upgradeable` trait) ---
//
// Reuses the checked-in data-feeds fixture whose only entrypoint is `peek() -> u32`.
// The fixture lives three levels up from this test file
// (contracts/examples/ccip_receiver/src -> ccip_receiver -> examples -> contracts).
const UPGRADE_TARGET_WASM: &[u8] =
    include_bytes!("../../../data-feeds/data-feeds-common/test_fixtures/upgrade_target.wasm");

/// Decode the `new_wasm_hash` field from the last `Upgraded` event emitted by
/// `contract`. `#[contractevent]` serializes the struct as a `Map<Symbol, Val>`
/// keyed by field name, so the lookup is topic-agnostic.
fn upgraded_event_hash(env: &Env, contract: &Address) -> BytesN<32> {
    let evs = env.events().all().filter_by_contract(contract);
    for e in evs.events().iter().rev() {
        let soroban_sdk::xdr::ContractEventBody::V0(ref v0) = e.body;
        let val: Val = v0
            .data
            .clone()
            .try_into_val(env)
            .expect("event data ScVal to Val");
        let Ok(map) = Map::<Symbol, Val>::try_from_val(env, &val) else {
            continue;
        };
        let Some(hval) = map.get(Symbol::new(env, "new_wasm_hash")) else {
            continue;
        };
        if let Ok(hash) = BytesN::<32>::try_from_val(env, &hval) {
            return hash;
        }
    }
    panic!("expected Upgraded event with new_wasm_hash from ccip_receiver");
}

#[test]
fn test_upgrade_by_owner_swaps_executable_and_emits_event() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    // Upload the fixture Wasm and upgrade the receiver to it. `mock_all_auths`
    // satisfies `require_owner` -> `owner.require_auth()`.
    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    client.upgrade(&hash);

    // The `Upgraded` event was published carrying the new Wasm hash.
    assert_eq!(upgraded_event_hash(&env, &client.address), hash);

    // The receiver has no `peek`; the fixture does. A successful `peek` at the
    // receiver address proves the executable was swapped in place.
    let peeked: u32 = env.invoke_contract(
        &client.address,
        &symbol_short!("peek"),
        Vec::<Val>::new(&env),
    );
    assert_eq!(
        peeked, 0,
        "fixture peek must run at the receiver address after upgrade"
    );
}

#[test]
#[should_panic(expected = "Error(Auth, InvalidAction)")]
fn test_upgrade_by_non_owner_rejected() {
    let env = Env::default();
    // No `mock_all_auths`: `require_owner` -> `owner.require_auth()` fails before
    // the executable is touched. `initialize` needs no auth (only
    // `require_not_initialized` + `init_owner`), so setup succeeds.
    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    // A hash need not correspond to a real uploaded Wasm here — the auth check
    // runs first and panics, so `update_current_contract_wasm` is never reached.
    let hash = BytesN::<32>::from_array(&env, &[0u8; 32]);
    client.upgrade(&hash);
}

#[test]
fn test_extend_config_ttl_bumps_persistent_and_instance_entries() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    // Create persistent config entries: REM_CFG(sel) via enable_remote_chain and
    // CCV_KEY(sel) via apply_ccv_config_updates.
    let sel: u64 = 1;
    let extra = Bytes::from_slice(&env, &[0x01]);
    client.enable_remote_chain(&owner, &sel, &extra, &0u32);
    let required = vec![&env, Address::generate(&env)];
    let updates = vec![
        &env,
        CcvConfigUpdate {
            source_chain_selector: sel,
            required_ccvs: required,
            optional_ccvs: vec![&env],
            optional_threshold: 0,
        },
    ];
    client.apply_ccv_config_updates(&owner, &updates);

    let rem_key = (symbol_short!("RMCFG"), sel);
    let ccv_key = (symbol_short!("CCVCG"), sel);

    let rem_ttl_before = env.as_contract(&receiver_id, || {
        env.storage().persistent().get_ttl(&rem_key)
    });
    let ccv_ttl_before = env.as_contract(&receiver_id, || {
        env.storage().persistent().get_ttl(&ccv_key)
    });
    let inst_ttl_before = env.as_contract(&receiver_id, || env.storage().instance().get_ttl());

    // extend_ttl requires threshold <= extend_to and only fires when the remaining
    // TTL drops below threshold. Set threshold == extend_to (satisfies the <= bound)
    // and extend_to comfortably larger than every current live-until, so the bump
    // fires and is observable on all three entries.
    let extend_to = rem_ttl_before.max(ccv_ttl_before).max(inst_ttl_before) + 50_000;
    let threshold = extend_to;
    client.extend_config_ttl(&vec![&env, sel], &vec![&env, sel], &threshold, &extend_to);

    let rem_ttl_after = env.as_contract(&receiver_id, || {
        env.storage().persistent().get_ttl(&rem_key)
    });
    let ccv_ttl_after = env.as_contract(&receiver_id, || {
        env.storage().persistent().get_ttl(&ccv_key)
    });
    let inst_ttl_after = env.as_contract(&receiver_id, || env.storage().instance().get_ttl());

    assert!(
        rem_ttl_after > rem_ttl_before,
        "REM_CFG TTL not bumped: {} -> {}",
        rem_ttl_before,
        rem_ttl_after
    );
    assert!(
        ccv_ttl_after > ccv_ttl_before,
        "CCV_KEY TTL not bumped: {} -> {}",
        ccv_ttl_before,
        ccv_ttl_after
    );
    assert!(
        inst_ttl_after > inst_ttl_before,
        "instance TTL not bumped: {} -> {}",
        inst_ttl_before,
        inst_ttl_after
    );

    // Entries remain readable with their configured values after the bump.
    assert!(!client.get_remote_chain_config(&sel).extra_args.is_empty());
    assert_eq!(client.get_ccv_config(&sel).required_ccvs.len(), 1);
}

#[test]
fn test_extend_config_ttl_skips_absent_selectors_without_panic() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let router = Address::generate(&env);
    let receiver_id = env.register(ExampleCcipReceiver, ());
    let client = ExampleCcipReceiverClient::new(&env, &receiver_id);
    client.initialize(&owner, &router);

    // No REM_CFG/CCV_KEY entries exist for selector 999. The keeper's has() guard
    // must skip absent entries rather than panic; instance TTL is still bumped.
    // threshold == extend_to satisfies extend_ttl's `threshold <= extend_to` bound.
    client.extend_config_ttl(
        &vec![&env, 999u64],
        &vec![&env, 999u64],
        &10_000u32,
        &10_000u32,
    );

    // Unconfigured selector returns the documented default (empty extra_args).
    let cfg = client.get_remote_chain_config(&999u64);
    assert!(cfg.extra_args.is_empty());
}
