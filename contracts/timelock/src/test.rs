//! Integration-style contract tests for RBACTimelock.

#![cfg(test)]

use soroban_sdk::testutils::{Address as _, Ledger as _};
use soroban_sdk::xdr::ToXdr;
use soroban_sdk::{
    address_payload::AddressPayload, contract, contractimpl, symbol_short, Address, Bytes, BytesN,
    Env, IntoVal, Symbol, Val, Vec as SorobanVec,
};

use crate::error::TimelockError;
use crate::types::{
    Call, Calls, ADMIN_ROLE, BYPASSER_ROLE, CANCELLER_ROLE, EXECUTOR_ROLE, PROPOSER_ROLE,
};
use crate::{TimelockContract, TimelockContractClient};

// -------------------------------------------------------------------------
// Mock target contract
// -------------------------------------------------------------------------

#[contract]
pub struct MockTarget;

#[contractimpl]
impl MockTarget {
    pub fn set_value(env: Env, value: u32) {
        env.storage().instance().set(&symbol_short!("VAL"), &value);
    }
    pub fn get_value(env: Env) -> u32 {
        env.storage()
            .instance()
            .get::<_, u32>(&symbol_short!("VAL"))
            .unwrap_or(0)
    }
}

// -------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------

fn register_timelock(env: &Env) -> TimelockContractClient<'_> {
    let id = env.register(TimelockContract, ());
    TimelockContractClient::new(env, &id)
}

fn addr_to_contract_id(addr: &Address, env: &Env) -> BytesN<32> {
    match addr.to_payload() {
        Some(AddressPayload::ContractIdHash(id)) => id,
        _ => BytesN::from_array(env, &[0u8; 32]),
    }
}

fn zero_bytes32(env: &Env) -> BytesN<32> {
    BytesN::from_array(env, &[0u8; 32])
}

fn salt(env: &Env, v: u8) -> BytesN<32> {
    let mut s = [0u8; 32];
    s[31] = v;
    BytesN::from_array(env, &s)
}

fn encode_set_value(env: &Env, value: u32) -> Bytes {
    let mut payload: SorobanVec<Val> = SorobanVec::new(env);
    payload.push_back(Symbol::new(env, "set_value").into_val(env));
    payload.push_back(value.into_val(env));
    payload.to_xdr(env)
}

fn make_call(to: &BytesN<32>, data: Bytes) -> Call {
    Call {
        to: to.clone(),
        data,
    }
}

fn single_calls(env: &Env, call: Call) -> Calls {
    Calls {
        inner: SorobanVec::from_array(env, [call]),
    }
}

/// Initialize timelock with one address in each role, min_delay = 100.
fn setup_full<'a>(
    env: &'a Env,
    admin: &Address,
    proposer: &Address,
    executor: &Address,
    canceller: &Address,
    bypasser: &Address,
) -> TimelockContractClient<'a> {
    let client = register_timelock(env);
    client.initialize(
        &100u64,
        admin,
        &SorobanVec::from_array(env, [proposer.clone()]),
        &SorobanVec::from_array(env, [executor.clone()]),
        &SorobanVec::from_array(env, [canceller.clone()]),
        &SorobanVec::from_array(env, [bypasser.clone()]),
    );
    client
}

// -------------------------------------------------------------------------
// Initialization
// -------------------------------------------------------------------------

#[test]
fn test_initialize_sets_roles_and_delay() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let proposer = Address::generate(&env);
    let executor = Address::generate(&env);
    let canceller = Address::generate(&env);
    let bypasser = Address::generate(&env);

    let client = setup_full(&env, &admin, &proposer, &executor, &canceller, &bypasser);

    assert_eq!(client.get_min_delay(), 100u64);
    assert!(client.has_role(&ADMIN_ROLE, &admin));
    assert!(client.has_role(&PROPOSER_ROLE, &proposer));
    assert!(client.has_role(&EXECUTOR_ROLE, &executor));
    assert!(client.has_role(&CANCELLER_ROLE, &canceller));
    assert!(client.has_role(&BYPASSER_ROLE, &bypasser));
    assert_eq!(client.get_role_member_count(&ADMIN_ROLE), 1);
    assert_eq!(client.get_role_member(&ADMIN_ROLE, &0u32), admin);
}

