#![no_std]

mod error;
mod events;
mod types;

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use error::ForwarderError;
use soroban_sdk::{
    address_payload::AddressPayload, contract, contractimpl, crypto::Hash, panic_with_error,
    symbol_short, Address, Bytes, BytesN, Env, Executable, IntoVal, InvokeError, String, Symbol,
    TryFromVal, Vec,
};

use common_signature::{
    config::{SignatureConfigManager, SignatureVerificationConfig},
    Ed25519, SignatureConfig, SignatureScheme,
};
use events::{ConfigSetEvent, ForwarderAddedEvent, ForwarderRemovedEvent, ReportProcessedEvent};
use types::{DataKey, Ed25519Signature, Transmission, TransmissionInfo, TransmissionState};

use crate::types::ParsedReport;

// ============================================================
// Storage Keys
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const SIGNATURE_CONFIGS: Symbol = symbol_short!("SIGNCFG");

// ============================================================
// Protocol Constants
// ============================================================

const MAX_ORACLES: u32 = 31;
const METADATA_LENGTH: u32 = 109;
const FORWARDER_METADATA_LENGTH: u32 = 45;
const REPORT_CONTEXT_LENGTH: u32 = 96;

// Storage TTL constants (ledger counts; 1 ledger ≈ 5 s on Mainnet).
const BUMP_AFTER_3_DAYS: u32 = 51_840; // ~3 days
const BUMP_FOR_1_DAY: u32 = 17_280;

#[contract]
pub struct KeystoneForwarder;

#[contractimpl]
impl Initializable for KeystoneForwarder {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for KeystoneForwarder {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

// Internal helper for managing signature configurations storage
impl SignatureConfigManager for KeystoneForwarder {
    const SIGNATURE_CONFIGS: Symbol = SIGNATURE_CONFIGS;
    const IS_PERSISTENT: bool = false;
    type Error = ForwarderError;
    type DataKey = DataKey;

    fn validate_config(env: &Env, config: &SignatureConfig) -> Result<(), Self::Error> {
        // CREForwarder only supports the fault-tolerance (`Failure`) mode, where
        // `f` faulty signers are tolerated and `f + 1` valid signatures are
        // required. An explicit `Threshold` count is not a valid config here.
        let f = match config.verification {
            SignatureVerificationConfig::Failure(f) => f,
            SignatureVerificationConfig::Threshold(_) => return Err(ForwarderError::InvalidConfig),
        };
        let signers = &config.signers;

        if f == 0 {
            return Err(ForwarderError::FaultToleranceMustBePositive);
        }
        if signers.len() > MAX_ORACLES {
            return Err(ForwarderError::ExcessSigners);
        }
        // BFT bound: need ≥ 3f + 1 signers configured to tolerate f faulty.
        if signers.len() < f * 3 + 1 {
            return Err(ForwarderError::InsufficientSigners);
        }

        ensure_unique_pubkeys(&env, &signers);
        Ok(())
    }
}

#[contractimpl]
impl KeystoneForwarder {
    pub fn initialize(env: Env, owner: Address) -> Result<(), ForwarderError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;

        let self_addr = env.current_contract_address();
        let key = DataKey::Forwarder(self_addr.clone());
        env.storage().instance().set(&key, &true);

        ForwarderAddedEvent {
            forwarder: self_addr,
        }
        .publish(&env);

        // Mark as initialized
        env.storage().instance().set(&INITIALIZED, &true);
        env.storage()
            .instance()
            .extend_ttl(BUMP_AFTER_3_DAYS, BUMP_FOR_1_DAY);
        Ok(())
    }

    pub fn add_forwarder(env: Env, forwarder: Address) -> Result<(), ForwarderError> {
        <KeystoneForwarder as Initializable>::require_initialized(&env)?;
        <KeystoneForwarder as Ownable>::require_owner(&env)?;

        let key = DataKey::Forwarder(forwarder.clone());
        env.storage().instance().set(&key, &true);

        ForwarderAddedEvent { forwarder }.publish(&env);
        Ok(())
    }

