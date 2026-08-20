//! Sharded persistent storage and explicit TTL management for timelock v2.

use crate::types::{
    BlockedFunction, TimelockDataKey, ADMIN_ROLE, BYPASSER_ROLE, CANCELLER_ROLE, PROPOSER_ROLE,
};
use soroban_sdk::{symbol_short, Address, BytesN, Env, Symbol, Vec};

pub const MIN_DELAY: Symbol = symbol_short!("MNDELAY");
pub const BLOCKED_FUNCTIONS: Symbol = symbol_short!("BLKFUNCS");
pub const LEDGER_THRESHOLD: u32 = 120_960;
pub const LEDGER_BUMP: u32 = 6_307_200;

fn extend_if_present(env: &Env, key: &impl soroban_sdk::IntoVal<Env, soroban_sdk::Val>) {
    let st = env.storage().persistent();
    if st.has(key) {
        st.extend_ttl(key, LEDGER_THRESHOLD, LEDGER_BUMP);
    }
}

pub fn get_op_timestamp(env: &Env, id: &BytesN<32>) -> u64 {
    env.storage()
        .persistent()
        .get(&TimelockDataKey::OpTime(id.clone()))
        .unwrap_or(0)
}

pub fn set_op_timestamp(env: &Env, id: &BytesN<32>, ts: u64) {
    let key = TimelockDataKey::OpTime(id.clone());
    env.storage().persistent().set(&key, &ts);
    extend_if_present(env, &key);
}

pub fn delete_op_timestamp(env: &Env, id: &BytesN<32>) {
    env.storage()
        .persistent()
        .remove(&TimelockDataKey::OpTime(id.clone()));
}

pub fn extend_op_time_entry_ttl(env: &Env, id: &BytesN<32>) {
    extend_if_present(env, &TimelockDataKey::OpTime(id.clone()));
}

pub fn has_role_member(env: &Env, role: &Symbol, account: &Address) -> bool {
    env.storage()
        .persistent()
        .has(&TimelockDataKey::RoleMember(role.clone(), account.clone()))
}

pub fn get_role_members(env: &Env, role: &Symbol) -> Vec<Address> {
    env.storage()
        .persistent()
        .get(&TimelockDataKey::RoleMembers(role.clone()))
        .unwrap_or(Vec::new(env))
}

pub fn set_role_member(env: &Env, role: &Symbol, account: &Address, present: bool) {
    let membership = TimelockDataKey::RoleMember(role.clone(), account.clone());
    let list_key = TimelockDataKey::RoleMembers(role.clone());
    let mut members = get_role_members(env, role);
    if present {
        if !env.storage().persistent().has(&membership) {
            env.storage().persistent().set(&membership, &true);
            members.push_back(account.clone());
            env.storage().persistent().set(&list_key, &members);
        }
    } else if env.storage().persistent().has(&membership) {
        env.storage().persistent().remove(&membership);
        let mut next = Vec::new(env);
        for member in members.iter() {
            if member != *account {
                next.push_back(member);
            }
        }
        env.storage().persistent().set(&list_key, &next);
    }
    extend_if_present(env, &membership);
    extend_if_present(env, &list_key);
}

pub fn is_function_blocked(env: &Env, target: &Address, function: &Symbol) -> bool {
    env.storage()
        .persistent()
        .has(&TimelockDataKey::BlockedFunction(
            target.clone(),
            function.clone(),
        ))
}

pub fn get_blocked_functions(env: &Env) -> Vec<BlockedFunction> {
    env.storage()
        .persistent()
        .get(&BLOCKED_FUNCTIONS)
        .unwrap_or(Vec::new(env))
}

pub fn set_function_blocked(env: &Env, target: &Address, function: &Symbol, blocked: bool) {
    let key = TimelockDataKey::BlockedFunction(target.clone(), function.clone());
    let mut entries = get_blocked_functions(env);
    if blocked {
        if !env.storage().persistent().has(&key) {
            env.storage().persistent().set(&key, &true);
            entries.push_back(BlockedFunction {
                target: target.clone(),
                function: function.clone(),
            });
            env.storage().persistent().set(&BLOCKED_FUNCTIONS, &entries);
        }
    } else if env.storage().persistent().has(&key) {
        env.storage().persistent().remove(&key);
        let mut next = Vec::new(env);
        for entry in entries.iter() {
            if entry.target != *target || entry.function != *function {
                next.push_back(entry);
            }
        }
        env.storage().persistent().set(&BLOCKED_FUNCTIONS, &next);
    }
    extend_if_present(env, &key);
    extend_if_present(env, &BLOCKED_FUNCTIONS);
}

pub fn get_min_delay(env: &Env) -> u64 {
    env.storage().instance().get(&MIN_DELAY).unwrap_or(0)
}
pub fn set_min_delay(env: &Env, delay: u64) {
    env.storage().instance().set(&MIN_DELAY, &delay);
}

/// Fixed-key and sharded-key maintenance. Extends instance storage, the blocked-functions list,
/// the per-role member list and each membership key (the four roles are a fixed known set), and
/// each per-target-function block key (enumerable via [`BLOCKED_FUNCTIONS`]). Called at the end of
/// every successful mutation and by `extend_all_ttls`; read-only queries never mutate rent state.
///
/// Keys that have already archived are skipped (their `has` check is false); protocol-23
/// restoration handles them on the next touch. The purpose is to keep live sharded keys from
/// archiving during normal operation, so authorization/blocking reads never need a restore.
pub fn bump_ttls(env: &Env) {
    env.storage()
        .instance()
        .extend_ttl(LEDGER_THRESHOLD, LEDGER_BUMP);
    extend_if_present(env, &BLOCKED_FUNCTIONS);

    // Sharded role membership: the four roles are a fixed known set. Keep the per-role member
    // list and each individual membership key alive so `has_role_member` reads never touch
    // archived entries during normal operation. Guard with `has` so a list that has already
    // archived is skipped rather than panicking here.
    for role in [PROPOSER_ROLE, CANCELLER_ROLE, BYPASSER_ROLE, ADMIN_ROLE] {
        let list_key = TimelockDataKey::RoleMembers(role.clone());
        if env.storage().persistent().has(&list_key) {
            for member in get_role_members(env, &role).iter() {
                extend_if_present(env, &TimelockDataKey::RoleMember(role.clone(), member));
            }
            extend_if_present(env, &list_key);
        }
    }

    // Per-target-function block keys are enumerable via the BLOCKED_FUNCTIONS list.
    if env.storage().persistent().has(&BLOCKED_FUNCTIONS) {
        for entry in get_blocked_functions(env).iter() {
            extend_if_present(
                env,
                &TimelockDataKey::BlockedFunction(entry.target.clone(), entry.function.clone()),
            );
        }
    }
}
