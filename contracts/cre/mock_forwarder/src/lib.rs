#![no_std]
//! Permissionless CRE forwarder for workflow simulation (`cre workflow simulate`).
//!
//! Exposes the same `report(...)` entrypoint, report layout, replay protection,
//! receiver dispatch and `forwarder_ReportProcessed` event as the production
//! forwarder (`contracts/cre/forwarder`), but performs **no** DON signature
//! verification and keeps **no** transmitter allowlist: any account may call
//! `report`, and the `signatures` argument is ignored. That lets a local
//! simulator, which cannot produce DON quorum signatures, deliver reports to a
//! receiver on a live network.
//!
//! Receivers that gate on the forwarder (`sender`) must explicitly trust this
//! contract's address. Never configure a production receiver to accept it.

mod events;
mod types;

#[cfg(test)]
mod test;

use soroban_sdk::{
    address_payload::AddressPayload, contract, contracterror, contractimpl, panic_with_error,
    symbol_short, Address, Bytes, BytesN, Env, Executable, IntoVal, InvokeError, String, Vec,
};

use events::ReportProcessedEvent;
use types::{DataKey, ParsedReport, Transmission};
pub use types::{Ed25519Signature, TransmissionInfo, TransmissionState};

const METADATA_LENGTH: u32 = 109;
const FORWARDER_METADATA_LENGTH: u32 = 45;
const REPORT_CONTEXT_LENGTH: u32 = 96;

// Storage TTL constants (ledger counts; 1 ledger ≈ 5 s on Mainnet).
const TTL_THRESHOLD: u32 = 17_280; // ~1 day
const TTL_EXTENSION: u32 = 51_840; // ~3 days

/// Codes match the production ForwarderError for the shared conditions, so
/// `Error(Contract, #n)` means the same thing from either contract.
#[contracterror]
#[derive(Copy, Clone, Eq, PartialEq, Debug)]
#[repr(u32)]
pub enum MockForwarderError {
    InvalidReport = 2,
    InvalidReportContext = 3,
    InvalidReportVersion = 4,
    AlreadyProcessed = 13,
    InvalidReceiver = 18,
}

#[contract]
pub struct MockForwarder;

#[contractimpl]
impl MockForwarder {
    /// Deliver `raw_report` to `receiver.on_report(self, metadata, payload)`.
    ///
    /// Same signature as the production forwarder's `report`; `signatures` is
    /// accepted for interface compatibility and ignored.
    pub fn report(
        env: Env,
        transmitter: Address,
        receiver: Address,
        raw_report: Bytes,
        report_context: Bytes,
        signatures: Vec<Ed25519Signature>,
    ) -> Result<(), MockForwarderError> {
        let _ = signatures;
        transmitter.require_auth();

        if raw_report.len() < METADATA_LENGTH {
            panic_with_error!(&env, MockForwarderError::InvalidReport);
        }
        if report_context.len() != REPORT_CONTEXT_LENGTH {
            panic_with_error!(&env, MockForwarderError::InvalidReportContext);
        }

        let parsed = ParsedReport::parse(&env, &raw_report)?;
        let key = DataKey::Transmission(get_transmission_id(
            &env,
            &receiver,
            &parsed.workflow_execution_id,
            &parsed.report_id,
        ));
        require_not_terminal(&env, &key);

        let state = if !matches!(receiver.executable(), Some(Executable::Wasm(_))) {
            TransmissionState::InvalidReceiver
        } else {
            let args = (
                env.current_contract_address(),
                parsed.metadata.clone(),
                parsed.payload.clone(),
            )
                .into_val(&env);
            match env.try_invoke_contract::<(), InvokeError>(
                &receiver,
                &symbol_short!("on_report"),
                args,
            ) {
                Ok(Ok(())) => TransmissionState::Succeeded,
                // Receiver returned Err or trapped: retryable.
                Ok(Err(_)) | Err(Ok(_)) => TransmissionState::Failed,
                // Receiver does not speak the protocol: terminal.
                Err(Err(_)) => TransmissionState::InvalidReceiver,
            }
        };
        write_transmission(&env, &key, &Transmission { state, transmitter });

        ReportProcessedEvent {
            receiver,
            workflow_execution_id: parsed.workflow_execution_id,
            report_id: parsed.report_id,
            success: state == TransmissionState::Succeeded,
        }
        .publish(&env);

        Ok(())
    }

    pub fn type_and_version(env: Env) -> String {
        String::from_str(&env, "MockForwarder 1.0.0")
    }

    pub fn get_transmission_info(
        env: Env,
        receiver: Address,
        workflow_execution_id: BytesN<32>,
        report_id: BytesN<2>,
    ) -> TransmissionInfo {
        let key = DataKey::Transmission(get_transmission_id(
            &env,
            &receiver,
            &workflow_execution_id,
            &report_id,
        ));
        match env.storage().persistent().get::<_, Transmission>(&key) {
            Some(t) => TransmissionInfo {
                state: t.state,
                transmitter: Some(t.transmitter),
            },
            None => TransmissionInfo {
                state: TransmissionState::NotAttempted,
                transmitter: None,
            },
        }
    }
}

/// `sha256(contract_id(receiver) ‖ workflow_execution_id ‖ report_id)`; same
/// derivation as the production forwarder. Non-contract receivers trap.
fn get_transmission_id(
    env: &Env,
    receiver: &Address,
    workflow_execution_id: &BytesN<32>,
    report_id: &BytesN<2>,
) -> BytesN<32> {
    let mut data = match receiver.to_payload() {
        Some(AddressPayload::ContractIdHash(hash)) => Bytes::from(hash),
        _ => panic_with_error!(env, MockForwarderError::InvalidReceiver),
    };
    data.extend_from_array(&workflow_execution_id.to_array());
    data.extend_from_array(&report_id.to_array());
    env.crypto().sha256(&data).into()
}

fn require_not_terminal(env: &Env, key: &DataKey) {
    if let Some(t) = env.storage().persistent().get::<_, Transmission>(key) {
        if t.state == TransmissionState::Succeeded || t.state == TransmissionState::InvalidReceiver
        {
            panic_with_error!(env, MockForwarderError::AlreadyProcessed);
        }
    }
}

fn write_transmission(env: &Env, key: &DataKey, tx: &Transmission) {
    env.storage().persistent().set(key, tx);
    env.storage()
        .persistent()
        .extend_ttl(key, TTL_THRESHOLD, TTL_EXTENSION);
}
