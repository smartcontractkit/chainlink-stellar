use soroban_sdk::{contract, contractimpl, Address, BytesN, Env, String, Symbol, Vec};

use data_feeds_common::{TokenRecoverable, Upgradeable, Versioned};
use data_feeds_proxy::{DataFeedsProxyReaderClient, DataId};
use stellar_access::ownable::{self, enforce_owner_auth, Ownable};

use crate::events::{FeedAdded, FeedRemoved, FeedUpdated, ProxySet};
use crate::interface::Sep40ProxyError;
use crate::interface::{Asset, PriceData, PriceFeedTrait};
use crate::storage;

pub const DECIMALS: u32 = 18;

const RESOLUTION: u32 = 1;

#[contract]
pub struct DataFeedsSep40Proxy;

#[contractimpl]
impl DataFeedsSep40Proxy {
    pub fn __constructor(env: Env, owner: Address, proxy: Address, base_symbol: Symbol) {
        ownable::set_owner(&env, &owner);
        storage::set_base_asset(&env, &Asset::Other(base_symbol));
        storage::set_proxy(&env, &proxy);
        storage::extend_ttl(&env);
    }

    pub fn add_feed(env: Env, asset: Asset, data_id: DataId) -> Result<(), Sep40ProxyError> {
        enforce_owner_auth(&env);
        storage::extend_ttl(&env);
        if asset == storage::get_base_asset(&env) {
            return Err(Sep40ProxyError::AssetIsBase);
        }
        assert_nonzero(&data_id)?;
        if storage::feed_for(&env, &asset).is_some() {
            return Err(Sep40ProxyError::AssetAlreadyMapped);
        }
        if storage::asset_for(&env, &data_id).is_some() {
            return Err(Sep40ProxyError::DataIdInUse);
        }
        assert_readable(&env, &data_id)?;

        storage::insert_feed(&env, &asset, &data_id);
        FeedAdded { asset, data_id }.publish(&env);
        Ok(())
    }

    pub fn update_feed(env: Env, asset: Asset, data_id: DataId) -> Result<(), Sep40ProxyError> {
        enforce_owner_auth(&env);
        storage::extend_ttl(&env);
        let old_data_id = storage::feed_for(&env, &asset).ok_or(Sep40ProxyError::AssetNotMapped)?;
        assert_nonzero(&data_id)?;
        if let Some(owner_of_feed) = storage::asset_for(&env, &data_id) {
            if owner_of_feed != asset {
                return Err(Sep40ProxyError::DataIdInUse);
            }
        }
        assert_readable(&env, &data_id)?;

        storage::replace_feed(&env, &asset, &old_data_id, &data_id);
        FeedUpdated {
            asset,
            old_data_id,
            new_data_id: data_id,
        }
        .publish(&env);
        Ok(())
    }

    pub fn remove_feed(env: Env, asset: Asset) -> Result<(), Sep40ProxyError> {
        enforce_owner_auth(&env);
        storage::extend_ttl(&env);
        let data_id = storage::feed_for(&env, &asset).ok_or(Sep40ProxyError::AssetNotMapped)?;

        storage::delete_feed(&env, &asset, &data_id);
        FeedRemoved { asset, data_id }.publish(&env);
        Ok(())
    }

    pub fn set_proxy(env: Env, proxy: Address) {
        enforce_owner_auth(&env);
        storage::extend_ttl(&env);
        let old_proxy = storage::get_proxy(&env);
        storage::set_proxy(&env, &proxy);
        ProxySet {
            old_proxy,
            new_proxy: proxy,
        }
        .publish(&env);
    }

    pub fn get_proxy(env: Env) -> Address {
        storage::extend_ttl(&env);
        storage::get_proxy(&env)
    }

    pub fn get_feed(env: Env, asset: Asset) -> Option<DataId> {
        storage::extend_ttl(&env);
        storage::feed_for(&env, &asset)
    }
}

#[contractimpl]
impl PriceFeedTrait for DataFeedsSep40Proxy {
    fn base(env: Env) -> Asset {
        storage::get_base_asset(&env)
    }

    fn assets(env: Env) -> Vec<Asset> {
        storage::assets(&env)
    }

    fn decimals(_env: Env) -> u32 {
        DECIMALS
    }

    fn resolution(_env: Env) -> u32 {
        RESOLUTION
    }

    fn price(_env: Env, _asset: Asset, _timestamp: u64) -> Option<PriceData> {
        None
    }

    fn prices(_env: Env, _asset: Asset, _records: u32) -> Option<Vec<PriceData>> {
        None
    }

    fn lastprice(env: Env, asset: Asset) -> Option<PriceData> {
        storage::extend_ttl(&env);
        let data_id = storage::feed_for(&env, &asset)?;
        storage::extend_feed_ttl(&env, &asset, &data_id);
        fetch_latest(&env, &data_id)
    }
}

fn assert_nonzero(data_id: &DataId) -> Result<(), Sep40ProxyError> {
    if data_id.to_array() == [0u8; 32] {
        return Err(Sep40ProxyError::InvalidDataId);
    }
    Ok(())
}

fn assert_readable(env: &Env, data_id: &DataId) -> Result<(), Sep40ProxyError> {
    fetch_latest(env, data_id)
        .map(|_| ())
        .ok_or(Sep40ProxyError::FeedUnreadable)
}

fn fetch_latest(env: &Env, data_id: &DataId) -> Option<PriceData> {
    let proxy = DataFeedsProxyReaderClient::new(env, &storage::get_proxy(env));
    let round = match proxy.try_latest_round(data_id, &DECIMALS) {
        Ok(Ok(round)) => round,
        _ => return None,
    };
    let price = round.answer.to_i128()?;
    Some(PriceData {
        price,
        timestamp: round.timestamp,
    })
}

#[contractimpl(contracttrait)]
impl Versioned for DataFeedsSep40Proxy {
    fn version(_env: Env) -> u32 {
        1
    }
    fn type_and_version(env: Env) -> String {
        String::from_str(&env, "DataFeedsSep40Proxy 1.0.0")
    }
}

#[contractimpl(contracttrait)]
impl Ownable for DataFeedsSep40Proxy {}

#[contractimpl(contracttrait)]
impl Upgradeable for DataFeedsSep40Proxy {}

#[contractimpl(contracttrait)]
impl TokenRecoverable for DataFeedsSep40Proxy {}
