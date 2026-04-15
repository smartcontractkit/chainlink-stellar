#![cfg(test)]

use soroban_sdk::{testutils::Address as _, testutils::Ledger, token, Address, Bytes, Env, Vec};

use crate::{LockReleaseTokenPoolContract, LockReleaseTokenPoolContractClient};
use common_error::CCIPError;
use common_pool::{
    encode_local_decimals, ChainUpdate, LockOrBurnIn, RateLimitConfig, ReleaseOrMintIn,
};

fn setup_env() -> (
    Env,
    LockReleaseTokenPoolContractClient<'static>,
    Address,
    Address,
    token::Client<'static>,
    token::StellarAssetClient<'static>,
) {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let pool_id = env.register(LockReleaseTokenPoolContract, ());
    let pool_client = LockReleaseTokenPoolContractClient::new(&env, &pool_id);

    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_client = token::Client::new(&env, &token_address);
    let token_admin_client = token::StellarAssetClient::new(&env, &token_address);

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
fn test_lock_and_release() {
    let (env, pool_client, _owner, token_address, token_client, token_admin_client) = setup_env();

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
    let lock_amount: i128 = 1_000_000_000;
    token_admin_client.mint(&sender, &lock_amount);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: lock_amount,
        local_token: token_address.clone(),
    };

    let lock_result = pool_client.lock_or_burn(&lock_input, &0u32);
    assert_eq!(lock_result.dest_token_address, remote_token);

    let pool_address = pool_client.address.clone();
    assert_eq!(token_client.balance(&pool_address), lock_amount);
    assert_eq!(token_client.balance(&sender), 0);

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: lock_amount,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };

    let release_result = pool_client.release_or_mint(&release_input, &0u32);
    assert_eq!(release_result.destination_amount, lock_amount);
    assert_eq!(token_client.balance(&receiver), lock_amount);
    assert_eq!(token_client.balance(&pool_address), 0);
}

#[test]
fn test_unsupported_chain_rejected() {
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1_000_000_000);

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
    assert_eq!(token_client.balance(&pool_client.address), 0);
    assert_eq!(token_client.balance(&sender), 0);
}

#[test]
fn test_release_or_mint_zero_amount_succeeds_without_pool_balance() {
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
fn test_release_or_mint_insufficient_pool_liquidity() {
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    let locked: i128 = 50;
    token_admin_client.mint(&sender, &locked);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: locked,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&lock_input, &0u32);

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver,
        amount: locked + 1,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };

    let result = pool_client.try_release_or_mint(&release_input, &0u32);
    assert_eq!(result, Err(Ok(CCIPError::InsufficientPoolLiquidity)));
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

    // Owner can re-add the same selector with fresh config
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
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    assert_eq!(
        pool_client.get_remote_token(&remote_chain),
        Bytes::from_slice(&env, &[2u8; 20])
    );

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 3, 4)]),
        &Vec::new(&env),
    );
    assert_eq!(
        pool_client.get_remote_token(&remote_chain),
        Bytes::from_slice(&env, &[4u8; 20])
    );
    assert_eq!(
        pool_client.get_remote_pool(&remote_chain),
        Bytes::from_slice(&env, &[3u8; 20])
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 1,
        local_token: token_address,
    };
    let out = pool_client.try_lock_or_burn(&lock_input, &0u32);
    assert!(out.is_ok());
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
        original_sender: sender,
        amount: 100,
        local_token: token_address,
    };
    let out = pool_client.lock_or_burn(&lock_input, &0u32);
    assert_eq!(out.dest_pool_data, encode_local_decimals(&env, 7).unwrap());
    assert_eq!(token_client.balance(&pool_client.address), 100);
}