    pub fn remove_forwarder(env: Env, forwarder: Address) -> Result<(), ForwarderError> {
        <KeystoneForwarder as Initializable>::require_initialized(&env)?;
        <KeystoneForwarder as Ownable>::require_owner(&env)?;

        if forwarder == env.current_contract_address() {
            panic_with_error!(&env, ForwarderError::CannotRemoveSelf);
        }

        env.storage()
            .instance()
            .remove(&DataKey::Forwarder(forwarder.clone()));

        ForwarderRemovedEvent { forwarder }.publish(&env);
        Ok(())
    }

    pub fn set_config(
        env: Env,
        don_id: u32,
        config_version: u32,
        f: u32,
        signers: Vec<BytesN<32>>,
    ) -> Result<(), ForwarderError> {
        <KeystoneForwarder as Initializable>::require_initialized(&env)?;
        <KeystoneForwarder as Ownable>::require_owner(&env)?;

        let cfg = SignatureConfig {
            signers,
            // `f` is fault tolerance; quorum requires `f + 1` valid signatures.
            verification: SignatureVerificationConfig::Failure(f),
        };
        let key = DataKey::Config(ParsedReport::config_id(don_id, config_version));
        <KeystoneForwarder as SignatureConfigManager>::set_config(&env, &key, &cfg)?;

        ConfigSetEvent {
            don_id,
            config_version,
            f: f,
            signers: cfg.signers,
        }
        .publish(&env);

        Ok(())
    }

    pub fn clear_config(env: Env, don_id: u32, config_version: u32) -> Result<(), ForwarderError> {
        <KeystoneForwarder as Initializable>::require_initialized(&env)?;
        <KeystoneForwarder as Ownable>::require_owner(&env)?;

        let key = DataKey::Config(ParsedReport::config_id(don_id, config_version));
        <KeystoneForwarder as SignatureConfigManager>::remove_config(&env, &key);

        ConfigSetEvent {
            don_id,
            config_version,
            f: 0,
            signers: Vec::new(&env),
        }
        .publish(&env);
        Ok(())
    }

    pub fn report(
        env: Env,
        transmitter: Address,
        receiver: Address,
        raw_report: Bytes,
        report_context: Bytes,
        signatures: Vec<Ed25519Signature>,
    ) -> Result<(), ForwarderError> {
        transmitter.require_auth();
        <KeystoneForwarder as Initializable>::require_initialized(&env)?;

        if raw_report.len() < METADATA_LENGTH {
            panic_with_error!(&env, ForwarderError::InvalidReport);
        }
        if report_context.len() != REPORT_CONTEXT_LENGTH {
            panic_with_error!(&env, ForwarderError::InvalidReportContext);
        }
        if signatures.is_empty() {
            panic_with_error!(&env, ForwarderError::InvalidSignatureCount);
        }

        let parsed = ParsedReport::try_from_val(&env, &raw_report)?;
        let transmission_id = get_transmission_id(
            &env,
            &receiver,
            &parsed.workflow_execution_id,
            &parsed.report_id,
        );

        match env
            .storage()
            .persistent()
            .get::<_, Transmission>(&DataKey::Transmission(transmission_id.clone()))
        {
            Some(t)
                if t.state == TransmissionState::Succeeded
                    || t.state == TransmissionState::InvalidReceiver =>
            {
                panic_with_error!(&env, ForwarderError::AlreadyProcessed);
            }
            _ => {}
        }

        let datakey = DataKey::Config(parsed.config_id);
        let cfg = <KeystoneForwarder as SignatureConfigManager>::get_config(&env, &datakey)
            .ok_or(ForwarderError::InvalidConfig)?;

        // Verify that the number of signatures matches the failed tolerance threshold
        if signatures.len() != cfg.verification.threshold() {
            panic_with_error!(&env, ForwarderError::InvalidSignatureCount);
        }

        verify_signatures(
            &env,
            &cfg.signers,
            &raw_report,
            &report_context,
            &signatures,
        );

        // Authorize the transmitter against the forwarder registry. The previous
        // design relied on a self-call to `route()` for this check, but Soroban
        // forbids contract re-entry, so we do the check inline and dispatch
        // directly via the helper.
        require_valid_forwarder(&env, &transmitter)?;
        let dispatch_result = dispatch_to_receiver(
            &env,
            transmission_id,
            transmitter,
            &receiver,
            &parsed.metadata,
            &parsed.payload,
        )?;

        ReportProcessedEvent {
            receiver,
            workflow_execution_id: parsed.workflow_execution_id,
            report_id: parsed.report_id,
            success: dispatch_result,
        }
        .publish(&env);

        Ok(())
    }

