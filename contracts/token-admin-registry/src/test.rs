#![cfg(test)]

use super::*;
use soroban_sdk::{
    symbol_short,
    testutils::{Address as _, Events as _},
    vec, Address, BytesN, Env, Map, Symbol, TryFromVal, TryIntoVal, Val, Vec,
};
use types::TokenConfig;

fn setup(env: &Env) -> (TokenAdminRegistryContractClient<'_>, Address) {
    let contract_id = env.register(TokenAdminRegistryContract, ());
    let client = TokenAdminRegistryContractClient::new(env, &contract_id);
    let owner = Address::generate(env);
    client.initialize(&owner);
    (client, owner)
}

// ============================================================
// Initialization
// ============================================================

#[test]
fn test_initialize() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, owner) = setup(&env);

    assert_eq!(client.owner(), Some(owner));
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")] // AlreadyInitialized
fn test_double_initialize_fails() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, _owner) = setup(&env);
    let another = Address::generate(&env);
    client.initialize(&another);
}

// ============================================================
// Propose Administrator
// ============================================================

#[test]
fn test_propose_administrator_by_owner() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, owner) = setup(&env);
    let token = Address::generate(&env);
    let admin = Address::generate(&env);

    client.propose_administrator(&owner, &token, &admin);

    let config = client.get_token_config(&token);
    assert_eq!(config.administrator, None);
    assert_eq!(config.pending_administrator, Some(admin));
    assert_eq!(config.token_pool, None);
}

#[test]
fn test_propose_administrator_by_registry_module() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, _owner) = setup(&env);
    let module = Address::generate(&env);
    let token = Address::generate(&env);
    let admin = Address::generate(&env);

    client.add_registry_module(&module);
    assert!(client.is_registry_module(&module));

    client.propose_administrator(&module, &token, &admin);

    let config = client.get_token_config(&token);
    assert_eq!(config.pending_administrator, Some(admin));
}

#[test]
#[should_panic(expected = "Error(Contract, #201)")] // OnlyRegistryModuleOrOwner
fn test_propose_administrator_unauthorized() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, _owner) = setup(&env);
    let random = Address::generate(&env);
    let token = Address::generate(&env);
    let admin = Address::generate(&env);

    client.propose_administrator(&random, &token, &admin);
}

#[test]
#[should_panic(expected = "Error(Contract, #204)")] // TokenAlreadyRegistered
fn test_propose_administrator_already_registered() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, owner) = setup(&env);
    let token = Address::generate(&env);
    let admin = Address::generate(&env);

    client.propose_administrator(&owner, &token, &admin);
    client.accept_admin_role(&token);

    // Token now has an administrator, proposing again should fail
    let admin2 = Address::generate(&env);
    client.propose_administrator(&owner, &token, &admin2);
}

// ============================================================
// Accept Admin Role
// ============================================================

#[test]
fn test_accept_admin_role() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, owner) = setup(&env);
    let token = Address::generate(&env);
    let admin = Address::generate(&env);

    client.propose_administrator(&owner, &token, &admin);
    assert_eq!(
        client.get_token_config(&token).pending_administrator,
        Some(admin.clone())
    );

    client.accept_admin_role(&token);

    let config = client.get_token_config(&token);
    assert_eq!(config.administrator, Some(admin));
    assert_eq!(config.pending_administrator, None);
}

#[test]
#[should_panic(expected = "Error(Contract, #203)")] // OnlyPendingAdministrator
fn test_accept_admin_role_no_pending() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, _owner) = setup(&env);
    let token = Address::generate(&env);

    // No pending admin, should fail
    client.accept_admin_role(&token);
}

// ============================================================
// Set Pool
// ============================================================

#[test]
fn test_set_pool() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, owner) = setup(&env);
    let token = Address::generate(&env);
    let pool = Address::generate(&env);
    let admin = Address::generate(&env);

    // Register admin
    client.propose_administrator(&owner, &token, &admin);
    client.accept_admin_role(&token);

    // Set pool
    client.set_pool(&token, &Some(pool.clone()));

    assert_eq!(client.get_pool(&token), Some(pool));
}