#[test]
fn test_release_or_mint_scales_down_remote_more_decimals() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let pool_id = env.register(LockReleaseTokenPoolContract, ());
    let pool_client = LockReleaseTokenPoolContractClient::new(&env, &pool_id);
    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_client = token::Client::new(&env, &token_address);
    let token_admin_client = token::StellarAssetClient::new(&env, &token_address);

    let local_decimals: u32 = 6;
    pool_client.initialize(&owner, &token_address, &local_decimals);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let expected_local: i128 = 1_000_000;
    token_admin_client.mint(&pool_id, &expected_local);

    let remote_decimals: u32 = 9;
    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 1_000_000_000,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: encode_local_decimals(&env, remote_decimals).unwrap(),
    };

    let out = pool_client.release_or_mint(&release_input, &0u32);
    assert_eq!(out.destination_amount, expected_local);
    assert_eq!(token_client.balance(&receiver), expected_local);
    assert_eq!(token_client.balance(&pool_id), 0);
}

#[test]
fn test_initialize_rejects_decimals_above_uint8() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let pool_id = env.register(LockReleaseTokenPoolContract, ());
    let pool_client = LockReleaseTokenPoolContractClient::new(&env, &pool_id);
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
fn test_lock_or_burn_outbound_refills_over_time() {
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
    token_admin_client.mint(&sender, &5000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 1000,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&lock_input, &0u32);
    assert_eq!(token_client.balance(&pool_client.address), 1000);

    // Advance 50s => 500 tokens refilled
    env.ledger().with_mut(|li| li.timestamp = 150);
    let lock_input2 = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 500,
        local_token: token_address,
    };
    pool_client.lock_or_burn(&lock_input2, &0u32);
    assert_eq!(token_client.balance(&pool_client.address), 1500);
}

#[test]
fn test_release_or_mint_exceeds_inbound_capacity_rejected() {
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();
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

    // Fund pool with enough liquidity
    token_admin_client.mint(&pool_client.address, &1000);

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
    let (env, pool_client, _owner, token_address, token_client, token_admin_client) = setup_env();
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

    token_admin_client.mint(&pool_client.address, &5000);

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
    assert_eq!(token_client.balance(&receiver), 1000);

    // Advance 30s => 300 refilled
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
    let (env, pool_client, _owner, _token_address, _token_client, _token_admin_client) =
        setup_env();
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

    let state = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(!state.outbound.is_enabled);

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

    // Verify enforcement
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

    pool_client.apply_chain_updates(&Vec::new(&env), &Vec::from_array(&env, [remote_chain]));

    let state2 = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(!state2.outbound.is_enabled);
    assert_eq!(state2.outbound.tokens, 0);
}

