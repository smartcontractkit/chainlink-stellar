#![cfg(test)]

use super::*;
use soroban_sdk::{contract, contractimpl, testutils::Address as _, Address, Env, Symbol, Vec};

// ============================================================
// Test Contract
// ============================================================

/// A test contract that wraps the authorization library for testing.
#[contract]
pub struct TestAuthContract;

#[contractimpl]
impl TestAuthContract {
    // ---- Ownable (using DefaultOwnable for testing) ----
    pub fn init_owner(env: Env, owner: Address) {
        DefaultOwnable::init_owner(&env, &owner);
    }

    pub fn get_owner(env: Env) -> Option<Address> {
        DefaultOwnable::owner(&env)
    }

    pub fn is_owner(env: Env, addr: Address) -> bool {
        DefaultOwnable::is_owner(&env, &addr)
    }

    pub fn require_owner(env: Env) -> Result<Address, CCIPError> {
        DefaultOwnable::require_owner(&env)
    }

    pub fn transfer_ownership(env: Env, new_owner: Address) -> Result<(), CCIPError> {
        DefaultOwnable::transfer_ownership(&env, &new_owner)
    }

    pub fn accept_ownership(env: Env) -> Result<(), CCIPError> {
        DefaultOwnable::accept_ownership(&env)
    }

    pub fn get_pending_owner(env: Env) -> Option<Address> {
        DefaultOwnable::get_pending_owner(&env)
    }

    pub fn cancel_transfer(env: Env) -> Result<(), CCIPError> {
        DefaultOwnable::cancel_ownership_transfer(&env)
    }

    // ---- AuthorizedCallers ----
    pub fn init_callers(env: Env, initial_callers: Vec<Address>) {
        AuthorizedCallers::init(&env, initial_callers);
    }

    pub fn callers_enabled(env: Env) -> bool {
        AuthorizedCallers::is_enabled(&env)
    }

    pub fn add_callers(env: Env, callers: Vec<Address>) -> Result<(), CCIPError> {
        AuthorizedCallers::add_callers(&env, callers)
    }

    pub fn remove_callers(env: Env, callers: Vec<Address>) -> Result<(), CCIPError> {
        AuthorizedCallers::remove_callers(&env, callers)
    }

    pub fn get_callers(env: Env) -> Vec<Address> {
        AuthorizedCallers::get_callers(&env)
    }

    pub fn is_authorized(env: Env, addr: Address) -> bool {
        AuthorizedCallers::is_authorized(&env, &addr)
    }

    pub fn require_authorized(env: Env, caller: Address) -> Result<Address, CCIPError> {
        AuthorizedCallers::require_authorized(&env, &caller)
    }

    // ---- AccessControl ----
    pub fn init_access(env: Env) {
        AccessControl::init(&env);
    }

    pub fn access_enabled(env: Env) -> bool {
        AccessControl::is_enabled(&env)
    }

    pub fn grant_role(
        env: Env,
        caller: Address,
        role: Symbol,
        account: Address,
    ) -> Result<(), CCIPError> {
        AccessControl::grant_role(&env, &caller, role, &account)
    }

    pub fn revoke_role(
        env: Env,
        caller: Address,
        role: Symbol,
        account: Address,
    ) -> Result<(), CCIPError> {
        AccessControl::revoke_role(&env, &caller, role, &account)
    }

    pub fn renounce_role(env: Env, role: Symbol, account: Address) -> Result<(), CCIPError> {
        AccessControl::renounce_role(&env, role, &account)
    }

    pub fn has_role(env: Env, role: Symbol, account: Address) -> bool {
        AccessControl::has_role(&env, role, &account)
    }

    pub fn require_role(env: Env, role: Symbol, caller: Address) -> Result<Address, CCIPError> {
        AccessControl::require_role(&env, role, &caller)
    }

    pub fn get_role_members(env: Env, role: Symbol) -> Vec<Address> {
        AccessControl::get_role_members(&env, role)
    }
}

// ============================================================
// Test Helpers
// ============================================================

fn setup_env() -> (Env, Address) {
    let env = Env::default();
    env.mock_all_auths();
    let contract_id = env.register(TestAuthContract, ());
    (env, contract_id)
}

// ============================================================
// Ownable Tests
// ============================================================

#[test]
fn test_ownable_init() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);

    client.init_owner(&owner);

    assert_eq!(client.get_owner(), Some(owner.clone()));
    assert!(client.is_owner(&owner));
}

#[test]
fn test_ownable_not_initialized() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let other = Address::generate(&env);

    assert_eq!(client.get_owner(), None);
    assert!(!client.is_owner(&other));
}

#[test]
fn test_ownable_require_owner() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);

    client.init_owner(&owner);

    let result = client.require_owner();
    assert_eq!(result, owner);
}

