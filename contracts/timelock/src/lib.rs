#![no_std]

mod encoding;
mod error;
mod events;
mod roles;
mod storage;
mod types;

pub use error::TimelockError;
pub use types::{
    BlockedFunction, Call, Calls, ADMIN_ROLE, BYPASSER_ROLE, CANCELLER_ROLE, DONE_TIMESTAMP,
    PROPOSER_ROLE,
};

use common_guard::initializable::Initializable;
use common_helpers::soroban_invoke::decode_invoke_args;
use encoding::{hash_args, hash_operation_batch as hash_batch, validate_call};
use events::{
    BypasserCallExecutedEvent, CallExecutedEvent, CallScheduledEvent, CancelledEvent,
    FunctionBlockedEvent, FunctionUnblockedEvent, MinDelayChangeEvent,
};
use roles::{
    get_role_members, grant_role_internal, has_role, require_admin, require_role,
    revoke_role_internal,
};
use soroban_sdk::{
    contract, contractimpl, symbol_short, Address, BytesN, Env, InvokeError, Symbol, Val, Vec,
};
use storage::{
    bump_ttls, delete_op_timestamp, extend_op_time_entry_ttl, get_blocked_functions, get_min_delay,
    get_op_timestamp, is_function_blocked, set_function_blocked, set_min_delay, set_op_timestamp,
};

const INITIALIZED_KEY: Symbol = symbol_short!("INIT");

#[contract]
pub struct TimelockContract;

#[contractimpl]
impl Initializable for TimelockContract {
    const INITIALIZED: Symbol = INITIALIZED_KEY;
}

#[contractimpl]
impl TimelockContract {
    /// Initialize the role topology atomically. The contract grants ADMIN only to itself.
    pub fn initialize(
        env: Env,
        min_delay: u64,
        proposers: Vec<Address>,
        cancellers: Vec<Address>,
        bypassers: Vec<Address>,
    ) -> Result<(), TimelockError> {
        <Self as Initializable>::require_not_initialized(&env).map_err(TimelockError::from)?;
        <Self as Initializable>::init(&env).map_err(TimelockError::from)?;
        let self_address = env.current_contract_address();
        grant_role_internal(&env, &ADMIN_ROLE, &self_address, &self_address)?;
        grant_all(&env, &PROPOSER_ROLE, &proposers, &self_address)?;
        grant_all(&env, &CANCELLER_ROLE, &cancellers, &self_address)?;
        grant_all(&env, &BYPASSER_ROLE, &bypassers, &self_address)?;
        set_min_delay(&env, min_delay);
        MinDelayChangeEvent {
            old_duration: 0,
            new_duration: min_delay,
        }
        .publish(&env);
        bump_ttls(&env);
        Ok(())
    }

