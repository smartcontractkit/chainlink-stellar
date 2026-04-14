#![cfg(test)]

use soroban_sdk::{testutils::Address as _, testutils::Ledger, token, Address, Bytes, Env, Vec};

use crate::{BurnMintTokenPoolContract, BurnMintTokenPoolContractClient};
use common_error::CCIPError;
use common_pool::{
    encode_local_decimals, ChainUpdate, LockOrBurnIn, RateLimitConfig, ReleaseOrMintIn,
};

fn setup_env() -> (
    Env,
    BurnMintTokenPoolContractClient<'static>,
    Address,
    Address,
    token::Client<'static>,
    token::StellarAssetClient<'static>,
) {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let pool_id = env.register(BurnMintTokenPoolContract, ());
    let pool_client = BurnMintTokenPoolContractClient::new(&env, &pool_id);

    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_client = token::Client::new(&env, &token_address);
    let token_admin_client = token::StellarAssetClient::new(&env, &token_address);

    // Set the pool contract as the token admin so it can mint
    token_admin_client.set_admin(&pool_id);

    pool_client.initialize(&owner, &token_address, &7u32);

    (
        env,
        pool_client,
        owner,
        token_address,
        token_client,
        token_admin_client,
    )
}

#[test]
fn test_initialize() {
    let (env, pool_client, _owner, token_address, _token_client, _token_admin_client) = setup_env();

    let pool_token = pool_client.get_token();
    assert_eq!(pool_token, token_address);
    assert_eq!(pool_client.get_token_decimals(), 7);

    assert!(pool_client.is_supported_token(&token_address));
    let other_token = Address::generate(&env);
    assert!(!pool_client.is_supported_token(&other_token));
}

#[test]
fn test_burn_and_mint() {
    let (env, pool_client, _owner, token_address, token_client, _token_admin_client) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    let remote_pool = Bytes::from_slice(&env, &[1u8; 20]);
    let remote_token = Bytes::from_slice(&env, &[2u8; 20]);

    let chain_update = ChainUpdate {
        remote_chain_selector: remote_chain,
        remote_pool_addresses: remote_pool,
        remote_token_address: remote_token.clone(),
        outbound_rate_limiter_config: RateLimitConfig::disabled(),
        inbound_rate_limiter_config: RateLimitConfig::disabled(),
    };
    pool_client.apply_chain_updates(&Vec::from_array(&env, [chain_update]), &Vec::new(&env));

    let sender = Address::generate(&env);
    let burn_amount: i128 = 1_000_000_000;
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    sac_client.mint(&sender, &burn_amount);
    assert_eq!(token_client.balance(&sender), burn_amount);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: burn_amount,
        local_token: token_address.clone(),
    };

    let burn_result = pool_client.lock_or_burn(&lock_input, &0u32);
    assert_eq!(burn_result.dest_token_address, remote_token);
    assert_eq!(token_client.balance(&sender), 0);

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: burn_amount,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };

    let mint_result = pool_client.release_or_mint(&release_input, &0u32);
    assert_eq!(mint_result.destination_amount, burn_amount);
    assert_eq!(token_client.balance(&receiver), burn_amount);
}

#[test]
fn test_unsupported_chain_rejected() {
    let (env, pool_client, _owner, token_address, _token_client, _token_admin_client) = setup_env();

    let sender = Address::generate(&env);
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    sac_client.mint(&sender, &1_000_000_000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[1u8; 20]),
        remote_chain_selector: 999,
        original_sender: sender,
        amount: 100,
        local_token: token_address,
    };

    let result = pool_client.try_lock_or_burn(&lock_input, &0u32);
    assert!(result.is_err());
}

#[test]
fn test_wrong_token_rejected() {
    let (env, pool_client, _owner, _token_address, _token_client, _token_admin_client) =
        setup_env();

    let wrong_token = Address::generate(&env);
    let sender = Address::generate(&env);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[1u8; 20]),
        remote_chain_selector: 1,
        original_sender: sender,
        amount: 100,
        local_token: wrong_token,
    };

    let result = pool_client.try_lock_or_burn(&lock_input, &0u32);
    assert!(result.is_err());
}