    pub fn route(
        env: Env,
        transmission_id: BytesN<32>,
        transmitter: Address,
        receiver: Address,
        metadata: Bytes,
        validated_report: Bytes,
    ) -> Result<bool, ForwarderError> {
        <KeystoneForwarder as Initializable>::require_initialized(&env)?;
        transmitter.require_auth();
        require_valid_forwarder(&env, &transmitter)?;

        dispatch_to_receiver(
            &env,
            transmission_id,
            transmitter,
            &receiver,
            &metadata,
            &validated_report,
        )?;
        Ok(true)
    }

    // ========================================
    // Query Functions
    // ========================================

    /// Off-chain identifier for this contract version. Matches the EVM
    /// `ITypeAndVersion` pattern: off-chain agents can distinguish forwarder
    /// instances in a multi-version deployment without parsing the wasm.
    pub fn type_and_version(env: Env) -> String {
        String::from_str(&env, "KeystoneForwarder 1.0.0")
    }

    pub fn get_transmission_info(
        env: Env,
        receiver: Address,
        workflow_execution_id: BytesN<32>,
        report_id: BytesN<2>,
    ) -> Result<TransmissionInfo, ForwarderError> {
        <KeystoneForwarder as Initializable>::require_initialized(&env)?;

        let transmission_id =
            get_transmission_id(&env, &receiver, &workflow_execution_id, &report_id);

        let key = DataKey::Transmission(transmission_id);

        match env.storage().persistent().get::<_, Transmission>(&key) {
            Some(t) => Ok(TransmissionInfo {
                state: t.state,
                transmitter: Some(t.transmitter),
            }),
            None => Ok(TransmissionInfo {
                state: TransmissionState::NotAttempted,
                transmitter: None,
            }),
        }
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Forwarder registry helpers
// ─────────────────────────────────────────────────────────────────────────────

fn require_valid_forwarder(env: &Env, forwarder: &Address) -> Result<(), ForwarderError> {
    let key = DataKey::Forwarder(forwarder.clone());
    if !env.storage().instance().has(&key) {
        return Err(ForwarderError::UnauthorizedForwarder);
    }
    Ok(())
}

// ─────────────────────────────────────────────────────────────────────────────
// Config helpers
// ─────────────────────────────────────────────────────────────────────────────

fn ensure_unique_pubkeys(env: &Env, signers: &Vec<BytesN<32>>) {
    let zero = BytesN::<32>::from_array(env, &[0u8; 32]);

    let mut i = 0;
    while i < signers.len() {
        let a = signers.get(i).unwrap();
        if a == zero {
            panic_with_error!(env, ForwarderError::InvalidSigner);
        }

        let mut j = i + 1;
        while j < signers.len() {
            if a == signers.get(j).unwrap() {
                panic_with_error!(env, ForwarderError::DuplicateSigner);
            }
            j += 1;
        }

        i += 1;
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Signature verification
// ─────────────────────────────────────────────────────────────────────────────

fn report_digest(env: &Env, raw_report: &Bytes, report_context: &Bytes) -> Hash<32> {
    let raw_hash = env.crypto().keccak256(raw_report);
    let mut data = Bytes::from(raw_hash);
    data.append(report_context);
    env.crypto().keccak256(&data)
}

fn signer_in_set(signers: &Vec<BytesN<32>>, signer: &BytesN<32>) -> bool {
    let mut i = 0;
    while i < signers.len() {
        if &signers.get(i).unwrap() == signer {
            return true;
        }
        i += 1;
    }
    false
}

/// Verify each ed25519 signature against its accompanying public key and confirm
/// the signer belongs to the configured set. Signatures must be presented in
/// strictly-ascending public-key order, which both deduplicates signers and makes
/// the accepted ordering deterministic (ed25519 has no signer recovery, so the
/// bitmask dedup used by the secp256k1 design is not applicable here).
fn verify_signatures(
    env: &Env,
    signers: &Vec<BytesN<32>>,
    raw_report: &Bytes,
    report_context: &Bytes,
    signatures: &Vec<Ed25519Signature>,
) {
    let digest: BytesN<32> = report_digest(env, raw_report, report_context).into();

    let mut prev: Option<BytesN<32>> = None;

    let mut i = 0;
    while i < signatures.len() {
        let entry = signatures.get(i).unwrap();

        // Verify the signature against its claimed public key (traps if invalid),
        // yielding the signer identity.
        let signer =
            Ed25519::identify_signer(env, &digest, &entry.signature, entry.public_key.clone());

        // Strictly-ascending signer order → dedup + deterministic acceptance.
        if let Some(ref p) = prev {
            if *p >= signer {
                panic_with_error!(env, ForwarderError::InvalidSignerOrder);
            }
        }

        if !signer_in_set(signers, &signer) {
            panic_with_error!(env, ForwarderError::InvalidSigner);
        }

        prev = Some(signer);
        i += 1;
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Receiver dispatch
// ─────────────────────────────────────────────────────────────────────────────

fn receiver_contract_id_bytes(env: &Env, receiver: &Address) -> Bytes {
    match receiver.to_payload() {
        Some(AddressPayload::ContractIdHash(hash)) => Bytes::from(hash),
        _ => panic_with_error!(env, ForwarderError::InvalidReceiver),
    }
}

fn get_transmission_id(
    env: &Env,
    receiver: &Address,
    workflow_execution_id: &BytesN<32>,
    report_id: &BytesN<2>,
) -> BytesN<32> {
    let mut data = receiver_contract_id_bytes(env, receiver);
    let workflow_execution_id = workflow_execution_id.to_array();
    data.extend_from_array(&workflow_execution_id);
    let report_id = report_id.to_array();
    data.extend_from_array(&report_id);
    env.crypto().sha256(&data).into()
}

/// Common dispatch path shared by `report()` and the public `route()` entry.
/// Soroban forbids contract re-entry, so the two cannot self-call each other;
/// they both invoke this helper directly. Caller is responsible for the
/// is_forwarder_impl + auth + ensure_initialized checks before calling.
fn dispatch_to_receiver(
    env: &Env,
    transmission_id: BytesN<32>,
    transmitter: Address,
    receiver: &Address,
    metadata: &Bytes,
    validated_report: &Bytes,
) -> Result<bool, ForwarderError> {
    let key = DataKey::Transmission(transmission_id);

    match env.storage().persistent().get::<_, Transmission>(&key) {
        Some(t)
            if t.state == TransmissionState::Succeeded
                || t.state == TransmissionState::InvalidReceiver =>
        {
            panic_with_error!(env, ForwarderError::AlreadyProcessed);
        }
        _ => {}
    }

    let state = if !matches!(receiver.executable(), Some(Executable::Wasm(_))) {
        // Not a Wasm contract — terminal; AlreadyProcessed above blocks retries.
        TransmissionState::InvalidReceiver
    } else {
        let args = (metadata.clone(), validated_report.clone()).into_val(env);
        let call =
            env.try_invoke_contract::<(), InvokeError>(receiver, &symbol_short!("on_report"), args);
        match call {
            Ok(Ok(())) => TransmissionState::Succeeded,
            Ok(Err(_)) | Err(Ok(_)) => TransmissionState::Failed,
            Err(Err(_)) => TransmissionState::InvalidReceiver,
        }
    };

    let tx = Transmission { state, transmitter };
    env.storage().persistent().set(&key, &tx);
    env.storage()
        .persistent()
        .extend_ttl(&key, BUMP_AFTER_3_DAYS, BUMP_FOR_1_DAY);

    Ok(state == TransmissionState::Succeeded)
}

#[cfg(test)]
mod test;
