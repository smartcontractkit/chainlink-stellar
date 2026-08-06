use soroban_sdk::xdr::ToXdr;
use soroban_sdk::{Address, Env, String, Vec, I256};

use crate::domain::search;
use crate::interface::data_id::decimals_of;
use crate::interface::types::{Bound, RoundData, WorkflowName, WorkflowOwner, WorkflowPermission};
use crate::interface::{CacheError, DataId, FeedConfig};
use crate::storage::{CanonicalId, FeedState, PermissionHash, Store, StoredConfig, Window};

pub(crate) fn configure(
    env: &Env,
    id: &DataId,
    cfg: &FeedConfig,
    decimals: u32,
) -> Result<(), CacheError> {
    let key = CanonicalId::new(env, id);
    if let Some(old) = env.config_store().get(&key) {
        if old.decimals != decimals {
            return Err(CacheError::DecimalsMismatch);
        }
        for p in old.config.workflow_permissions.iter() {
            env.permission_store()
                .remove(&(key.clone(), perm_key(env, &p)));
        }
    }

    env.config_store().set(
        &key,
        &StoredConfig {
            config: cfg.clone(),
            decimals,
        },
    );
    env.config_store().extend_ttl(&key);
    for p in cfg.workflow_permissions.iter() {
        let perm = (key.clone(), perm_key(env, &p));
        env.permission_store().set(&perm, &());
        env.permission_store().extend_ttl(&perm);
    }
    Ok(())
}

pub(crate) enum Recorded {
    Appended { round_id: u64 },
    Stale { stored_ts: u64 },
    NonCanonicalDecimals { expected: u32 },
}

pub(crate) fn record(env: &Env, id: &DataId, answer: &I256, timestamp: u64) -> Recorded {
    let key = CanonicalId::new(env, id);
    let config = env.config_store().get(&key);

    if let Some(expected) = config.as_ref().map(|c| c.decimals) {
        if decimals_of(id) != Some(expected) {
            return Recorded::NonCanonicalDecimals { expected };
        }
    }

    let state = feed_state_at(env, &key);
    let stored_ts = state.as_ref().map_or(0, |t| t.latest_round.timestamp);

    let outcome = if timestamp <= stored_ts {
        Recorded::Stale { stored_ts }
    } else {
        let seq = env.ledger().sequence();
        let round_id = state.as_ref().map_or(0, |t| t.latest_round.round_id) + 1;
        let ttl = env.round_store().max_ttl();
        let window = next_window(state.as_ref(), ttl, seq);
        let frozen = state.as_ref().is_some_and(|t| t.frozen);
        let round = RoundData {
            round_id,
            answer: answer.clone(),
            timestamp,
            ledger_seq: seq,
            primary: true,
        };
        env.round_store().set(&(key.clone(), round_id), &round);
        env.feed_state_store().set(
            &key,
            &FeedState {
                latest_round: round,
                window,
                frozen,
            },
        );
        Recorded::Appended { round_id }
    };

    env.feed_state_store().extend_ttl(&key);
    if let Some(stored) = config {
        env.config_store().extend_ttl(&key);
        for p in stored.config.workflow_permissions.iter() {
            env.permission_store()
                .extend_ttl(&(key.clone(), perm_key(env, &p)));
        }
    }

    outcome
}

fn next_window(state: Option<&FeedState>, ttl: u32, seq: u32) -> Window {
    let Some(state) = state else {
        return Window {
            shortest_ttl: ttl,
            grow_to_ttl: ttl,
            grow_at_ledger: 0,
        };
    };
    let mut grow_at_ledger = state.window.grow_at_ledger;

    if ttl != state.window.grow_to_ttl {
        let anchor = state.latest_round.ledger_seq;
        grow_at_ledger = anchor.saturating_add(ttl).saturating_add(1);
    }

    Window {
        shortest_ttl: state.window.width_at(seq).min(ttl),
        grow_to_ttl: ttl,
        grow_at_ledger,
    }
}

pub(crate) fn feed_state(env: &Env, id: &DataId) -> Option<FeedState> {
    feed_state_at(env, &CanonicalId::new(env, id))
}

