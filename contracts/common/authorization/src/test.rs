#![cfg(test)]

use super::*;
use common_error::CCIPError;
use soroban_sdk::{
    contract, contractimpl,
    testutils::{Address as _, Events as _},
    Address, Env, Event, Symbol, Vec,
};

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
        let _ = DefaultOwnable::init_owner(&env, &owner);
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

    pub fn set_new_owner(env: Env, new_owner: Address) -> Result<(), CCIPError> {
        DefaultOwnable::set_new_owner(&env, &new_owner)
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

    pub fn require_authorized_caller(env: Env, caller: Address) -> Result<(), CCIPError> {
        AuthorizedCallers::require_authorized_caller(&env, &caller)
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
        sender: Address,
        role: Symbol,
        account: Address,
    ) -> Result<(), CCIPError> {
        AccessControl::grant_role(&env, &sender, role, &account)
    }

    pub fn revoke_role(
        env: Env,
        sender: Address,
        role: Symbol,
        account: Address,
    ) -> Result<(), CCIPError> {
        AccessControl::revoke_role(&env, &sender, role, &account)
    }

    pub fn renounce_role(env: Env, role: Symbol, account: Address) -> Result<(), CCIPError> {
        AccessControl::renounce_role(&env, role, &account)
    }

    pub fn has_role(env: Env, role: Symbol, account: Address) -> bool {
        AccessControl::has_role(&env, role, &account)
    }

    pub fn require_role(env: Env, subject: Address, role: Symbol) -> Result<Address, CCIPError> {
        AccessControl::require_role(&env, &subject, role)
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

/// Assert that the most recent event published by `contract_id` is `ev`.
fn assert_latest_event<E: Event>(env: &Env, contract_id: &Address, ev: E) {
    let want = ev.to_xdr(env, contract_id);
    let evs = env.events().all().filter_by_contract(contract_id);
    assert_eq!(
        evs.events().last(),
        Some(&want),
        "{} was not the most recent event emitted",
        core::any::type_name::<E>()
    );
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
fn test_ownable_two_step_transfer_publishes_events() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let new_owner = Address::generate(&env);

    client.init_owner(&owner);

    client.transfer_ownership(&new_owner);
    assert_latest_event(
        &env,
        &contract_id,
        OwnershipTransferStartedEvent {
            previous_owner: owner.clone(),
            new_owner: new_owner.clone(),
        },
    );

    client.accept_ownership();
    assert_latest_event(
        &env,
        &contract_id,
        OwnershipTransferredEvent {
            previous_owner: owner,
            new_owner,
        },
    );
}

#[test]
fn test_ownable_set_new_owner() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let new_owner = Address::generate(&env);

    client.init_owner(&owner);
    client.set_new_owner(&new_owner);

    assert_eq!(client.get_owner(), Some(new_owner.clone()));
    assert!(client.is_owner(&new_owner));
    assert!(!client.is_owner(&owner));
    assert_eq!(client.get_pending_owner(), None);
}

#[test]
fn test_ownable_set_new_owner_publishes_event() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let new_owner = Address::generate(&env);

    client.init_owner(&owner);
    client.set_new_owner(&new_owner);

    assert_latest_event(
        &env,
        &contract_id,
        OwnershipTransferredEvent {
            previous_owner: owner,
            new_owner,
        },
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #4)")]
fn test_ownable_set_new_owner_not_initialized() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let new_owner = Address::generate(&env);

    client.set_new_owner(&new_owner);
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
fn test_authorized_callers_require_not_enabled() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let addr = Address::generate(&env);

    let err = client
        .try_require_authorized_caller(&addr)
        .unwrap_err()
        .unwrap();
    assert_eq!(err, CCIPError::FeatureNotEnabled);
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
fn test_authorized_callers_require_authorized_caller() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let caller = Address::generate(&env);

    client.init_owner(&owner);

    let mut initial: Vec<Address> = Vec::new(&env);
    initial.push_back(caller.clone());
    client.init_callers(&initial);

    client.require_authorized_caller(&caller);
}

#[test]
fn test_authorized_callers_second_member_can_authorize() {
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

    // Either allowlisted principal can satisfy auth when passed explicitly (OR semantics).
    client.require_authorized_caller(&caller2);
}

#[test]
fn test_authorized_callers_require_empty_list() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let addr = Address::generate(&env);

    client.init_owner(&owner);
    client.init_callers(&Vec::new(&env));

    let err = client
        .try_require_authorized_caller(&addr)
        .unwrap_err()
        .unwrap();
    assert_eq!(err, CCIPError::CallerNotAuthorized);
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
fn test_access_control_require_not_enabled() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let subject = Address::generate(&env);

    let err = client
        .try_require_role(&subject, &ROLE_MINTER)
        .unwrap_err()
        .unwrap();
    assert_eq!(err, CCIPError::FeatureNotEnabled);
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
    let user = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    client.renounce_role(&ROLE_MINTER, &user);
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

    let result = client.require_role(&minter, &ROLE_MINTER);
    assert_eq!(result, minter);
}

#[test]
fn test_access_control_require_role_not_granted() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    let err = client
        .try_require_role(&owner, &ROLE_MINTER)
        .unwrap_err()
        .unwrap();
    assert_eq!(err, CCIPError::RoleNotGranted);
}

#[test]
fn test_access_control_require_role_second_member() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let minter1 = Address::generate(&env);
    let minter2 = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    client.grant_role(&owner, &ROLE_MINTER, &minter1);
    client.grant_role(&owner, &ROLE_MINTER, &minter2);

    assert_eq!(client.require_role(&minter1, &ROLE_MINTER), minter1);
    assert_eq!(client.require_role(&minter2, &ROLE_MINTER), minter2);
}

#[test]
fn test_access_control_grant_role_wrong_sender() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let intruder = Address::generate(&env);
    let minter = Address::generate(&env);

    client.init_owner(&owner);
    client.init_access();

    let err = client
        .try_grant_role(&intruder, &ROLE_MINTER, &minter)
        .unwrap_err()
        .unwrap();
    assert_eq!(err, CCIPError::CallerNotAuthorized);
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

    // Admin can now grant other roles
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

    let result = client.require_role(&operator, &custom_role);
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