fn chain_update(env: &Env, selector: u64, pool_byte: u8, token_byte: u8) -> ChainUpdate {
    ChainUpdate {
        remote_chain_selector: selector,
        remote_pool_addresses: Bytes::from_slice(env, &[pool_byte; 20]),
        remote_token_address: Bytes::from_slice(env, &[token_byte; 20]),
        outbound_rate_limiter_config: RateLimitConfig::disabled(),
        inbound_rate_limiter_config: RateLimitConfig::disabled(),
    }
}

fn chain_update_with_limits(
    env: &Env,
    selector: u64,
    pool_byte: u8,
    token_byte: u8,
    outbound: RateLimitConfig,
    inbound: RateLimitConfig,
) -> ChainUpdate {
    ChainUpdate {
        remote_chain_selector: selector,
        remote_pool_addresses: Bytes::from_slice(env, &[pool_byte; 20]),
        remote_token_address: Bytes::from_slice(env, &[token_byte; 20]),
        outbound_rate_limiter_config: outbound,
        inbound_rate_limiter_config: inbound,
    }
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")] // AlreadyInitialized
fn test_initialize_twice_rejected() {
    let (_env, pool_client, owner, token_address, _token_client, _token_admin_client) = setup_env();
    pool_client.initialize(&owner, &token_address, &7u32);
}

#[test]
fn test_lock_or_burn_zero_amount_succeeds_when_chain_configured() {
    let (env, pool_client, _owner, token_address, token_client, _token_admin_client) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 0,
        local_token: token_address.clone(),
    };

    let out = pool_client.lock_or_burn(&lock_input, &0u32);
    assert_eq!(out.dest_token_address, Bytes::from_slice(&env, &[2u8; 20]));
    assert_eq!(token_client.balance(&sender), 0);
}

#[test]
fn test_release_or_mint_zero_amount_succeeds() {
    let (env, pool_client, _owner, token_address, token_client, _token_admin_client) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 0,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };

    let out = pool_client.release_or_mint(&release_input, &0u32);
    assert_eq!(out.destination_amount, 0);
    assert_eq!(token_client.balance(&receiver), 0);
}

#[test]
fn test_lock_or_burn_amount_exceeds_sender_balance_fails() {
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &100);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 101,
        local_token: token_address,
    };

    let result = pool_client.try_lock_or_burn(&lock_input, &0u32);
    assert!(result.is_err());
}

#[test]
fn test_lock_or_burn_negative_amount_fails() {
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1_000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: -1,
        local_token: token_address,
    };

    let result = pool_client.try_lock_or_burn(&lock_input, &0u32);
    assert!(result.is_err());
}

#[test]
fn test_apply_chain_updates_remove_unlists_chain() {
    let (env, pool_client, _owner, token_address, _token_client, _token_admin_client) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    assert!(pool_client.is_supported_chain(&remote_chain));

    pool_client.apply_chain_updates(&Vec::new(&env), &Vec::from_array(&env, [remote_chain]));
    assert!(!pool_client.is_supported_chain(&remote_chain));

    let sender = Address::generate(&env);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 1,
        local_token: token_address.clone(),
    };
    let result = pool_client.try_lock_or_burn(&lock_input, &0u32);
    assert_eq!(result, Err(Ok(CCIPError::ChainNotSupported)));

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 9, 8)]),
        &Vec::new(&env),
    );
    assert!(pool_client.is_supported_chain(&remote_chain));
    assert_eq!(
        pool_client.get_remote_token(&remote_chain),
        Bytes::from_slice(&env, &[8u8; 20])
    );
}

