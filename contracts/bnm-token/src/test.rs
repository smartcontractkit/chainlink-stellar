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
/// (mint, set_admin) are reachable in happy-path tests.
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
// authorized / unsupported clawback / negative amounts
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

/// clawback is unsupported and traps for EVERY caller — even the admin with all
/// auths mocked (EVM `BurnMintERC20` has no clawback; the entrypoint exists
/// only because the StellarAssetInterface ABI requires it). No BnM balance
/// can ever be seized.
#[test]
fn test_clawback_unsupported_traps_for_every_caller() {
    let (env, client, _admin) = setup();
    let holder = soroban_sdk::Address::generate(&env);
    client.mint(&holder, &10_000_000);

    // Authenticated admin (setup mocks all auths) still traps: the rejection
    // is unconditional, not auth-based.
    assert!(
        client.try_clawback(&holder, &1_000_000).is_err(),
        "clawback must trap unconditionally — it is unsupported"
    );

    // And with no authorizations at all.
    env.mock_auths(&[]);
    assert!(client.try_clawback(&holder, &1_000_000).is_err());

    // The holder's balance is untouchable either way.
    assert_eq!(client.balance(&holder), 10_000_000);
}

#[test]
fn test_mint_rejects_negative_amount() {
    let (env, client, _admin) = setup();
    let holder = soroban_sdk::Address::generate(&env);
    client.mint(&holder, &10_000_000);
    // A negative mint must not silently DEBIT the recipient (the credit helper
    // guards, so every mint path is negative-safe).
    assert!(client.try_mint(&holder, &-1_000_000).is_err());
    assert_eq!(client.balance(&holder), 10_000_000);
}

#[test]
fn test_approve_rejects_negative_amount() {
    let (env, client, _admin) = setup();
    let owner = soroban_sdk::Address::generate(&env);
    let spender = soroban_sdk::Address::generate(&env);
    // A negative allowance is nonsense state; approve rejects it outright.
    assert!(client
        .try_approve(&owner, &spender, &-1_000_000, &1_000_000)
        .is_err());
    assert_eq!(client.allowance(&owner, &spender), 0);
    let _ = env;
}

// ============================================================
// Owner / minters — ownership transfer, minter rotation, migration
// (docs/token-ownership-and-minters.md §5–§6)
// ============================================================

/// Two-step ownership round-trip: `transfer_ownership(MCMS)` → MCMS
/// `accept_ownership`. Until acceptance the owner is unchanged; after it the
/// old owner's authorization no longer satisfies the owner gate, and the new
/// owner can start and cancel a transfer. The minters set is untouched
/// throughout — ownership controls minter *management*, not minting.
#[test]
fn test_ownership_transfer_round_trip() {
    let (env, client, deployer) = setup();
    assert_eq!(client.owner(), Some(deployer.clone()));
    assert_eq!(client.get_pending_owner(), None);

    let mcms = soroban_sdk::Address::generate(&env);
    client.transfer_ownership(&mcms);
    assert_eq!(client.get_pending_owner(), Some(mcms.clone()));
    // Not yet transferred: the deployer is still the owner.
    assert_eq!(client.owner(), Some(deployer.clone()));

    client.accept_ownership();
    assert_eq!(client.owner(), Some(mcms.clone()));
    assert_eq!(client.get_pending_owner(), None);

    // The OLD owner's authorization no longer satisfies the owner gate
    // (precise auths: only the deployer is authorized for this exact invoke).
    env.mock_auths(&[MockAuth {
        address: &deployer,
        invoke: &MockAuthInvoke {
            contract: &client.address,
            fn_name: "transfer_ownership",
            args: soroban_sdk::vec![&env, mcms.clone().into_val(&env)],
            sub_invokes: &[],
        },
    }]);
    assert!(
        client.try_transfer_ownership(&mcms).is_err(),
        "the former owner must not be able to start an ownership transfer"
    );

    // The NEW owner can start a transfer and cancel it.
    env.mock_auths(&[
        MockAuth {
            address: &mcms,
            invoke: &MockAuthInvoke {
                contract: &client.address,
                fn_name: "transfer_ownership",
                args: soroban_sdk::vec![&env, deployer.clone().into_val(&env)],
                sub_invokes: &[],
            },
        },
        MockAuth {
            address: &mcms,
            invoke: &MockAuthInvoke {
                contract: &client.address,
                fn_name: "cancel_ownership_transfer",
                args: soroban_sdk::vec![&env],
                sub_invokes: &[],
            },
        },
    ]);
    client.transfer_ownership(&deployer);
    assert_eq!(client.get_pending_owner(), Some(deployer.clone()));
    client.cancel_ownership_transfer();
    assert_eq!(client.get_pending_owner(), None);

    // The minters set was untouched by the whole round-trip.
    assert_eq!(
        client.get_minters(),
        soroban_sdk::vec![&env, deployer.clone()]
    );
}