#[test]
#[should_panic(expected = "Error(Contract, #4)")]
fn test_ownable_require_owner_not_initialized() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);

    client.require_owner();
}

#[test]
fn test_ownable_two_step_transfer() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let new_owner = Address::generate(&env);

    client.init_owner(&owner);

    // Step 1: Transfer ownership (initiated by owner)
    client.transfer_ownership(&new_owner);
    assert_eq!(client.get_pending_owner(), Some(new_owner.clone()));
    assert_eq!(client.get_owner(), Some(owner.clone())); // Still the old owner

    // Step 2: Accept ownership (by new owner)
    client.accept_ownership();
    assert_eq!(client.get_owner(), Some(new_owner.clone()));
    assert_eq!(client.get_pending_owner(), None);
}

#[test]
fn test_ownable_cancel_transfer() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let new_owner = Address::generate(&env);

    client.init_owner(&owner);
    client.transfer_ownership(&new_owner);

    assert_eq!(client.get_pending_owner(), Some(new_owner.clone()));

    // Cancel the transfer
    client.cancel_transfer();
    assert_eq!(client.get_pending_owner(), None);
    assert_eq!(client.get_owner(), Some(owner)); // Still the original owner
}

#[test]
#[should_panic(expected = "Error(Contract, #5)")]
fn test_ownable_accept_no_pending() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);

    client.init_owner(&owner);
    client.accept_ownership();
}

// ============================================================
// AuthorizedCallers Tests
// ============================================================

#[test]
fn test_authorized_callers_not_enabled() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);

    assert!(!client.callers_enabled());
}

#[test]
#[should_panic(expected = "Error(Contract, #10)")]
fn test_authorized_callers_require_not_enabled() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let random = Address::generate(&env);

    client.require_authorized(&random);
}

#[test]
fn test_authorized_callers_init() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let caller1 = Address::generate(&env);
    let caller2 = Address::generate(&env);

    client.init_owner(&owner);

    let mut initial: Vec<Address> = Vec::new(&env);
    initial.push_back(caller1.clone());
    initial.push_back(caller2.clone());

    client.init_callers(&initial);

    assert!(client.callers_enabled());
    assert!(client.is_authorized(&caller1));
    assert!(client.is_authorized(&caller2));

    let callers = client.get_callers();
    assert_eq!(callers.len(), 2);
}

#[test]
fn test_authorized_callers_add() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let caller1 = Address::generate(&env);
    let caller2 = Address::generate(&env);

    client.init_owner(&owner);
    client.init_callers(&Vec::new(&env));

    let mut to_add: Vec<Address> = Vec::new(&env);
    to_add.push_back(caller1.clone());
    to_add.push_back(caller2.clone());

    client.add_callers(&to_add);

    assert!(client.is_authorized(&caller1));
    assert!(client.is_authorized(&caller2));
}

#[test]
fn test_authorized_callers_add_duplicate() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let caller = Address::generate(&env);

    client.init_owner(&owner);

    let mut initial: Vec<Address> = Vec::new(&env);
    initial.push_back(caller.clone());
    client.init_callers(&initial);

    // Try to add the same caller again
    let mut to_add: Vec<Address> = Vec::new(&env);
    to_add.push_back(caller.clone());
    client.add_callers(&to_add);

    // Should still have only one entry
    let callers = client.get_callers();
    assert_eq!(callers.len(), 1);
}

#[test]
fn test_authorized_callers_remove() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let caller1 = Address::generate(&env);
    let caller2 = Address::generate(&env);

    client.init_owner(&owner);

    let mut initial: Vec<Address> = Vec::new(&env);
    initial.push_back(caller1.clone());
    initial.push_back(caller2.clone());
    client.init_callers(&initial);

    // Remove caller1
    let mut to_remove: Vec<Address> = Vec::new(&env);
    to_remove.push_back(caller1.clone());
    client.remove_callers(&to_remove);

    assert!(!client.is_authorized(&caller1));
    assert!(client.is_authorized(&caller2));

    let callers = client.get_callers();
    assert_eq!(callers.len(), 1);
}

#[test]
fn test_authorized_callers_require_authorized() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let caller = Address::generate(&env);

    client.init_owner(&owner);

    let mut initial: Vec<Address> = Vec::new(&env);
    initial.push_back(caller.clone());
    client.init_callers(&initial);

    let result = client.require_authorized(&caller);
    assert_eq!(result, caller);
}

#[test]
#[should_panic(expected = "Error(Contract, #6)")]
fn test_authorized_callers_require_empty_list() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let random = Address::generate(&env);

    client.init_owner(&owner);
    client.init_callers(&Vec::new(&env));

    client.require_authorized(&random);
}