#[test]
fn test_set_pool_to_none_delists() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, owner) = setup(&env);
    let token = Address::generate(&env);
    let pool = Address::generate(&env);

    client.propose_administrator(&owner, &token, &Address::generate(&env));
    client.accept_admin_role(&token);

    client.set_pool(&token, &Some(pool.clone()));
    assert_eq!(client.get_pool(&token), Some(pool));

    // Delist by setting pool to None
    client.set_pool(&token, &None);
    assert_eq!(client.get_pool(&token), None);
}

#[test]
#[should_panic(expected = "Error(Contract, #202)")] // OnlyAdministrator
fn test_set_pool_no_admin() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, _owner) = setup(&env);
    let token = Address::generate(&env);
    let pool = Address::generate(&env);

    // Token has no administrator, should fail
    client.set_pool(&token, &Some(pool));
}

// ============================================================
// Transfer Admin Role
// ============================================================

#[test]
fn test_transfer_admin_role() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, owner) = setup(&env);
    let token = Address::generate(&env);
    let admin = Address::generate(&env);
    let new_admin = Address::generate(&env);

    // Register admin
    client.propose_administrator(&owner, &token, &admin);
    client.accept_admin_role(&token);
    assert!(client.is_administrator(&token, &admin));

    // Transfer admin role
    client.transfer_admin_role(&token, &Some(new_admin.clone()));

    let config = client.get_token_config(&token);
    assert_eq!(config.administrator, Some(admin.clone()));
    assert_eq!(config.pending_administrator, Some(new_admin.clone()));

    // New admin accepts
    client.accept_admin_role(&token);

    let config = client.get_token_config(&token);
    assert_eq!(config.administrator, Some(new_admin.clone()));
    assert_eq!(config.pending_administrator, None);
    assert!(!client.is_administrator(&token, &admin));
    assert!(client.is_administrator(&token, &new_admin));
}

#[test]
fn test_transfer_admin_role_cancel() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, owner) = setup(&env);
    let token = Address::generate(&env);
    let new_admin = Address::generate(&env);

    client.propose_administrator(&owner, &token, &Address::generate(&env));
    client.accept_admin_role(&token);

    // Start transfer
    client.transfer_admin_role(&token, &Some(new_admin.clone()));
    assert_eq!(
        client.get_token_config(&token).pending_administrator,
        Some(new_admin)
    );

    // Cancel by setting to None
    client.transfer_admin_role(&token, &None);
    assert_eq!(client.get_token_config(&token).pending_administrator, None);
}

#[test]
#[should_panic(expected = "Error(Contract, #202)")] // OnlyAdministrator
fn test_transfer_admin_role_not_admin() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, _owner) = setup(&env);
    let token = Address::generate(&env);
    let new_admin = Address::generate(&env);

    // No admin registered, should fail
    client.transfer_admin_role(&token, &Some(new_admin));
}

// ============================================================
// Registry Module Management
// ============================================================

#[test]
fn test_add_and_remove_registry_module() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, _owner) = setup(&env);
    let module = Address::generate(&env);

    assert!(!client.is_registry_module(&module));

    client.add_registry_module(&module);
    assert!(client.is_registry_module(&module));

    // Adding again is a no-op
    client.add_registry_module(&module);
    assert!(client.is_registry_module(&module));

    client.remove_registry_module(&module);
    assert!(!client.is_registry_module(&module));

    // Removing again is a no-op
    client.remove_registry_module(&module);
    assert!(!client.is_registry_module(&module));
}

#[test]
fn test_multiple_registry_modules() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, _owner) = setup(&env);
    let module1 = Address::generate(&env);
    let module2 = Address::generate(&env);
    let module3 = Address::generate(&env);

    client.add_registry_module(&module1);
    client.add_registry_module(&module2);
    client.add_registry_module(&module3);

    assert!(client.is_registry_module(&module1));
    assert!(client.is_registry_module(&module2));
    assert!(client.is_registry_module(&module3));

    client.remove_registry_module(&module2);
    assert!(client.is_registry_module(&module1));
    assert!(!client.is_registry_module(&module2));
    assert!(client.is_registry_module(&module3));
}

// ============================================================
// Pagination
// ============================================================

