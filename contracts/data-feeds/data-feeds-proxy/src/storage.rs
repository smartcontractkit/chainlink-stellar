use soroban_sdk::{contracttype, Address, Env};

use crate::DataId;

#[contracttype]
#[derive(Clone, Debug)]
pub(crate) enum DataKey {
    Cache,
    MinDecimals(DataId),
}

pub(crate) fn get_cache(env: &Env) -> Address {
    env.storage().instance().get(&DataKey::Cache).unwrap()
}

pub(crate) fn set_cache(env: &Env, cache: &Address) {
    env.storage().instance().set(&DataKey::Cache, cache);
}

pub(crate) fn get_min_decimals(env: &Env, data_id: &DataId) -> Option<u32> {
    env.storage()
        .persistent()
        .get(&DataKey::MinDecimals(data_id.clone()))
}

pub(crate) fn extend_min_decimals_ttl(env: &Env, data_id: &DataId) {
    let key = DataKey::MinDecimals(data_id.clone());
    let store = env.storage().persistent();
    if store.has(&key) {
        let max = env.storage().max_ttl();
        store.extend_ttl(&key, max.saturating_sub(1), max);
    }
}

pub(crate) fn set_min_decimals(env: &Env, data_id: &DataId, min: u32) {
    let key = DataKey::MinDecimals(data_id.clone());
    let store = env.storage().persistent();
    store.set(&key, &min);
    let max = env.storage().max_ttl();
    store.extend_ttl(&key, max.saturating_sub(1), max);
}

pub(crate) fn extend_ttl(env: &Env) {
    let max = env.storage().max_ttl();
    env.storage()
        .instance()
        .extend_ttl(max.saturating_sub(1), max);
}