#[test]
#[should_panic]
fn test_double_initialize_panics() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&100u64, &admin, &empty, &empty, &empty, &empty);
    client.initialize(&100u64, &admin, &empty, &empty, &empty, &empty);
}

// -------------------------------------------------------------------------
// Role management
// -------------------------------------------------------------------------

#[test]
fn test_grant_and_revoke_role() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let user = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    assert!(!client.has_role(&PROPOSER_ROLE, &user));
    client.grant_role(&admin, &PROPOSER_ROLE, &user);
    assert!(client.has_role(&PROPOSER_ROLE, &user));
    assert_eq!(client.get_role_member_count(&PROPOSER_ROLE), 1);

    client.revoke_role(&admin, &PROPOSER_ROLE, &user);
    assert!(!client.has_role(&PROPOSER_ROLE, &user));
    assert_eq!(client.get_role_member_count(&PROPOSER_ROLE), 0);
}

#[test]
fn test_grant_role_requires_admin() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let non_admin = Address::generate(&env);
    let user = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    assert!(matches!(
        client.try_grant_role(&non_admin, &PROPOSER_ROLE, &user),
        Err(Ok(TimelockError::NotAuthorized))
    ));
}

#[test]
fn test_renounce_role() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let user = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(
        &0u64,
        &admin,
        &SorobanVec::from_array(&env, [user.clone()]),
        &empty,
        &empty,
        &empty,
    );

    assert!(client.has_role(&PROPOSER_ROLE, &user));
    client.renounce_role(&user, &PROPOSER_ROLE);
    assert!(!client.has_role(&PROPOSER_ROLE, &user));
}

#[test]
fn test_admin_has_all_role_access() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    // Admin can schedule (PROPOSER or ADMIN)
    let calls = Calls {
        inner: SorobanVec::new(&env),
    };
    client.schedule_batch(&admin, &calls, &zero_bytes32(&env), &salt(&env, 1), &0u64);

    // Admin can cancel scheduled op (using empty calls batch here)
    let id = client.hash_operation_batch(&calls, &zero_bytes32(&env), &salt(&env, 1));
    client.cancel(&admin, &id);
}

// -------------------------------------------------------------------------
// Scheduling
// -------------------------------------------------------------------------

#[test]
fn test_schedule_batch_creates_pending_operation() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| li.timestamp = 1000);

    let admin = Address::generate(&env);
    let proposer = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(
        &100u64,
        &admin,
        &SorobanVec::from_array(&env, [proposer.clone()]),
        &empty,
        &empty,
        &empty,
    );

    let calls = Calls {
        inner: SorobanVec::new(&env),
    };
    let predecessor = zero_bytes32(&env);
    let s = salt(&env, 42);

    client.schedule_batch(&proposer, &calls, &predecessor, &s, &100u64);

    let id = client.hash_operation_batch(&calls, &predecessor, &s);
    assert!(client.is_operation(&id));
    assert!(client.is_operation_pending(&id));
    assert!(!client.is_operation_ready(&id));
    assert!(!client.is_operation_done(&id));
    // Ready at 1000 + 100 = 1100
    assert_eq!(client.get_timestamp(&id), 1100u64);
}

#[test]
fn test_schedule_batch_insufficient_delay() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let proposer = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(
        &100u64,
        &admin,
        &SorobanVec::from_array(&env, [proposer.clone()]),
        &empty,
        &empty,
        &empty,
    );

    let calls = Calls {
        inner: SorobanVec::new(&env),
    };
    assert!(matches!(
        client.try_schedule_batch(
            &proposer,
            &calls,
            &zero_bytes32(&env),
            &salt(&env, 1),
            &50u64
        ),
        Err(Ok(TimelockError::InsufficientDelay))
    ));
}

