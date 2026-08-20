#![cfg(test)]

use soroban_sdk::testutils::{Address as _, Ledger as _};
use soroban_sdk::xdr::ToXdr;
use soroban_sdk::{
    contract, contracterror, contractimpl, symbol_short, Address, Bytes, BytesN, Env, IntoVal,
    Symbol, Val, Vec,
};

use crate::{
    Call, Calls, TimelockContract, TimelockContractClient, TimelockError, ADMIN_ROLE,
    BYPASSER_ROLE, CANCELLER_ROLE, PROPOSER_ROLE,
};

#[contract]
struct MockTarget;

#[contractimpl]
impl MockTarget {
    pub fn set_value(env: Env, value: u32) {
        env.storage().instance().set(&symbol_short!("VAL"), &value);
    }
    pub fn get_value(env: Env) -> u32 {
        env.storage()
            .instance()
            .get(&symbol_short!("VAL"))
            .unwrap_or(0)
    }
}

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq)]
#[repr(u32)]
enum FailingTargetError {
    Rejected = 1,
}

#[contract]
struct FailingTarget;

#[contractimpl]
impl FailingTarget {
    pub fn set_reject(env: Env, reject: bool) {
        env.storage()
            .instance()
            .set(&symbol_short!("REJECT"), &reject);
    }
    pub fn maybe_fail(env: Env) -> Result<(), FailingTargetError> {
        if env
            .storage()
            .instance()
            .get(&symbol_short!("REJECT"))
            .unwrap_or(false)
        {
            Err(FailingTargetError::Rejected)
        } else {
            Ok(())
        }
    }
    pub fn abort() {
        panic!("intentional abort");
    }
}

fn register(env: &Env) -> TimelockContractClient<'_> {
    let address = env.register(TimelockContract, ());
    TimelockContractClient::new(env, &address)
}

fn args(env: &Env, values: &[Val]) -> Bytes {
    let mut out = Vec::<Val>::new(env);
    for value in values {
        out.push_back(*value);
    }
    out.to_xdr(env)
}

fn call(env: &Env, target: &Address, function: &str, values: &[Val]) -> Call {
    Call {
        target: target.clone(),
        function: Symbol::new(env, function),
        args_xdr: args(env, values),
    }
}

fn calls(env: &Env, call: Call) -> Calls {
    Calls {
        inner: Vec::from_array(env, [call]),
    }
}

fn zero(env: &Env) -> BytesN<32> {
    BytesN::from_array(env, &[0u8; 32])
}

fn salt(env: &Env, marker: u8) -> BytesN<32> {
    let mut bytes = [0u8; 32];
    bytes[31] = marker;
    BytesN::from_array(env, &bytes)
}

fn initialize<'a>(
    env: &'a Env,
    min_delay: u64,
    proposer: &Address,
    canceller: &Address,
    bypasser: &Address,
) -> TimelockContractClient<'a> {
    let client = register(env);
    client.initialize(
        &min_delay,
        &Vec::from_array(env, [proposer.clone()]),
        &Vec::from_array(env, [canceller.clone()]),
        &Vec::from_array(env, [bypasser.clone()]),
    );
    client
}

#[test]
fn initialization_is_self_administered_and_roles_are_exact() {
    let env = Env::default();
    env.mock_all_auths();
    let proposer = Address::generate(&env);
    let canceller = Address::generate(&env);
    let bypasser = Address::generate(&env);
    let client = initialize(&env, 100, &proposer, &canceller, &bypasser);

    assert!(client.has_role(&ADMIN_ROLE, &client.address));
    assert!(client.has_role(&PROPOSER_ROLE, &proposer));
    assert!(client.has_role(&CANCELLER_ROLE, &canceller));
    assert!(client.has_role(&BYPASSER_ROLE, &bypasser));
    assert!(!client.has_role(&PROPOSER_ROLE, &client.address));
    assert_eq!(client.get_role_member(&ADMIN_ROLE, &0), client.address);
    assert_eq!(client.get_min_delay(), 100);
}

#[test]
fn unknown_roles_are_rejected() {
    let env = Env::default();
    let address = Address::generate(&env);
    let client = initialize(&env, 0, &address, &address, &address);
    let unknown = Symbol::new(&env, "EXECUTOR");
    assert!(matches!(
        client.try_has_role(&unknown, &address),
        Err(Ok(TimelockError::UnknownRole))
    ));
    assert!(matches!(
        client.try_get_role_member_count(&unknown),
        Err(Ok(TimelockError::UnknownRole))
    ));
}

