use soroban_sdk::{
    contract, contractimpl, panic_with_error, vec, Address, BytesN, Env, String, I256,
};

use data_feeds_cache::{CacheError, DataFeedsCacheReaderClient, DECIMALS};
use data_feeds_common::{TokenRecoverable, Upgradeable, Versioned};
use stellar_access::ownable::{self, enforce_owner_auth, Ownable};

use crate::events::{CacheSet, MinDecimalsSet};
use crate::interface::{DataFeedsProxyAdmin, DataFeedsProxyReader, ProxyReadError, Round};
use crate::storage;

#[contract]
pub struct DataFeedsProxy;

#[contractimpl]
impl DataFeedsProxy {
    pub fn __constructor(env: Env, owner: Address, cache: Address) {
        ownable::set_owner(&env, &owner);
        storage::extend_ttl(&env);
        storage::set_cache(&env, &cache);
    }
}

fn assert_not_frozen(env: &Env, data_id: &BytesN<32>) {
    let cache = DataFeedsCacheReaderClient::new(env, &storage::get_cache(env));
    if cache
        .is_frozen(&vec![env, data_id.clone()])
        .get_unchecked(0)
    {
        panic_with_error!(env, CacheError::FeedFrozen);
    }
}

fn validate_decimals(env: &Env, data_id: &BytesN<32>, decimals: u32) -> Result<(), ProxyReadError> {
    let min = storage::get_min_decimals(env, data_id).unwrap_or(DECIMALS);
    if decimals < min || decimals > DECIMALS {
        return Err(ProxyReadError::InvalidDecimals);
    }
    Ok(())
}

fn scale_answer(env: &Env, answer: I256, decimals: u32) -> Result<I256, ProxyReadError> {
    let scaled = answer.div(&I256::from_i128(env, 10).pow(DECIMALS - decimals));
    if answer != I256::from_i128(env, 0) && scaled == I256::from_i128(env, 0) {
        return Err(ProxyReadError::RoundsToZero);
    }
    Ok(scaled)
}

#[contractimpl]
impl DataFeedsProxyReader for DataFeedsProxy {
    fn latest_round(env: Env, data_id: BytesN<32>, decimals: u32) -> Result<Round, ProxyReadError> {
        storage::extend_ttl(&env);
        storage::extend_min_decimals_ttl(&env, &data_id);
        validate_decimals(&env, &data_id, decimals)?;
        assert_not_frozen(&env, &data_id);
        let r = DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env))
            .latest_round(&vec![&env, data_id])
            .get_unchecked(0)
            .ok_or(ProxyReadError::NoDataPresent)?;
        Ok(Round {
            round_id: r.round_id,
            answer: scale_answer(&env, r.answer, decimals)?,
            timestamp: r.timestamp,
        })
    }

    fn get_round(
        env: Env,
        data_id: BytesN<32>,
        round_id: u64,
        decimals: u32,
    ) -> Result<Round, ProxyReadError> {
        storage::extend_ttl(&env);
        storage::extend_min_decimals_ttl(&env, &data_id);
        validate_decimals(&env, &data_id, decimals)?;
        assert_not_frozen(&env, &data_id);
        let r = DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env))
            .get_round(&data_id, &round_id)
            .ok_or(ProxyReadError::NoDataPresent)?;
        Ok(Round {
            round_id: r.round_id,
            answer: scale_answer(&env, r.answer, decimals)?,
            timestamp: r.timestamp,
        })
    }

    fn decimals(env: Env, data_id: BytesN<32>) -> Result<u32, ProxyReadError> {
        storage::extend_ttl(&env);
        storage::extend_min_decimals_ttl(&env, &data_id);
        assert_not_frozen(&env, &data_id);
        DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env))
            .decimals(&vec![&env, data_id])
            .get_unchecked(0)
            .ok_or(ProxyReadError::NoDataPresent)
    }

    fn description(env: Env, data_id: BytesN<32>) -> Result<String, ProxyReadError> {
        storage::extend_ttl(&env);
        storage::extend_min_decimals_ttl(&env, &data_id);
        assert_not_frozen(&env, &data_id);
        DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env))
            .description(&vec![&env, data_id])
            .get_unchecked(0)
            .ok_or(ProxyReadError::NoDataPresent)
    }

    fn get_min_decimals(env: Env, data_id: BytesN<32>) -> u32 {
        storage::extend_ttl(&env);
        storage::extend_min_decimals_ttl(&env, &data_id);
        storage::get_min_decimals(&env, &data_id).unwrap_or(DECIMALS)
    }

    fn get_cache(env: Env) -> Address {
        storage::extend_ttl(&env);
        storage::get_cache(&env)
    }
}

#[contractimpl]
impl DataFeedsProxyAdmin for DataFeedsProxy {
    fn set_cache(env: Env, cache: Address) {
        enforce_owner_auth(&env);
        storage::extend_ttl(&env);
        let old_cache = storage::get_cache(&env);
        storage::set_cache(&env, &cache);
        CacheSet {
            old_cache,
            new_cache: cache,
        }
        .publish(&env);
    }

    fn set_min_decimals(env: Env, data_id: BytesN<32>, min: u32) -> Result<(), ProxyReadError> {
        enforce_owner_auth(&env);
        if min > DECIMALS {
            return Err(ProxyReadError::InvalidDecimals);
        }
        storage::extend_ttl(&env);
        storage::set_min_decimals(&env, &data_id, min);
        MinDecimalsSet { data_id, min }.publish(&env);
        Ok(())
    }
}

#[contractimpl(contracttrait)]
impl Versioned for DataFeedsProxy {
    fn version(_env: Env) -> u32 {
        1
    }
    fn type_and_version(env: Env) -> String {
        String::from_str(&env, "DataFeedsProxy 1.0.0")
    }
}

#[contractimpl(contracttrait)]
impl Ownable for DataFeedsProxy {}

#[contractimpl(contracttrait)]
impl Upgradeable for DataFeedsProxy {}

#[contractimpl(contracttrait)]
impl TokenRecoverable for DataFeedsProxy {}
