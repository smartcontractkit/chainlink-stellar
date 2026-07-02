use soroban_sdk::{contracttype, Address, Bytes, BytesN, Env, TryFromVal, Vec};

use crate::{error::ForwarderError, FORWARDER_METADATA_LENGTH, METADATA_LENGTH};

// ============================================================
// Types & Structs
// ============================================================

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
#[derive(Clone)]
pub struct TransmissionInfo {
    pub state: TransmissionState,
    pub transmitter: Option<Address>,
}

#[contracttype]
#[derive(Clone)]
pub struct Config {
    pub f: u32,
    /// ed25519 public keys (32 bytes) of the authorized signers.
    pub signers: Vec<BytesN<32>>,
}

/// A single ed25519 signature over the report digest, paired with the signer's
/// public key. ed25519 has no signer recovery, so the key must accompany the
/// signature; the report path verifies the pair and checks set membership.
#[contracttype]
#[derive(Clone)]
pub struct Ed25519Signature {
    pub public_key: BytesN<32>,
    pub signature: BytesN<64>,
}

#[contracttype]
#[derive(Clone)]
pub enum DataKey {
    Forwarder(Address),
    Config(u64),
    Transmission(BytesN<32>),
}

// ─────────────────────────────────────────────────────────────────────────────
// Report parsing
// ─────────────────────────────────────────────────────────────────────────────

const EXECUTION_ID_OFFSET: u32 = 1;
const DON_ID_OFFSET: u32 = 37;
const CONFIG_VERSION_OFFSET: u32 = 41;
const REPORT_ID_OFFSET: u32 = 107;

pub struct ParsedReport {
    pub workflow_execution_id: BytesN<32>,
    pub config_id: u64,
    pub report_id: BytesN<2>,
    pub metadata: Bytes,
    pub payload: Bytes,
}

impl ParsedReport {
    pub fn config_id(don_id: u32, config_version: u32) -> u64 {
        ((don_id as u64) << 32) | config_version as u64
    }
}

impl TryFromVal<Env, Bytes> for ParsedReport {
    type Error = ForwarderError;

    fn try_from_val(env: &Env, raw_report: &Bytes) -> Result<ParsedReport, Self::Error> {
        if raw_report.get(0).unwrap() != 1 {
            return Err(ForwarderError::InvalidReportVersion);
        }

        let don_id = read_u32_be(raw_report, DON_ID_OFFSET);
        let config_version = read_u32_be(raw_report, CONFIG_VERSION_OFFSET);

        let report = ParsedReport {
            workflow_execution_id: read_bytesn::<32>(env, raw_report, EXECUTION_ID_OFFSET),
            config_id: Self::config_id(don_id, config_version),
            report_id: read_bytesn::<2>(env, raw_report, REPORT_ID_OFFSET),
            metadata: raw_report.slice(FORWARDER_METADATA_LENGTH..METADATA_LENGTH),
            payload: raw_report.slice(METADATA_LENGTH..raw_report.len()),
        };

        Ok(report)
    }
}

fn read_bytesn<const N: usize>(env: &Env, bytes: &Bytes, start: u32) -> BytesN<N> {
    let mut buf = [0u8; N];
    bytes
        .slice(start..start + N as u32)
        .copy_into_slice(&mut buf);
    BytesN::from_array(env, &buf)
}

fn read_u32_be(bytes: &Bytes, start: u32) -> u32 {
    let mut buf = [0u8; 4];
    bytes.slice(start..start + 4).copy_into_slice(&mut buf);
    u32::from_be_bytes(buf)
}