#[test]
fn double_initialization_is_rejected() {
    let env = Env::default();
    let address = Address::generate(&env);
    let client = initialize(&env, 0, &address, &address, &address);
    let empty = Vec::<Address>::new(&env);
    assert!(matches!(
        client.try_initialize(&0, &empty, &empty, &empty),
        Err(Ok(TimelockError::AlreadyInitialized))
    ));
}

#[test]
fn typed_batch_schedules_and_anyone_executes_when_ready() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|ledger| ledger.timestamp = 1_000);
    let proposer = Address::generate(&env);
    let other = Address::generate(&env);
    let client = initialize(&env, 100, &proposer, &other, &other);
    let target = env.register(MockTarget, ());
    let target_client = MockTargetClient::new(&env, &target);
    let batch = calls(
        &env,
        call(&env, &target, "set_value", &[77u32.into_val(&env)]),
    );
    let predecessor = zero(&env);
    let operation_salt = salt(&env, 1);

    client.schedule_batch(&proposer, &batch, &predecessor, &operation_salt, &100);
    let id = client.hash_operation_batch(&batch, &predecessor, &operation_salt);
    assert!(client.is_operation_pending(&id));
    assert!(matches!(
        client.try_execute_batch(&batch, &predecessor, &operation_salt),
        Err(Ok(TimelockError::OperationNotReady))
    ));
    env.ledger().with_mut(|ledger| ledger.timestamp = 1_100);
    client.execute_batch(&batch, &predecessor, &operation_salt);
    assert_eq!(target_client.get_value(), 77);
    assert!(client.is_operation_done(&id));
}

#[test]
fn schedule_requires_exact_proposer_role() {
    let env = Env::default();
    env.mock_all_auths();
    let proposer = Address::generate(&env);
    let canceller = Address::generate(&env);
    let client = initialize(&env, 0, &proposer, &canceller, &canceller);
    let target = env.register(MockTarget, ());
    let batch = calls(
        &env,
        call(&env, &target, "set_value", &[1u32.into_val(&env)]),
    );
    assert!(matches!(
        client.try_schedule_batch(&canceller, &batch, &zero(&env), &salt(&env, 2), &0),
        Err(Ok(TimelockError::NotAuthorized))
    ));
}

#[test]
fn cancellation_requires_exact_canceller_role() {
    let env = Env::default();
    env.mock_all_auths();
    let proposer = Address::generate(&env);
    let canceller = Address::generate(&env);
    let client = initialize(&env, 100, &proposer, &canceller, &canceller);
    let target = env.register(MockTarget, ());
    let batch = calls(
        &env,
        call(&env, &target, "set_value", &[1u32.into_val(&env)]),
    );
    let predecessor = zero(&env);
    let operation_salt = salt(&env, 3);
    client.schedule_batch(&proposer, &batch, &predecessor, &operation_salt, &100);
    let id = client.hash_operation_batch(&batch, &predecessor, &operation_salt);
    assert!(matches!(
        client.try_cancel(&proposer, &id),
        Err(Ok(TimelockError::NotAuthorized))
    ));
    client.cancel(&canceller, &id);
    assert!(!client.is_operation(&id));
}

#[test]
fn predecessor_must_be_done() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|ledger| ledger.timestamp = 1_000);
    let proposer = Address::generate(&env);
    let client = initialize(&env, 0, &proposer, &proposer, &proposer);
    let target = env.register(MockTarget, ());
    let first = calls(
        &env,
        call(&env, &target, "set_value", &[1u32.into_val(&env)]),
    );
    let second = calls(
        &env,
        call(&env, &target, "set_value", &[2u32.into_val(&env)]),
    );
    let first_salt = salt(&env, 4);
    client.schedule_batch(&proposer, &first, &zero(&env), &first_salt, &0);
    let first_id = client.hash_operation_batch(&first, &zero(&env), &first_salt);
    let second_salt = salt(&env, 5);
    client.schedule_batch(&proposer, &second, &first_id, &second_salt, &0);
    assert!(matches!(
        client.try_execute_batch(&second, &first_id, &second_salt),
        Err(Ok(TimelockError::MissingPredecessor))
    ));
    client.execute_batch(&first, &zero(&env), &first_salt);
    client.execute_batch(&second, &first_id, &second_salt);
}

