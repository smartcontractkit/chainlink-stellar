#![no_std]

mod events;
pub mod types;

use common_authorization::allowlist::{AllowListEntry, AllowListUpdate, AllowListable};
use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_helpers::{curse_checkable::CurseCheckable, validation::Validatable};
use common_signature::config::{
    SignatureConfig, SignatureConfigManager, SignatureVerificationConfig,
};
use common_signature::quorum::{SignatureQuorum, PER_SIGNATURE_BYTES};
use soroban_sdk::{
    contract, contractimpl, symbol_short, token, Address, Bytes, BytesN, Env, Map, Symbol, Vec,
};
use types::{DynamicConfig, QuorumConfigKey, RemoteChainConfig, SignatureQuorumConfig};

use crate::types::FeeResponse;

// ============================================================
// Storage Keys
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const DYNAMIC_CONFIG: Symbol = symbol_short!("DYNCFG");
const SIGNATURE_CONFIGS: Symbol = symbol_short!("SIGCFGS");
const STORAGE_LOC_ADMIN: Symbol = symbol_short!("STORADM");
const PENDING_STORAGE_LOC_ADMIN: Symbol = symbol_short!("PSTORADM");
const STORAGE_LOCATIONS: Symbol = symbol_short!("STORLOC");
const RMN_PROXY: Symbol = symbol_short!("RMNPROXY");
const REMOTE_CHAINS: Symbol = symbol_short!("RCHAINS");
const ALLOWLIST: Symbol = symbol_short!("ALLOWLST");
/// Instance storage key for the immutable verifier version tag (`bytes4`), set at `initialize`.
const VERIFIER_VERSION_TAG_KEY: Symbol = symbol_short!("CVRTAG");

// ============================================================
// Constants
// ============================================================

/// Default CCIP committee verifier version tag (matches common EVM/Stellar devenv wiring).
/// Deployments may pass a different non-zero `version_tag` at `initialize` for domain separation.
/// bytes4(keccak256("CommitteeVerifier 2.0.0")) — aligned with EVM `CommitteeVerifierV2`.
pub const DEFAULT_VERIFIER_VERSION_TAG: [u8; 4] = [0xe9, 0xa0, 0x5a, 0x20];
const VERIFIER_VERSION_BYTES: u32 = 4;
const SIGNATURE_LENGTH_BYTES: u32 = 2;

// ============================================================
// Contract
// ============================================================

#[contract]
pub struct CommitteeVerifierContract;

#[contractimpl]
impl Initializable for CommitteeVerifierContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for CommitteeVerifierContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl]
impl CurseCheckable for CommitteeVerifierContract {
    const RMN_PROXY: Symbol = RMN_PROXY;
}

#[contractimpl(contracttrait)]
impl AllowListable for CommitteeVerifierContract {
    const ALLOW_LIST: Symbol = ALLOWLIST;

    fn emit_allowlist_updated_event(
        env: &Env,
        key: u64,
        _added_addresses: &Vec<Address>,
        _removed_addresses: &Vec<Address>,
    ) {
        let allowlist_enabled = Self::get_allowlist_entry(env, key)
            .map(|e| e.allowlist_enabled)
            .unwrap_or(false);

        events::AllowListStateChangedEvent {
            dest_chain_selector: key,
            allowlist_enabled,
        }
        .publish(env);
    }
}

impl SignatureConfigManager for CommitteeVerifierContract {
    const SIGNATURE_CONFIGS: Symbol = SIGNATURE_CONFIGS;
    const IS_PERSISTENT: bool = true;
    type Error = CCIPError;
    type DataKey = QuorumConfigKey;