    /// Admin functions are reachable only through a scheduled/bypassed self-call.
    pub fn grant_role(
        env: Env,
        caller: Address,
        role: Symbol,
        account: Address,
    ) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        require_admin(&env, &caller)?;
        grant_role_internal(&env, &role, &account, &caller)?;
        bump_ttls(&env);
        Ok(())
    }

    pub fn revoke_role(
        env: Env,
        caller: Address,
        role: Symbol,
        account: Address,
    ) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        require_admin(&env, &caller)?;
        revoke_role_internal(&env, &role, &account, &caller)?;
        bump_ttls(&env);
        Ok(())
    }

    pub fn renounce_role(env: Env, account: Address, role: Symbol) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        account.require_auth();
        revoke_role_internal(&env, &role, &account, &account)?;
        bump_ttls(&env);
        Ok(())
    }

    pub fn schedule_batch(
        env: Env,
        caller: Address,
        calls: Calls,
        predecessor: BytesN<32>,
        salt: BytesN<32>,
        delay: u64,
    ) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        require_role(&env, &caller, &PROPOSER_ROLE)?;
        let id = hash_batch(&env, &calls, &predecessor, &salt)?;
        if get_op_timestamp(&env, &id) > 0 {
            return Err(TimelockError::OperationAlreadyScheduled);
        }
        if delay < get_min_delay(&env) {
            return Err(TimelockError::InsufficientDelay);
        }

        let mut i = 0u32;
        while i < calls.inner.len() {
            let call = calls.inner.get(i).unwrap();
            if is_function_blocked(&env, &call.target, &call.function) {
                return Err(TimelockError::FunctionIsBlocked);
            }
            i += 1;
        }

        let ready_at = env
            .ledger()
            .timestamp()
            .saturating_add(delay)
            .max(DONE_TIMESTAMP + 1);
        set_op_timestamp(&env, &id, ready_at);
        i = 0;
        while i < calls.inner.len() {
            let call = calls.inner.get(i).unwrap();
            CallScheduledEvent {
                id: id.clone(),
                index: i,
                target: call.target.clone(),
                function: call.function.clone(),
                args_hash: hash_args(&env, &call),
                predecessor: predecessor.clone(),
                salt: salt.clone(),
                delay,
            }
            .publish(&env);
            i += 1;
        }
        bump_ttls(&env);
        Ok(())
    }

    /// Execute a ready operation. No executor role or caller parameter is required.
    pub fn execute_batch(
        env: Env,
        calls: Calls,
        predecessor: BytesN<32>,
        salt: BytesN<32>,
    ) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        let id = hash_batch(&env, &calls, &predecessor, &salt)?;
        let ts = get_op_timestamp(&env, &id);
        if ts <= DONE_TIMESTAMP || ts > env.ledger().timestamp() {
            return Err(TimelockError::OperationNotReady);
        }
        let zero = BytesN::from_array(&env, &[0u8; 32]);
        if predecessor != zero && get_op_timestamp(&env, &predecessor) != DONE_TIMESTAMP {
            return Err(TimelockError::MissingPredecessor);
        }

        // Persist before interaction. Any returned error or host trap rolls the transaction back.
        set_op_timestamp(&env, &id, DONE_TIMESTAMP);
        let mut i = 0u32;
        while i < calls.inner.len() {
            let call = calls.inner.get(i).unwrap();
            execute_call(&env, &call)?;
            CallExecutedEvent {
                id: id.clone(),
                index: i,
                target: call.target.clone(),
                function: call.function.clone(),
                args_hash: hash_args(&env, &call),
            }
            .publish(&env);
            i += 1;
        }
        bump_ttls(&env);
        Ok(())
    }

    pub fn cancel(env: Env, caller: Address, id: BytesN<32>) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        require_role(&env, &caller, &CANCELLER_ROLE)?;
        if get_op_timestamp(&env, &id) <= DONE_TIMESTAMP {
            return Err(TimelockError::OperationCannotBeCancelled);
        }
        delete_op_timestamp(&env, &id);
        CancelledEvent { id }.publish(&env);
        bump_ttls(&env);
        Ok(())
    }

    pub fn bypasser_execute_batch(
        env: Env,
        caller: Address,
        calls: Calls,
    ) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        require_role(&env, &caller, &BYPASSER_ROLE)?;
        if calls.inner.is_empty() {
            return Err(TimelockError::EmptyBatch);
        }
        let mut i = 0u32;
        while i < calls.inner.len() {
            let call = calls.inner.get(i).unwrap();
            validate_call(&env, &call)?;
            execute_call(&env, &call)?;
            BypasserCallExecutedEvent {
                index: i,
                target: call.target.clone(),
                function: call.function.clone(),
                args_hash: hash_args(&env, &call),
            }
            .publish(&env);
            i += 1;
        }
        bump_ttls(&env);
        Ok(())
    }

    pub fn block_function(
        env: Env,
        caller: Address,
        target: Address,
        function: Symbol,
    ) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        require_admin(&env, &caller)?;
        validate_target(&target)?;
        if !is_function_blocked(&env, &target, &function) {
            set_function_blocked(&env, &target, &function, true);
            FunctionBlockedEvent { target, function }.publish(&env);
        }
        bump_ttls(&env);
        Ok(())
    }

    pub fn unblock_function(
        env: Env,
        caller: Address,
        target: Address,
        function: Symbol,
    ) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        require_admin(&env, &caller)?;
        validate_target(&target)?;
        if is_function_blocked(&env, &target, &function) {
            set_function_blocked(&env, &target, &function, false);
            FunctionUnblockedEvent { target, function }.publish(&env);
        }
        bump_ttls(&env);
        Ok(())
    }

    pub fn update_delay(env: Env, caller: Address, new_delay: u64) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        require_admin(&env, &caller)?;
        let old_duration = get_min_delay(&env);
        set_min_delay(&env, new_delay);
        MinDelayChangeEvent {
            old_duration,
            new_duration: new_delay,
        }
        .publish(&env);
        bump_ttls(&env);
        Ok(())
    }

    pub fn hash_operation_batch(
        env: Env,
        calls: Calls,
        predecessor: BytesN<32>,
        salt: BytesN<32>,
    ) -> Result<BytesN<32>, TimelockError> {
        hash_batch(&env, &calls, &predecessor, &salt)
    }

    pub fn is_operation(env: Env, id: BytesN<32>) -> bool {
        get_op_timestamp(&env, &id) > 0
    }
    pub fn is_operation_pending(env: Env, id: BytesN<32>) -> bool {
        get_op_timestamp(&env, &id) > DONE_TIMESTAMP
    }
    pub fn is_operation_ready(env: Env, id: BytesN<32>) -> bool {
        let ts = get_op_timestamp(&env, &id);
        ts > DONE_TIMESTAMP && ts <= env.ledger().timestamp()
    }
    pub fn is_operation_done(env: Env, id: BytesN<32>) -> bool {
        get_op_timestamp(&env, &id) == DONE_TIMESTAMP
    }
    pub fn get_timestamp(env: Env, id: BytesN<32>) -> u64 {
        get_op_timestamp(&env, &id)
    }
    pub fn get_min_delay(env: Env) -> u64 {
        get_min_delay(&env)
    }

    pub fn has_role(env: Env, role: Symbol, account: Address) -> Result<bool, TimelockError> {
        has_role(&env, &role, &account)
    }
    pub fn get_role_member_count(env: Env, role: Symbol) -> Result<u32, TimelockError> {
        Ok(get_role_members(&env, &role)?.len())
    }
    pub fn get_role_member(env: Env, role: Symbol, index: u32) -> Result<Address, TimelockError> {
        get_role_members(&env, &role)?
            .get(index)
            .ok_or(TimelockError::IndexOutOfBounds)
    }

    pub fn is_function_blocked(
        env: Env,
        target: Address,
        function: Symbol,
    ) -> Result<bool, TimelockError> {
        validate_target(&target)?;
        Ok(is_function_blocked(&env, &target, &function))
    }
    pub fn get_blocked_function_count(env: Env) -> u32 {
        get_blocked_functions(&env).len()
    }
    pub fn get_blocked_function_at(env: Env, index: u32) -> Result<BlockedFunction, TimelockError> {
        get_blocked_functions(&env)
            .get(index)
            .ok_or(TimelockError::IndexOutOfBounds)
    }

    pub fn extend_all_ttls(env: Env) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        bump_ttls(&env);
        Ok(())
    }
    pub fn extend_op_time_ttl(env: Env, id: BytesN<32>) -> Result<(), TimelockError> {
        require_initialized(&env)?;
        extend_op_time_entry_ttl(&env, &id);
        Ok(())
    }
}

