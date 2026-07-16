//! RBACTimelock contract errors.

use soroban_sdk::contracterror;

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq, PartialOrd, Ord)]
#[repr(u32)]
pub enum TimelockError {
    // Initialization
    NotInitialized = 1,
    AlreadyInitialized = 2,
    // Authorization
    NotAuthorized = 3,
    // Scheduling
    OperationAlreadyScheduled = 20,
    InsufficientDelay = 21,
    SelectorIsBlocked = 22,
    // Execution
    OperationNotReady = 30,
    MissingPredecessor = 31,
    /// Downstream invoke failed: callee returned a contract error (`InvokeError::Contract`).
    /// The callee's specific u32 code can't be carried through Soroban's fixed-enum error
    /// model, so only the mode is distinguished — see [`Self::CallAborted`].
    CallReverted = 32,
    /// Downstream invoke failed: callee trapped / panicked / host error (`InvokeError::Abort`).
    CallAborted = 33,
    // Cancellation
    OperationCannotBeCancelled = 40,
    // Misc
    InvalidInvokeData = 50,
    IndexOutOfBounds = 51,
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
