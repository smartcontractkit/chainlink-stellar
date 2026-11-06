use soroban_sdk::{contracttype, Address, BytesN, Env, IntoVal, Val, Vec};

use crate::interface::Asset;

type DataId = BytesN<32>;

#[contracttype]
#[derive(Clone, Debug)]
pub(crate) enum DataKey {
    Proxy,
    BaseAsset,
    Assets,
    AssetToFeed(Asset),
    FeedToAsset(DataId),
}

pub(crate) fn extend_ttl(env: &Env) {
    let max = env.storage().max_ttl();
    env.storage()
        .instance()
        .extend_ttl(max.saturating_sub(1), max);
}

fn set_persistent<K: IntoVal<Env, Val>, V: IntoVal<Env, Val>>(env: &Env, key: &K, value: &V) {
    env.storage().persistent().set(key, value);
    extend_persistent(env, key);
}

fn extend_persistent<K: IntoVal<Env, Val>>(env: &Env, key: &K) {
    let store = env.storage().persistent();
    if store.has(key) {
        let max = env.storage().max_ttl();
        store.extend_ttl(key, max.saturating_sub(1), max);
    }
}

pub(crate) fn get_proxy(env: &Env) -> Address {
    env.storage().instance().get(&DataKey::Proxy).unwrap()
}

pub(crate) fn set_proxy(env: &Env, proxy: &Address) {
    env.storage().instance().set(&DataKey::Proxy, proxy);
}

pub(crate) fn get_base_asset(env: &Env) -> Asset {
    env.storage().instance().get(&DataKey::BaseAsset).unwrap()
}

pub(crate) fn set_base_asset(env: &Env, asset: &Asset) {
    env.storage().instance().set(&DataKey::BaseAsset, asset);
}

pub(crate) fn assets(env: &Env) -> Vec<Asset> {
    env.storage()
        .persistent()
        .get(&DataKey::Assets)
        .unwrap_or_else(|| Vec::new(env))
}

pub(crate) fn feed_for(env: &Env, asset: &Asset) -> Option<DataId> {
    env.storage()
        .persistent()
        .get(&DataKey::AssetToFeed(asset.clone()))
}

pub(crate) fn asset_for(env: &Env, data_id: &DataId) -> Option<Asset> {
    env.storage()
        .persistent()
        .get(&DataKey::FeedToAsset(data_id.clone()))
}

pub(crate) fn insert_feed(env: &Env, asset: &Asset, data_id: &DataId) {
    let mut list = assets(env);
    list.push_back(asset.clone());
    set_persistent(env, &DataKey::Assets, &list);
    link(env, asset, data_id);
}

pub(crate) fn replace_feed(env: &Env, asset: &Asset, old: &DataId, new: &DataId) {
    env.storage()
        .persistent()
        .remove(&DataKey::FeedToAsset(old.clone()));
    link(env, asset, new);
}

pub(crate) fn delete_feed(env: &Env, asset: &Asset, data_id: &DataId) {
    let mut list = assets(env);
    if let Some(idx) = list.iter().position(|a| a == *asset) {
        list.remove(idx as u32);
    }
    set_persistent(env, &DataKey::Assets, &list);
    env.storage()
        .persistent()
        .remove(&DataKey::AssetToFeed(asset.clone()));
    env.storage()
        .persistent()
        .remove(&DataKey::FeedToAsset(data_id.clone()));
}

pub(crate) fn extend_feed_ttl(env: &Env, asset: &Asset, data_id: &DataId) {
    extend_persistent(env, &DataKey::Assets);
    extend_persistent(env, &DataKey::AssetToFeed(asset.clone()));
    extend_persistent(env, &DataKey::FeedToAsset(data_id.clone()));
}

fn link(env: &Env, asset: &Asset, data_id: &DataId) {
    set_persistent(env, &DataKey::AssetToFeed(asset.clone()), data_id);
    set_persistent(env, &DataKey::FeedToAsset(data_id.clone()), asset);
}