#[test]
fn test_apply_chain_updates_duplicate_selector_overwrites_remote_token() {
    let (env, pool_client, _owner, token_address, _token_client, _token_admin_client) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 3, 4)]),
        &Vec::new(&env),
    );
    assert_eq!(
        pool_client.get_remote_token(&remote_chain),
        Bytes::from_slice(&env, &[4u8; 20])
    );

    let sender = Address::generate(&env);
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    sac_client.mint(&sender, &1);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 1,
        local_token: token_address,
    };
    assert!(pool_client.try_lock_or_burn(&lock_input, &0u32).is_ok());
}

#[test]
fn test_lock_or_burn_dest_pool_data_encodes_local_decimals() {
    let (env, pool_client, _owner, token_address, token_client, token_admin_client) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &100);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 100,
        local_token: token_address,
    };
    let out = pool_client.lock_or_burn(&lock_input, &0u32);
    let expected = encode_local_decimals(&env, 7).unwrap();
    assert_eq!(out.dest_pool_data, expected);
    assert_eq!(token_client.balance(&sender), 0);
}

#[test]
fn test_release_or_mint_scales_down_remote_more_decimals() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let pool_id = env.register(BurnMintTokenPoolContract, ());
    let pool_client = BurnMintTokenPoolContractClient::new(&env, &pool_id);
    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_client = token::Client::new(&env, &token_address);
    let token_admin_client = token::StellarAssetClient::new(&env, &token_address);
    token_admin_client.set_admin(&pool_id);

    let local_decimals: u32 = 6;
    pool_client.initialize(&owner, &token_address, &local_decimals);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let remote_decimals: u32 = 9;
    let source_amount: i128 = 1_000_000_000; // 1e9 in 9dp
    let expected_local: i128 = 1_000_000; // 1e6 in 6dp

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: source_amount,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: encode_local_decimals(&env, remote_decimals).unwrap(),
    };

    let out = pool_client.release_or_mint(&release_input, &0u32);
    assert_eq!(out.destination_amount, expected_local);
    assert_eq!(token_client.balance(&receiver), expected_local);
}

#[test]
fn test_release_or_mint_scales_up_remote_fewer_decimals() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let pool_id = env.register(BurnMintTokenPoolContract, ());
    let pool_client = BurnMintTokenPoolContractClient::new(&env, &pool_id);
    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_client = token::Client::new(&env, &token_address);
    let token_admin_client = token::StellarAssetClient::new(&env, &token_address);
    token_admin_client.set_admin(&pool_id);

    let local_decimals: u32 = 9;
    pool_client.initialize(&owner, &token_address, &local_decimals);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let remote_decimals: u32 = 6;
    let source_amount: i128 = 1_000_000;
    let expected_local: i128 = 1_000_000_000;

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: source_amount,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: encode_local_decimals(&env, remote_decimals).unwrap(),
    };

    let out = pool_client.release_or_mint(&release_input, &0u32);
    assert_eq!(out.destination_amount, expected_local);
    assert_eq!(token_client.balance(&receiver), expected_local);
}

#[test]
fn test_release_or_mint_invalid_source_pool_data_length() {
    let (env, pool_client, _owner, token_address, _token_client, _token_admin_client) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: Address::generate(&env),
        amount: 100,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::from_slice(&env, &[1u8; 31]),
    };

    let e = pool_client
        .try_release_or_mint(&release_input, &0u32)
        .unwrap_err()
        .unwrap();
    assert_eq!(e, CCIPError::InvalidRemoteChainDecimals);
}

#[test]
fn test_initialize_rejects_decimals_above_uint8() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let pool_id = env.register(BurnMintTokenPoolContract, ());
    let pool_client = BurnMintTokenPoolContractClient::new(&env, &pool_id);
    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin);
    let token_address = token_contract.address();

    let r = pool_client.try_initialize(&owner, &token_address, &256u32);
    assert_eq!(r, Err(Ok(CCIPError::InvalidPoolTokenDecimals)));
}

// ================================================================
//  Rate Limit Tests
// ================================================================

