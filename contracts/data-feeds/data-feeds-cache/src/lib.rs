#![no_std]

pub mod interface;

#[cfg(feature = "contract")]
mod contract;
#[cfg(feature = "contract")]
mod domain;
#[cfg(feature = "contract")]
mod events;
#[cfg(feature = "contract")]
mod storage;

pub use interface::{
    Bound, CacheError, DataFeedsCacheAdmin, DataFeedsCacheAdminClient, DataFeedsCacheReader,
    DataFeedsCacheReaderClient, DataFeedsCacheWriter, DataFeedsCacheWriterClient, DataId,
    FeedConfig, FeedConfigEntry, Metadata, ReportEntry, RoundData, WorkflowPermission,
};

#[cfg(feature = "contract")]
pub use contract::{DataFeedsCache, DataFeedsCacheClient};

#[cfg(any(test, feature = "testutils"))]
pub mod test_utils;

#[cfg(all(test, feature = "contract"))]
mod tests;
