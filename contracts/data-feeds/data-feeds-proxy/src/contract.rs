use soroban_sdk::{
    contract, contractimpl, panic_with_error, vec, Address, BytesN, Env, String, I256,
};

use data_feeds_cache::{CacheError, DataFeedsCacheReaderClient};
use data_feeds_common::{TokenRecoverable, Upgradeable, Versioned};
use stellar_access::ownable::{self, enforce_owner_auth, Ownable};

use crate::events::CacheSet;
use crate::interface::{
    DataFeedsProxyAdmin, DataFeedsProxyReader, ProxyReadError, Round, MAX_PRECISION,
};
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

#[contractimpl]
impl DataFeedsProxyReader for DataFeedsProxy {
    fn latest_round(env: Env, data_id: BytesN<32>) -> Result<Round, ProxyReadError> {
        storage::extend_ttl(&env);
        assert_not_frozen(&env, &data_id);
        DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env))
            .latest_round(&vec![&env, data_id])
            .get_unchecked(0)
            .map(|r| Round {
                round_id: r.round_id,
                answer: r.answer,
                timestamp: r.timestamp,
            })
            .ok_or(ProxyReadError::NoDataPresent)
    }

    fn latest_answer(
        env: Env,
        data_id: BytesN<32>,
        precision: u32,
    ) -> Result<I256, ProxyReadError> {
        if precision < storage::get_min_precision(&env) || precision > MAX_PRECISION {
            return Err(ProxyReadError::PrecisionOutOfRange);
        }
        let answer = Self::latest_round(env.clone(), data_id)?.answer;
        let scale = I256::from_i128(&env, 10i128.pow(MAX_PRECISION - precision));
        Ok(answer.div(&scale))
    }

    fn min_precision(env: Env) -> u32 {
        storage::get_min_precision(&env)
    }

    fn get_round(env: Env, data_id: BytesN<32>, round_id: u64) -> Result<Round, ProxyReadError> {
        storage::extend_ttl(&env);
        assert_not_frozen(&env, &data_id);
        DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env))
            .get_round(&data_id, &round_id)
            .map(|r| Round {
                round_id: r.round_id,
                answer: r.answer,
                timestamp: r.timestamp,
            })
            .ok_or(ProxyReadError::NoDataPresent)
    }

    fn decimals(env: Env, data_id: BytesN<32>) -> Result<u32, ProxyReadError> {
        storage::extend_ttl(&env);
        assert_not_frozen(&env, &data_id);
        DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env))
            .decimals(&vec![&env, data_id])
            .get_unchecked(0)
            .ok_or(ProxyReadError::NoDataPresent)
    }

    fn description(env: Env, data_id: BytesN<32>) -> Result<String, ProxyReadError> {
        storage::extend_ttl(&env);
        assert_not_frozen(&env, &data_id);
        DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env))
            .description(&vec![&env, data_id])
            .get_unchecked(0)
            .ok_or(ProxyReadError::NoDataPresent)
    }
}

#[contractimpl]
impl DataFeedsProxyAdmin for DataFeedsProxy {
    fn set_min_precision(env: Env, min_precision: u32) -> Result<(), ProxyReadError> {
        enforce_owner_auth(&env);
        storage::extend_ttl(&env);
        if min_precision > MAX_PRECISION {
            return Err(ProxyReadError::PrecisionOutOfRange);
        }
        storage::set_min_precision(&env, min_precision);
        Ok(())
    }

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
