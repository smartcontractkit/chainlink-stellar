//! RBACTimelock — Soroban/Stellar port of `RBACTimelock.sol`.
//!
//! Provides role-based access control around time-delayed batch execution.
//! Intended to wrap any Soroban contract that accepts this timelock as its
//! owner/admin, giving governance operations a mandatory delay window.
//!
//! # Roles
//! - `ADMIN`    — manages all roles (including itself); can call any gated function.
//! - `PROPOSER` — schedules batches via `schedule_batch`.
//! - `EXECUTOR` — executes ready batches via `execute_batch`.
//! - `CANCELLER`— cancels pending batches via `cancel`.
//! - `BYPASSER` — immediately executes without delay via `bypasser_execute_batch`.
//!
//! # Operation hashing
//! `hash_operation_batch(calls, predecessor, salt)`:
//! ```text
//! call_hash_i = keccak256(to_i || keccak256(data_i))
//! id = keccak256(n_calls || call_hash_0 || … || call_hash_n || predecessor || salt)
//! ```
//! All values are encoded as big-endian 32-byte words so off-chain tooling,
//! including the Go SDK, can reproduce the same operation id.
//!
//! # Security
//! - Operations are marked DONE **before** executing calls to prevent re-entrancy
//!   (stricter than Solidity which marks DONE after). On any `CallReverted` the
//!   entire transaction reverts, rolling back the DONE mark.
//! - If decoding invoke `data` or the target invocation **traps** (Soroban host fault),
//!   the whole transaction reverts and no storage changes are committed — including
//!   the DONE mark and any partial call side effects.
//! - Blocked selectors are checked at schedule time only (not at execute time),
//!   mirroring Solidity semantics. The bypasser ignores blocked selectors.
//! - TTL expiry of a per-operation timestamp entry causes it to be archived (not deleted).
//!   Archived entries can be restored with a `RestoreFootprint` transaction. Each entry is
//!   refreshed on read/write; call `extend_op_time_ttl(id)` for entries that won’t be touched
//!   soon (e.g. a DONE predecessor that a future operation will depend on). If `execute_batch`
//!   fails because a DONE predecessor is archived, restore it first, then retry.
//!
//! # Invoke payloads (`Call.data`)
//! Decoding uses [`common_helpers::soroban_invoke`]; recoverable failures surface as
//! [`TimelockError::InvalidInvokeData`]. Some malformed XDR may **trap** instead of
//! returning `Err` (see that module).

#![no_std]

mod error;
mod events;
mod roles;
mod storage;
mod types;

pub use error::TimelockError;
pub use types::{
    Call, Calls, ADMIN_ROLE, BYPASSER_ROLE, CANCELLER_ROLE, DONE_TIMESTAMP, EXECUTOR_ROLE,
    PROPOSER_ROLE,
};