    fn validate_config(_env: &Env, config: &SignatureConfig) -> Result<(), Self::Error> {
        let threshold = config.verification.threshold();
        if threshold == 0 || threshold > config.signers.len() {
            return Err(CCIPError::InvalidSignatureThreshold);
        }

        // Signers must be strictly ascending: rejects duplicates and makes the
        // set deterministic (mirrors the ordering enforced in verification).
        let signers = &config.signers;
        let mut j = 1u32;
        while j < signers.len() {
            let prev = signers
                .get(j - 1)
                .ok_or(CCIPError::InvalidSignaturePubkey)?;
            let curr = signers.get(j).ok_or(CCIPError::InvalidSignaturePubkey)?;
            if prev == curr {
                return Err(CCIPError::DuplicateOnchainPublicKey);
            }
            if prev > curr {
                return Err(CCIPError::InvalidSignerOrder);
            }
            j += 1;
        }

        Ok(())
    }
}

// Internal quorum-verification mixin; not exported as contract entrypoints.
impl SignatureQuorum for CommitteeVerifierContract {}

#[contractimpl]
impl CommitteeVerifierContract {
    fn load_verifier_version_tag(env: &Env) -> Result<BytesN<4>, CCIPError> {
        env.storage()
            .instance()
            .get(&VERIFIER_VERSION_TAG_KEY)
            .ok_or(CCIPError::NotInitialized)
    }

