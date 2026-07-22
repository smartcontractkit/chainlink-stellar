use soroban_sdk::{contractclient, contracterror, contracttype, Env, String, I256};

use data_feeds_cache::DataId;

#[contracttype]
#[derive(Clone, Debug)]
pub struct Round {
    pub round_id: u64,
    pub answer: I256,
    pub timestamp: u64,
}

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq, PartialOrd, Ord)]
#[repr(u32)]
pub enum ProxyReadError {
    NoDataPresent = 50,
}

#[contractclient(name = "DataFeedsProxyReaderClient")]
pub trait DataFeedsProxyReader {
    fn latest_round(env: Env, data_id: DataId) -> Result<Round, ProxyReadError>;

    fn get_round(env: Env, data_id: DataId, round_id: u64) -> Result<Round, ProxyReadError>;

    fn decimals(env: Env, data_id: DataId) -> Result<u32, ProxyReadError>;

    fn description(env: Env, data_id: DataId) -> Result<String, ProxyReadError>;
}