use common_guard::initializable::Initializable;
use common_helpers::soroban_invoke::decode_invoke_payload;
use events::{
    BypasserCallExecutedEvent, CallExecutedEvent, CallScheduledEvent, CancelledEvent,
    FunctionSelectorBlockedEvent, FunctionSelectorUnblockedEvent, MinDelayChangeEvent,
};
use roles::{
    get_role_members, grant_role_internal, has_role, require_admin, require_role_or_admin,
    revoke_role_internal,
};
use soroban_sdk::{
    address_payload::AddressPayload, contract, contractimpl, symbol_short, xdr::FromXdr, Address,
    Bytes, BytesN, Env, InvokeError, Symbol, TryFromVal, Val, Vec,
};
use stellar_strkey::Contract as StrkeyContract;
use storage::{
    bump_ttls, delete_op_timestamp, extend_op_time_entry_ttl, get_blocked_selectors, get_min_delay,
    get_op_timestamp, set_blocked_selectors, set_min_delay, set_op_timestamp,
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
    // -------------------------------------------------------------------------
    // Initialization
    // -------------------------------------------------------------------------

    /// One-time initialization. Sets roles and minimum delay.
    ///
    /// Security: `admin` should always be a contract requiring multisig quorum,
    /// never a bare EOA, since admin controls all roles including itself.
    pub fn initialize(
        env: Env,
        min_delay: u64,
        admin: Address,
        proposers: Vec<Address>,
        executors: Vec<Address>,
        cancellers: Vec<Address>,
        bypassers: Vec<Address>,
    ) -> Result<(), TimelockError> {
        <Self as Initializable>::require_not_initialized(&env).map_err(TimelockError::from)?;
        <Self as Initializable>::init(&env).map_err(TimelockError::from)?;

        // Grant admin role
        grant_role_internal(&env, ADMIN_ROLE, &admin, &env.current_contract_address());

        // Grant remaining roles
        let mut i = 0u32;
        while i < proposers.len() {
            grant_role_internal(
                &env,
                PROPOSER_ROLE,
                &proposers.get(i).unwrap(),
                &env.current_contract_address(),
            );
            i += 1;
        }
        i = 0;
        while i < executors.len() {
            grant_role_internal(
                &env,
                EXECUTOR_ROLE,
                &executors.get(i).unwrap(),
                &env.current_contract_address(),
            );
            i += 1;
        }
        i = 0;
        while i < cancellers.len() {
            grant_role_internal(
                &env,
                CANCELLER_ROLE,
                &cancellers.get(i).unwrap(),
                &env.current_contract_address(),
            );
            i += 1;
        }
        i = 0;
        while i < bypassers.len() {
            grant_role_internal(
                &env,
                BYPASSER_ROLE,
                &bypassers.get(i).unwrap(),
                &env.current_contract_address(),
            );
            i += 1;
        }

        set_min_delay(&env, min_delay);
        MinDelayChangeEvent {
            old_duration: 0,
            new_duration: min_delay,
        }
        .publish(&env);

        bump_ttls(&env);
        Ok(())
    }

    // -------------------------------------------------------------------------
    // Role management (admin only)
    // -------------------------------------------------------------------------

    /// Grant `role` to `account`. Only callable by an admin.
    pub fn grant_role(
        env: Env,
        caller: Address,
        role: Symbol,
        account: Address,
    ) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        require_admin(&env, &caller)?;
        grant_role_internal(&env, role, &account, &caller);
        bump_ttls(&env);
        Ok(())
    }

    /// Revoke `role` from `account`. Only callable by an admin.
    pub fn revoke_role(
        env: Env,
        caller: Address,
        role: Symbol,
        account: Address,
    ) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        require_admin(&env, &caller)?;
        revoke_role_internal(&env, role, &account, &caller);
        bump_ttls(&env);
        Ok(())
    }

    /// Renounce `role` from self. An account may only renounce its own roles.
    pub fn renounce_role(env: Env, account: Address, role: Symbol) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        account.require_auth();
        revoke_role_internal(&env, role, &account, &account);
        bump_ttls(&env);
        Ok(())
    }

    // -------------------------------------------------------------------------
    // Scheduling (proposer or admin)
    // -------------------------------------------------------------------------

    /// Schedule a batch of calls. The batch becomes executable after `delay` seconds
    /// have elapsed (delay must be >= min_delay).
    ///
    /// Blocked function selectors are checked here, not at execute time.
    pub fn schedule_batch(
        env: Env,
        caller: Address,
        calls: Calls,
        predecessor: BytesN<32>,
        salt: BytesN<32>,
        delay: u64,
    ) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        require_role_or_admin(&env, &caller, PROPOSER_ROLE)?;

        let id = hash_operation_batch_internal(&env, &calls.inner, &predecessor, &salt);

        let existing_ts = get_op_timestamp(&env, &id);
        if existing_ts > 0 {
            return Err(TimelockError::OperationAlreadyScheduled);
        }

        let min_delay = get_min_delay(&env);
        if delay < min_delay {
            return Err(TimelockError::InsufficientDelay);
        }

        // Check blocked selectors before scheduling
        let mut i = 0u32;
        while i < calls.inner.len() {
            check_not_blocked(&env, &calls.inner.get(i).unwrap().data)?;
            i += 1;
        }

        // Sentinel values: 0 = unset, 1 = DONE_TIMESTAMP = executed.
        // Ensure ready_at > DONE_TIMESTAMP so it is always distinguishable.
        let ready_at = env
            .ledger()
            .timestamp()
            .saturating_add(delay)
            .max(DONE_TIMESTAMP + 1);
        set_op_timestamp(&env, &id, ready_at);

        // Emit one event per call (mirrors Solidity)
        i = 0;
        while i < calls.inner.len() {
            let call = calls.inner.get(i).unwrap();
            CallScheduledEvent {
                id: id.clone(),
                index: i,
                to: call.to.clone(),
                data: call.data.clone(),
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

    // -------------------------------------------------------------------------
    // Execution (executor or admin)
    // -------------------------------------------------------------------------

    /// Execute a batch that has passed its delay window.
    ///
    /// Security: the operation is marked DONE **before** executing calls to
    /// prevent re-entrancy. A `CallReverted` return causes the full transaction
    /// to revert, rolling back the DONE mark.
    ///
    /// Host **traps** during XDR decode of `Call.data` or during target invocation
    /// also abort the entire transaction with no durable state change (same net
    /// effect as EVM revert).
    pub fn execute_batch(
        env: Env,
        caller: Address,
        calls: Calls,
        predecessor: BytesN<32>,
        salt: BytesN<32>,
    ) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        require_role_or_admin(&env, &caller, EXECUTOR_ROLE)?;

        let id = hash_operation_batch_internal(&env, &calls.inner, &predecessor, &salt);

        // Validate readiness before marking done
        let now = env.ledger().timestamp();
        let ts = get_op_timestamp(&env, &id);
        if ts <= DONE_TIMESTAMP || ts > now {
            return Err(TimelockError::OperationNotReady);
        }

        // Validate predecessor has been executed (or is zero = no dependency)
        let zero = BytesN::from_array(&env, &[0u8; 32]);
        if predecessor != zero {
            let pred_ts = get_op_timestamp(&env, &predecessor);
            if pred_ts != DONE_TIMESTAMP {
                return Err(TimelockError::MissingPredecessor);
            }
        }

        // Mark DONE before executing — prevents re-entrant calls with the same id from
        // passing the readiness check. On CallReverted this is rolled back automatically.
        set_op_timestamp(&env, &id, DONE_TIMESTAMP);

        // Execute each call
        let mut i = 0u32;
        while i < calls.inner.len() {
            let call = calls.inner.get(i).unwrap();
            execute_call(&env, &call)?;
            CallExecutedEvent {
                id: id.clone(),
                index: i,
                to: call.to.clone(),
                data: call.data.clone(),
            }
            .publish(&env);
            i += 1;
        }

        bump_ttls(&env);
        Ok(())
    }

    // -------------------------------------------------------------------------
    // Cancellation (canceller or admin)
    // -------------------------------------------------------------------------

    /// Cancel a pending (not yet executed) operation.
    pub fn cancel(env: Env, caller: Address, id: BytesN<32>) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        require_role_or_admin(&env, &caller, CANCELLER_ROLE)?;

        let ts = get_op_timestamp(&env, &id);
        if ts <= DONE_TIMESTAMP {
            return Err(TimelockError::OperationCannotBeCancelled);
        }

        delete_op_timestamp(&env, &id);
        CancelledEvent { id }.publish(&env);
        bump_ttls(&env);
        Ok(())
    }

    // -------------------------------------------------------------------------
    // Bypasser execution (bypasser or admin)
    // -------------------------------------------------------------------------

    /// Immediately execute calls, bypassing delay, predecessor checks, and blocked
    /// selector restrictions. For emergency use only.
    ///
    /// Security: the bypasser role grants unconditional execution authority.
    /// It should only be granted to contracts requiring strong quorum.
    pub fn bypasser_execute_batch(
        env: Env,
        caller: Address,
        calls: Calls,
    ) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        require_role_or_admin(&env, &caller, BYPASSER_ROLE)?;

        let mut i = 0u32;
        while i < calls.inner.len() {
            let call = calls.inner.get(i).unwrap();
            execute_call(&env, &call)?;
            BypasserCallExecutedEvent {
                index: i,
                to: call.to.clone(),
                data: call.data.clone(),
            }
            .publish(&env);
            i += 1;
        }

        bump_ttls(&env);
        Ok(())
    }

    // -------------------------------------------------------------------------
    // Function selector management (admin only)
    // -------------------------------------------------------------------------

    /// Block a function selector (Soroban Symbol) from being scheduled.
    ///
    /// Note: this only affects future `schedule_batch` calls; it does not prevent
    /// execution of already-scheduled operations containing the blocked selector.
    /// Cancel any such operations manually after blocking.
    pub fn block_function_selector(
        env: Env,
        caller: Address,
        selector: Symbol,
    ) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        require_admin(&env, &caller)?;

        let mut selectors = get_blocked_selectors(&env);
        let mut already_blocked = false;
        for s in selectors.iter() {
            if s == selector {
                already_blocked = true;
                break;
            }
        }
        if !already_blocked {
            selectors.push_back(selector.clone());
            set_blocked_selectors(&env, &selectors);
            FunctionSelectorBlockedEvent { selector }.publish(&env);
        }
        bump_ttls(&env);
        Ok(())
    }

    /// Unblock a previously blocked function selector.
    pub fn unblock_function_selector(
        env: Env,
        caller: Address,
        selector: Symbol,
    ) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        require_admin(&env, &caller)?;

        let selectors = get_blocked_selectors(&env);
        let mut new_selectors: Vec<Symbol> = Vec::new(&env);
        let mut found = false;
        for s in selectors.iter() {
            if s == selector {
                found = true;
            } else {
                new_selectors.push_back(s);
            }
        }
        if found {
            set_blocked_selectors(&env, &new_selectors);
            FunctionSelectorUnblockedEvent { selector }.publish(&env);
        }
        bump_ttls(&env);
        Ok(())
    }

    // -------------------------------------------------------------------------
    // Delay management (admin only)
    // -------------------------------------------------------------------------

    /// Update the minimum delay for future operations.
    ///
    /// Note: already-scheduled operations are not affected — they may execute
    /// after their original (potentially shorter) delay.
    pub fn update_delay(env: Env, caller: Address, new_delay: u64) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        require_admin(&env, &caller)?;

        let old_delay = get_min_delay(&env);
        set_min_delay(&env, new_delay);
        MinDelayChangeEvent {
            old_duration: old_delay,
            new_duration: new_delay,
        }
        .publish(&env);
        bump_ttls(&env);
        Ok(())
    }

    // -------------------------------------------------------------------------
    // Queries
    // -------------------------------------------------------------------------

    /// Compute the operation ID for a batch. Pure — no state access.
    pub fn hash_operation_batch(
        env: Env,
        calls: Calls,
        predecessor: BytesN<32>,
        salt: BytesN<32>,
    ) -> BytesN<32> {
        hash_operation_batch_internal(&env, &calls.inner, &predecessor, &salt)
    }

    pub fn is_operation(env: Env, id: BytesN<32>) -> bool {
        get_op_timestamp(&env, &id) > 0
    }

    /// Returns true if the operation exists and has not yet been executed (includes waiting + ready).
    pub fn is_operation_pending(env: Env, id: BytesN<32>) -> bool {
        get_op_timestamp(&env, &id) > DONE_TIMESTAMP
    }

    /// Returns true if the operation exists, delay has passed, and it has not yet been executed.
    pub fn is_operation_ready(env: Env, id: BytesN<32>) -> bool {
        let ts = get_op_timestamp(&env, &id);
        let now = env.ledger().timestamp();
        ts > DONE_TIMESTAMP && ts <= now
    }

    pub fn is_operation_done(env: Env, id: BytesN<32>) -> bool {
        get_op_timestamp(&env, &id) == DONE_TIMESTAMP
    }

    /// Returns the timestamp at which an operation becomes ready (0 = not scheduled, 1 = done).
    pub fn get_timestamp(env: Env, id: BytesN<32>) -> u64 {
        get_op_timestamp(&env, &id)
    }

    pub fn get_min_delay(env: Env) -> u64 {
        get_min_delay(&env)
    }

    pub fn has_role(env: Env, role: Symbol, account: Address) -> bool {
        has_role(&env, role, &account)
    }

    pub fn get_role_member_count(env: Env, role: Symbol) -> u32 {
        get_role_members(&env, role).len()
    }

    pub fn get_role_member(env: Env, role: Symbol, index: u32) -> Result<Address, TimelockError> {
        let members = get_role_members(&env, role);
        members.get(index).ok_or(TimelockError::IndexOutOfBounds)
    }

    pub fn get_blocked_selector_count(env: Env) -> u32 {
        get_blocked_selectors(&env).len()
    }

    pub fn get_blocked_selector_at(env: Env, index: u32) -> Result<Symbol, TimelockError> {
        get_blocked_selectors(&env)
            .get(index)
            .ok_or(TimelockError::IndexOutOfBounds)
    }

    /// Permissionless TTL extension for instance storage and fixed persistent keys
    /// (`ROLES`, blocked selectors). Per-operation timestamps use separate ledger entries;
    /// those are extended whenever accessed, or via [`Self::extend_op_time_ttl`].
    pub fn extend_all_ttls(env: Env) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        bump_ttls(&env);
        Ok(())
    }

    /// Permissionless: extend archival TTL for one operation timestamp entry, if it exists.
    ///
    /// Use this for DONE predecessors that won't be touched again for a long time but must
    /// remain accessible for future dependent operations. If an entry has already been archived,
    /// restore it with a `RestoreFootprint` transaction before calling this function.
    pub fn extend_op_time_ttl(env: Env, id: BytesN<32>) -> Result<(), TimelockError> {
        <Self as Initializable>::require_initialized(&env).map_err(TimelockError::from)?;
        extend_op_time_entry_ttl(&env, &id);
        Ok(())
    }
}

