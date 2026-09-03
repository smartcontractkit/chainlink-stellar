use soroban_sdk::{
    contractclient, contracterror, contracttype, Address, BytesN, Env, String, I256,
};

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
    InvalidDecimals = 51,
    RoundsToZero = 52,
}

#[contractclient(name = "DataFeedsProxyReaderClient")]
pub trait DataFeedsProxyReader {
    fn latest_round(env: Env, data_id: BytesN<32>, decimals: u32) -> Result<Round, ProxyReadError>;

    fn get_round(
        env: Env,
        data_id: BytesN<32>,
        round_id: u64,
        decimals: u32,
    ) -> Result<Round, ProxyReadError>;

    fn decimals(env: Env, data_id: BytesN<32>) -> Result<u32, ProxyReadError>;

    fn description(env: Env, data_id: BytesN<32>) -> Result<String, ProxyReadError>;

    fn get_min_decimals(env: Env, data_id: BytesN<32>) -> u32;

    fn get_cache(env: Env) -> Address;
}
