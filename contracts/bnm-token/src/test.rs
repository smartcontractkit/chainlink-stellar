#![cfg(test)]

use soroban_sdk::{
    testutils::{Address as _, MockAuth, MockAuthInvoke},
    IntoVal, String,
};

use crate::{BnmTokenContract, BnmTokenContractClient};

/// 0.1 token at 7 decimals — the amount `drip` mints per call.
const DRIP_AMOUNT: i128 = 1_000_000;

/// Fresh env + deployed BnM token initialized with `admin`, named
/// "CCIP BnM" / "BnM" / 7 decimals. All auths mocked so admin entrypoints
/// (mint, set_admin, clawback) are reachable in happy-path tests.
fn setup() -> (
    soroban_sdk::Env,
    BnmTokenContractClient<'static>,
    soroban_sdk::Address,
) {
    let env = soroban_sdk::Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let admin = soroban_sdk::Address::generate(&env);
    let contract_id = env.register(BnmTokenContract, ());
    let client = BnmTokenContractClient::new(&env, &contract_id);

    client.initialize(
        &admin,
        &String::from_str(&env, "CCIP BnM"),
        &String::from_str(&env, "BnM"),
        &7u32,
    );

    (env, client, admin)
}

// ============================================================
// Initialization & metadata
// ============================================================

