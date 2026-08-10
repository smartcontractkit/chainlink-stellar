use soroban_sdk::{contractclient, BytesN, Env, String, Vec};

use super::types::{Bound, RoundData};
use crate::interface::CacheError;

#[contractclient(name = "DataFeedsCacheReaderClient")]
pub trait DataFeedsCacheReader {
    fn latest_round(env: Env, data_id: BytesN<16>) -> Result<Option<RoundData>, CacheError>;

    fn get_round(
        env: Env,
        data_id: BytesN<16>,
        round_id: u64,
    ) -> Result<Option<RoundData>, CacheError>;

    fn round_range(
        env: Env,
        data_id: BytesN<16>,
        from: u64,
        to: u64,
    ) -> Result<Vec<RoundData>, CacheError>;

    fn find_round(
        env: Env,
        data_id: BytesN<16>,
        timestamp: u64,
        bound: Bound,
    ) -> Result<Option<RoundData>, CacheError>;

    fn decimals(env: Env, data_id: BytesN<16>) -> Result<Option<u32>, CacheError>;

    fn description(env: Env, data_id: BytesN<16>) -> Result<Option<String>, CacheError>;

    fn is_configured(env: Env, data_id: BytesN<16>) -> Result<bool, CacheError>;

    fn is_frozen(env: Env, data_id: BytesN<16>) -> bool;
}