// -------------------------------------------------------------------------
// Internal helpers
// -------------------------------------------------------------------------

/// Compute the operation ID for a batch of calls.
///
/// Encoding:
/// ```text
/// call_hash_i = keccak256(to_i || keccak256(data_i))
/// id = keccak256(n_calls_u256 || call_hash_0 || … || call_hash_n || predecessor || salt)
/// ```
/// All fields are big-endian 32-byte words.
// TODO: Verify byte-for-byte parity against the Go SDK when `mcms/sdk/stellar` is built.
fn hash_operation_batch_internal(
    env: &Env,
    calls: &Vec<Call>,
    predecessor: &BytesN<32>,
    salt: &BytesN<32>,
) -> BytesN<32> {
    let mut buf = Bytes::new(env);

    // Encode call count as uint256 (big-endian, 32 bytes)
    let n = calls.len() as u64;
    let mut n_word = [0u8; 32];
    let nb = n.to_be_bytes();
    n_word[24..32].copy_from_slice(&nb);
    buf.extend_from_slice(&n_word);

    // Encode each call as keccak256(to || keccak256(data))
    let mut i = 0u32;
    while i < calls.len() {
        let call = calls.get(i).unwrap();
        let call_hash = hash_single_call(env, &call);
        buf.extend_from_slice(&call_hash.to_array());
        i += 1;
    }

    // Append predecessor and salt
    buf.extend_from_slice(&predecessor.to_array());
    buf.extend_from_slice(&salt.to_array());

    env.crypto().keccak256(&buf).into()
}

