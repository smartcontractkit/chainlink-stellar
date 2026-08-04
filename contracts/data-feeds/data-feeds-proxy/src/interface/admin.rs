use soroban_sdk::{contractclient, Address, Env};

#[contractclient(name = "DataFeedsProxyAdminClient")]
pub trait DataFeedsProxyAdmin {
    fn set_cache(env: Env, cache: Address);
}
