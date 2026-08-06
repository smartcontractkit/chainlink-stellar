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
    UnsupportedDecimals = 51,
    AnswerTruncatedToZero = 52,
    InvalidDataId = 53,
}

#[contractclient(name = "DataFeedsProxyReaderClient")]
pub trait DataFeedsProxyReader {
    fn latest_round(env: Env, data_id: BytesN<16>) -> Result<Round, ProxyReadError>;

    fn get_round(env: Env, data_id: BytesN<16>, round_id: u64) -> Result<Round, ProxyReadError>;

    fn decimals(env: Env, data_id: BytesN<16>) -> Result<u32, ProxyReadError>;

    fn description(env: Env, data_id: BytesN<16>) -> Result<String, ProxyReadError>;
}
