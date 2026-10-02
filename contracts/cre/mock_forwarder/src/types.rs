use soroban_sdk::{contracttype, Address, Bytes, BytesN, Env};

use crate::{MockForwarderError, FORWARDER_METADATA_LENGTH, METADATA_LENGTH};

// Types mirror contracts/cre/forwarder/src/types.rs so the ABI (argument and
// return types) is identical to the production forwarder.

#[contracttype]
#[derive(Copy, Clone, Debug, Eq, PartialEq)]
#[repr(u32)]
pub enum TransmissionState {
    NotAttempted = 0,
    Succeeded = 1,
    InvalidReceiver = 2,
    Failed = 3,
}

#[contracttype]
#[derive(Clone)]
pub struct Transmission {
    pub state: TransmissionState,
    pub transmitter: Address,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TransmissionInfo {
    pub state: TransmissionState,
    pub transmitter: Option<Address>,
}

/// Accepted for interface compatibility with the production forwarder; the
/// mock never verifies signatures.
#[contracttype]
#[derive(Clone)]
pub struct Ed25519Signature {
    pub public_key: BytesN<32>,
    pub signature: BytesN<64>,
}

#[contracttype]
#[derive(Clone)]
pub enum DataKey {
    Transmission(BytesN<32>),
}

const EXECUTION_ID_OFFSET: u32 = 1;
const REPORT_ID_OFFSET: u32 = 107;

pub struct ParsedReport {
    pub workflow_execution_id: BytesN<32>,
    pub report_id: BytesN<2>,
    pub metadata: Bytes,
    pub payload: Bytes,
}

impl ParsedReport {
    /// Caller must have checked `raw_report.len() >= METADATA_LENGTH`.
    pub fn parse(env: &Env, raw_report: &Bytes) -> Result<ParsedReport, MockForwarderError> {
        if raw_report.get(0).unwrap() != 1 {
            return Err(MockForwarderError::InvalidReportVersion);
        }
        Ok(ParsedReport {
            workflow_execution_id: read_bytesn::<32>(env, raw_report, EXECUTION_ID_OFFSET),
            report_id: read_bytesn::<2>(env, raw_report, REPORT_ID_OFFSET),
            metadata: raw_report.slice(FORWARDER_METADATA_LENGTH..METADATA_LENGTH),
            payload: raw_report.slice(METADATA_LENGTH..raw_report.len()),
        })
    }
}

fn read_bytesn<const N: usize>(env: &Env, bytes: &Bytes, start: u32) -> BytesN<N> {
    let mut buf = [0u8; N];
    bytes
        .slice(start..start + N as u32)
        .copy_into_slice(&mut buf);
    BytesN::from_array(env, &buf)
}
