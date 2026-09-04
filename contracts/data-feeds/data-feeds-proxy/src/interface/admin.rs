use soroban_sdk::{contractclient, Address, BytesN, Env};

use crate::interface::ProxyReadError;

#[contractclient(name = "DataFeedsProxyAdminClient")]
pub trait DataFeedsProxyAdmin {
    fn set_cache(env: Env, cache: Address);

    fn set_min_decimals(env: Env, data_id: BytesN<32>, min: u32) -> Result<(), ProxyReadError>;
}
