#![cfg(test)]

extern crate std;

use soroban_sdk::{
    symbol_short, testutils::Address as _, testutils::Events as _, token, vec, Address, BytesN,
    Env, Map, Symbol, TryFromVal, TryIntoVal, Val, Vec,
};

use crate::{TokenLockBox, TokenLockBoxClient};

fn setup() -> (Env, Address, Address, Address, TokenLockBoxClient<'static>) {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_addr = token_contract.address();

    let lockbox_id = env.register(TokenLockBox, ());
    let client = TokenLockBoxClient::new(&env, &lockbox_id);
    client.initialize(&owner, &token_addr);

    (env, owner, token_addr, token_admin, client)
}

#[test]
fn initialize_and_get_token() {
    let (_env, _owner, token_addr, _admin, client) = setup();
    assert_eq!(client.get_token(), token_addr);
    assert!(client.is_token_supported(&token_addr));
}

#[test]
fn deposit_and_withdraw() {
    let (env, _owner, token_addr, _token_admin, client) = setup();

    let pool = Address::generate(&env);
    client.add_allowed_callers(&vec![&env, pool.clone()]);

    let sac = token::StellarAssetClient::new(&env, &token_addr);
    sac.mint(&pool, &1_000);

    let tc = token::Client::new(&env, &token_addr);
    let exp = env.ledger().sequence().saturating_add(10_000);
    tc.approve(&pool, &client.address, &500, &exp);
    client.deposit(&pool, &500);
    assert_eq!(tc.balance(&pool), 500);
    assert_eq!(tc.balance(&client.address), 500);

    let receiver = Address::generate(&env);
    client.withdraw(&pool, &200, &receiver);
    assert_eq!(tc.balance(&receiver), 200);
    assert_eq!(tc.balance(&client.address), 300);
}

#[test]
fn withdraw_insufficient_balance() {
    let (env, _owner, token_addr, _token_admin, client) = setup();

    let pool = Address::generate(&env);
    client.add_allowed_callers(&vec![&env, pool.clone()]);

    let sac = token::StellarAssetClient::new(&env, &token_addr);
    sac.mint(&pool, &100);
    let tc = token::Client::new(&env, &token_addr);
    let exp = env.ledger().sequence().saturating_add(10_000);
    tc.approve(&pool, &client.address, &100, &exp);
    client.deposit(&pool, &100);

    let receiver = Address::generate(&env);
    let r = client.try_withdraw(&pool, &200, &receiver);
    assert!(r.is_err());
}

#[test]
fn unauthorized_caller_rejected() {
    let (env, _owner, _token_addr, _admin, client) = setup();

    let stranger = Address::generate(&env);
    let r = client.try_deposit(&stranger, &100);
    assert!(r.is_err());
}

#[test]
fn add_and_remove_callers() {
    let (env, _owner, _token_addr, _admin, client) = setup();

    let a = Address::generate(&env);
    let b = Address::generate(&env);
    client.add_allowed_callers(&vec![&env, a.clone(), b.clone()]);

    let callers = client.get_allowed_callers();
    assert_eq!(callers.len(), 2);

    client.remove_allowed_callers(&vec![&env, a.clone()]);
    let callers2 = client.get_allowed_callers();
    assert_eq!(callers2.len(), 1);
    assert_eq!(callers2.get(0).unwrap(), b);
}

#[test]
fn deposit_zero_rejected() {
    let (env, _owner, _token_addr, _admin, client) = setup();

    let pool = Address::generate(&env);
    client.add_allowed_callers(&vec![&env, pool.clone()]);

    let r = client.try_deposit(&pool, &0);
    assert!(r.is_err());
}

// ============================================================
// Upgrade tests (shared `common_authorization::Upgradeable` opt-in)
// ============================================================

// Reuse the checked-in data-feeds fixture that exposes `peek() -> u32`.
// TokenLockBox has no `peek`, so a successful `peek` at the lockbox address
// after `upgrade` proves the executable was swapped in place. No `stellar` CLI
// in this env, so we reuse this fixture instead of adding one.
const UPGRADE_TARGET_WASM: &[u8] =
    include_bytes!("../../../data-feeds/data-feeds-common/test_fixtures/upgrade_target.wasm");

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
    let (env, _owner, _token_addr, _admin, client) = setup();

    // `setup` mocked all auths, so `require_owner` -> `owner.require_auth()` passes.
    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    client.upgrade(&hash);

    // The Upgraded event carries the new Wasm hash.
    assert_eq!(upgraded_event_hash(&env, &client.address), hash);

    // TokenLockBox has no `peek`; the fixture does. A successful `peek` at the
    // lockbox address proves the executable was swapped in place. Instance
    // storage is preserved by `update_current_contract_wasm` (host guarantee),
    // and the lockbox never wrote the fixture's "slot" key, so peek reads back 0.
    let peeked: u32 = env.invoke_contract(
        &client.address,
        &symbol_short!("peek"),
        Vec::<Val>::new(&env),
    );
    assert_eq!(
        peeked, 0,
        "fixture peek must run at the token lock box address after upgrade"
    );
}

#[test]
#[should_panic(expected = "Error(Auth, InvalidAction)")]
fn test_upgrade_by_non_owner_rejected() {
    let env = Env::default();
    // No `mock_all_auths`: nobody is authorized, so `require_owner` ->
    // `owner.require_auth()` fails. `initialize` needs no auth (it only stores
    // its args + guards against double-init), so the contract is set up with
    // the owner stored.
    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_addr = token_contract.address();

    let contract_id = env.register(TokenLockBox, ());
    let client = TokenLockBoxClient::new(&env, &contract_id);
    client.initialize(&owner, &token_addr);

    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    // Caller is not the owner and the owner does not authorize -> reject before
    // the executable is touched.
    client.upgrade(&hash);
}
