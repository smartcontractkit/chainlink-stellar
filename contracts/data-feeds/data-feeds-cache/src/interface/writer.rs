use soroban_sdk::{contractclient, Address, Bytes, Env};

use crate::interface::CacheError;

#[contractclient(name = "DataFeedsCacheWriterClient")]
pub trait DataFeedsCacheWriter {
    fn on_report(
        env: Env,
        sender: Address,
        metadata: Bytes,
        report: Bytes,
    ) -> Result<(), CacheError>;
}