#[test]
fn test_schedule_batch_already_scheduled() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    let calls = Calls {
        inner: SorobanVec::new(&env),
    };
    let s = salt(&env, 1);
    client.schedule_batch(&admin, &calls, &zero_bytes32(&env), &s, &0u64);
    assert!(matches!(
        client.try_schedule_batch(&admin, &calls, &zero_bytes32(&env), &s, &0u64),
        Err(Ok(TimelockError::OperationAlreadyScheduled))
    ));
}

#[test]
fn test_schedule_blocked_selector_rejected() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let target = BytesN::from_array(&env, &[5u8; 32]);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    let blocked_fn = Symbol::new(&env, "dangerous");
    client.block_function_selector(&admin, &blocked_fn);

    let mut payload: SorobanVec<Val> = SorobanVec::new(&env);
    payload.push_back(blocked_fn.into_val(&env));
    let data = payload.to_xdr(&env);

    let call = make_call(&target, data);
    let calls = single_calls(&env, call);

    assert!(matches!(
        client.try_schedule_batch(&admin, &calls, &zero_bytes32(&env), &salt(&env, 1), &0u64),
        Err(Ok(TimelockError::SelectorIsBlocked))
    ));
}

#[test]
fn test_schedule_requires_proposer_or_admin() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let stranger = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    let calls = Calls {
        inner: SorobanVec::new(&env),
    };
    assert!(matches!(
        client.try_schedule_batch(
            &stranger,
            &calls,
            &zero_bytes32(&env),
            &salt(&env, 1),
            &0u64
        ),
        Err(Ok(TimelockError::NotAuthorized))
    ));
}

// -------------------------------------------------------------------------
// Execution
// -------------------------------------------------------------------------

#[test]
fn test_execute_batch_after_delay() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| li.timestamp = 1000);

    let admin = Address::generate(&env);
    let proposer = Address::generate(&env);
    let executor = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);

    let client = register_timelock(&env);
    client.initialize(
        &100u64,
        &admin,
        &SorobanVec::from_array(&env, [proposer.clone()]),
        &SorobanVec::from_array(&env, [executor.clone()]),
        &empty,
        &empty,
    );

    // Deploy mock target
    let target_addr = env.register(MockTarget, ());
    let target_client = MockTargetClient::new(&env, &target_addr);
    let target_id = addr_to_contract_id(&target_addr, &env);

    let call = make_call(&target_id, encode_set_value(&env, 99));
    let calls = single_calls(&env, call);
    let predecessor = zero_bytes32(&env);
    let s = salt(&env, 7);

    client.schedule_batch(&proposer, &calls, &predecessor, &s, &100u64);

    // Before delay passes — not ready
    let id = client.hash_operation_batch(&calls, &predecessor, &s);
    assert!(!client.is_operation_ready(&id));
    assert!(matches!(
        client.try_execute_batch(&executor, &calls, &predecessor, &s),
        Err(Ok(TimelockError::OperationNotReady))
    ));

    // Advance time past delay
    env.ledger().with_mut(|li| li.timestamp = 1101);
    assert!(client.is_operation_ready(&id));

    client.execute_batch(&executor, &calls, &predecessor, &s);

    assert!(client.is_operation_done(&id));
    assert_eq!(target_client.get_value(), 99u32);
}

#[test]
fn test_execute_requires_executor_or_admin() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let stranger = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    // Schedule an empty batch that is immediately ready (delay 0)
    let calls = Calls {
        inner: SorobanVec::new(&env),
    };
    let s = salt(&env, 1);
    client.schedule_batch(&admin, &calls, &zero_bytes32(&env), &s, &0u64);

    assert!(matches!(
        client.try_execute_batch(&stranger, &calls, &zero_bytes32(&env), &s),
        Err(Ok(TimelockError::NotAuthorized))
    ));
}