    /// Initializes CommitteeVerifier with owner/dynamic config/storage locations/RMN proxy
    /// and immutable `version_tag` (non-zero `bytes4`, same role as EVM `BaseVerifier` `i_versionTag`).
    pub fn initialize(
        env: Env,
        owner: Address,
        dynamic_config: DynamicConfig,
        storage_locations: Vec<Bytes>,
        rmn_proxy: Address,
        version_tag: BytesN<4>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;

        let zero = BytesN::from_array(&env, &[0u8; 4]);
        if version_tag == zero {
            return Err(CCIPError::InvalidVersionTag);
        }

        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as CurseCheckable>::init(&env, &rmn_proxy)?;
        <Self as AllowListable>::init_allowlist(&env, Map::new(&env));

        env.storage()
            .instance()
            .set(&VERIFIER_VERSION_TAG_KEY, &version_tag);

        env.storage()
            .instance()
            .set(&STORAGE_LOCATIONS, &storage_locations);

        let remote_chains: Map<u64, RemoteChainConfig> = Map::new(&env);
        env.storage().instance().set(&REMOTE_CHAINS, &remote_chains);

        env.storage()
            .instance()
            .set(&DYNAMIC_CONFIG, &dynamic_config);
        env.storage().instance().set(&STORAGE_LOC_ADMIN, &owner);

        events::ConfigSetEvent {
            dynamic_config: dynamic_config.clone(),
        }
        .publish(&env);
        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "CommitteeVerifier 2.0.0")
    }

    // ========================================
    // Core verifier methods
    // ========================================

    /// Source-side hook that checks sender permissions and returns version tag.
    ///
    /// Allowlist: when enabled for `dest_chain_selector`, `sender` must be on the stored list
    /// (`AllowListable::require_in_allowlist` — membership only, no `require_auth` on `sender`).
    /// OnRamp binds `original_sender` on `forward_from_router`; binding auth on this nested
    /// `forward_to_verifier` call for `sender` is optional and needs matching sub-invocation auth
    /// in the transaction (see `common_authorization::allowlist::require_in_allowlist_authorized`).
    pub fn forward_to_verifier(
        env: Env,
        dest_chain_selector: u64,
        sender: Address,
        _message_id: BytesN<32>,
        _fee_token: Address,
        _fee_token_amount: i128,
        _verifier_args: Bytes,
    ) -> Result<Bytes, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let mut verification_blob = Bytes::new(&env);

        <Self as CurseCheckable>::require_not_cursed(&env)?;
        <Self as AllowListable>::require_in_allowlist(&env, dest_chain_selector, &sender)?;

        let tag = Self::load_verifier_version_tag(&env)?;
        verification_blob.append(&Bytes::from_slice(&env, &tag.to_array()));
        Ok(verification_blob)
    }

    /// Destination-side hook that parses verifier result payload and validates signatures.
    ///
    /// TODO: bind to canonical inbound message struct instead of `(source_chain_selector, message_hash)`.
    pub fn verify_message(
        env: Env,
        source_chain_selector: u64,
        message_hash: BytesN<32>,
        verifier_results: Bytes,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        <Self as CurseCheckable>::require_not_cursed(&env)?;

        if verifier_results.len() < VERIFIER_VERSION_BYTES + SIGNATURE_LENGTH_BYTES {
            return Err(CCIPError::InvalidVerifierResults);
        }

        let version = extract_version_tag(&env, &verifier_results)?;
        let expected_tag = Self::load_verifier_version_tag(&env)?;
        if version != expected_tag {
            return Err(CCIPError::InvalidCCVVersion);
        }

        let signature_len = extract_signature_len(&verifier_results)?;
        let expected = VERIFIER_VERSION_BYTES + SIGNATURE_LENGTH_BYTES + signature_len;
        if verifier_results.len() < expected {
            return Err(CCIPError::InvalidVerifierResults);
        }

        let mut signed_payload = Bytes::new(&env);
        signed_payload.append(&Bytes::from_slice(&env, &version.to_array()));
        signed_payload.append(&Bytes::from_array(&env, &message_hash.to_array()));
        let signed_hash: BytesN<32> = env.crypto().keccak256(&signed_payload).into();

        // Slice the raw signature blob into EIP-2098 compact 64-byte signatures.
        let sig_blob =
            verifier_results.slice(VERIFIER_VERSION_BYTES + SIGNATURE_LENGTH_BYTES..expected);
        if sig_blob.len() % PER_SIGNATURE_BYTES != 0 {
            return Err(CCIPError::InvalidSignatureLength);
        }
        let mut signatures: Vec<BytesN<64>> = Vec::new(&env);
        let mut offset = 0u32;
        while offset < sig_blob.len() {
            let mut raw = [0u8; PER_SIGNATURE_BYTES as usize];
            let mut i = 0u32;
            while i < PER_SIGNATURE_BYTES {
                raw[i as usize] = sig_blob
                    .get(offset + i)
                    .ok_or(CCIPError::InvalidSignature)?;
                i += 1;
            }
            signatures.push_back(BytesN::from_array(&env, &raw));
            offset += PER_SIGNATURE_BYTES;
        }

        let key = QuorumConfigKey::SourceChainSelector(source_chain_selector);
        <Self as SignatureQuorum>::validate_signatures(&env, &key, signed_hash, signatures)
    }

    /// Returns the configured verifier version tag (`bytes4`), matching EVM `versionTag()`.
    pub fn version_tag(env: Env) -> BytesN<4> {
        <Self as Initializable>::require_initialized(&env).unwrap();
        Self::load_verifier_version_tag(&env)
            .unwrap_or_else(|_| panic!("invariant: version tag is set during initialize"))
    }

    // ========================================
    // Signature configs
    // ========================================

    /// Owner-only batch update of per-source-chain signer sets.
    ///
    /// Removals are applied first, then upserts. Each upsert is validated and
    /// persisted through `SignatureConfigManager` (see `validate_config`).
    pub fn apply_signature_configs(
        env: Env,
        source_chains_to_remove: Vec<u64>,
        signature_configs: Vec<SignatureQuorumConfig>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        for source_chain_selector in source_chains_to_remove.iter() {
            let key = QuorumConfigKey::SourceChainSelector(source_chain_selector);
            <Self as SignatureConfigManager>::remove_config(&env, &key);

            events::SignatureConfigSetEvent {
                source_chain_selector,
                signers: Vec::new(&env),
                threshold: 0,
            }
            .publish(&env);
        }

        for update in signature_configs.iter() {
            let cfg = SignatureConfig {
                signers: update.signers.clone(),
                verification: SignatureVerificationConfig::Threshold(update.threshold),
            };
            let key = QuorumConfigKey::SourceChainSelector(update.source_chain_selector);
            <Self as SignatureConfigManager>::set_config(&env, &key, &cfg)?;

            events::SignatureConfigSetEvent {
                source_chain_selector: update.source_chain_selector,
                signers: update.signers.clone(),
                threshold: update.threshold,
            }
            .publish(&env);
        }

        Ok(())
    }

    /// Returns the configured signer set for `source_chain_selector`.
    pub fn get_signature_config(
        env: Env,
        source_chain_selector: u64,
    ) -> Result<SignatureQuorumConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let key = QuorumConfigKey::SourceChainSelector(source_chain_selector);
        let cfg = <Self as SignatureConfigManager>::get_config(&env, &key)
            .ok_or(CCIPError::SourceSignersNotConfigured)?;

        Ok(SignatureQuorumConfig {
            source_chain_selector,
            threshold: cfg.verification.threshold(),
            signers: cfg.signers,
        })
    }

    // ========================================
    // Dynamic config
    // ========================================

    pub fn get_dynamic_config(env: Env) -> Result<DynamicConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&DYNAMIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)
    }

    pub fn set_dynamic_config(env: Env, dynamic_config: DynamicConfig) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        env.storage()
            .instance()
            .set(&DYNAMIC_CONFIG, &dynamic_config);
        events::ConfigSetEvent {
            dynamic_config: dynamic_config.clone(),
        }
        .publish(&env);
        Ok(())
    }

    // ========================================
    // Base verifier config
    // ========================================

    pub fn get_storage_locations(env: Env) -> Result<Vec<Bytes>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&STORAGE_LOCATIONS)
            .ok_or(CCIPError::NotInitialized)
    }

    pub fn apply_remote_chain_cfg_updates(
        env: Env,
        remote_chain_config_args: Vec<RemoteChainConfig>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut remote_chains: Map<u64, RemoteChainConfig> = env
            .storage()
            .instance()
            .get(&REMOTE_CHAINS)
            .unwrap_or(Map::new(&env));

        for update in remote_chain_config_args.iter() {
            update.validate()?;

            remote_chains.set(update.remote_chain_selector, update.clone());

            events::RemoteChainConfigSetEvent {
                remote_chain_selector: update.remote_chain_selector,
                router: update.router.clone(),
                allowlist_enabled: update.allowlist_enabled,
            }
            .publish(&env);
        }

        env.storage().instance().set(&REMOTE_CHAINS, &remote_chains);

        Ok(())
    }

    pub fn get_remote_chain_config(
        env: Env,
        remote_chain_selector: u64,
    ) -> Result<RemoteChainConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let remote_chains: Map<u64, RemoteChainConfig> = env
            .storage()
            .instance()
            .get(&REMOTE_CHAINS)
            .unwrap_or(Map::new(&env));

        remote_chains
            .get(remote_chain_selector)
            .ok_or(CCIPError::RemoteChainNotSupported)
    }

    /// EVM-equivalent fee quote shape.
    pub fn get_fee(
        env: Env,
        dest_chain_selector: u64,
        _message: Bytes,
        _extra_args: Bytes,
        _block_confirmations: u32,
    ) -> Result<FeeResponse, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let cfg = Self::get_remote_chain_config(env, dest_chain_selector)?;
        Ok(FeeResponse {
            fee: cfg.fee_usd_cents,
            dest_gas_limit: cfg.gas_for_verification,
            dest_bytes_overhead: cfg.payload_size_bytes,
        })
    }

    // ========================================
    // Storage locations
    // ========================================

    pub fn get_storage_locations_admin(env: Env) -> Result<Address, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&STORAGE_LOC_ADMIN)
            .ok_or(CCIPError::NotInitialized)
    }

    pub fn get_pending_storage_loc_admin(env: Env) -> Result<Option<Address>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Ok(env.storage().instance().get(&PENDING_STORAGE_LOC_ADMIN))
    }

    pub fn transfer_storage_locations_admin(env: Env, to: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let current_admin = Self::get_storage_locations_admin(env.clone())?;
        current_admin.require_auth();

        env.storage()
            .instance()
            .set(&PENDING_STORAGE_LOC_ADMIN, &to);

        events::StorageAdminTransferReqEvent {
            from: current_admin,
            to: to.clone(),
        }
        .publish(&env);
        Ok(())
    }

    pub fn accept_storage_locations_admin(env: Env) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let pending: Address = env
            .storage()
            .instance()
            .get(&PENDING_STORAGE_LOC_ADMIN)
            .ok_or(CCIPError::NoPendingOwner)?;

        pending.require_auth();

        let from = Self::get_storage_locations_admin(env.clone())?;

        env.storage().instance().set(&STORAGE_LOC_ADMIN, &pending);
        env.storage().instance().remove(&PENDING_STORAGE_LOC_ADMIN);

        events::StorageAdminTransferredEvent {
            from,
            to: pending.clone(),
        }
        .publish(&env);
        Ok(())
    }

    pub fn update_storage_locations(env: Env, new_locations: Vec<Bytes>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let admin = Self::get_storage_locations_admin(env.clone())?;
        admin.require_auth();

        let old_locations = Self::get_storage_locations(env.clone())?;

        env.storage()
            .instance()
            .set(&STORAGE_LOCATIONS, &new_locations);

        events::StorageLocationsUpdatedEvent {
            old_locations,
            new_locations: new_locations.clone(),
        }
        .publish(&env);
        Ok(())
    }

    // ========================================
    // Fees
    // ========================================

    /// Withdraws outstanding fee token balances to the configured fee aggregator.
    ///
    /// Permissionless: only transfers to the trusted aggregator address (same model as EVM).
    ///
    /// # Errors
    /// * [`CCIPError::ZeroFeeAggregatorNotAllowed`] — dynamic config has no fee aggregator or it is the zero account.
    pub fn withdraw_fee_tokens(env: Env, fee_tokens: Vec<Address>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let dynamic = Self::get_dynamic_config(env.clone())?;
        let fee_agg = dynamic
            .fee_aggregator
            .ok_or(CCIPError::ZeroFeeAggregatorNotAllowed)?;
        if is_zero_fee_recipient(&env, &fee_agg) {
            return Err(CCIPError::ZeroFeeAggregatorNotAllowed);
        }

        let cv_address = env.current_contract_address();
        for i in 0..fee_tokens.len() {
            if let Some(fee_token) = fee_tokens.get(i) {
                let token_client = token::Client::new(&env, &fee_token);
                let balance = token_client.balance(&cv_address);
                if balance > 0 {
                    token_client.transfer(&cv_address, &fee_agg, &balance);
                }
            }
        }

        Ok(())
    }
}