/// `set_admin` is owner-gated, not minter-gated: once ownership has moved to
/// MCMS, the deployer (the former owner and primary minter) can no longer
/// hand the primary mint to a new pool — even when fully self-authorized.
#[test]
fn test_set_admin_is_owner_gated() {
    let (env, client, deployer) = setup();
    let mcms = soroban_sdk::Address::generate(&env);
    client.transfer_ownership(&mcms);
    client.accept_ownership();

    let new_pool = soroban_sdk::Address::generate(&env);
    // Precise auths: ONLY the deployer (a non-owner now) is authorized.
    env.mock_auths(&[MockAuth {
        address: &deployer,
        invoke: &MockAuthInvoke {
            contract: &client.address,
            fn_name: "set_admin",
            args: soroban_sdk::vec![&env, new_pool.clone().into_val(&env)],
            sub_invokes: &[],
        },
    }]);
    assert!(
        client.try_set_admin(&new_pool).is_err(),
        "set_admin must be owner-gated after the ownership transfer"
    );

    // And with nobody authorized at all.
    env.mock_auths(&[]);
    assert!(client.try_set_admin(&new_pool).is_err());
}

/// `add_minter` / `remove_minter` are owner-gated; the owner manages the
/// secondary minters with precise auths.
#[test]
fn test_minter_management_is_owner_gated() {
    let (env, client, deployer) = setup();
    let mcms = soroban_sdk::Address::generate(&env);
    client.transfer_ownership(&mcms);
    client.accept_ownership();

    let some_pool = soroban_sdk::Address::generate(&env);
    // Precise auths: only the deployer (a non-owner now) is authorized for
    // each invoke — the owner gate's require_auth traps for both.
    env.mock_auths(&[MockAuth {
        address: &deployer,
        invoke: &MockAuthInvoke {
            contract: &client.address,
            fn_name: "add_minter",
            args: soroban_sdk::vec![&env, some_pool.clone().into_val(&env)],
            sub_invokes: &[],
        },
    }]);
    assert!(client.try_add_minter(&some_pool).is_err());

    env.mock_auths(&[MockAuth {
        address: &deployer,
        invoke: &MockAuthInvoke {
            contract: &client.address,
            fn_name: "remove_minter",
            args: soroban_sdk::vec![&env, some_pool.clone().into_val(&env)],
            sub_invokes: &[],
        },
    }]);
    assert!(client.try_remove_minter(&some_pool).is_err());

    // The owner (MCMS) adds and removes a secondary minter fine.
    env.mock_auths(&[
        MockAuth {
            address: &mcms,
            invoke: &MockAuthInvoke {
                contract: &client.address,
                fn_name: "add_minter",
                args: soroban_sdk::vec![&env, some_pool.clone().into_val(&env)],
                sub_invokes: &[],
            },
        },
        MockAuth {
            address: &mcms,
            invoke: &MockAuthInvoke {
                contract: &client.address,
                fn_name: "remove_minter",
                args: soroban_sdk::vec![&env, some_pool.clone().into_val(&env)],
                sub_invokes: &[],
            },
        },
    ]);
    client.add_minter(&some_pool);
    assert_eq!(
        client.get_minters(),
        soroban_sdk::vec![&env, deployer.clone(), some_pool.clone()]
    );
    client.remove_minter(&some_pool);
    assert_eq!(
        client.get_minters(),
        soroban_sdk::vec![&env, deployer.clone()]
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #9)")] // MinterAlreadyExists
fn test_add_minter_duplicate_traps() {
    let (_env, client, deployer) = setup();
    // The deployer is already the primary minter (MINTERS[0]).
    client.add_minter(&deployer);
}

#[test]
#[should_panic(expected = "Error(Contract, #11)")] // CannotRemovePrimaryMinter
fn test_remove_minter_refuses_primary() {
    let (_env, client, deployer) = setup();
    // The primary minter must be replaced via set_admin, not removed.
    client.remove_minter(&deployer);
}

#[test]
#[should_panic(expected = "Error(Contract, #10)")] // MinterNotFound
fn test_remove_minter_not_found_traps() {
    let (env, client, _deployer) = setup();
    let stranger = soroban_sdk::Address::generate(&env);
    client.remove_minter(&stranger);
}

/// `set_admin` onto an address that is already a secondary minter (the
/// migration flow) repositions it to `MINTERS[0]` — no duplicate entry, no
/// `MinterAlreadyExists` trap — and demotes the old primary out of the set.
#[test]
fn test_set_admin_repositions_existing_secondary_minter() {
    let (env, client, deployer) = setup();
    let pool_b = soroban_sdk::Address::generate(&env);

    client.add_minter(&pool_b);
    assert_eq!(
        client.get_minters(),
        soroban_sdk::vec![&env, deployer.clone(), pool_b.clone()]
    );

    client.set_admin(&pool_b);
    assert_eq!(client.admin(), pool_b);
    // The old primary (deployer) is demoted out; pool_b is NOT duplicated.
    assert_eq!(
        client.get_minters(),
        soroban_sdk::vec![&env, pool_b.clone()]
    );
}

/// The deployer is demoted out of the minters set on the `set_admin(pool)`
/// handoff — neither `mint` (primary-minter auth) nor `mint_as(deployer, …)`
/// can mint. (Unlike LINK, BnM still has the permissionless `drip` faucet —
/// supply can still grow without any minter — but the *mint paths* are the
/// pool's alone.)
#[test]
fn test_deployer_demoted_out_of_minters_on_handoff() {
    let (env, client, deployer) = setup();
    let pool = soroban_sdk::Address::generate(&env);
    client.set_admin(&pool);
    assert_eq!(client.get_minters(), soroban_sdk::vec![&env, pool.clone()]);

    let to = soroban_sdk::Address::generate(&env);
    let amt: i128 = 1_000_000;

    // mint: the stored primary is the pool; with only the deployer
    // authorized, the pool's require_auth is unsatisfied → traps.
    env.mock_auths(&[MockAuth {
        address: &deployer,
        invoke: &MockAuthInvoke {
            contract: &client.address,
            fn_name: "mint",
            args: soroban_sdk::vec![&env, to.clone().into_val(&env), amt.into_val(&env),],
            sub_invokes: &[],
        },
    }]);
    assert!(
        client.try_mint(&to, &amt).is_err(),
        "the demoted deployer must not be able to mint after set_admin(pool)"
    );

    // mint_as(deployer, …): fully authorized but NOT a minter → NotMinter.
    env.mock_auths(&[MockAuth {
        address: &deployer,
        invoke: &MockAuthInvoke {
            contract: &client.address,
            fn_name: "mint_as",
            args: soroban_sdk::vec![
                &env,
                deployer.clone().into_val(&env),
                to.clone().into_val(&env),
                amt.into_val(&env),
            ],
            sub_invokes: &[],
        },
    }]);
    assert!(
        client.try_mint_as(&deployer, &to, &amt).is_err(),
        "the demoted deployer must not be able to mint_as itself after set_admin(pool)"
    );
    assert_eq!(client.balance(&to), 0);
}

/// `mint_as` security triad (the advanced-pool-hooks explicit-caller
/// reasoning): a registered minter mints fine; a random fully-authorized
/// address fails MEMBERSHIP (NotMinter); claiming a minter's identity
/// without the minter's own authorization fails its require_auth.
#[test]
fn test_mint_as_membership_and_auth_gates() {
    let (env, client, deployer) = setup();
    let to = soroban_sdk::Address::generate(&env);
    let amt: i128 = 1_000_000;

    // A registered minter (the deployer is the primary) mints fine.
    client.mint_as(&deployer, &to, &amt);
    assert_eq!(client.balance(&to), amt);

    // A random fully-authorized address fails MEMBERSHIP (NotMinter, #8) —
    // setup mocks all auths, so only the membership check can be rejecting.
    let stranger = soroban_sdk::Address::generate(&env);
    assert!(client.try_mint_as(&stranger, &to, &amt).is_err());

    // Claiming the minter's identity without the minter's authorization:
    // only the stranger is authorized → deployer.require_auth() traps.
    let to2 = soroban_sdk::Address::generate(&env);
    env.mock_auths(&[MockAuth {
        address: &stranger,
        invoke: &MockAuthInvoke {
            contract: &client.address,
            fn_name: "mint_as",
            args: soroban_sdk::vec![
                &env,
                deployer.clone().into_val(&env),
                to2.clone().into_val(&env),
                amt.into_val(&env),
            ],
            sub_invokes: &[],
        },
    }]);
    assert!(
        client.try_mint_as(&deployer, &to2, &amt).is_err(),
        "mint_as must require the claimed minter's own authorization"
    );
    assert_eq!(client.balance(&to2), 0);
}

/// The full zero-downtime migration flow (doc §6): the deployer hands the
/// primary mint to oldPool; the owner adds newPool as a secondary minter —
/// BOTH pools mint through `mint_as` during the overlap; `set_admin(newPool)`
/// then repositions the new pool to primary and removes the old pool from
/// the set in the SAME operation (no separate `remove_minter(oldPool)` — it
/// would trap MinterNotFound, and it could not run before `set_admin`
/// either, because `remove_minter` refuses the primary).
#[test]
fn test_two_pool_migration_overlap() {
    let (env, client, deployer) = setup();

    // Pre-funding while the deployer is still the primary minter (though for
    // BnM the permissionless drip faucet is also available).
    let user = soroban_sdk::Address::generate(&env);
    client.mint(&user, &10_000_000);
    assert_eq!(client.balance(&user), 10_000_000);

    // Onboarding: oldPool becomes the primary minter; the deployer is
    // demoted out of the minters set.
    let old_pool = soroban_sdk::Address::generate(&env);
    client.set_admin(&old_pool);
    assert_eq!(client.admin(), old_pool);
    assert_eq!(
        client.get_minters(),
        soroban_sdk::vec![&env, old_pool.clone()]
    );

    // Migration overlap: the owner adds newPool as a secondary minter —
    // both pools are now authorized minters, no ordering constraint.
    let new_pool = soroban_sdk::Address::generate(&env);
    client.add_minter(&new_pool);
    assert_eq!(
        client.get_minters(),
        soroban_sdk::vec![&env, old_pool.clone(), new_pool.clone()]
    );

    // BOTH pools mint through mint_as during the overlap (precise auths per
    // pool — identity-bound successes).
    let build_mint_as_invoke =
        |caller: &soroban_sdk::Address, to: &soroban_sdk::Address, amount: i128| MockAuthInvoke {
            contract: &client.address,
            fn_name: "mint_as",
            args: soroban_sdk::vec![
                &env,
                caller.clone().into_val(&env),
                to.clone().into_val(&env),
                amount.into_val(&env),
            ],
            sub_invokes: &[],
        };
    let r1 = soroban_sdk::Address::generate(&env);
    let r2 = soroban_sdk::Address::generate(&env);
    let amt: i128 = 1_000_000;

    env.mock_auths(&[MockAuth {
        address: &old_pool,
        invoke: &build_mint_as_invoke(&old_pool, &r1, amt),
    }]);
    client.mint_as(&old_pool, &r1, &amt);
    assert_eq!(client.balance(&r1), amt);

    env.mock_auths(&[MockAuth {
        address: &new_pool,
        invoke: &build_mint_as_invoke(&new_pool, &r2, amt),
    }]);
    client.mint_as(&new_pool, &r2, &amt);
    assert_eq!(client.balance(&r2), amt);

    // Re-point: set_admin(newPool) — owner-gated (the deployer is still the
    // owner here) — repositions the new pool to primary and removes the old
    // pool from the set in the same op.
    env.mock_auths(&[MockAuth {
        address: &deployer,
        invoke: &MockAuthInvoke {
            contract: &client.address,
            fn_name: "set_admin",
            args: soroban_sdk::vec![&env, new_pool.clone().into_val(&env)],
            sub_invokes: &[],
        },
    }]);
    client.set_admin(&new_pool);
    assert_eq!(client.admin(), new_pool);
    assert_eq!(
        client.get_minters(),
        soroban_sdk::vec![&env, new_pool.clone()]
    );

    // The old pool can no longer mint — even fully authorized, its
    // membership is gone (NotMinter, #8).
    env.mock_auths(&[MockAuth {
        address: &old_pool,
        invoke: &build_mint_as_invoke(&old_pool, &r1, amt),
    }]);
    assert!(
        client.try_mint_as(&old_pool, &r1, &amt).is_err(),
        "the old pool must not be able to mint after set_admin(newPool)"
    );
    assert_eq!(client.balance(&r1), amt); // unchanged
}
