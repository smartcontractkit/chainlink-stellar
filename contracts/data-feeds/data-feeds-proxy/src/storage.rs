use soroban_sdk::{contracttype, Address, Env};

#[contracttype]
#[derive(Clone, Debug)]
pub(crate) enum DataKey {
    Cache,
    MinPrecision,
}

pub(crate) fn get_cache(env: &Env) -> Address {
    env.storage().instance().get(&DataKey::Cache).unwrap()
}

pub(crate) fn set_cache(env: &Env, cache: &Address) {
    env.storage().instance().set(&DataKey::Cache, cache);
}

pub(crate) fn get_min_precision(env: &Env) -> u32 {
    env.storage()
        .instance()
        .get(&DataKey::MinPrecision)
        .unwrap_or(crate::interface::MAX_PRECISION)
}

pub(crate) fn set_min_precision(env: &Env, min_precision: u32) {
    env.storage()
        .instance()
        .set(&DataKey::MinPrecision, &min_precision);
}

pub(crate) fn extend_ttl(env: &Env) {
    let max = env.storage().max_ttl();
    env.storage()
        .instance()
        .extend_ttl(max.saturating_sub(1), max);
}