#[test]
fn test_execute_predecessor_dependency() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| li.timestamp = 1000);

    let admin = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    let target_addr = env.register(MockTarget, ());
    let target_id = addr_to_contract_id(&target_addr, &env);

    // Operation A
    let calls_a = single_calls(&env, make_call(&target_id, encode_set_value(&env, 1)));
    let s_a = salt(&env, 1);
    client.schedule_batch(&admin, &calls_a, &zero_bytes32(&env), &s_a, &0u64);
    let id_a = client.hash_operation_batch(&calls_a, &zero_bytes32(&env), &s_a);

    // Operation B depends on A
    let calls_b = single_calls(&env, make_call(&target_id, encode_set_value(&env, 2)));
    let s_b = salt(&env, 2);
    client.schedule_batch(&admin, &calls_b, &id_a, &s_b, &0u64);

    // B cannot execute before A
    assert!(matches!(
        client.try_execute_batch(&admin, &calls_b, &id_a, &s_b),
        Err(Ok(TimelockError::MissingPredecessor))
    ));

    // Execute A, then B
    client.execute_batch(&admin, &calls_a, &zero_bytes32(&env), &s_a);
    assert!(client.is_operation_done(&id_a));
    client.execute_batch(&admin, &calls_b, &id_a, &s_b);
    let id_b = client.hash_operation_batch(&calls_b, &id_a, &s_b);
    assert!(client.is_operation_done(&id_b));
}

// -------------------------------------------------------------------------
// Cancellation
// -------------------------------------------------------------------------

#[test]
fn test_cancel_pending_operation() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| li.timestamp = 1000);

    let admin = Address::generate(&env);
    let canceller = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(
        &100u64,
        &admin,
        &empty,
        &empty,
        &SorobanVec::from_array(&env, [canceller.clone()]),
        &empty,
    );

    let calls = Calls {
        inner: SorobanVec::new(&env),
    };
    let s = salt(&env, 1);
    client.schedule_batch(&admin, &calls, &zero_bytes32(&env), &s, &100u64);

    let id = client.hash_operation_batch(&calls, &zero_bytes32(&env), &s);
    assert!(client.is_operation_pending(&id));

    client.cancel(&canceller, &id);
    assert!(!client.is_operation(&id));
}

#[test]
fn test_cancel_executed_operation_fails() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| li.timestamp = 1000);
    let admin = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    let calls = Calls {
        inner: SorobanVec::new(&env),
    };
    let s = salt(&env, 1);
    client.schedule_batch(&admin, &calls, &zero_bytes32(&env), &s, &0u64);
    client.execute_batch(&admin, &calls, &zero_bytes32(&env), &s);

    let id = client.hash_operation_batch(&calls, &zero_bytes32(&env), &s);
    assert!(client.is_operation_done(&id));

    assert!(matches!(
        client.try_cancel(&admin, &id),
        Err(Ok(TimelockError::OperationCannotBeCancelled))
    ));
}

#[test]
fn test_cancel_requires_canceller_or_admin() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let stranger = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    let calls = Calls {
        inner: SorobanVec::new(&env),
    };
    let s = salt(&env, 1);
    client.schedule_batch(&admin, &calls, &zero_bytes32(&env), &s, &0u64);
    let id = client.hash_operation_batch(&calls, &zero_bytes32(&env), &s);

    assert!(matches!(
        client.try_cancel(&stranger, &id),
        Err(Ok(TimelockError::NotAuthorized))
    ));
}

// -------------------------------------------------------------------------
// Bypasser execution
// -------------------------------------------------------------------------

#[test]
fn test_bypasser_executes_immediately() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| li.timestamp = 1000);

    let admin = Address::generate(&env);
    let bypasser = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(
        &100u64, // high min delay — bypasser ignores it
        &admin,
        &empty,
        &empty,
        &empty,
        &SorobanVec::from_array(&env, [bypasser.clone()]),
    );

    let target_addr = env.register(MockTarget, ());
    let target_client = MockTargetClient::new(&env, &target_addr);
    let target_id = addr_to_contract_id(&target_addr, &env);

    let calls = single_calls(&env, make_call(&target_id, encode_set_value(&env, 77)));
    client.bypasser_execute_batch(&bypasser, &calls);

    assert_eq!(target_client.get_value(), 77u32);
}