/// Reads the leading `bytes4` version tag from the verifier-results blob.
fn extract_version_tag(env: &Env, verifier_results: &Bytes) -> Result<BytesN<4>, CCIPError> {
    if verifier_results.len() < VERIFIER_VERSION_BYTES {
        return Err(CCIPError::InvalidVerifierResults);
    }
    let mut out = [0u8; VERIFIER_VERSION_BYTES as usize];
    let mut i = 0u32;
    while i < VERIFIER_VERSION_BYTES {
        out[i as usize] = verifier_results
            .get(i)
            .ok_or(CCIPError::InvalidVerifierResults)?;
        i += 1;
    }
    Ok(BytesN::from_array(env, &out))
}

/// Reads the big-endian `u16` signature-payload length that follows the version tag.
fn extract_signature_len(verifier_results: &Bytes) -> Result<u32, CCIPError> {
    if verifier_results.len() < VERIFIER_VERSION_BYTES + SIGNATURE_LENGTH_BYTES {
        return Err(CCIPError::InvalidVerifierResults);
    }
    let b0 = verifier_results
        .get(VERIFIER_VERSION_BYTES)
        .ok_or(CCIPError::InvalidVerifierResults)?;
    let b1 = verifier_results
        .get(VERIFIER_VERSION_BYTES + 1)
        .ok_or(CCIPError::InvalidVerifierResults)?;
    Ok(((b0 as u32) << 8) | (b1 as u32))
}

fn is_zero_fee_recipient(env: &Env, addr: &Address) -> bool {
    addr == &Address::from_str(
        env,
        "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
    )
}

mod test;
