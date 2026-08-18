use soroban_sdk::{contractclient, contracterror, contracttype, BytesN, Env, String, I256};

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
    PrecisionOutOfRange = 51,
}

pub const MAX_PRECISION: u32 = data_feeds_cache::DECIMALS;

#[contractclient(name = "DataFeedsProxyReaderClient")]
pub trait DataFeedsProxyReader {
    fn latest_round(env: Env, data_id: BytesN<32>) -> Result<Round, ProxyReadError>;

    fn latest_answer(env: Env, data_id: BytesN<32>, precision: u32)
        -> Result<I256, ProxyReadError>;

    fn min_precision(env: Env, data_id: BytesN<32>) -> u32;

    fn get_round(env: Env, data_id: BytesN<32>, round_id: u64) -> Result<Round, ProxyReadError>;

    fn decimals(env: Env, data_id: BytesN<32>) -> Result<u32, ProxyReadError>;

    fn description(env: Env, data_id: BytesN<32>) -> Result<String, ProxyReadError>;
}