#[test]
fn test_bypasser_ignores_blocked_selector() {
    let env = Env::default();
    env.mock_all_auths();

    let admin = Address::generate(&env);
    let bypasser = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(
        &0u64,
        &admin,
        &empty,
        &empty,
        &empty,
        &SorobanVec::from_array(&env, [bypasser.clone()]),
    );

    let target_addr = env.register(MockTarget, ());
    let target_id = addr_to_contract_id(&target_addr, &env);

    // Block "set_value"
    client.block_function_selector(&admin, &Symbol::new(&env, "set_value"));

    // Bypasser can still call set_value despite it being blocked for schedule_batch
    let calls = single_calls(&env, make_call(&target_id, encode_set_value(&env, 55)));
    client.bypasser_execute_batch(&bypasser, &calls); // must not error
}

#[test]
fn test_bypasser_requires_role() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let stranger = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    let calls = Calls {
        inner: SorobanVec::new(&env),
    };
    assert!(matches!(
        client.try_bypasser_execute_batch(&stranger, &calls),
        Err(Ok(TimelockError::NotAuthorized))
    ));
}

// -------------------------------------------------------------------------
// Blocked selectors
// -------------------------------------------------------------------------

#[test]
fn test_block_and_unblock_selector() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    assert_eq!(client.get_blocked_selector_count(), 0u32);

    let sel = Symbol::new(&env, "dangerous_fn");
    client.block_function_selector(&admin, &sel);
    assert_eq!(client.get_blocked_selector_count(), 1u32);
    assert_eq!(client.get_blocked_selector_at(&0u32), sel.clone());

    client.unblock_function_selector(&admin, &sel);
    assert_eq!(client.get_blocked_selector_count(), 0u32);
}

#[test]
fn test_block_selector_is_idempotent() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    let sel = Symbol::new(&env, "fn_name");
    client.block_function_selector(&admin, &sel);
    client.block_function_selector(&admin, &sel); // second call is no-op
    assert_eq!(client.get_blocked_selector_count(), 1u32);
}

#[test]
fn test_block_requires_admin() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let stranger = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);

    assert!(matches!(
        client.try_block_function_selector(&stranger, &Symbol::new(&env, "fn")),
        Err(Ok(TimelockError::NotAuthorized))
    ));
}

// -------------------------------------------------------------------------
// Delay management
// -------------------------------------------------------------------------

#[test]
fn test_update_delay() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&100u64, &admin, &empty, &empty, &empty, &empty);

    assert_eq!(client.get_min_delay(), 100u64);
    client.update_delay(&admin, &200u64);
    assert_eq!(client.get_min_delay(), 200u64);
}

#[test]
fn test_update_delay_requires_admin() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let stranger = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&100u64, &admin, &empty, &empty, &empty, &empty);

    assert!(matches!(
        client.try_update_delay(&stranger, &50u64),
        Err(Ok(TimelockError::NotAuthorized))
    ));
}

#[test]
fn test_delay_increase_does_not_affect_pending_ops() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|li| li.timestamp = 1000);

    let admin = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&100u64, &admin, &empty, &empty, &empty, &empty);

    let calls = Calls {
        inner: SorobanVec::new(&env),
    };
    let s = salt(&env, 1);
    client.schedule_batch(&admin, &calls, &zero_bytes32(&env), &s, &100u64);
    // ready_at = 1100

    // Increase delay dramatically
    client.update_delay(&admin, &10_000u64);

    // Op still executes at its original ready_at
    env.ledger().with_mut(|li| li.timestamp = 1101);
    client.execute_batch(&admin, &calls, &zero_bytes32(&env), &s); // must succeed
}

// -------------------------------------------------------------------------
// TTL extension
// -------------------------------------------------------------------------

#[test]
fn test_extend_all_ttls() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let empty: SorobanVec<Address> = SorobanVec::new(&env);
    let client = register_timelock(&env);
    client.initialize(&0u64, &admin, &empty, &empty, &empty, &empty);
    client.extend_all_ttls(); // permissionless — must not error
}
