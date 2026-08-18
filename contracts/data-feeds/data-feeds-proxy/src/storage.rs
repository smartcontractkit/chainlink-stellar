use soroban_sdk::{contracttype, Address, BytesN, Env};

#[contracttype]
#[derive(Clone, Debug)]
pub(crate) enum DataKey {
    Cache,
    MinPrecision(BytesN<32>),
}

pub(crate) fn get_cache(env: &Env) -> Address {
    env.storage().instance().get(&DataKey::Cache).unwrap()
}

pub(crate) fn set_cache(env: &Env, cache: &Address) {
    env.storage().instance().set(&DataKey::Cache, cache);
}

pub(crate) fn get_min_precision(env: &Env, data_id: &BytesN<32>) -> u32 {
    env.storage()
        .persistent()
        .get(&DataKey::MinPrecision(data_id.clone()))
        .unwrap_or(crate::interface::MAX_PRECISION)
}

pub(crate) fn set_min_precision(env: &Env, data_id: &BytesN<32>, min_precision: u32) {
    let key = DataKey::MinPrecision(data_id.clone());
    env.storage().persistent().set(&key, &min_precision);
    let max = env.storage().max_ttl();
    env.storage()
        .persistent()
        .extend_ttl(&key, max.saturating_sub(1), max);
}

pub(crate) fn extend_ttl(env: &Env) {
    let max = env.storage().max_ttl();
    env.storage()
        .instance()
        .extend_ttl(max.saturating_sub(1), max);
}
