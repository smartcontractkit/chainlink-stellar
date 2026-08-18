use soroban_sdk::{contractclient, Address, BytesN, Env};

use super::reader::ProxyReadError;

#[contractclient(name = "DataFeedsProxyAdminClient")]
pub trait DataFeedsProxyAdmin {
    fn set_cache(env: Env, cache: Address);

    fn set_min_precision(
        env: Env,
        data_id: BytesN<32>,
        min_precision: u32,
    ) -> Result<(), ProxyReadError>;
}
