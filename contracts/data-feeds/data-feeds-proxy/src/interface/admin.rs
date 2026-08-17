use soroban_sdk::{contractclient, Address, Env};

use super::reader::ProxyReadError;

#[contractclient(name = "DataFeedsProxyAdminClient")]
pub trait DataFeedsProxyAdmin {
    fn set_cache(env: Env, cache: Address);

    fn set_min_precision(env: Env, min_precision: u32) -> Result<(), ProxyReadError>;
}