#[test]
fn test_get_all_configured_tokens_pagination() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, owner) = setup(&env);

    let token1 = Address::generate(&env);
    let token2 = Address::generate(&env);
    let token3 = Address::generate(&env);

    client.propose_administrator(&owner, &token1, &Address::generate(&env));
    client.propose_administrator(&owner, &token2, &Address::generate(&env));
    client.propose_administrator(&owner, &token3, &Address::generate(&env));

    // Get all
    let all = client.get_all_configured_tokens(&0, &100);
    assert_eq!(all.len(), 3);
    assert_eq!(all.get(0).unwrap(), token1);
    assert_eq!(all.get(1).unwrap(), token2);
    assert_eq!(all.get(2).unwrap(), token3);

    // Get first 2
    let first_two = client.get_all_configured_tokens(&0, &2);
    assert_eq!(first_two.len(), 2);
    assert_eq!(first_two.get(0).unwrap(), token1);
    assert_eq!(first_two.get(1).unwrap(), token2);

    // Get from index 1, max 2
    let middle = client.get_all_configured_tokens(&1, &2);
    assert_eq!(middle.len(), 2);
    assert_eq!(middle.get(0).unwrap(), token2);
    assert_eq!(middle.get(1).unwrap(), token3);

    // Get from index 2, max 100 (should clamp)
    let last = client.get_all_configured_tokens(&2, &100);
    assert_eq!(last.len(), 1);
    assert_eq!(last.get(0).unwrap(), token3);

    // Start index beyond total
    let empty = client.get_all_configured_tokens(&10, &100);
    assert_eq!(empty.len(), 0);
}

// ============================================================
// Batch Get Pools
// ============================================================

#[test]
fn test_get_pools_batch() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, owner) = setup(&env);

    let token1 = Address::generate(&env);
    let token2 = Address::generate(&env);
    let token3 = Address::generate(&env);
    let pool1 = Address::generate(&env);
    let pool2 = Address::generate(&env);

    // Register and set pools for token1 and token2
    client.propose_administrator(&owner, &token1, &Address::generate(&env));
    client.accept_admin_role(&token1);
    client.set_pool(&token1, &Some(pool1.clone()));

    client.propose_administrator(&owner, &token2, &Address::generate(&env));
    client.accept_admin_role(&token2);
    client.set_pool(&token2, &Some(pool2.clone()));

    // token3 is not registered
    let tokens = vec![&env, token1, token2, token3];
    let pools = client.get_pools(&tokens);

    assert_eq!(pools.len(), 3);
    assert_eq!(pools.get(0).unwrap(), Some(pool1));
    assert_eq!(pools.get(1).unwrap(), Some(pool2));
    assert_eq!(pools.get(2).unwrap(), None);
}

// ============================================================
// Full Lifecycle
// ============================================================

#[test]
fn test_full_lifecycle() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, _) = setup(&env);
    let module = Address::generate(&env);
    let token = Address::generate(&env);
    let admin = Address::generate(&env);
    let pool_v1 = Address::generate(&env);
    let pool_v2 = Address::generate(&env);
    let new_admin = Address::generate(&env);

    // 1. Add registry module
    client.add_registry_module(&module);

    // 2. Module proposes admin (caller=module, proposed admin=admin)
    client.propose_administrator(&module, &token, &admin);

    // 3. Pending admin accepts
    client.accept_admin_role(&token);

    let config = client.get_token_config(&token);
    assert_eq!(config.administrator, Some(admin.clone()));

    // 4. Admin sets pool v1
    client.set_pool(&token, &Some(pool_v1.clone()));
    assert_eq!(client.get_pool(&token), Some(pool_v1));

    // 5. Admin upgrades to pool v2
    client.set_pool(&token, &Some(pool_v2.clone()));
    assert_eq!(client.get_pool(&token), Some(pool_v2));

    // 6. Admin transfers role to new admin
    client.transfer_admin_role(&token, &Some(new_admin.clone()));
    client.accept_admin_role(&token);

    assert!(client.is_administrator(&token, &new_admin));
    assert!(!client.is_administrator(&token, &admin));

    // 7. New admin delists token
    client.set_pool(&token, &None);
    assert_eq!(client.get_pool(&token), None);

    // 8. Token still in enumeration
    let tokens = client.get_all_configured_tokens(&0, &100);
    assert_eq!(tokens.len(), 1);
    assert_eq!(tokens.get(0).unwrap(), token);
}