fn require_initialized(env: &Env) -> Result<(), TimelockError> {
    <TimelockContract as Initializable>::require_initialized(env).map_err(TimelockError::from)
}

fn grant_all(
    env: &Env,
    role: &Symbol,
    accounts: &Vec<Address>,
    sender: &Address,
) -> Result<(), TimelockError> {
    let mut i = 0u32;
    while i < accounts.len() {
        grant_role_internal(env, role, &accounts.get(i).unwrap(), sender)?;
        i += 1;
    }
    Ok(())
}

fn validate_target(target: &Address) -> Result<(), TimelockError> {
    use soroban_sdk::address_payload::AddressPayload;
    match target.to_payload() {
        Some(AddressPayload::ContractIdHash(_)) => Ok(()),
        _ => Err(TimelockError::InvalidTarget),
    }
}

fn execute_call(env: &Env, call: &Call) -> Result<(), TimelockError> {
    validate_call(env, call)?;
    let args =
        decode_invoke_args(env, &call.args_xdr).map_err(|_| TimelockError::InvalidArgsXdr)?;
    if call.target == env.current_contract_address() {
        // EVM's RBACTimelock lets the timelock-as-admin administer itself via a `timelock ->
        // timelock` self-call (msg.sender == the timelock, which holds ADMIN_ROLE). Soroban
        // forbids contract re-entry, so that self-call is impossible: scheduled
        // self-administration (plan §7.9) cannot go through `invoke_contract`. We reproduce the
        // same effect by dispatching admin functions internally here; the schedule delay /
        // bypasser authorization already gates these calls.
        return execute_self_admin(env, &call.function, &args);
    }
    // A callee-returned contract error arrives as `Err(Ok(InvokeError::Contract(_)))`;
    // the outer `Ok(Err(_))` arm only covers return-value conversion failures.
    match env.try_invoke_contract::<Val, InvokeError>(&call.target, &call.function, args) {
        Ok(Ok(_)) => Ok(()),
        Err(Ok(InvokeError::Contract(_))) => Err(TimelockError::CallReverted),
        Ok(Err(_)) | Err(_) => Err(TimelockError::CallAborted),
    }
}

