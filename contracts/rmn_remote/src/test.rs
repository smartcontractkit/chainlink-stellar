#![cfg(test)]

use soroban_sdk::testutils::Address as _;
use soroban_sdk::{vec, Address, BytesN, Env, Vec};

use crate::types::{Config, Signer};
use crate::{RmnRemoteContract, RmnRemoteContractClient};

const GLOBAL_CURSE_SUBJECT: [u8; 16] = [
    0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
];

fn setup_contract(env: &Env) -> (RmnRemoteContractClient, Address) {
    let contract_id = env.register(RmnRemoteContract, ());
    let client = RmnRemoteContractClient::new(env, &contract_id);
    let owner = Address::generate(env);

    env.mock_all_auths();
    client.initialize(&owner, &12345u64);

    (client, owner)
}

fn make_config(env: &Env, num_signers: u32, f_sign: u64) -> Config {
    let mut signers: Vec<Signer> = Vec::new(env);
    for i in 0..num_signers {
        signers.push_back(Signer {
            onchain_pub_key: BytesN::from_array(env, &[i as u8 + 1; 32]),
            node_index: i as u64,
        });
    }
    Config {
        rmn_home_config_digest: BytesN::from_array(env, &[0xAA; 32]),
        signers,
        f_sign,
    }
}

// ============================================================
// Initialization
// ============================================================

#[test]
fn test_initialize() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);
    assert_eq!(client.get_local_chain_selector(), 12345u64);
    assert!(!client.is_cursed());
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")]
fn test_double_initialize_fails() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);
    let owner2 = Address::generate(&env);
    client.initialize(&owner2, &99999u64);
}

// ============================================================
// Configuration
// ============================================================

#[test]
fn test_set_config() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    let config = make_config(&env, 3, 1); // 3 signers, f=1 (needs 2f+1=3)
    client.set_config(&config);

    let (version, stored_config) = client.get_versioned_config();
    assert_eq!(version, 1u32);
    assert_eq!(stored_config.f_sign, 1);
    assert_eq!(stored_config.signers.len(), 3);
}

#[test]
#[should_panic(expected = "Error(Contract, #73)")]
fn test_set_config_zero_digest_fails() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    let config = Config {
        rmn_home_config_digest: BytesN::from_array(&env, &[0u8; 32]),
        signers: vec![
            &env,
            Signer {
                onchain_pub_key: BytesN::from_array(&env, &[1u8; 32]),
                node_index: 0,
            },
        ],
        f_sign: 0,
    };
    client.set_config(&config);
}

#[test]
#[should_panic(expected = "Error(Contract, #68)")]
fn test_set_config_not_enough_signers_fails() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    // f=1 requires 2*1+1=3 signers, but we only provide 2
    let config = make_config(&env, 2, 1);
    client.set_config(&config);
}

#[test]
#[should_panic(expected = "Error(Contract, #67)")]
fn test_set_config_out_of_order_signers_fails() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    let config = Config {
        rmn_home_config_digest: BytesN::from_array(&env, &[0xAA; 32]),
        signers: vec![
            &env,
            Signer {
                onchain_pub_key: BytesN::from_array(&env, &[1u8; 32]),
                node_index: 5,
            },
            Signer {
                onchain_pub_key: BytesN::from_array(&env, &[2u8; 32]),
                node_index: 3, // out of order
            },
            Signer {
                onchain_pub_key: BytesN::from_array(&env, &[3u8; 32]),
                node_index: 7,
            },
        ],
        f_sign: 1,
    };
    client.set_config(&config);
}

#[test]
fn test_config_version_increments() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    let config1 = make_config(&env, 3, 1);
    client.set_config(&config1);
    let (v1, _) = client.get_versioned_config();
    assert_eq!(v1, 1);

    let config2 = make_config(&env, 5, 2);
    client.set_config(&config2);
    let (v2, _) = client.get_versioned_config();
    assert_eq!(v2, 2);
}

// ============================================================
// Cursing
// ============================================================

#[test]
fn test_curse_and_uncurse() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    let subject = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
    let subjects = vec![&env, subject.clone()];

    assert!(!client.is_cursed());

    client.curse(&subjects);
    assert!(client.is_cursed());

    let cursed = client.get_cursed_subjects();
    assert_eq!(cursed.len(), 1);

    client.uncurse(&subjects);
    assert!(!client.is_cursed());

    let cursed = client.get_cursed_subjects();
    assert_eq!(cursed.len(), 0);
}

#[test]
fn test_curse_specific_subject() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    let lane_subject = BytesN::from_array(&env, &[0x02; 16]);
    let subjects = vec![&env, lane_subject.clone()];

    // Global is_cursed should still be false
    assert!(!client.is_cursed());

    client.curse(&subjects);

    // Global is_cursed is still false (only specific subject is cursed)
    assert!(!client.is_cursed());

    // Subject-specific check should be true
    assert!(client.is_cursed_by_subject(&lane_subject));

    // Different subject should not be cursed
    let other = BytesN::from_array(&env, &[0x03; 16]);
    assert!(!client.is_cursed_by_subject(&other));
}

#[test]
fn test_global_curse_affects_all_subjects() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    let global = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
    client.curse(&vec![&env, global.clone()]);

    // Any subject check should return true when global is cursed
    let arbitrary = BytesN::from_array(&env, &[0xFF; 16]);
    assert!(client.is_cursed_by_subject(&arbitrary));
    assert!(client.is_cursed());
}

#[test]
#[should_panic(expected = "Error(Contract, #64)")]
fn test_curse_already_cursed_fails() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    let subject = BytesN::from_array(&env, &[0x05; 16]);
    let subjects = vec![&env, subject.clone()];

    client.curse(&subjects);
    client.curse(&subjects); // should fail
}

#[test]
#[should_panic(expected = "Error(Contract, #69)")]
fn test_uncurse_not_cursed_fails() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    let subject = BytesN::from_array(&env, &[0x05; 16]);
    let subjects = vec![&env, subject.clone()];

    client.uncurse(&subjects); // should fail, not cursed
}

#[test]
fn test_multiple_subjects_curse() {
    let env = Env::default();
    let (client, _owner) = setup_contract(&env);

    let s1 = BytesN::from_array(&env, &[0x01; 16]);
    let s2 = BytesN::from_array(&env, &[0x02; 16]);
    let subjects = vec![&env, s1.clone(), s2.clone()];

    client.curse(&subjects);

    let cursed = client.get_cursed_subjects();
    assert_eq!(cursed.len(), 2);
    assert!(client.is_cursed_by_subject(&s1));
    assert!(client.is_cursed_by_subject(&s2));

    // Uncurse only s1
    client.uncurse(&vec![&env, s1.clone()]);
    assert!(!client.is_cursed_by_subject(&s1));
    assert!(client.is_cursed_by_subject(&s2));
}