// ============================================================
// Unregistered Token Defaults
// ============================================================

#[test]
fn test_unregistered_token_returns_defaults() {
    let env = Env::default();
    env.mock_all_auths();

    let (client, _) = setup(&env);
    let token = Address::generate(&env);

    let config = client.get_token_config(&token);
    assert_eq!(
        config,
        TokenConfig {
            administrator: None,
            pending_administrator: None,
            token_pool: None,
        }
    );
    assert_eq!(client.get_pool(&token), None);
    assert!(!client.is_administrator(&token, &Address::generate(&env)));
}

// ============================================================
// Upgradeability (shared `common_authorization::Upgradeable` trait)
// ============================================================

// Real, committed Wasm fixture (checked into data-feeds-common) that exposes a
// `peek() -> u32` reader. TokenAdminRegistry has no `peek`, so a successful
// `peek` call at the registry address after `upgrade` proves the executable was
// swapped in place. Reused instead of adding a new fixture crate (no `stellar`
// CLI here).
const UPGRADE_TARGET_WASM: &[u8] =
    include_bytes!("../../data-feeds/data-feeds-common/test_fixtures/upgrade_target.wasm");

/// Decode the `new_wasm_hash` field from the last `Upgraded` event emitted by
/// `contract`. `#[contractevent]` serializes the struct as a `Map<Symbol, Val>`
/// keyed by field name.
fn upgraded_event_hash(env: &Env, contract: &Address) -> BytesN<32> {
    let evs = env.events().all().filter_by_contract(contract);
    for e in evs.events().iter().rev() {
        let soroban_sdk::xdr::ContractEventBody::V0(ref v0) = e.body;
        let val: Val = v0
            .data
            .clone()
            .try_into_val(env)
            .expect("event data ScVal to Val");
        let Ok(map) = Map::<Symbol, Val>::try_from_val(env, &val) else {
            continue;
        };
        let Some(hval) = map.get(Symbol::new(env, "new_wasm_hash")) else {
            continue;
        };
        if let Ok(hash) = BytesN::<32>::try_from_val(env, &hval) {
            return hash;
        }
    }
    panic!("expected Upgraded event with new_wasm_hash from contract");
}

#[test]
fn test_upgrade_by_owner_swaps_executable_and_emits_event() {
    let env = Env::default();
    env.mock_all_auths();
    let contract_id = env.register(TokenAdminRegistryContract, ());
    let client = TokenAdminRegistryContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    client.initialize(&owner);

    // `mock_all_auths` authorizes the owner, so `require_owner` ->
    // `owner.require_auth()` passes.
    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    client.upgrade(&hash);

    assert_eq!(upgraded_event_hash(&env, &client.address), hash);

    // TokenAdminRegistry has no `peek`; the fixture does. A successful `peek` at
    // the registry address proves the executable was swapped in place.
    let peeked: u32 = env.invoke_contract(
        &client.address,
        &symbol_short!("peek"),
        Vec::<Val>::new(&env),
    );
    assert_eq!(
        peeked, 0,
        "fixture peek must run at the registry address after upgrade"
    );
}

#[test]
#[should_panic(expected = "Error(Auth, InvalidAction)")]
fn test_upgrade_by_non_owner_rejected() {
    let env = Env::default();
    // No `mock_all_auths`: nobody is authorized, so `require_owner` ->
    // `owner.require_auth()` fails. `initialize` needs no auth (it only guards
    // against double-init), so the contract is set up with the owner stored.
    let contract_id = env.register(TokenAdminRegistryContract, ());
    let client = TokenAdminRegistryContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    client.initialize(&owner);

    let hash = env.deployer().upload_contract_wasm(UPGRADE_TARGET_WASM);
    // Caller is not the owner and the owner does not authorize -> reject before
    // the executable is touched.
    client.upgrade(&hash);
}