fn feed_state_at(env: &Env, key: &CanonicalId) -> Option<FeedState> {
    env.feed_state_store().get(key)
}

pub(crate) fn permitted(env: &Env, id: &DataId, phash: &PermissionHash) -> bool {
    let key = CanonicalId::new(env, id);
    env.permission_store().exists(&(key, phash.clone()))
}

pub(crate) fn configured(env: &Env, id: &DataId) -> bool {
    env.config_store().exists(&CanonicalId::new(env, id))
}

pub(crate) fn is_frozen(env: &Env, id: &DataId) -> bool {
    feed_state(env, id).is_some_and(|t| t.frozen)
}

pub(crate) fn set_frozen(env: &Env, id: &DataId, frozen: bool) -> bool {
    let key = CanonicalId::new(env, id);
    let Some(mut state) = feed_state_at(env, &key) else {
        return false;
    };
    state.frozen = frozen;
    env.feed_state_store().set(&key, &state);
    env.feed_state_store().extend_ttl(&key);
    true
}

pub(crate) fn permissions(env: &Env, id: &DataId) -> Vec<WorkflowPermission> {
    env.config_store()
        .get(&CanonicalId::new(env, id))
        .map(|c| c.config.workflow_permissions)
        .unwrap_or_else(|| Vec::new(env))
}

pub(crate) fn decimals(env: &Env, id: &DataId) -> Option<u32> {
    env.config_store()
        .get(&CanonicalId::new(env, id))
        .map(|c| c.decimals)
}

pub(crate) fn description(env: &Env, id: &DataId) -> Option<String> {
    env.config_store()
        .get(&CanonicalId::new(env, id))
        .map(|c| c.config.description)
}

pub(crate) fn perm_hash(
    env: &Env,
    sender: &Address,
    owner: &WorkflowOwner,
    name: &WorkflowName,
) -> PermissionHash {
    let mut buf = sender.clone().to_xdr(env);
    buf.append(&owner.clone().to_xdr(env));
    buf.append(&name.clone().to_xdr(env));
    env.crypto().keccak256(&buf).to_bytes()
}

fn perm_key(env: &Env, p: &WorkflowPermission) -> PermissionHash {
    perm_hash(
        env,
        &p.allowed_sender,
        &p.allowed_workflow_owner,
        &p.allowed_workflow_name,
    )
}

pub(crate) fn latest(env: &Env, id: &DataId) -> Option<RoundData> {
    feed_state(env, id).map(|s| s.latest_round)
}

pub(crate) fn round(env: &Env, id: &DataId, round_id: u64) -> Option<RoundData> {
    let key = CanonicalId::new(env, id);
    let state = feed_state_at(env, &key)?;
    read_round(env, &key, &state, round_id)
}

pub(crate) fn range(env: &Env, id: &DataId, from: u64, to: u64) -> Vec<RoundData> {
    let mut out = Vec::new(env);
    let key = CanonicalId::new(env, id);
    let Some(state) = feed_state_at(env, &key) else {
        return out;
    };
    for rid in from.max(1)..=to.min(state.latest_round.round_id) {
        if let Some(r) = read_round(env, &key, &state, rid) {
            out.push_back(r);
        }
    }
    out
}

pub(crate) fn find_round(env: &Env, id: &DataId, ts: u64, bound: Bound) -> Option<RoundData> {
    let key = CanonicalId::new(env, id);
    let state = feed_state_at(env, &key)?;
    search::boundary(1, state.latest_round.round_id, ts, bound, |rid| {
        read_round(env, &key, &state, rid).map(|r| (r.timestamp, r))
    })
}

fn read_round(env: &Env, key: &CanonicalId, state: &FeedState, round_id: u64) -> Option<RoundData> {
    if round_id == state.latest_round.round_id {
        return Some(state.latest_round.clone());
    }
    let now = env.ledger().sequence();
    let window_start = now.saturating_sub(state.window.width_at(now));
    env.round_store()
        .get(&(key.clone(), round_id))
        .filter(|r| r.ledger_seq >= window_start)
}

#[cfg(test)]
mod tests;
