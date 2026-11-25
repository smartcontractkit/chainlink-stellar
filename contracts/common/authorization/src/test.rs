#![cfg(test)]

use super::*;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use soroban_sdk::{contract, contractimpl, testutils::Address as _, Address, Env, Symbol, Vec};

// ============================================================
// Test Contract
// ============================================================

/// A test contract that wires the authorization traits the way production
/// contracts do: `Ownable`/`AuthorizedCallers`/`AccessControl` implemented on
/// the contract itself, each with its own storage key. The previous form
/// wrapped the free-function helpers and seeded the `DefaultOwnable` stand-in
/// (`AUTHOWN`) directly, which no production contract writes — masking the
/// permanently-`NotOwner` owner gates this file now pins as regressions.
#[contract]
pub struct TestAuthContract;

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const AUTHORIZED_CALLERS: Symbol = symbol_short!("AUTHCALL");
const ACCESS_ROLES: Symbol = symbol_short!("AUTHROLE");

#[contractimpl]
impl Initializable for TestAuthContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl]
impl Ownable for TestAuthContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

impl AuthorizedCallers for TestAuthContract {
    const AUTHORIZED_CALLERS: Symbol = AUTHORIZED_CALLERS;
}

impl AccessControl for TestAuthContract {
    const ACCESS_ROLES: Symbol = ACCESS_ROLES;
}

#[contractimpl]
impl TestAuthContract {
    // ---- Ownable ----
    pub fn init_owner(env: Env, owner: Address) -> Result<(), CCIPError> {
        <Self as Ownable>::init_owner(&env, &owner)
    }

    pub fn get_owner(env: Env) -> Option<Address> {
        <Self as Ownable>::owner(&env)
    }

    pub fn is_owner(env: Env, addr: Address) -> bool {
        <Self as Ownable>::is_owner(&env, &addr)
    }

    pub fn require_owner(env: Env) -> Result<Address, CCIPError> {
        <Self as Ownable>::require_owner(&env)
    }

    pub fn transfer_ownership(env: Env, new_owner: Address) -> Result<(), CCIPError> {
        <Self as Ownable>::transfer_ownership(&env, &new_owner)
    }

    pub fn accept_ownership(env: Env) -> Result<(), CCIPError> {
        <Self as Ownable>::accept_ownership(&env)
    }

    pub fn get_pending_owner(env: Env) -> Option<Address> {
        <Self as Ownable>::get_pending_owner(&env)
    }

    pub fn cancel_transfer(env: Env) -> Result<(), CCIPError> {
        <Self as Ownable>::cancel_ownership_transfer(&env)
    }

    // ---- AuthorizedCallers ----
    pub fn init_callers(env: Env, initial_callers: Vec<Address>) {
        <Self as AuthorizedCallers>::init_authorized_callers(&env, initial_callers);
    }

    pub fn callers_enabled(env: Env) -> bool {
        <Self as AuthorizedCallers>::is_authorized_callers_enabled(&env)
    }

    pub fn get_callers(env: Env) -> Vec<Address> {
        <Self as AuthorizedCallers>::get_authorized_callers(&env)
    }

    pub fn is_authorized(env: Env, addr: Address) -> bool {
        <Self as AuthorizedCallers>::is_authorized_caller(&env, &addr)
    }

    pub fn require_authorized_caller(env: Env, caller: Address) -> Result<(), CCIPError> {
        <Self as AuthorizedCallers>::require_authorized_caller(&env, &caller)
    }

    pub fn apply_caller_updates(
        env: Env,
        caller: Address,
        added: Vec<Address>,
        removed: Vec<Address>,
    ) -> Result<(), CCIPError> {
        <Self as AuthorizedCallers>::apply_authorized_caller_updates(&env, &caller, added, removed)
    }

    // ---- AccessControl ----
    pub fn init_access(env: Env) {
        <Self as AccessControl>::init_access_control(&env);
    }

    pub fn access_enabled(env: Env) -> bool {
        <Self as AccessControl>::is_access_control_enabled(&env)
    }

    pub fn grant_role(
        env: Env,
        sender: Address,
        role: Symbol,
        account: Address,
    ) -> Result<(), CCIPError> {
        <Self as AccessControl>::grant_role(&env, &sender, role, &account)
    }

    pub fn revoke_role(
        env: Env,
        sender: Address,
        role: Symbol,
        account: Address,
    ) -> Result<(), CCIPError> {
        <Self as AccessControl>::revoke_role(&env, &sender, role, &account)
    }

    pub fn renounce_role(env: Env, role: Symbol, account: Address) -> Result<(), CCIPError> {
        <Self as AccessControl>::renounce_role(&env, role, &account)
    }

    pub fn has_role(env: Env, role: Symbol, account: Address) -> bool {
        <Self as AccessControl>::has_role(&env, role, &account)
    }

    pub fn require_role(env: Env, subject: Address, role: Symbol) -> Result<Address, CCIPError> {
        <Self as AccessControl>::require_role(&env, &subject, role)
    }

