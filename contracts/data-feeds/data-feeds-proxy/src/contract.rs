use soroban_sdk::{contract, contractimpl, Address, BytesN, Env, String, I256};

use data_feeds_cache::{DataFeedsCacheReaderClient, RoundData};
use data_feeds_common::{TokenRecoverable, Upgradeable, Versioned};
use stellar_access::ownable::{self, enforce_owner_auth, Ownable};

use crate::events::CacheSet;
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

#[contractimpl]
impl DataFeedsProxyReader for DataFeedsProxy {
    fn latest_round(
        env: Env,
        data_id: BytesN<16>,
        precision: u32,
    ) -> Result<Round, ProxyReadError> {
        storage::extend_ttl(&env);
        let cache = DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env));
        let round = cache
            .latest_round(&data_id)
            .ok_or(ProxyReadError::NoDataPresent)?;
        downscale(&env, &cache, &data_id, precision, round)
    }

    fn get_round(
        env: Env,
        data_id: BytesN<16>,
        round_id: u64,
        precision: u32,
    ) -> Result<Round, ProxyReadError> {
        storage::extend_ttl(&env);
        let cache = DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env));
        let round = cache
            .get_round(&data_id, &round_id)
            .ok_or(ProxyReadError::NoDataPresent)?;
        downscale(&env, &cache, &data_id, precision, round)
    }

    fn decimals(env: Env, data_id: BytesN<16>) -> Result<u32, ProxyReadError> {
        storage::extend_ttl(&env);
        DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env))
            .decimals(&data_id)
            .ok_or(ProxyReadError::NoDataPresent)
    }

    fn description(env: Env, data_id: BytesN<16>) -> Result<String, ProxyReadError> {
        storage::extend_ttl(&env);
        DataFeedsCacheReaderClient::new(&env, &storage::get_cache(&env))
            .description(&data_id)
            .ok_or(ProxyReadError::NoDataPresent)
    }
}

fn downscale(
    env: &Env,
    cache: &DataFeedsCacheReaderClient,
    data_id: &BytesN<16>,
    precision: u32,
    round: RoundData,
) -> Result<Round, ProxyReadError> {
    let stored = cache
        .decimals(data_id)
        .ok_or(ProxyReadError::NoDataPresent)?;
    if precision > stored {
        return Err(ProxyReadError::UnsupportedDecimals);
    }

    let answer = if precision == stored {
        round.answer
    } else {
        let zero = I256::from_i32(env, 0);
        let divisor = I256::from_i32(env, 10).pow(stored - precision);
        let scaled = round.answer.div(&divisor);
        if scaled == zero && round.answer != zero {
            return Err(ProxyReadError::AnswerTruncatedToZero);
        }
        scaled
    };

    Ok(Round {
        round_id: round.round_id,
        answer,
        timestamp: round.timestamp,
    })
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