#[test]
fn target_scoped_block_does_not_block_same_function_elsewhere() {
    let env = Env::default();
    env.mock_all_auths();
    let proposer = Address::generate(&env);
    let bypasser = Address::generate(&env);
    let client = initialize(&env, 0, &proposer, &proposer, &bypasser);
    let first = env.register(MockTarget, ());
    let second = env.register(MockTarget, ());

    // Exercise admin through an authorized self-call, never an external admin key.
    let admin_call = call(
        &env,
        &client.address,
        "block_function",
        &[
            client.address.clone().into_val(&env),
            first.clone().into_val(&env),
            Symbol::new(&env, "set_value").into_val(&env),
        ],
    );
    client.bypasser_execute_batch(&bypasser, &calls(&env, admin_call));
    assert!(client.is_function_blocked(&first, &Symbol::new(&env, "set_value")));
    assert!(!client.is_function_blocked(&second, &Symbol::new(&env, "set_value")));

    let blocked = calls(
        &env,
        call(&env, &first, "set_value", &[1u32.into_val(&env)]),
    );
    assert!(matches!(
        client.try_schedule_batch(&proposer, &blocked, &zero(&env), &salt(&env, 6), &0),
        Err(Ok(TimelockError::FunctionIsBlocked))
    ));
    let allowed = calls(
        &env,
        call(&env, &second, "set_value", &[1u32.into_val(&env)]),
    );
    client.schedule_batch(&proposer, &allowed, &zero(&env), &salt(&env, 7), &0);
    client.bypasser_execute_batch(&bypasser, &blocked);
}

#[test]
fn self_admin_can_change_roles() {
    let env = Env::default();
    env.mock_all_auths();
    let initial = Address::generate(&env);
    let replacement = Address::generate(&env);
    let client = initialize(&env, 0, &initial, &initial, &initial);
    let grant = call(
        &env,
        &client.address,
        "grant_role",
        &[
            client.address.clone().into_val(&env),
            PROPOSER_ROLE.into_val(&env),
            replacement.clone().into_val(&env),
        ],
    );
    client.bypasser_execute_batch(&initial, &calls(&env, grant));
    assert!(client.has_role(&PROPOSER_ROLE, &replacement));
}

#[test]
fn empty_batch_invalid_target_and_empty_args_are_rejected() {
    let env = Env::default();
    env.mock_all_auths();
    let role = Address::generate(&env);
    let client = initialize(&env, 0, &role, &role, &role);
    let empty = Calls {
        inner: Vec::new(&env),
    };
    assert!(matches!(
        client.try_hash_operation_batch(&empty, &zero(&env), &salt(&env, 8)),
        Err(Ok(TimelockError::EmptyBatch))
    ));

    let account_target = Address::from_str(
        &env,
        "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
    );
    let account_call = Call {
        target: account_target,
        function: Symbol::new(&env, "x"),
        args_xdr: args(&env, &[]),
    };
    assert!(matches!(
        client.try_bypasser_execute_batch(&role, &calls(&env, account_call)),
        Err(Ok(TimelockError::InvalidTarget))
    ));

    let target = env.register(MockTarget, ());
    let invalid_args = Call {
        target,
        function: Symbol::new(&env, "set_value"),
        args_xdr: Bytes::new(&env),
    };
    assert!(matches!(
        client.try_bypasser_execute_batch(&role, &calls(&env, invalid_args)),
        Err(Ok(TimelockError::InvalidArgsXdr))
    ));
}

#[test]
fn returned_call_error_rolls_back_done_and_can_retry() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|ledger| ledger.timestamp = 1_000);
    let role = Address::generate(&env);
    let client = initialize(&env, 0, &role, &role, &role);
    let target = env.register(FailingTarget, ());
    let target_client = FailingTargetClient::new(&env, &target);
    target_client.set_reject(&true);
    let batch = calls(&env, call(&env, &target, "maybe_fail", &[]));
    let operation_salt = salt(&env, 9);
    client.schedule_batch(&role, &batch, &zero(&env), &operation_salt, &0);
    let id = client.hash_operation_batch(&batch, &zero(&env), &operation_salt);
    assert!(matches!(
        client.try_execute_batch(&batch, &zero(&env), &operation_salt),
        Err(Ok(TimelockError::CallReverted))
    ));
    assert!(!client.is_operation_done(&id));
    target_client.set_reject(&false);
    client.execute_batch(&batch, &zero(&env), &operation_salt);
    assert!(client.is_operation_done(&id));
}

#[test]
fn aborted_call_is_distinct_and_rolls_back_done() {
    let env = Env::default();
    env.mock_all_auths();
    env.ledger().with_mut(|ledger| ledger.timestamp = 1_000);
    let role = Address::generate(&env);
    let client = initialize(&env, 0, &role, &role, &role);
    let target = env.register(FailingTarget, ());
    let batch = calls(&env, call(&env, &target, "abort", &[]));
    let operation_salt = salt(&env, 10);
    client.schedule_batch(&role, &batch, &zero(&env), &operation_salt, &0);
    let id = client.hash_operation_batch(&batch, &zero(&env), &operation_salt);
    assert!(matches!(
        client.try_execute_batch(&batch, &zero(&env), &operation_salt),
        Err(Ok(TimelockError::CallAborted))
    ));
    assert!(!client.is_operation_done(&id));
}