    pub fn get_role_members(env: Env, role: Symbol) -> Vec<Address> {
        <Self as AccessControl>::get_role_members(&env, role)
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

/// Regression for the original defect: the owner gate must read the EMBEDDING
/// contract's own owner key. The free-function form gated on `DefaultOwnable`
/// (`AUTHOWN`), a stand-in no production contract writes, so every owner-gated
/// update failed with `NotOwner` even for the real owner.
#[test]
fn test_authorized_caller_updates_owner_gate_reads_contract_owner() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let updater1 = Address::generate(&env);
    let updater2 = Address::generate(&env);

    // Owner is set through the contract's Ownable impl only — no stand-in key.
    client.init_owner(&owner);
    client.init_callers(&Vec::new(&env));

    let mut added: Vec<Address> = Vec::new(&env);
    added.push_back(updater1.clone());
    added.push_back(updater2.clone());
    client.apply_caller_updates(&owner, &added, &Vec::new(&env));

    assert!(client.is_authorized(&updater1));
    assert!(client.is_authorized(&updater2));

    // The new owner after a two-step transfer can update the set too.
    client.transfer_ownership(&updater2);
    client.accept_ownership();

    let mut removed: Vec<Address> = Vec::new(&env);
    removed.push_back(updater1.clone());
    client.apply_caller_updates(&updater2, &Vec::new(&env), &removed);

    assert!(!client.is_authorized(&updater1));
    assert!(client.is_authorized(&updater2));
}

#[test]
fn test_authorized_caller_updates_non_owner_rejected() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let intruder = Address::generate(&env);
    let updater = Address::generate(&env);

    client.init_owner(&owner);
    client.init_callers(&Vec::new(&env));

    let mut added: Vec<Address> = Vec::new(&env);
    added.push_back(updater.clone());
    let err = client
        .try_apply_caller_updates(&intruder, &added, &Vec::new(&env))
        .unwrap_err()
        .unwrap();
    assert_eq!(err, CCIPError::CallerNotAuthorized);
    assert!(!client.is_authorized(&updater));
}

#[test]
fn test_authorized_caller_updates_add_duplicate() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let updater = Address::generate(&env);

    client.init_owner(&owner);

    let mut initial: Vec<Address> = Vec::new(&env);
    initial.push_back(updater.clone());
    client.init_callers(&initial);

    // Re-adding an existing caller is a no-op (deduped).
    let mut added: Vec<Address> = Vec::new(&env);
    added.push_back(updater.clone());
    client.apply_caller_updates(&owner, &added, &Vec::new(&env));

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

/// Within-batch idempotency: adding the same address twice in one call must
/// store it once. The pre-fix membership check read the stored list (written
/// only after the loops), so both iterations missed the address.
#[test]
fn test_authorized_caller_updates_same_batch_duplicate_add() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let updater = Address::generate(&env);

    client.init_owner(&owner);
    let mut initial: Vec<Address> = Vec::new(&env);
    initial.push_back(Address::generate(&env));
    client.init_callers(&initial);

    let mut added: Vec<Address> = Vec::new(&env);
    added.push_back(updater.clone());
    added.push_back(updater.clone());
    client.apply_caller_updates(&owner, &added, &Vec::new(&env));

    let callers = client.get_callers();
    assert_eq!(
        callers.len(),
        2,
        "duplicate in-batch add must store one entry"
    );
    let mut occurrences: u32 = 0;
    for c in callers.iter() {
        if c == updater {
            occurrences += 1;
        }
    }
    assert_eq!(occurrences, 1);
}

/// Within-batch remove-then-add nets to present (EVM applyAuthorizedCallerUpdates
/// semantics: removals first, then additions). The pre-fix storage-backed
/// membership check saw the still-stored address and skipped the re-add,
/// leaving it removed.
#[test]
fn test_authorized_caller_updates_remove_and_readd_same_batch() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let updater = Address::generate(&env);

    client.init_owner(&owner);
    let mut initial: Vec<Address> = Vec::new(&env);
    initial.push_back(updater.clone());
    client.init_callers(&initial);

    let mut added: Vec<Address> = Vec::new(&env);
    added.push_back(updater.clone());
    let mut removed: Vec<Address> = Vec::new(&env);
    removed.push_back(updater.clone());
    client.apply_caller_updates(&owner, &added, &removed);

    assert!(
        client.is_authorized(&updater),
        "remove-then-add in one batch must net to present"
    );
    assert_eq!(client.get_callers().len(), 1);
}

#[test]
#[should_panic(expected = "Error(Contract, #10)")]
fn test_authorized_caller_updates_not_enabled() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let updater = Address::generate(&env);

    client.init_owner(&owner);

    let mut added: Vec<Address> = Vec::new(&env);
    added.push_back(updater);
    client.apply_caller_updates(&owner, &added, &Vec::new(&env));
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

/// Regression for the original defect: role management must resolve the owner
/// through the EMBEDDING contract's own owner key. The free-function form read
/// the `DefaultOwnable` stand-in (`AUTHOWN`), which nothing writes — the owner
/// branch was unreachable and role grants fell back to ADMIN-only (with no
/// ADMIN members seeded, fully deadlocked).
#[test]
fn test_access_control_owner_gate_reads_contract_owner() {
    let (env, contract_id) = setup_env();
    let client = TestAuthContractClient::new(&env, &contract_id);
    let owner = Address::generate(&env);
    let minter = Address::generate(&env);

    // Owner is set through the contract's Ownable impl only — no stand-in key.
    client.init_owner(&owner);
    client.init_access();

    client.grant_role(&owner, &ROLE_MINTER, &minter);
    assert!(client.has_role(&ROLE_MINTER, &minter));
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

    // Initialize all three features through the contract's own wiring
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
