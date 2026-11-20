#![cfg(test)]
extern crate std;

use super::*;
use soroban_sdk::{
    contract, contractimpl,
    testutils::{Address as _, MockAuth, MockAuthInvoke},
    token::{StellarAssetClient, TokenClient},
    vec, Address, Env, IntoVal,
};

struct Setup<'a> {
    env: Env,
    owner: Address,
    issuer: Address,
    asset: Address,
    proxy: TokenPoolProxyClient<'a>,
    sac: StellarAssetClient<'a>,
    token: TokenClient<'a>,
}

/// Real SAC, proxy initialized and installed as the SAC admin. Auths are
/// mocked; tests that exercise auth failures clear them with `set_auths(&[])`.
fn setup<'a>() -> Setup<'a> {
    let env = Env::default();
    env.mock_all_auths();

    let owner = Address::generate(&env);
    let issuer = Address::generate(&env);
    let asset = env
        .register_stellar_asset_contract_v2(issuer.clone())
        .address();

    let proxy_id = env.register(TokenPoolProxy, ());
    let proxy = TokenPoolProxyClient::new(&env, &proxy_id);
    proxy.initialize(&owner, &asset);

    let sac = StellarAssetClient::new(&env, &asset);
    sac.set_admin(&proxy_id);
    let token = TokenClient::new(&env, &asset);

    Setup {
        env,
        owner,
        issuer,
        asset,
        proxy,
        sac,
        token,
    }
}

fn add_caller(s: &Setup, caller: &Address, mint_allowance: Option<i128>) {
    s.proxy.apply_caller_updates(
        &vec![
            &s.env,
            CallerUpdate {
                caller: caller.clone(),
                mint_allowance,
            },
        ],
        &vec![&s.env],
    );
}

fn contract_err(e: CCIPError) -> soroban_sdk::Error {
    soroban_sdk::Error::from_contract_error(e as u32)
}

// ------------------------------------------------------------------
// Initialization / views
// ------------------------------------------------------------------

#[test]
fn test_initialize_sets_owner_and_asset() {
    let s = setup();
    assert_eq!(s.proxy.owner(), Some(s.owner.clone()));
    assert_eq!(s.proxy.get_asset(), s.asset);
    assert_eq!(s.sac.admin(), s.proxy.address);
    assert_eq!(s.proxy.get_allowed_callers().len(), 0);
    assert_eq!(
        s.proxy.type_and_version(),
        String::from_str(&s.env, "TokenPoolProxy 1.0.0")
    );
}

#[test]
fn test_double_initialize_fails() {
    let s = setup();
    let res = s.proxy.try_initialize(&s.owner, &s.asset);
    assert_eq!(res, Err(Ok(CCIPError::AlreadyInitialized)));
}

#[test]
fn test_mint_before_initialize_fails() {
    let env = Env::default();
    env.mock_all_auths();
    let proxy = TokenPoolProxyClient::new(&env, &env.register(TokenPoolProxy, ()));
    let who = Address::generate(&env);
    let res = proxy.try_mint(&who, &1, &who);
    assert_eq!(res, Err(Ok(contract_err(CCIPError::NotInitialized))));
}

// ------------------------------------------------------------------
// Mint
// ------------------------------------------------------------------

#[test]
fn test_unlimited_caller_mints() {
    let s = setup();
    let pool = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    add_caller(&s, &pool, None);

    s.proxy.mint(&user, &1_000, &pool);
    s.proxy.mint(&user, &500, &pool);

    assert_eq!(s.token.balance(&user), 1_500);
    assert_eq!(
        s.proxy.get_caller_config(&pool),
        Some(CallerConfig {
            mint_allowance: None
        })
    );
}

#[test]
fn test_mint_requires_operator_auth() {
    let s = setup();
    let pool = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    add_caller(&s, &pool, None);

    s.proxy.mint(&user, &10, &pool);
    assert_eq!(
        s.env.auths(),
        std::vec![(
            pool.clone(),
            soroban_sdk::testutils::AuthorizedInvocation {
                function: soroban_sdk::testutils::AuthorizedFunction::Contract((
                    s.proxy.address.clone(),
                    Symbol::new(&s.env, "mint"),
                    (user.clone(), 10_i128, pool.clone()).into_val(&s.env),
                )),
                sub_invocations: std::vec![],
            }
        )]
    );
}

