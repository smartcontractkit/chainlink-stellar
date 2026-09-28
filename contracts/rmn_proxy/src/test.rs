#![cfg(test)]

use super::*;
use rmn_remote::{RmnRemoteContract, RmnRemoteContractClient};
use soroban_sdk::{
    symbol_short, testutils::Address as _, testutils::Events as _, vec, Address, BytesN, Env, Map,
    Symbol, TryFromVal, TryIntoVal, Val, Vec,
};

/// Global curse subject — cursing this on RMN Remote causes `is_cursed()` to return true.
const GLOBAL_CURSE_SUBJECT: [u8; 16] = [
    0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
];

fn setup_env() -> (Env, Address, Address, Address) {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);

    // Deploy and initialize RMN Remote so proxy can delegate is_cursed() to it
    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &soroban_sdk::Vec::new(&env));

    let contract_id = env.register(RmnProxyContract, ());

    (env, contract_id, owner, rmn_remote_id)
}

#[test]
fn test_initialize() {
    let (env, contract_id, owner, rmn) = setup_env();
    let client = RmnProxyContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn);

    env.as_contract(&client.address, || {
        assert_eq!(RmnProxyContract::owner(&env).unwrap(), owner);
    });
    assert_eq!(client.get_rmn(), rmn);
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")] // AlreadyInitialized
fn test_double_initialize_fails() {
    let (env, contract_id, owner, rmn) = setup_env();
    let client = RmnProxyContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn);
    client.initialize(&owner, &rmn);
}

#[test]
fn test_set_rmn() {
    let (env, contract_id, owner, rmn) = setup_env();
    let client = RmnProxyContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn);

    let new_rmn = Address::generate(&env);
    client.set_rmn(&new_rmn);

    assert_eq!(client.get_rmn(), new_rmn);
}

#[test]
fn test_is_cursed_returns_false() {
    let (env, contract_id, owner, rmn) = setup_env();
    let client = RmnProxyContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn);

    // Proxy delegates to RMN Remote; when not cursed, returns false
    assert!(!client.is_cursed());
}

#[test]
fn test_is_cursed_returns_true_when_global_cursed() {
    let (env, contract_id, owner, rmn) = setup_env();
    let client = RmnProxyContractClient::new(&env, &contract_id);
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn);

    client.initialize(&owner, &rmn);

    assert!(!client.is_cursed());

    // Curse the global subject on RMN Remote
    let global = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
    rmn_remote_client.curse(&owner, &vec![&env, global]);

    // Proxy should now report cursed
    assert!(client.is_cursed());
}

#[test]
fn test_is_cursed_returns_false_after_uncurse() {
    let (env, contract_id, owner, rmn) = setup_env();
    let client = RmnProxyContractClient::new(&env, &contract_id);
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn);

    client.initialize(&owner, &rmn);

    let global = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
    rmn_remote_client.curse(&owner, &vec![&env, global.clone()]);
    assert!(client.is_cursed());

    rmn_remote_client.uncurse(&vec![&env, global]);
    assert!(!client.is_cursed());
}

#[test]
fn test_set_rmn_switches_delegation() {
    let (env, contract_id, owner, rmn1) = setup_env();
    let client = RmnProxyContractClient::new(&env, &contract_id);

    // Deploy a second RMN Remote
    let rmn2_id = env.register(RmnRemoteContract, ());
    let rmn2_client = RmnRemoteContractClient::new(&env, &rmn2_id);
    rmn2_client.initialize(&owner, &soroban_sdk::Vec::new(&env));

    client.initialize(&owner, &rmn1);
    assert!(!client.is_cursed());

    // Curse the first remote
    let rmn1_client = RmnRemoteContractClient::new(&env, &rmn1);
    let global = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
    rmn1_client.curse(&owner, &vec![&env, global]);
    assert!(client.is_cursed());

    // Switch proxy to second (uncursed) remote
    client.set_rmn(&rmn2_id);
    assert!(!client.is_cursed());

    // Switch back to first (still cursed) remote
    client.set_rmn(&rmn1);
    assert!(client.is_cursed());
}

#[test]
fn test_transfer_ownership_two_step() {
    let (env, contract_id, owner, rmn) = setup_env();
    let client = RmnProxyContractClient::new(&env, &contract_id);

    client.initialize(&owner, &rmn);

    let new_owner = Address::generate(&env);

    env.as_contract(&client.address, || {
        // Step 1: Initiate transfer
        let _ = RmnProxyContract::transfer_ownership(&env, &new_owner);
        // Owner should still be the original owner until accepted
        assert_eq!(RmnProxyContract::owner(&env).unwrap(), owner);
        // Step 2: Accept transfer
        let _ = RmnProxyContract::accept_ownership(&env);
        // Now the new owner should be set
        assert_eq!(RmnProxyContract::owner(&env).unwrap(), new_owner);
    });
}

// ============================================================
// Upgrade tests (shared `common_authorization::Upgradeable` opt-in)
// ============================================================

// Reuse the checked-in data-feeds fixture that exposes `peek() -> u32`.
// RmnProxy has no `peek`, so a successful `peek` at the proxy address after
// `upgrade` proves the executable was swapped in place. No `stellar` CLI in
// this env, so we reuse this fixture instead of adding one.
const UPGRADE_TARGET_WASM: &[u8] =
    include_bytes!("../../data-feeds/data-feeds-common/test_fixtures/upgrade_target.wasm");

/// Decode the `new_wasm_hash` field from the last `Upgraded` event emitted by
/// `contract`. `#[contractevent]` serializes the struct as a `Map<Symbol, Val>`
/// keyed by field name.
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
    panic!("expected Upgraded event with new_wasm_hash from contract");
}

#[test]
fn test_upgrade_by_owner_swaps_executable_and_emits_event() {
    let (env, contract_id, owner, rmn) = setup_env();
    let client = RmnProxyContractClient::new(&env, &contract_id);
    client.initialize(&owner, &rmn);

    // `setup_env` mocked all auths, so `require_owner` -> `owner.require_auth()`
    // passes.
    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    client.upgrade(&hash);

    // The Upgraded event carries the new Wasm hash.
    assert_eq!(upgraded_event_hash(&env, &client.address), hash);

    // RmnProxy has no `peek`; the fixture does. A successful `peek` at the proxy
    // address proves the executable was swapped in place. Instance storage is
    // preserved by `update_current_contract_wasm` (host guarantee), and the
    // proxy never wrote the fixture's "slot" key, so peek reads back 0.
    let peeked: u32 = env.invoke_contract(
        &client.address,
        &symbol_short!("peek"),
        Vec::<Val>::new(&env),
    );
    assert_eq!(
        peeked, 0,
        "fixture peek must run at the rmn proxy address after upgrade"
    );
}

#[test]
#[should_panic(expected = "Error(Auth, InvalidAction)")]
fn test_upgrade_by_non_owner_rejected() {
    let env = Env::default();
    // No `mock_all_auths`: nobody is authorized, so `require_owner` ->
    // `owner.require_auth()` fails. `initialize` (rmn_remote, proxy) needs no
    // auth (only double-init guards), so the contract is set up with the owner
    // stored.
    let owner = Address::generate(&env);

    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(&env, &rmn_remote_id);
    rmn_remote_client.initialize(&owner, &soroban_sdk::Vec::new(&env));

    let contract_id = env.register(RmnProxyContract, ());
    let client = RmnProxyContractClient::new(&env, &contract_id);
    client.initialize(&owner, &rmn_remote_id);

    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    // Caller is not the owner and the owner does not authorize -> reject before
    // the executable is touched.
    client.upgrade(&hash);
}
