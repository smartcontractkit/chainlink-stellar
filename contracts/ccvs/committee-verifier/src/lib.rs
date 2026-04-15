#![no_std]

mod events;
pub mod types;

use common_authorization::allowlist::{AllowListEntry, AllowListUpdate, AllowListable};
use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_helpers::curse_checkable::CurseCheckable;
use common_helpers::validation::Validatable;
use common_verifier::signatures::{SignatureQuorum, SignatureQuorumConfig};
use soroban_sdk::{
    contract, contractimpl, symbol_short, Address, Bytes, BytesN, Env, Map, Symbol, Vec,
};
use types::{DynamicConfig, RemoteChainConfig};

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

// ============================================================
// Constants
// ============================================================

const VERSION_TAG_V1_7_0: [u8; 4] = [0x49, 0xff, 0x34, 0xed];
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

#[contractimpl(contracttrait)]
impl SignatureQuorum for CommitteeVerifierContract {
    const SIGNATURE_CONFIGS: Symbol = SIGNATURE_CONFIGS;

    fn emit_signature_config_set(
        env: &Env,
        source_chain_selector: u64,
        signers: &Vec<BytesN<32>>,
        threshold: u32,
    ) {
        events::SignatureConfigSetEvent {
            source_chain_selector,
            signers: signers.clone(),
            threshold,
        }
        .publish(env);
    }
}

#[contractimpl]
impl CommitteeVerifierContract {
    /// Initializes CommitteeVerifier with owner/dynamic config/storage locations/RMN proxy.
    pub fn initialize(
        env: Env,
        owner: Address,
        dynamic_config: DynamicConfig,
        storage_locations: Vec<Bytes>,
        rmn_proxy: Address,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;

        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as CurseCheckable>::init(&env, &rmn_proxy)?;
        <Self as AllowListable>::init_allowlist(&env, Map::new(&env));

        env.storage()
            .instance()
            .set(&STORAGE_LOCATIONS, &storage_locations);

        let remote_chains: Map<u64, RemoteChainConfig> = Map::new(&env);
        env.storage().instance().set(&REMOTE_CHAINS, &remote_chains);

        env.storage()
            .instance()
            .set(&DYNAMIC_CONFIG, &dynamic_config);
        env.storage().instance().set(&STORAGE_LOC_ADMIN, &owner);

        let sig_cfgs: Map<u64, SignatureQuorumConfig> = Map::new(&env);
        env.storage()
            .persistent()
            .set(&SIGNATURE_CONFIGS, &sig_cfgs);

        events::ConfigSetEvent {
            dynamic_config: dynamic_config.clone(),
        }
        .publish(&env);
        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "CommitteeVerifier 1.0.0")
    }

    // ========================================
    // Core verifier methods
    // ========================================

    /// Source-side hook that checks sender permissions and returns version tag.
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

        // TODO: this currently just returns the version tag, do we need to add more data?
        verification_blob.append(&Bytes::from_array(&env, &VERSION_TAG_V1_7_0));
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

        let version = <Self as SignatureQuorum>::extract_version_tag(&env, &verifier_results)?;
        // Version-based routing is handled by the VVR; no need to re-check here.

        let signature_len = <Self as SignatureQuorum>::extract_signature_len(&verifier_results)?;
        let expected = VERIFIER_VERSION_BYTES + SIGNATURE_LENGTH_BYTES + signature_len;
        if verifier_results.len() < expected {
            return Err(CCIPError::InvalidVerifierResults);
        }

        let mut signed_payload = Bytes::new(&env);
        signed_payload.append(&Bytes::from_slice(&env, &version.to_array()));
        signed_payload.append(&Bytes::from_array(&env, &message_hash.to_array()));
        let signed_hash: BytesN<32> = env.crypto().keccak256(&signed_payload).into();

        let signatures =
            verifier_results.slice(VERIFIER_VERSION_BYTES + SIGNATURE_LENGTH_BYTES..expected);
        <Self as SignatureQuorum>::validate_signatures(
            &env,
            source_chain_selector,
            signed_hash,
            signatures,
        )
    }

    /// Returns static version tag used in outbound verifier responses.
    pub fn version_tag(env: Env) -> BytesN<4> {
        BytesN::from_array(&env, &VERSION_TAG_V1_7_0)
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

    pub fn withdraw_fee_tokens(env: Env, fee_tokens: Vec<Address>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let dynamic = Self::get_dynamic_config(env.clone())?;

        let _ = (dynamic.fee_aggregator, fee_tokens);
        Ok(())
    }
}

mod test;
