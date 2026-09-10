use common_error::CCIPError;
use soroban_sdk::contracterror;

#[contracterror]
#[derive(Copy, Clone, Eq, PartialEq, Debug)]
#[repr(u32)]
pub enum ForwarderError {
    AlreadyInitialized = 1,
    InvalidReport = 2,
    InvalidReportContext = 3,
    InvalidReportVersion = 4,
    FaultToleranceMustBePositive = 5,
    ExcessSigners = 6,
    InsufficientSigners = 7,
    InvalidConfig = 8,
    InvalidSignatureCount = 9,
    DuplicateSigner = 10,
    InvalidSignature = 11,
    InvalidSignerOrder = 12,
    AlreadyProcessed = 13,
    NotOwner = 14,
    NotProposedOwner = 15,
    Uninitialized = 16,
    UnauthorizedForwarder = 17,
    InvalidReceiver = 18,
    InvalidSigner = 19,
    CannotRemoveSelf = 20,
    UnauthorizedRelayer = 21,
}

impl From<CCIPError> for ForwarderError {
    fn from(e: CCIPError) -> Self {
        match e {
            CCIPError::AlreadyInitialized => ForwarderError::AlreadyInitialized,
            CCIPError::NotInitialized => ForwarderError::Uninitialized,
            CCIPError::NotOwner => ForwarderError::NotOwner,
            CCIPError::NoPendingOwner => ForwarderError::NotProposedOwner,
            _ => unreachable!("unexpected CCIPError variant from Ownable/Initializable"),
        }
    }
}