#[test]
#[should_panic(expected = "Error(Contract, #10)")]
fn test_authorized_callers_add_not_enabled() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let caller = Address::generate(&env);

    client.init_owner(&owner);

    let mut to_add: Vec<Address> = Vec::new(&env);
    to_add.push_back(caller);

    client.add_callers(&to_add);
}

// ============================================================
// AccessControl Tests
// ============================================================

#[test]
fn test_access_control_not_enabled() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);

    assert!(!client.access_enabled());
}

#[test]
#[should_panic(expected = "Error(Contract, #10)")]
fn test_access_control_require_not_enabled() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let random = Address::generate(&env);

    client.require_role(&ROLE_MINTER, &random);
}

#[test]
fn test_access_control_init() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    assert!(client.access_enabled());
}

#[test]
fn test_access_control_grant_role() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let minter = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    client.grant_role(&owner, &ROLE_MINTER, &minter);
    assert!(client.has_role(&ROLE_MINTER, &minter));
}

#[test]
fn test_access_control_revoke_role() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let minter = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    client.grant_role(&owner, &ROLE_MINTER, &minter);
    assert!(client.has_role(&ROLE_MINTER, &minter));

    client.revoke_role(&owner, &ROLE_MINTER, &minter);
    assert!(!client.has_role(&ROLE_MINTER, &minter));
}

#[test]
fn test_access_control_renounce_role() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let minter = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    client.grant_role(&owner, &ROLE_MINTER, &minter);
    assert!(client.has_role(&ROLE_MINTER, &minter));

    client.renounce_role(&ROLE_MINTER, &minter);
    assert!(!client.has_role(&ROLE_MINTER, &minter));
}

#[test]
#[should_panic(expected = "Error(Contract, #12)")]
fn test_access_control_renounce_role_not_granted() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let random = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    client.renounce_role(&ROLE_MINTER, &random);
}

#[test]
fn test_access_control_require_role() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let minter = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    client.grant_role(&owner, &ROLE_MINTER, &minter);

    let result = client.require_role(&ROLE_MINTER, &minter);
    assert_eq!(result, minter);
}

#[test]
#[should_panic(expected = "Error(Contract, #9)")]
fn test_access_control_require_role_not_granted() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let random = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    client.require_role(&ROLE_MINTER, &random);
}

#[test]
fn test_access_control_get_role_members() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let minter1 = Address::generate(&env);
    let minter2 = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    client.grant_role(&owner, &ROLE_MINTER, &minter1);
    client.grant_role(&owner, &ROLE_MINTER, &minter2);

    let members = client.get_role_members(&ROLE_MINTER);
    assert_eq!(members.len(), 2);
}

#[test]
fn test_access_control_admin_can_grant() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let admin = Address::generate(&env);
    let minter = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    // Owner grants ADMIN role to admin
    client.grant_role(&owner, &ROLE_ADMIN, &admin);

    // Admin can now grant other roles (passing admin as caller)
    client.grant_role(&admin, &ROLE_MINTER, &minter);
    assert!(client.has_role(&ROLE_MINTER, &minter));
}

#[test]
fn test_access_control_multiple_roles() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let user = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    // Grant multiple roles to same user
    client.grant_role(&owner, &ROLE_MINTER, &user);
    client.grant_role(&owner, &ROLE_BURNER, &user);

    assert!(client.has_role(&ROLE_MINTER, &user));
    assert!(client.has_role(&ROLE_BURNER, &user));

    // Revoke one role
    client.revoke_role(&owner, &ROLE_MINTER, &user);

    assert!(!client.has_role(&ROLE_MINTER, &user));
    assert!(client.has_role(&ROLE_BURNER, &user));
}

#[test]
fn test_access_control_custom_role() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let operator = Address::generate(&env);
    let custom_role = symbol_short!("OPERATOR");

    client.init_owner(&owner);
    client.init_access();

    client.grant_role(&owner, &custom_role, &operator);

    assert!(client.has_role(&custom_role, &operator));

    let result = client.require_role(&custom_role, &operator);
    assert_eq!(result, operator);
}

// ============================================================
// Integration Tests
// ============================================================

#[test]
fn test_combined_authorization() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let price_updater = Address::generate(&env);
    let minter = Address::generate(&env);

    // Initialize all three features
    client.init_owner(&owner);

    let mut callers: Vec<Address> = Vec::new(&env);
    callers.push_back(price_updater.clone());
    client.init_callers(&callers);

    client.init_access();
    client.grant_role(&owner, &ROLE_MINTER, &minter);

    // Verify all work correctly
    assert!(client.is_owner(&owner));
    assert!(client.is_authorized(&price_updater));
    assert!(client.has_role(&ROLE_MINTER, &minter));

    // Different addresses are not authorized
    let random = Address::generate(&env);
    assert!(!client.is_owner(&random));
    assert!(!client.is_authorized(&random));
    assert!(!client.has_role(&ROLE_MINTER, &random));
}
