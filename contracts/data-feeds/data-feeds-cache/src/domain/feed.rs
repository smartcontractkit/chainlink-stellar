use soroban_sdk::xdr::ToXdr;
use soroban_sdk::{Address, Env, String, Vec, I256};

use crate::domain::data_id::{decimals_of, CanonicalId};
use crate::domain::search;
use crate::interface::types::{Bound, RoundData, WorkflowName, WorkflowOwner, WorkflowPermission};
use crate::interface::{CacheError, DataId, FeedConfig};
use crate::storage::{FeedState, PermissionHash, Store, StoredConfig, Window};

/// A feed read at a particular scale: which entry to load, what the stored
/// answers are scaled at, and what the caller asked for. `canonical` is `None`
/// for an unconfigured feed, whose rounds are served unscaled.
struct View {
    key: CanonicalId,
    canonical: Option<u32>,
    target: u32,
}

pub(crate) fn configure(env: &Env, id: &DataId, cfg: &FeedConfig, decimals: u32) -> bool {
    let key = CanonicalId::new(env, id);
    let existed = clear_permissions(env, &key);
    let stored = StoredConfig {
        config: cfg.clone(),
        decimals,
    };
    env.config_store().set(&key, &stored);
    env.config_store().extend_ttl(&key);
    for p in cfg.workflow_permissions.iter() {
        let perm = (key.clone(), perm_key(env, &p));
        env.permission_store().set(&perm, &());
        env.permission_store().extend_ttl(&perm);
    }
    existed
}

pub(crate) fn remove(env: &Env, id: &DataId) -> bool {
    let key = CanonicalId::new(env, id);
    if !clear_permissions(env, &key) {
        return false;
    }
    env.config_store().remove(&key);
    true
}

fn clear_permissions(env: &Env, key: &CanonicalId) -> bool {
    let Some(old) = env.config_store().get(key) else {
        return false;
    };
    for p in old.config.workflow_permissions.iter() {
        env.permission_store()
            .remove(&(key.clone(), perm_key(env, &p)));
    }
    true
}

pub(crate) enum Recorded {
    Appended {
        round_id: u64,
    },
    Stale {
        stored_ts: u64,
    },
    /// The report addressed the feed at a scale other than the one it is stored
    /// at. Accepting it would silently change the scale of the stored answers.
    NonCanonicalDecimals {
        expected: u32,
    },
}

pub(crate) fn record(env: &Env, id: &DataId, answer: &I256, timestamp: u64) -> Recorded {
    let key = CanonicalId::new(env, id);

    if let Some(expected) = env.config_store().get(&key).map(|c| c.decimals) {
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
    if let Some(stored) = env.config_store().get(&key) {
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

/// Decimals the caller addressed, once the feed is known to serve that scale.
/// `None` while the feed is unconfigured, matching [`description`].
pub(crate) fn decimals(env: &Env, id: &DataId) -> Result<Option<u32>, CacheError> {
    let v = view(env, id)?;
    Ok(v.canonical.map(|_| v.target))
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

pub(crate) fn latest(env: &Env, id: &DataId) -> Result<Option<RoundData>, CacheError> {
    let v = view(env, id)?;
    let Some(state) = feed_state_at(env, &v.key) else {
        return Ok(None);
    };
    scale(env, state.latest_round, &v).map(Some)
}

pub(crate) fn round(
    env: &Env,
    id: &DataId,
    round_id: u64,
) -> Result<Option<RoundData>, CacheError> {
    let v = view(env, id)?;
    let Some(state) = feed_state_at(env, &v.key) else {
        return Ok(None);
    };
    match read_round(env, &v.key, &state, round_id) {
        Some(r) => scale(env, r, &v).map(Some),
        None => Ok(None),
    }
}

pub(crate) fn range(
    env: &Env,
    id: &DataId,
    from: u64,
    to: u64,
) -> Result<Vec<RoundData>, CacheError> {
    let mut out = Vec::new(env);
    let v = view(env, id)?;
    let Some(state) = feed_state_at(env, &v.key) else {
        return Ok(out);
    };
    for rid in from.max(1)..=to.min(state.latest_round.round_id) {
        if let Some(r) = read_round(env, &v.key, &state, rid) {
            out.push_back(scale(env, r, &v)?);
        }
    }
    Ok(out)
}

pub(crate) fn find_round(
    env: &Env,
    id: &DataId,
    ts: u64,
    bound: Bound,
) -> Result<Option<RoundData>, CacheError> {
    let v = view(env, id)?;
    let Some(state) = feed_state_at(env, &v.key) else {
        return Ok(None);
    };
    let found = search::boundary(1, state.latest_round.round_id, ts, bound, |rid| {
        read_round(env, &v.key, &state, rid).map(|r| (r.timestamp, r))
    });
    match found {
        Some(r) => scale(env, r, &v).map(Some),
        None => Ok(None),
    }
}

/// Resolves `id` to the feed it addresses and the scale it asks for.
fn view(env: &Env, id: &DataId) -> Result<View, CacheError> {
    let key = CanonicalId::new(env, id);
    let target = decimals_of(id).ok_or(CacheError::InvalidDataId)?;
    let canonical = env.config_store().get(&key).map(|c| c.decimals);
    if canonical.is_some_and(|c| target > c) {
        return Err(CacheError::UnsupportedDecimals);
    }
    Ok(View {
        key,
        canonical,
        target,
    })
}

/// Downscales a stored answer to the scale the caller addressed. Scaling up is
/// rejected by [`view`], so this only ever divides. An unconfigured feed has no
/// scale to convert from, so its rounds are served as stored.
fn scale(env: &Env, round: RoundData, v: &View) -> Result<RoundData, CacheError> {
    let Some(canonical) = v.canonical.filter(|c| *c != v.target) else {
        return Ok(round);
    };
    let zero = I256::from_i32(env, 0);
    let divisor = I256::from_i32(env, 10).pow(canonical - v.target);
    let answer = round.answer.div(&divisor);
    if answer == zero && round.answer != zero {
        return Err(CacheError::AnswerTruncatedToZero);
    }
    Ok(RoundData { answer, ..round })
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
