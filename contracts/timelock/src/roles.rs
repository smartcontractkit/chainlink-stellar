use soroban_sdk::{Address, Env, Symbol, Vec};

use crate::error::TimelockError;
use crate::events::{RoleGrantedEvent, RoleRevokedEvent};
use crate::storage::{get_role_members as load_role_members, has_role_member, set_role_member};
use crate::types::{ADMIN_ROLE, BYPASSER_ROLE, CANCELLER_ROLE, PROPOSER_ROLE};

pub fn validate_role(role: &Symbol) -> Result<(), TimelockError> {
    if *role == ADMIN_ROLE
        || *role == PROPOSER_ROLE
        || *role == CANCELLER_ROLE
        || *role == BYPASSER_ROLE
    {
        Ok(())
    } else {
        Err(TimelockError::UnknownRole)
    }
}

pub fn has_role(env: &Env, role: &Symbol, account: &Address) -> Result<bool, TimelockError> {
    validate_role(role)?;
    Ok(has_role_member(env, role, account))
}

pub fn require_role(env: &Env, caller: &Address, role: &Symbol) -> Result<(), TimelockError> {
    if !has_role(env, role, caller)? {
        return Err(TimelockError::NotAuthorized);
    }
    caller.require_auth();
    Ok(())
}

pub fn require_admin(env: &Env, caller: &Address) -> Result<(), TimelockError> {
    require_role(env, caller, &ADMIN_ROLE)
}

pub fn grant_role_internal(
    env: &Env,
    role: &Symbol,
    account: &Address,
    sender: &Address,
) -> Result<(), TimelockError> {
    validate_role(role)?;
    if !has_role_member(env, role, account) {
        set_role_member(env, role, account, true);
        RoleGrantedEvent {
            role: role.clone(),
            account: account.clone(),
            sender: sender.clone(),
        }
        .publish(env);
    }
    Ok(())
}

pub fn revoke_role_internal(
    env: &Env,
    role: &Symbol,
    account: &Address,
    sender: &Address,
) -> Result<(), TimelockError> {
    validate_role(role)?;
    if has_role_member(env, role, account) {
        set_role_member(env, role, account, false);
        RoleRevokedEvent {
            role: role.clone(),
            account: account.clone(),
            sender: sender.clone(),
        }
        .publish(env);
    }
    Ok(())
}

pub fn get_role_members(env: &Env, role: &Symbol) -> Result<Vec<Address>, TimelockError> {
    validate_role(role)?;
    Ok(load_role_members(env, role))
}