fn hash_single_call(env: &Env, call: &Call) -> BytesN<32> {
    let mut buf = Bytes::new(env);
    buf.extend_from_slice(&call.to.to_array());
    let data_hash: BytesN<32> = env.crypto().keccak256(&call.data).into();
    buf.extend_from_slice(&data_hash.to_array());
    env.crypto().keccak256(&buf).into()
}

/// Execute a single call by decoding its XDR data and invoking the target contract.
fn execute_call(env: &Env, call: &Call) -> Result<(), TimelockError> {
    let target = contract_address_from_contract_id(env, &call.to);
    let (fn_sym, args) =
        decode_invoke_payload(env, &call.data).map_err(|_| TimelockError::InvalidInvokeData)?;
    match env.try_invoke_contract::<Val, InvokeError>(&target, &fn_sym, args) {
        Ok(Ok(_)) => Ok(()),
        // `Ok(Err(_))` is unreachable for T = Val (T::Error = Infallible); kept for exhaustiveness.
        Ok(Err(_)) => Err(TimelockError::CallReverted),
        // Surface the failure mode: callee-returned contract error vs. host trap/panic.
        Err(e) => {
            let ie = match e {
                Ok(ie) | Err(ie) => ie,
            };
            Err(match ie {
                InvokeError::Abort => TimelockError::CallAborted,
                InvokeError::Contract(_) => TimelockError::CallReverted,
            })
        }
    }
}