#[test]
fn test_lock_or_burn_disabled_rate_limit_passes() {
    let (env, pool_client, _owner, token_address, token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1_000_000);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 1_000_000,
        local_token: token_address,
    };
    pool_client.lock_or_burn(&lock_input, &0u32);
    assert_eq!(token_client.balance(&sender), 0);
}

#[test]
fn test_lock_or_burn_within_outbound_rate_limit() {
    let (env, pool_client, _owner, token_address, token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                RateLimitConfig::disabled(),
            )],
        ),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &2000);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 500,
        local_token: token_address,
    };
    pool_client.lock_or_burn(&lock_input, &0u32);
    assert_eq!(token_client.balance(&sender), 1500);
}

#[test]
fn test_lock_or_burn_exceeds_outbound_capacity_rejected() {
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 500,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                RateLimitConfig::disabled(),
            )],
        ),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1000);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 501,
        local_token: token_address,
    };
    let r = pool_client.try_lock_or_burn(&lock_input, &0u32);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenMaxCapacityExceeded);
}

#[test]
fn test_lock_or_burn_exceeds_available_tokens_rejected() {
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                RateLimitConfig::disabled(),
            )],
        ),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &2000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 800,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&lock_input, &0u32);

    // 200 tokens left, try to burn 201
    let lock_input2 = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 201,
        local_token: token_address,
    };
    let r = pool_client.try_lock_or_burn(&lock_input2, &0u32);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);
}

#[test]
fn test_lock_or_burn_refills_over_time() {
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                RateLimitConfig::disabled(),
            )],
        ),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &5000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 1000,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&lock_input, &0u32);

    // Advance 50 seconds => refill 500
    env.ledger().with_mut(|li| li.timestamp = 150);
    let lock_input2 = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 500,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&lock_input2, &0u32);

    // Try to burn 1 more — should fail (0 tokens remaining)
    let lock_input3 = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 1,
        local_token: token_address,
    };
    let r = pool_client.try_lock_or_burn(&lock_input3, &0u32);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);
}

#[test]
fn test_release_or_mint_within_inbound_rate_limit() {
    let (env, pool_client, _owner, token_address, token_client, _token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                RateLimitConfig::disabled(),
                inbound,
            )],
        ),
        &Vec::new(&env),
    );

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 500,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    pool_client.release_or_mint(&release_input, &0u32);
    assert_eq!(token_client.balance(&receiver), 500);
}

#[test]
fn test_release_or_mint_exceeds_inbound_capacity_rejected() {
    let (env, pool_client, _owner, token_address, _token_client, _token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 500,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                RateLimitConfig::disabled(),
                inbound,
            )],
        ),
        &Vec::new(&env),
    );

    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: Address::generate(&env),
        amount: 501,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    let r = pool_client.try_release_or_mint(&release_input, &0u32);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenMaxCapacityExceeded);
}

#[test]
fn test_release_or_mint_inbound_refills_over_time() {
    let (env, pool_client, _owner, token_address, token_client, _token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                RateLimitConfig::disabled(),
                inbound,
            )],
        ),
        &Vec::new(&env),
    );

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 1000,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    pool_client.release_or_mint(&release_input, &0u32);

    // Advance 30s => refill 300
    env.ledger().with_mut(|li| li.timestamp = 130);
    let release_input2 = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 300,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    pool_client.release_or_mint(&release_input2, &0u32);
    assert_eq!(token_client.balance(&receiver), 1300);
}

#[test]
fn test_get_current_rate_limiter_state() {
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 2000,
        rate: 20,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                inbound,
            )],
        ),
        &Vec::new(&env),
    );

    let state = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(state.outbound.is_enabled);
    assert_eq!(state.outbound.capacity, 1000);
    assert_eq!(state.outbound.tokens, 1000);
    assert!(state.inbound.is_enabled);
    assert_eq!(state.inbound.capacity, 2000);
    assert_eq!(state.inbound.tokens, 2000);

    // Consume some outbound
    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &500);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 500,
        local_token: token_address,
    };
    pool_client.lock_or_burn(&lock_input, &0u32);

    let state2 = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert_eq!(state2.outbound.tokens, 500);
    assert_eq!(state2.inbound.tokens, 2000);
}

