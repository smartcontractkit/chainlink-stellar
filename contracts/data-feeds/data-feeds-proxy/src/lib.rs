#![no_std]

pub mod interface;

#[cfg(feature = "contract")]
mod contract;
#[cfg(feature = "contract")]
mod events;
#[cfg(feature = "contract")]
mod storage;

pub use interface::{
    DataFeedsProxyAdmin, DataFeedsProxyAdminClient, DataFeedsProxyReader,
    DataFeedsProxyReaderClient, ProxyReadError, Round,
};

pub use data_feeds_cache::DataId;

#[cfg(feature = "contract")]
pub use contract::{DataFeedsProxy, DataFeedsProxyClient};

#[cfg(test)]
mod tests;