/// Extract the function name from XDR call data without building the full arg vec.
///
/// Returns `None` if data is empty, [`Vec::from_xdr`] returns `Err`, the vec is empty,
/// or the first element is not a [`Symbol`].
///
/// **Trap caveat:** like [`common_helpers::soroban_invoke::decode_invoke_payload`], some
/// malformed XDR may cause a host trap instead of `None`.
fn decode_function_name(env: &Env, data: &Bytes) -> Option<Symbol> {
    if data.len() == 0 {
        return None;
    }
    let payload = Vec::<Val>::from_xdr(env, data).ok()?;
    if payload.len() == 0 {
        return None;
    }
    Symbol::try_from_val(env, &payload.get(0).unwrap()).ok()
}

/// Check that `data`'s function name is not in the blocked selectors list.
/// If data is empty or not decodable, the call is allowed (mirrors Solidity:
/// `if data.length < 4 return`).
fn check_not_blocked(env: &Env, data: &Bytes) -> Result<(), TimelockError> {
    let fn_name = match decode_function_name(env, data) {
        Some(name) => name,
        None => return Ok(()),
    };
    let blocked = get_blocked_selectors(env);
    for s in blocked.iter() {
        if s == fn_name {
            return Err(TimelockError::SelectorIsBlocked);
        }
    }
    Ok(())
}

fn contract_address_from_contract_id(env: &Env, id: &BytesN<32>) -> Address {
    let sk = StrkeyContract(id.to_array());
    let encoded = sk.to_string();
    Address::from_str(env, encoded.as_str())
}

fn _contract_id_of_address(addr: &Address) -> BytesN<32> {
    match addr.to_payload() {
        Some(AddressPayload::ContractIdHash(id)) => id,
        _ => BytesN::from_array(addr.env(), &[0u8; 32]),
    }
}

#[cfg(test)]
mod test;
