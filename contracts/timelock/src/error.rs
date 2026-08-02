use soroban_sdk::contracterror;

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq, PartialOrd, Ord)]
#[repr(u32)]
pub enum TimelockError {
    NotInitialized = 1,
    AlreadyInitialized = 2,
    NotAuthorized = 3,
    OperationAlreadyScheduled = 20,
    InsufficientDelay = 21,
    FunctionIsBlocked = 22,
    OperationNotReady = 30,
    MissingPredecessor = 31,
    CallReverted = 32,
    CallAborted = 33,
    OperationCannotBeCancelled = 40,
    InvalidInvokeData = 50, // reserved v1 code
    IndexOutOfBounds = 51,
    UnknownRole = 52,
    InvalidTarget = 53,
    InvalidArgsXdr = 54,
    EmptyBatch = 55,
    UnsupportedSelfCall = 56,
}

impl From<common_error::CCIPError> for TimelockError {
    fn from(e: common_error::CCIPError) -> Self {
        match e {
            common_error::CCIPError::AlreadyInitialized => TimelockError::AlreadyInitialized,
            common_error::CCIPError::NotInitialized => TimelockError::NotInitialized,
            _ => TimelockError::NotAuthorized,
        }
    }
}