#[test]
fn test_capped_caller_allowance_decrements_and_exhausts() {
    let s = setup();
    let pool = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    add_caller(&s, &pool, Some(100));

    s.proxy.mint(&user, &40, &pool);
    assert_eq!(
        s.proxy.get_caller_config(&pool).unwrap().mint_allowance,
        Some(60)
    );

    // Minting exactly the remainder drains it to zero.
    s.proxy.mint(&user, &60, &pool);
    assert_eq!(
        s.proxy.get_caller_config(&pool).unwrap().mint_allowance,
        Some(0)
    );
    assert_eq!(s.token.balance(&user), 100);

    let res = s.proxy.try_mint(&user, &1, &pool);
    assert_eq!(res, Err(Ok(contract_err(CCIPError::MintAllowanceExceeded))));
    assert_eq!(s.token.balance(&user), 100);
}

#[test]
fn test_mint_over_allowance_rejected_without_partial_consumption() {
    let s = setup();
    let pool = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    add_caller(&s, &pool, Some(50));

    let res = s.proxy.try_mint(&user, &51, &pool);
    assert_eq!(res, Err(Ok(contract_err(CCIPError::MintAllowanceExceeded))));
    assert_eq!(
        s.proxy.get_caller_config(&pool).unwrap().mint_allowance,
        Some(50)
    );
}

#[test]
fn test_owner_tops_up_allowance_by_overwrite() {
    let s = setup();
    let pool = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    add_caller(&s, &pool, Some(10));
    s.proxy.mint(&user, &10, &pool);

    add_caller(&s, &pool, Some(25));
    assert_eq!(
        s.proxy.get_caller_config(&pool).unwrap().mint_allowance,
        Some(25)
    );
    // Re-adding does not duplicate the listing index.
    assert_eq!(s.proxy.get_allowed_callers(), vec![&s.env, pool.clone()]);

    s.proxy.mint(&user, &25, &pool);
    assert_eq!(s.token.balance(&user), 35);

    // Switching to unlimited.
    add_caller(&s, &pool, None);
    s.proxy.mint(&user, &1_000_000, &pool);
}

#[test]
fn test_non_allowlisted_caller_rejected() {
    let s = setup();
    let stranger = Address::generate(&s.env);
    let res = s.proxy.try_mint(&stranger, &1, &stranger);
    assert_eq!(res, Err(Ok(contract_err(CCIPError::CallerNotAuthorized))));
}

#[test]
#[should_panic(expected = "Error(Auth, InvalidAction)")]
fn test_allowlisted_caller_without_auth_panics() {
    let s = setup();
    let pool = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    add_caller(&s, &pool, None);

    // An attacker passes the allowlisted pool as `operator` but only the
    // attacker signs.
    let attacker = Address::generate(&s.env);
    s.env.mock_auths(&[MockAuth {
        address: &attacker,
        invoke: &MockAuthInvoke {
            contract: &s.proxy.address,
            fn_name: "mint",
            args: (user.clone(), 1_i128, pool.clone()).into_val(&s.env),
            sub_invokes: &[],
        },
    }]);
    s.proxy.mint(&user, &1, &pool);
}

#[test]
fn test_non_positive_amount_rejected() {
    let s = setup();
    let pool = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    add_caller(&s, &pool, Some(10));

    for amount in [0_i128, -1] {
        let res = s.proxy.try_mint(&user, &amount, &pool);
        assert_eq!(res, Err(Ok(contract_err(CCIPError::InvalidTokenAmount))));
        let res = s.proxy.try_burn(&pool, &user, &amount);
        assert_eq!(res, Err(Ok(CCIPError::InvalidTokenAmount)));
    }
}

// ------------------------------------------------------------------
// Caller management
// ------------------------------------------------------------------

#[test]
fn test_negative_allowance_rejected() {
    let s = setup();
    let pool = Address::generate(&s.env);
    let res = s.proxy.try_apply_caller_updates(
        &vec![
            &s.env,
            CallerUpdate {
                caller: pool.clone(),
                mint_allowance: Some(-1),
            },
        ],
        &vec![&s.env],
    );
    assert_eq!(res, Err(Ok(CCIPError::InvalidConfig)));
    assert!(!s.proxy.is_allowed_caller(&pool));
}

