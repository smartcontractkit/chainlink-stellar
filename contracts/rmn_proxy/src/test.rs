#![cfg(test)]

use super::*;
use rmn_remote::{RmnRemoteContract, RmnRemoteContractClient};
use soroban_sdk::{testutils::Address as _, vec, Address, BytesN, Env};

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
    rmn_remote_client.initialize(&owner, &1u64);

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
    rmn_remote_client.curse(&vec![&env, global]);

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
    rmn_remote_client.curse(&vec![&env, global.clone()]);
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
    rmn2_client.initialize(&owner, &2u64);

    client.initialize(&owner, &rmn1);
    assert!(!client.is_cursed());

    // Curse the first remote
    let rmn1_client = RmnRemoteContractClient::new(&env, &rmn1);
    let global = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
    rmn1_client.curse(&vec![&env, global]);
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
