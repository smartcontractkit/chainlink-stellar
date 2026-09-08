#![cfg(test)]

use soroban_sdk::testutils::Address as _;
use soroban_sdk::{vec, Address, BytesN, Env, Vec};

use crate::{RmnRemoteContract, RmnRemoteContractClient};

const GLOBAL_CURSE_SUBJECT: [u8; 16] = [
    0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
];

fn setup_contract(env: &Env) -> (RmnRemoteContractClient, Address) {
    let contract_id = env.register(RmnRemoteContract, ());
    let client = RmnRemoteContractClient::new(env, &contract_id);
    let owner = Address::generate(env);

    env.mock_all_auths();
    client.initialize(&owner, &Vec::new(env));

    (client, owner)
}

#[test]
fn test_initialize() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);
    assert!(!client.is_cursed());
    assert_eq!(
        client.type_and_version(),
        soroban_sdk::String::from_str(&env, "RMN-dev 2.0.0")
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")]
fn test_double_initialize_fails() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);
    let owner2 = Address::generate(&env);
    client.initialize(&owner2, &Vec::new(&env));
}

#[test]
fn test_curse_and_uncurse() {
    let env = Env::default();
    let (client, owner) = setup_contract(&env);

    let subject = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
    let subjects = vec![&env, subject.clone()];

    client.curse(&owner, &subjects);
    assert!(client.is_cursed());

    client.uncurse(&subjects);
    assert!(!client.is_cursed());
}

#[test]
fn test_curse_specific_subject() {
    let env = Env::default();
    let (client, owner) = setup_contract(&env);

    let lane_subject = BytesN::from_array(&env, &[0x02; 16]);
    let subjects = vec![&env, lane_subject.clone()];

    client.curse(&owner, &subjects);
    assert!(!client.is_cursed());
    assert!(client.is_cursed_by_subject(&lane_subject));
}

#[test]
fn test_global_curse_affects_all_subjects() {
    let env = Env::default();
    let (client, owner) = setup_contract(&env);

    let global = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
    client.curse(&owner, &vec![&env, global]);

    let arbitrary = BytesN::from_array(&env, &[0xFF; 16]);
    assert!(client.is_cursed_by_subject(&arbitrary));
    assert!(client.is_cursed());
}

#[test]
fn test_curse_recurse_silent_skip() {
    let env = Env::default();
    let (client, owner) = setup_contract(&env);

    let subject = BytesN::from_array(&env, &[0x05; 16]);
    let subjects = vec![&env, subject.clone()];

    client.curse(&owner, &subjects);
    client.curse(&owner, &subjects);

    let cursed = client.get_cursed_subjects();
    assert_eq!(cursed.len(), 1);
}

#[test]
fn test_curse_duplicate_in_batch_silent_skip() {
    let env = Env::default();
    let (client, owner) = setup_contract(&env);

    let subject = BytesN::from_array(&env, &[0x06; 16]);
    let subjects = vec![&env, subject.clone(), subject.clone()];

    client.curse(&owner, &subjects);

    let cursed = client.get_cursed_subjects();
    assert_eq!(cursed.len(), 1);
}

#[test]
#[should_panic(expected = "Error(Contract, #6)")]
fn test_curse_unauthorized_caller() {
    let env = Env::default();
    let contract_id = env.register(RmnRemoteContract, ());
    let client = RmnRemoteContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    env.mock_all_auths();
    client.initialize(&owner, &Vec::new(&env));
    env.mock_auths(&[]);

    let intruder = Address::generate(&env);
    let subject = BytesN::from_array(&env, &[0x07; 16]);
    client.curse(&intruder, &vec![&env, subject]);
}

#[test]
fn test_curse_by_curse_admin() {
    let env = Env::default();
    let contract_id = env.register(RmnRemoteContract, ());
    let client = RmnRemoteContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let curse_admin = Address::generate(&env);

    env.mock_all_auths();
    client.initialize(&owner, &vec![&env, curse_admin.clone()]);

    let subject = BytesN::from_array(&env, &[0x08; 16]);
    client.curse(&curse_admin, &vec![&env, subject.clone()]);
    assert!(client.is_cursed_by_subject(&subject));
}

#[test]
fn test_owner_can_curse_without_being_curse_admin() {
    let env = Env::default();
    let (client, owner) = setup_contract(&env);

    let admins = client.get_curse_admins();
    assert_eq!(admins.len(), 0);

    let subject = BytesN::from_array(&env, &[0x09; 16]);
    client.curse(&owner, &vec![&env, subject.clone()]);
    assert!(client.is_cursed_by_subject(&subject));
}

#[test]
fn test_apply_curse_admin_updates() {
    let env = Env::default();
    let (client, owner) = setup_contract(&env);

    let admin = Address::generate(&env);
    client.apply_curse_admin_updates(&vec![&env, admin.clone()], &Vec::new(&env));

    let admins = client.get_curse_admins();
    assert_eq!(admins.len(), 1);

    let subject = BytesN::from_array(&env, &[0x0A; 16]);
    client.curse(&admin, &vec![&env, subject.clone()]);
    assert!(client.is_cursed_by_subject(&subject));

    client.apply_curse_admin_updates(&Vec::new(&env), &vec![&env, admin.clone()]);
    assert_eq!(client.get_curse_admins().len(), 0);

    let _ = owner;
}

#[test]
#[should_panic(expected = "Error(Contract, #69)")]
fn test_uncurse_not_cursed_fails() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    let subject = BytesN::from_array(&env, &[0x05; 16]);
    client.uncurse(&vec![&env, subject]);
}

#[test]
fn test_multiple_subjects_curse() {
    let env = Env::default();
    let (client, owner) = setup_contract(&env);

    let s1 = BytesN::from_array(&env, &[0x01; 16]);
    let s2 = BytesN::from_array(&env, &[0x02; 16]);
    client.curse(&owner, &vec![&env, s1.clone(), s2.clone()]);

    assert_eq!(client.get_cursed_subjects().len(), 2);
    client.uncurse(&vec![&env, s1.clone()]);
    assert!(!client.is_cursed_by_subject(&s1));
    assert!(client.is_cursed_by_subject(&s2));
}