#[test]
fn test_set_rate_limit_config_admin_can_set() {
    let (env, pool_client, owner, _token_address, _token_client, _token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

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

// ================================================================
//  Fast-Finality (FTF) Rate Limit Tests
// ================================================================

const WAIT_FOR_SAFE: u32 = 1 << 16; // 0x00010000

/// Helper to fund the pool with liquidity (needed for release_or_mint).
fn fund_pool(token_admin_client: &token::StellarAssetClient, pool_address: &Address, amount: i128) {
    token_admin_client.mint(pool_address, &amount);
}

#[test]
fn test_ftf_inbound_uses_ftf_bucket_when_configured() {
    let (env, pool_client, owner, token_address, token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    // Configure FTF inbound bucket with smaller capacity than what we'll try
    let ftf_inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 200,
        rate: 2,
    };
    pool_client.set_rate_limit_config(
        &owner,
        &remote_chain,
        &RateLimitConfig::disabled(),
        &ftf_inbound,
        &true,
    );

    let pool_address = pool_client.address.clone();
    fund_pool(&token_admin_client, &pool_address, 10_000);

    let receiver = Address::generate(&env);

    // FTF inbound: 200 should succeed (exactly at FTF capacity)
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 200,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    pool_client.release_or_mint(&release_input, &WAIT_FOR_SAFE);
    assert_eq!(token_client.balance(&receiver), 200);

    // FTF inbound: 1 more should fail (FTF bucket exhausted, only 2 refilled)
    env.ledger().with_mut(|li| li.timestamp = 101);
    let release_input2 = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 3,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    let r = pool_client.try_release_or_mint(&release_input2, &WAIT_FOR_SAFE);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);

    // Default inbound should still be unaffected (disabled = no limit)
    let release_default = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 500,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    pool_client.release_or_mint(&release_default, &0u32);
    assert_eq!(token_client.balance(&receiver), 700);
}

#[test]
fn test_ftf_inbound_falls_back_to_default_bucket_when_not_configured() {
    let (env, pool_client, _owner, token_address, token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 500,
        rate: 5,
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
    // No FTF buckets configured — FTF requests should fall back to the default inbound bucket.

    let pool_address = pool_client.address.clone();
    fund_pool(&token_admin_client, &pool_address, 10_000);

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 500,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    pool_client.release_or_mint(&release_input, &WAIT_FOR_SAFE);
    assert_eq!(token_client.balance(&receiver), 500);

    // Default bucket exhausted; another FTF request should fail
    env.ledger().with_mut(|li| li.timestamp = 101);
    let release_input2 = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 6,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    let r = pool_client.try_release_or_mint(&release_input2, &WAIT_FOR_SAFE);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);
}

#[test]
fn test_ftf_outbound_uses_ftf_bucket_when_configured() {
    let (env, pool_client, owner, token_address, _token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    // Configure allowed finality to permit WAIT_FOR_SAFE
    pool_client.set_allowed_finality_config(&WAIT_FOR_SAFE);

    // Configure FTF outbound bucket
    let ftf_outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 300,
        rate: 3,
    };
    pool_client.set_rate_limit_config(
        &owner,
        &remote_chain,
        &ftf_outbound,
        &RateLimitConfig::disabled(),
        &true,
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &5000);

    // FTF outbound: 300 should succeed (exactly at FTF capacity)
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 300,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&lock_input, &WAIT_FOR_SAFE);

    // FTF outbound: exceeding refill should fail (FTF bucket exhausted)
    env.ledger().with_mut(|li| li.timestamp = 101);
    let lock_input2 = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 4,
        local_token: token_address.clone(),
    };
    let r = pool_client.try_lock_or_burn(&lock_input2, &WAIT_FOR_SAFE);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);

    // Default outbound should still be unaffected (disabled = no limit)
    let lock_default = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 1000,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&lock_default, &0u32);
}

#[test]
fn test_ftf_outbound_rejected_when_finality_not_allowed() {
    let (env, pool_client, _owner, token_address, _token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    // allowed finality is default (0) — WAIT_FOR_SAFE is not allowed

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 100,
        local_token: token_address,
    };
    let r = pool_client.try_lock_or_burn(&lock_input, &WAIT_FOR_SAFE);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::InvalidRequestedFinality);
}

#[test]
fn test_ftf_and_default_buckets_are_independent() {
    let (env, pool_client, owner, token_address, token_client, token_admin_client) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    // Set up default inbound bucket
    let default_inbound = RateLimitConfig {
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
                default_inbound,
            )],
        ),
        &Vec::new(&env),
    );

    // Set up FTF inbound bucket with different capacity
    let ftf_inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 300,
        rate: 3,
    };
    pool_client.set_rate_limit_config(
        &owner,
        &remote_chain,
        &RateLimitConfig::disabled(),
        &ftf_inbound,
        &true,
    );

    let pool_address = pool_client.address.clone();
    fund_pool(&token_admin_client, &pool_address, 10_000);

    let receiver = Address::generate(&env);

    // Exhaust the FTF bucket
    let release_ftf = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 300,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    pool_client.release_or_mint(&release_ftf, &WAIT_FOR_SAFE);
    assert_eq!(token_client.balance(&receiver), 300);

    // Default bucket should still have its full 1000 capacity
    let release_default = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 1000,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[5u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    pool_client.release_or_mint(&release_default, &0u32);
    assert_eq!(token_client.balance(&receiver), 1300);
}
