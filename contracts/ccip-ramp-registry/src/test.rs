#![cfg(test)]

use soroban_sdk::{
    symbol_short, testutils::Address as _, testutils::Events as _, Address, BytesN, Env, Map,
    Symbol, TryFromVal, TryIntoVal, Val, Vec,
};

use crate::types::{OffRampUpdate, OnRampUpdate};
use crate::{RampRegistryContract, RampRegistryContractClient};
use common_error::CCIPError;

#[test]
fn test_ramp_lifecycle() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let onramp = Address::generate(&env);
    let offramp = Address::generate(&env);

    let id = env.register(RampRegistryContract, ());
    let client = RampRegistryContractClient::new(&env, &id);
    client.initialize(&owner);

    let dest: u64 = 100;
    let src: u64 = 200;

    client.apply_onramp_updates(&Vec::from_array(
        &env,
        [OnRampUpdate {
            dest_chain_selector: dest,
            onramp: Some(onramp.clone()),
        }],
    ));
    assert_eq!(client.get_onramp(&dest), onramp);

    client.apply_offramp_updates(&Vec::from_array(
        &env,
        [OffRampUpdate {
            source_chain_selector: src,
            offramp: offramp.clone(),
            enabled: true,
        }],
    ));
    assert!(client.is_offramp(&src, &offramp));

    client.apply_offramp_updates(&Vec::from_array(
        &env,
        [OffRampUpdate {
            source_chain_selector: src,
            offramp: offramp.clone(),
            enabled: false,
        }],
    ));
    assert!(!client.is_offramp(&src, &offramp));
}

#[test]
fn test_apply_ramp_updates() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let o1 = Address::generate(&env);
    let o2 = Address::generate(&env);
    let f = Address::generate(&env);

    let id = env.register(RampRegistryContract, ());
    let client = RampRegistryContractClient::new(&env, &id);
    client.initialize(&owner);

    client.apply_offramp_updates(&Vec::from_array(
        &env,
        [OffRampUpdate {
            source_chain_selector: 300,
            offramp: f.clone(),
            enabled: true,
        }],
    ));
    assert!(client.is_offramp(&300, &f));

    client.apply_onramp_updates(&Vec::from_array(
        &env,
        [OnRampUpdate {
            dest_chain_selector: 1,
            onramp: Some(o1.clone()),
        }],
    ));

    client.apply_offramp_updates(&Vec::from_array(
        &env,
        [
            OffRampUpdate {
                source_chain_selector: 300,
                offramp: f.clone(),
                enabled: false,
            },
            OffRampUpdate {
                source_chain_selector: 400,
                offramp: o2.clone(),
                enabled: true,
            },
        ],
    ));

    assert_eq!(client.get_onramp(&1), o1);
    assert!(!client.is_offramp(&300, &f));
    assert!(client.is_offramp(&400, &o2));
}

#[test]
fn test_get_onramp_missing_chain() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let id = env.register(RampRegistryContract, ());
    let client = RampRegistryContractClient::new(&env, &id);
    client.initialize(&owner);

    let r = client.try_get_onramp(&999u64);
    assert_eq!(r, Err(Ok(CCIPError::UnsupportedDestinationChain)));
}

#[test]
fn test_apply_onramp_rejects_zero_selector() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let onramp = Address::generate(&env);

    let id = env.register(RampRegistryContract, ());
    let client = RampRegistryContractClient::new(&env, &id);
    client.initialize(&owner);

    let r = client.try_apply_onramp_updates(&Vec::from_array(
        &env,
        [OnRampUpdate {
            dest_chain_selector: 0,
            onramp: Some(onramp),
        }],
    ));
    assert_eq!(r, Err(Ok(CCIPError::InvalidChainSelector)));
}

#[test]
fn test_apply_offramp_rejects_zero_selector() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let offramp = Address::generate(&env);

    let id = env.register(RampRegistryContract, ());
    let client = RampRegistryContractClient::new(&env, &id);
    client.initialize(&owner);

    let r = client.try_apply_offramp_updates(&Vec::from_array(
        &env,
        [OffRampUpdate {
            source_chain_selector: 0,
            offramp,
            enabled: true,
        }],
    ));
    assert_eq!(r, Err(Ok(CCIPError::InvalidChainSelector)));
}

// ===========================================================================
// In-place upgradeability (shared `common_authorization::Upgradeable` trait)
// ===========================================================================
//
// Reuses the checked-in data-feeds fixture whose only entrypoint is
// `peek() -> u32`. RampRegistry has no `peek`, so a successful `peek` call at
// the RampRegistry address after `upgrade` proves the executable was swapped
// in place (address + storage preserved, only the Wasm backing replaced).
const UPGRADE_TARGET_WASM: &[u8] =
    include_bytes!("../../data-feeds/data-feeds-common/test_fixtures/upgrade_target.wasm");

/// Decode the `new_wasm_hash` field from the last `Upgraded` event emitted by
/// `contract`. `#[contractevent]` serializes the struct as a `Map<Symbol, Val>`
/// keyed by field name, so we match on the field name (not the topic) — this
/// stays correct whether the event topic is `onramp_1_7_Upgraded` or the shared
/// trait's `Upgraded`.
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
    panic!("expected Upgraded event with new_wasm_hash from ramp registry");
}

#[test]
fn test_upgrade_by_owner_swaps_executable_and_emits_event() {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let id = env.register(RampRegistryContract, ());
    let client = RampRegistryContractClient::new(&env, &id);
    client.initialize(&owner);

    // Upload the fixture Wasm and upgrade RampRegistry to it. `mock_all_auths`
    // satisfies `require_owner` -> `owner.require_auth()`.
    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    client.upgrade(&hash);

    // The `Upgraded` event was published carrying the new Wasm hash.
    assert_eq!(upgraded_event_hash(&env, &client.address), hash);

    // RampRegistry has no `peek`; the fixture does. A successful `peek` at the
    // RampRegistry address proves the executable was swapped in place.
    let peeked: u32 = env.invoke_contract(
        &client.address,
        &symbol_short!("peek"),
        Vec::<Val>::new(&env),
    );
    assert_eq!(
        peeked, 0,
        "fixture peek must run at the ramp registry address after upgrade"
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
    let id = env.register(RampRegistryContract, ());
    let client = RampRegistryContractClient::new(&env, &id);
    client.initialize(&owner);

    // A hash need not correspond to a real uploaded Wasm here — the auth check
    // runs first and panics, so `update_current_contract_wasm` is never reached.
    let hash = BytesN::<32>::from_array(&env, &[0u8; 32]);
    client.upgrade(&hash);
}