fn self_arg<T: soroban_sdk::TryFromVal<Env, Val>>(
    env: &Env,
    args: &Vec<Val>,
    index: u32,
) -> Result<T, TimelockError> {
    let val = args.get(index).ok_or(TimelockError::InvalidArgsXdr)?;
    T::try_from_val(env, &val).map_err(|_| TimelockError::InvalidArgsXdr)
}

/// Internal dispatch for self-targeted calls. Argument layout mirrors the public ABI
/// (including the leading `caller`, which must be the timelock itself) so proposal
/// encoding and operation hashing are identical to an external call.
fn execute_self_admin(env: &Env, function: &Symbol, args: &Vec<Val>) -> Result<(), TimelockError> {
    let self_address = env.current_contract_address();
    let require_self_caller = |count: u32| -> Result<(), TimelockError> {
        if args.len() != count {
            return Err(TimelockError::InvalidArgsXdr);
        }
        let caller: Address = self_arg(env, args, 0)?;
        if caller != self_address {
            return Err(TimelockError::NotAuthorized);
        }
        Ok(())
    };

    if *function == Symbol::new(env, "grant_role") {
        require_self_caller(3)?;
        grant_role_internal(
            env,
            &self_arg(env, args, 1)?,
            &self_arg(env, args, 2)?,
            &self_address,
        )
    } else if *function == Symbol::new(env, "revoke_role") {
        require_self_caller(3)?;
        revoke_role_internal(
            env,
            &self_arg(env, args, 1)?,
            &self_arg(env, args, 2)?,
            &self_address,
        )
    } else if *function == Symbol::new(env, "update_delay") {
        require_self_caller(2)?;
        let new_delay: u64 = self_arg(env, args, 1)?;
        let old_duration = get_min_delay(env);
        set_min_delay(env, new_delay);
        MinDelayChangeEvent {
            old_duration,
            new_duration: new_delay,
        }
        .publish(env);
        Ok(())
    } else if *function == Symbol::new(env, "block_function") {
        require_self_caller(3)?;
        let target: Address = self_arg(env, args, 1)?;
        let function: Symbol = self_arg(env, args, 2)?;
        validate_target(&target)?;
        if !is_function_blocked(env, &target, &function) {
            set_function_blocked(env, &target, &function, true);
            FunctionBlockedEvent { target, function }.publish(env);
        }
        Ok(())
    } else if *function == Symbol::new(env, "unblock_function") {
        require_self_caller(3)?;
        let target: Address = self_arg(env, args, 1)?;
        let function: Symbol = self_arg(env, args, 2)?;
        validate_target(&target)?;
        if is_function_blocked(env, &target, &function) {
            set_function_blocked(env, &target, &function, false);
            FunctionUnblockedEvent { target, function }.publish(env);
        }
        Ok(())
    } else {
        Err(TimelockError::UnsupportedSelfCall)
    }
}

#[cfg(test)]
mod test;