#[test]
fn test_initialize_sets_metadata_and_admin() {
    let (env, client, admin) = setup();
    assert_eq!(client.admin(), admin);
    assert_eq!(client.decimals(), 7);
    assert_eq!(client.name(), String::from_str(&env, "CCIP BnM"));
    assert_eq!(client.symbol(), String::from_str(&env, "BnM"));
    assert_eq!(
        client.type_and_version(),
        String::from_str(&env, "BnmToken 1.0.0")
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #1)")] // AlreadyInitialized
fn test_double_initialize_fails() {
    let (env, client, _admin) = setup();
    client.initialize(
        &soroban_sdk::Address::generate(&env),
        &String::from_str(&env, "x"),
        &String::from_str(&env, "x"),
        &7u32,
    );
}

#[test]
fn test_read_before_initialize_panics() {
    let env = soroban_sdk::Env::default();
    env.mock_all_auths();
    let contract_id = env.register(BnmTokenContract, ());
    let client = BnmTokenContractClient::new(&env, &contract_id);
    // `admin` requires initialization; the try_ variant surfaces the trap as Err.
    assert!(client.try_admin().is_err());
    assert!(client.try_decimals().is_err());
}

// ============================================================
// Mint — admin-gated
// ============================================================

#[test]
fn test_mint_by_admin_credits_recipient() {
    let (env, client, admin) = setup();
    let recipient = soroban_sdk::Address::generate(&env);
    let amount: i128 = 5_000_000;
    client.mint(&recipient, &amount);
    assert_eq!(client.balance(&recipient), amount);
    // No supply was minted at initialize; total minted == this credit.
    assert_eq!(client.balance(&admin), 0);
}

#[test]
fn test_mint_rejects_unauthenticated_caller() {
    let (env, client, _admin) = setup();
    // Turn off mock_all_auths: nobody is authorized, so the stored admin's
    // `require_auth()` inside `mint` is not satisfied → the call traps.
    env.mock_auths(&[]);
    let recipient = soroban_sdk::Address::generate(&env);
    let r = client.try_mint(&recipient, &1_000_000);
    assert!(
        r.is_err(),
        "mint must require admin authorization (unauthenticated caller rejected)"
    );
    // Balance unchanged.
    assert_eq!(client.balance(&recipient), 0);
}

// ============================================================
// Transfer / burn / approve
// ============================================================

#[test]
fn test_transfer_moves_balance() {
    let (env, client, _admin) = setup();
    let sender = soroban_sdk::Address::generate(&env);
    let recipient = soroban_sdk::Address::generate(&env);
    client.mint(&sender, &10_000_000);
    client.transfer(&sender, &recipient, &4_000_000);
    assert_eq!(client.balance(&sender), 6_000_000);
    assert_eq!(client.balance(&recipient), 4_000_000);
}

#[test]
fn test_transfer_insufficient_balance_rejected() {
    let (env, client, _admin) = setup();
    let sender = soroban_sdk::Address::generate(&env);
    let recipient = soroban_sdk::Address::generate(&env);
    client.mint(&sender, &1_000_000);
    // 2M > 1M available → InsufficientBalance (#3).
    assert!(client
        .try_transfer(&sender, &recipient, &2_000_000)
        .is_err());
    assert_eq!(client.balance(&sender), 1_000_000);
}

#[test]
fn test_burn_reduces_balance() {
    let (env, client, _admin) = setup();
    let holder = soroban_sdk::Address::generate(&env);
    client.mint(&holder, &10_000_000);
    client.burn(&holder, &3_000_000);
    assert_eq!(client.balance(&holder), 7_000_000);
}

#[test]
fn test_approve_transfer_from_consumes_allowance() {
    let (env, client, _admin) = setup();
    let owner = soroban_sdk::Address::generate(&env);
    let spender = soroban_sdk::Address::generate(&env);
    let recipient = soroban_sdk::Address::generate(&env);
    client.mint(&owner, &10_000_000);

    client.approve(&owner, &spender, &4_000_000, &1_000_000);
    assert_eq!(client.allowance(&owner, &spender), 4_000_000);

    client.transfer_from(&spender, &owner, &recipient, &3_000_000);
    assert_eq!(client.allowance(&owner, &spender), 1_000_000);
    assert_eq!(client.balance(&owner), 7_000_000);
    assert_eq!(client.balance(&recipient), 3_000_000);
}

#[test]
fn test_transfer_from_insufficient_allowance_rejected() {
    let (env, client, _admin) = setup();
    let owner = soroban_sdk::Address::generate(&env);
    let spender = soroban_sdk::Address::generate(&env);
    let recipient = soroban_sdk::Address::generate(&env);
    client.mint(&owner, &10_000_000);
    client.approve(&owner, &spender, &1_000_000, &1_000_000);
    // 3M > 1M allowance → InsufficientAllowance (#4).
    assert!(client
        .try_transfer_from(&spender, &owner, &recipient, &3_000_000)
        .is_err());
    // Allowance unchanged on failure.
    assert_eq!(client.allowance(&owner, &spender), 1_000_000);
}

// ============================================================
// drip — permissionless faucet (the headline BnM feature)
// ============================================================

#[test]
fn test_drip_is_permissionless_and_mints_a_tenth() {
    let (env, client, _admin) = setup();
    let to = soroban_sdk::Address::generate(&env);

    // Strict auth mode: NO address is authorized. `drip` performs no
    // `require_auth`, so it must still succeed — this is the proof it is
    // permissionless (EVM `BurnMintERC20WithDrip.drip(to)` parity).
    env.mock_auths(&[]);
    client.drip(&to);

    assert_eq!(client.balance(&to), DRIP_AMOUNT);
    // 0.1 token at 7 decimals == 1_000_000 base units.
    assert_eq!(DRIP_AMOUNT, 1_000_000);

    // A second drip stacks — no per-address rate limit (matches EVM drip).
    client.drip(&to);
    assert_eq!(client.balance(&to), 2 * DRIP_AMOUNT);

    // Drip to one address must not affect another.
    let other = soroban_sdk::Address::generate(&env);
    assert_eq!(client.balance(&other), 0);
    client.drip(&other);
    assert_eq!(client.balance(&other), DRIP_AMOUNT);
    assert_eq!(client.balance(&to), 2 * DRIP_AMOUNT);

    let _ = env; // env still in strict-auth mode for the assertions above
}

#[test]
fn test_drip_requires_initialization() {
    let env = soroban_sdk::Env::default();
    env.mock_all_auths();
    let contract_id = env.register(BnmTokenContract, ());
    let client = BnmTokenContractClient::new(&env, &contract_id);
    let to = soroban_sdk::Address::generate(&env);
    // `drip` guards on initialization even though it is permissionless.
    assert!(client.try_drip(&to).is_err());
}

// ============================================================
// set_admin — burn-mint mint-authority handoff
// ============================================================

/// `set_admin` transfers mint authority: after handing admin to the pool
/// address, the NEW admin can mint (its auth satisfies `require_auth`) while
/// the OLD admin can no longer mint (its auth is no longer the stored admin).
/// Uses precise `mock_auths` so only one party is authorized per leg — the
/// positive and negative results are identity-bound, not artifacts of
/// `mock_all_auths`.
#[test]
fn test_set_admin_transfers_mint_authority() {
    let (env, client, old_admin) = setup();

    // Sanity: the deployer is admin and can mint while all auths are mocked.
    let a = soroban_sdk::Address::generate(&env);
    client.mint(&a, &1_000_000);
    assert_eq!(client.balance(&a), 1_000_000);

    // Hand mint authority to a pool-styled address (still under mock_all_auths).
    let pool = soroban_sdk::Address::generate(&env);
    client.set_admin(&pool);
    assert_eq!(client.admin(), pool);

    // Mint invocation args (to, amount) shared by both legs.
    let build_mint_invoke = |recipient: &soroban_sdk::Address, amount: i128| MockAuthInvoke {
        contract: &client.address,
        fn_name: "mint",
        args: soroban_sdk::vec![
            &env,
            recipient.clone().into_val(&env),
            amount.into_val(&env),
        ],
        sub_invokes: &[],
    };

    // LEG 1 — the NEW admin (pool) is authorized → mint succeeds.
    let r1 = soroban_sdk::Address::generate(&env);
    let amt: i128 = 2_000_000;
    env.mock_auths(&[MockAuth {
        address: &pool,
        invoke: &build_mint_invoke(&r1, amt),
    }]);
    client.mint(&r1, &amt);
    assert_eq!(client.balance(&r1), amt);

    // LEG 2 — ONLY the OLD admin (deployer) is authorized. The stored admin is
    // now `pool`, so `pool.require_auth()` is not satisfied → mint traps.
    let r2 = soroban_sdk::Address::generate(&env);
    env.mock_auths(&[MockAuth {
        address: &old_admin,
        invoke: &build_mint_invoke(&r2, amt),
    }]);
    assert!(
        client.try_mint(&r2, &amt).is_err(),
        "the former admin must not be able to mint after set_admin"
    );
    assert_eq!(client.balance(&r2), 0);
}

#[test]
fn test_set_admin_rejects_unauthenticated_caller() {
    let (env, client, _admin) = setup();
    env.mock_auths(&[]);
    let new_admin = soroban_sdk::Address::generate(&env);
    assert!(
        client.try_set_admin(&new_admin).is_err(),
        "set_admin must require the current admin's authorization"
    );
}

// ============================================================
// authorized / clawback
// ============================================================

#[test]
fn test_authorized_always_true() {
    let (env, client, _admin) = setup();
    let holder = soroban_sdk::Address::generate(&env);
    // BnM is a plain ERC20-style test token: every holder is always authorized,
    // even after set_authorized(false) (the entrypoint is retained for SAC
    // interface fidelity but does not gate transfers).
    assert!(client.authorized(&holder));
    client.set_authorized(&holder, &false);
    assert!(client.authorized(&holder));
    // And transfers remain unaffected.
    client.mint(&holder, &1_000_000);
    let recipient = soroban_sdk::Address::generate(&env);
    client.transfer(&holder, &recipient, &500_000);
    assert_eq!(client.balance(&recipient), 500_000);
    let _ = env;
}

#[test]
fn test_clawback_admin_gated_debits_holder() {
    let (env, client, _admin) = setup();
    let holder = soroban_sdk::Address::generate(&env);
    client.mint(&holder, &10_000_000);
    client.clawback(&holder, &4_000_000);
    assert_eq!(client.balance(&holder), 6_000_000);
}

#[test]
fn test_clawback_rejects_unauthenticated_caller() {
    let (env, client, _admin) = setup();
    let holder = soroban_sdk::Address::generate(&env);
    client.mint(&holder, &10_000_000);
    env.mock_auths(&[]);
    assert!(
        client.try_clawback(&holder, &1_000_000).is_err(),
        "clawback must require admin authorization"
    );
    // Balance untouched on the rejected clawback.
    assert_eq!(client.balance(&holder), 10_000_000);
}
