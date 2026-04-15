#![cfg(test)]

extern crate std;

use soroban_sdk::{testutils::Address as _, token, vec, Address, Env};

use common_error::CCIPError;

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
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::CallerNotAuthorized);
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
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::InvalidTokenAmount);
}

#[test]
fn withdraw_unauthorized_caller_rejected() {
    let (env, _owner, token_addr, _token_admin, client) = setup();

    let pool = Address::generate(&env);
    client.add_allowed_callers(&vec![&env, pool.clone()]);

    let sac = token::StellarAssetClient::new(&env, &token_addr);
    sac.mint(&pool, &500);
    let tc = token::Client::new(&env, &token_addr);
    let exp = env.ledger().sequence().saturating_add(10_000);
    tc.approve(&pool, &client.address, &500, &exp);
    client.deposit(&pool, &500);
    assert_eq!(tc.balance(&client.address), 500);

    let stranger = Address::generate(&env);
    let receiver = Address::generate(&env);
    let r = client.try_withdraw(&stranger, &100, &receiver);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::CallerNotAuthorized);
    assert_eq!(tc.balance(&client.address), 500);
}

#[test]
fn withdraw_zero_amount_rejected() {
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
    let r = client.try_withdraw(&pool, &0, &receiver);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::InvalidTokenAmount);
}

#[test]
fn multiple_deposits_accumulate_in_lockbox() {
    let (env, _owner, token_addr, _token_admin, client) = setup();

    let pool = Address::generate(&env);
    client.add_allowed_callers(&vec![&env, pool.clone()]);

    let sac = token::StellarAssetClient::new(&env, &token_addr);
    sac.mint(&pool, &3_000);
    let tc = token::Client::new(&env, &token_addr);
    let exp = env.ledger().sequence().saturating_add(10_000);
    tc.approve(&pool, &client.address, &3_000, &exp);

    client.deposit(&pool, &1_000);
    client.deposit(&pool, &2_000);

    assert_eq!(tc.balance(&client.address), 3_000);
    assert_eq!(tc.balance(&pool), 0);
}

#[test]
fn two_allowed_callers_each_deposit() {
    let (env, _owner, token_addr, _token_admin, client) = setup();

    let caller1 = Address::generate(&env);
    let caller2 = Address::generate(&env);
    client.add_allowed_callers(&vec![&env, caller1.clone(), caller2.clone()]);

    let sac = token::StellarAssetClient::new(&env, &token_addr);
    sac.mint(&caller1, &1_000);
    sac.mint(&caller2, &1_000);
    let tc = token::Client::new(&env, &token_addr);
    let exp = env.ledger().sequence().saturating_add(10_000);
    tc.approve(&caller1, &client.address, &1_000, &exp);
    tc.approve(&caller2, &client.address, &1_000, &exp);

    client.deposit(&caller1, &1_000);
    client.deposit(&caller2, &1_000);

    assert_eq!(tc.balance(&client.address), 2_000);
}

#[test]
fn multiple_withdrawals_to_distinct_recipients() {
    let (env, _owner, token_addr, _token_admin, client) = setup();

    let pool = Address::generate(&env);
    client.add_allowed_callers(&vec![&env, pool.clone()]);

    let sac = token::StellarAssetClient::new(&env, &token_addr);
    sac.mint(&pool, &3_000);
    let tc = token::Client::new(&env, &token_addr);
    let exp = env.ledger().sequence().saturating_add(10_000);
    tc.approve(&pool, &client.address, &3_000, &exp);
    client.deposit(&pool, &3_000);

    let recipient1 = Address::generate(&env);
    let recipient2 = Address::generate(&env);
    client.withdraw(&pool, &1_000, &recipient1);
    client.withdraw(&pool, &2_000, &recipient2);

    assert_eq!(tc.balance(&recipient1), 1_000);
    assert_eq!(tc.balance(&recipient2), 2_000);
    assert_eq!(tc.balance(&client.address), 0);
}