#[test]
fn test_set_rate_limit_config_updates_limits() {
    let (env, pool_client, owner, token_address, _token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    // Initially disabled
    let state = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(!state.outbound.is_enabled);

    // Enable via set_rate_limit_config
    let new_outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 500,
        rate: 5,
    };
    pool_client.set_rate_limit_config(
        &owner,
        &remote_chain,
        &new_outbound,
        &RateLimitConfig::disabled(),
        &false,
    );

    let state2 = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(state2.outbound.is_enabled);
    assert_eq!(state2.outbound.capacity, 500);
    assert_eq!(state2.outbound.tokens, 500);

    // Verify it enforces the limit
    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1000);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 501,
        local_token: token_address,
    };
    let r = pool_client.try_lock_or_burn(&lock_input, &0u32);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenMaxCapacityExceeded);
}

#[test]
fn test_set_rate_limit_admin_and_admin_can_set_config() {
    let (env, pool_client, owner, _token_address, _token_client, _token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    assert!(pool_client.get_rate_limit_admin().is_none());

    let admin = Address::generate(&env);
    pool_client.set_rate_limit_admin(&admin);
    assert_eq!(pool_client.get_rate_limit_admin().unwrap(), admin);

    let cfg = RateLimitConfig {
        is_enabled: true,
        capacity: 100,
        rate: 1,
    };
    pool_client.set_rate_limit_config(
        &admin,
        &remote_chain,
        &cfg,
        &RateLimitConfig::disabled(),
        &false,
    );

    let state = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(state.outbound.is_enabled);
    assert_eq!(state.outbound.capacity, 100);

    // Owner can still set rate limits
    pool_client.set_rate_limit_config(
        &owner,
        &remote_chain,
        &RateLimitConfig::disabled(),
        &RateLimitConfig::disabled(),
        &false,
    );

    let state2 = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(!state2.outbound.is_enabled);
}

#[test]
fn test_set_rate_limit_config_unauthorized_rejected() {
    let (env, pool_client, _owner, _token_address, _token_client, _token_admin_client) =
        setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let stranger = Address::generate(&env);
    let r = pool_client.try_set_rate_limit_config(
        &stranger,
        &remote_chain,
        &RateLimitConfig::disabled(),
        &RateLimitConfig::disabled(),
        &false,
    );
    assert_eq!(r, Err(Ok(CCIPError::Unauthorized)));
}

#[test]
fn test_chain_remove_clears_rate_limits() {
    let (env, pool_client, _owner, _token_address, _token_client, _token_admin_client) =
        setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                RateLimitConfig::disabled(),
            )],
        ),
        &Vec::new(&env),
    );

    let state = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(state.outbound.is_enabled);

    // Remove the chain
    pool_client.apply_chain_updates(&Vec::new(&env), &Vec::from_array(&env, [remote_chain]));

    // Rate limit state should be gone (defaults to disabled)
    let state2 = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(!state2.outbound.is_enabled);
    assert_eq!(state2.outbound.tokens, 0);
}

#[test]
fn test_both_outbound_and_inbound_limits_enforced() {
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 500,
        rate: 5,
    };
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 300,
        rate: 3,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                inbound,
            )],
        ),
        &Vec::new(&env),
    );

    // Outbound: burn 400 (within 500 capacity)
    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1000);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 400,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&lock_input, &0u32);

    // Inbound: mint 300 (exactly capacity)
    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 300,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    pool_client.release_or_mint(&release_input, &0u32);

    // Inbound: 1 more should fail (0 tokens remaining, no time elapsed)
    let release_input2 = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver,
        amount: 1,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    let r = pool_client.try_release_or_mint(&release_input2, &0u32);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);
}
