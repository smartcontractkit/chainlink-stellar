use soroban_sdk::contracterror;

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq, PartialOrd, Ord)]
#[repr(u32)]
pub enum CacheError {
    MalformedReport = 100,
    UnauthorizedCaller = 101,
    FeedNotConfigured = 102,
    EmptyConfig = 103,
    InvalidAddress = 104,
    InvalidWorkflowName = 105,
    DuplicatePermission = 106,
    InvalidDataId = 107,
    DuplicateFeedConfig = 108,
    FeedFrozen = 109,
    NoFeedState = 110,
    /// The `DataId` asked for more decimals than the feed is stored at.
    UnsupportedDecimals = 111,
    /// Downscaling the stored answer to the requested decimals would leave zero.
    AnswerTruncatedToZero = 112,
}
