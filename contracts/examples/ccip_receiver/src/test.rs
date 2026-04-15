#![cfg(test)]

extern crate std;

use soroban_sdk::{testutils::Address as _, vec, Address, Bytes, BytesN, Env};

use crate::{ExampleCcipReceiver, ExampleCcipReceiverClient};

use common_interfaces::ccip_receiver::{CcvChainConfig, CcvConfigUpdate};
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
    // optional_threshold >= optional.len() is invalid (EVM parity)
    let upd = CcvConfigUpdate {
        source_chain_selector: 42,
        required_ccvs: vec![&env],
        optional_ccvs: vec![&env, o1, o2],
        optional_threshold: 2,
    };
    let r = client.try_apply_ccv_config_updates(&owner, &vec![&env, upd]);
    assert!(r.is_err());
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