#[test]
fn test_removed_caller_cannot_mint() {
    let s = setup();
    let pool_a = Address::generate(&s.env);
    let pool_b = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    add_caller(&s, &pool_a, None);
    add_caller(&s, &pool_b, Some(5));

    s.proxy
        .apply_caller_updates(&vec![&s.env], &vec![&s.env, pool_a.clone()]);

    assert!(!s.proxy.is_allowed_caller(&pool_a));
    assert_eq!(s.proxy.get_caller_config(&pool_a), None);
    assert_eq!(s.proxy.get_allowed_callers(), vec![&s.env, pool_b.clone()]);
    let res = s.proxy.try_mint(&user, &1, &pool_a);
    assert_eq!(res, Err(Ok(contract_err(CCIPError::CallerNotAuthorized))));

    // Removing an unknown caller is a no-op.
    s.proxy
        .apply_caller_updates(&vec![&s.env], &vec![&s.env, pool_a.clone()]);
    assert_eq!(s.proxy.get_allowed_callers(), vec![&s.env, pool_b]);
}

#[test]
fn test_removes_apply_before_adds() {
    let s = setup();
    let pool = Address::generate(&s.env);
    add_caller(&s, &pool, Some(1));

    // Remove + re-add in one call: the caller ends up present with the new value.
    s.proxy.apply_caller_updates(
        &vec![
            &s.env,
            CallerUpdate {
                caller: pool.clone(),
                mint_allowance: Some(7),
            },
        ],
        &vec![&s.env, pool.clone()],
    );
    assert_eq!(
        s.proxy.get_caller_config(&pool).unwrap().mint_allowance,
        Some(7)
    );
    assert_eq!(s.proxy.get_allowed_callers(), vec![&s.env, pool]);
}

#[test]
#[should_panic(expected = "Error(Auth, InvalidAction)")]
fn test_apply_caller_updates_is_owner_gated() {
    let s = setup();
    let pool = Address::generate(&s.env);
    s.env.set_auths(&[]);
    add_caller(&s, &pool, None);
}

// ------------------------------------------------------------------
// Owner-gated SAC admin functions
// ------------------------------------------------------------------

#[test]
fn test_sac_admin_functions_reject_non_owner_operator() {
    let s = setup();
    let pool = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    // Even an allowlisted minter cannot use the owner-only functions.
    add_caller(&s, &pool, None);

    let not_owner = contract_err(CCIPError::NotOwner);
    assert_eq!(s.proxy.try_set_admin(&user, &pool), Err(Ok(not_owner)));
    assert_eq!(
        s.proxy.try_set_authorized(&user, &false, &pool),
        Err(Ok(not_owner))
    );
    assert_eq!(s.proxy.try_clawback(&user, &1, &pool), Err(Ok(not_owner)));
    assert_eq!(s.sac.admin(), s.proxy.address);
}

#[test]
#[should_panic(expected = "Error(Auth, InvalidAction)")]
fn test_set_admin_requires_owner_auth() {
    let s = setup();
    let new_admin = Address::generate(&s.env);
    s.env.set_auths(&[]);
    s.proxy.set_admin(&new_admin, &s.owner);
}

#[test]
fn test_set_admin_moves_sac_admin_away() {
    let s = setup();
    let pool = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    let new_admin = Address::generate(&s.env);
    add_caller(&s, &pool, None);

    s.proxy.set_admin(&new_admin, &s.owner);
    assert_eq!(s.sac.admin(), new_admin);

    // The proxy is no longer the SAC admin, so the SAC rejects its mint.
    s.env.set_auths(&[]);
    s.env.mock_auths(&[MockAuth {
        address: &pool,
        invoke: &MockAuthInvoke {
            contract: &s.proxy.address,
            fn_name: "mint",
            args: (user.clone(), 1_i128, pool.clone()).into_val(&s.env),
            sub_invokes: &[],
        },
    }]);
    assert!(s.proxy.try_mint(&user, &1, &pool).is_err());
    assert_eq!(s.token.balance(&user), 0);
}

#[test]
fn test_set_authorized_and_clawback_forward_to_sac() {
    let env = Env::default();
    env.mock_all_auths();
    let owner = Address::generate(&env);
    let issuer = Address::generate(&env);
    let sac_contract = env.register_stellar_asset_contract_v2(issuer.clone());
    sac_contract
        .issuer()
        .set_flag(soroban_sdk::xdr::AccountFlags::RevocableFlag);
    sac_contract
        .issuer()
        .set_flag(soroban_sdk::xdr::AccountFlags::ClawbackEnabledFlag);
    let asset = sac_contract.address();

    let proxy = TokenPoolProxyClient::new(&env, &env.register(TokenPoolProxy, ()));
    proxy.initialize(&owner, &asset);
    StellarAssetClient::new(&env, &asset).set_admin(&proxy.address);

    let pool = Address::generate(&env);
    let user = Address::generate(&env);
    proxy.apply_caller_updates(
        &vec![
            &env,
            CallerUpdate {
                caller: pool.clone(),
                mint_allowance: None,
            },
        ],
        &vec![&env],
    );
    proxy.mint(&user, &100, &pool);

    let sac = StellarAssetClient::new(&env, &asset);
    proxy.set_authorized(&user, &false, &owner);
    assert!(!sac.authorized(&user));
    proxy.set_authorized(&user, &true, &owner);
    assert!(sac.authorized(&user));

    proxy.clawback(&user, &30, &owner);
    assert_eq!(TokenClient::new(&env, &asset).balance(&user), 70);
}

// ------------------------------------------------------------------
// Burn
// ------------------------------------------------------------------

#[test]
fn test_burn_reduces_balance_and_keeps_allowance() {
    let s = setup();
    let pool = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    add_caller(&s, &pool, Some(100));
    s.proxy.mint(&user, &100, &pool);

    // `user` authorizes the nested SAC burn (non-root auth).
    s.env.mock_all_auths_allowing_non_root_auth();
    s.proxy.burn(&pool, &user, &40);

    assert_eq!(s.token.balance(&user), 60);
    assert_eq!(
        s.proxy.get_caller_config(&pool).unwrap().mint_allowance,
        Some(0)
    );
}

#[test]
fn test_burn_by_non_allowlisted_caller_rejected() {
    let s = setup();
    let stranger = Address::generate(&s.env);
    let user = Address::generate(&s.env);
    let res = s.proxy.try_burn(&stranger, &user, &1);
    assert_eq!(res, Err(Ok(CCIPError::CallerNotAuthorized)));
}

// ------------------------------------------------------------------
// Contract caller (pool-style) — invoker-contract auth
// ------------------------------------------------------------------

#[contract]
pub struct MockPool;

#[contractimpl]
impl MockPool {
    pub fn mint_via(env: Env, proxy: Address, to: Address, amount: i128) {
        TokenPoolProxyClient::new(&env, &proxy).mint(&to, &amount, &env.current_contract_address());
    }
}

#[test]
fn test_contract_caller_mints_via_invoker_auth() {
    let s = setup();
    let pool_id = s.env.register(MockPool, ());
    let pool = MockPoolClient::new(&s.env, &pool_id);
    let user = Address::generate(&s.env);
    add_caller(&s, &pool_id, Some(1_000));

    // No mocked auths: the pool authorizes `operator = pool` implicitly as the
    // direct invoker, and the proxy authorizes the SAC mint the same way.
    s.env.set_auths(&[]);
    pool.mint_via(&s.proxy.address, &user, &250);

    assert_eq!(s.token.balance(&user), 250);
    assert_eq!(
        s.proxy.get_caller_config(&pool_id).unwrap().mint_allowance,
        Some(750)
    );
}

// ------------------------------------------------------------------
// Ownership
// ------------------------------------------------------------------

#[test]
fn test_ownership_transfer_moves_owner_privileges() {
    let s = setup();
    let new_owner = Address::generate(&s.env);
    let pool = Address::generate(&s.env);

    s.proxy.transfer_ownership(&new_owner);
    s.proxy.accept_ownership();
    assert_eq!(s.proxy.owner(), Some(new_owner.clone()));

    // The old owner no longer passes the SAC-admin owner gate.
    assert_eq!(
        s.proxy.try_set_authorized(&pool, &false, &s.owner),
        Err(Ok(contract_err(CCIPError::NotOwner)))
    );
    let _ = &s.issuer;
}
