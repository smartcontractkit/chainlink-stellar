#![no_std]

mod events;
pub mod types;

use common_authorization::allowlist::{AllowListEntry, AllowListUpdate, AllowListable};
use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_helpers::curse_checkable::CurseCheckable;
use common_verifier::base_verifier::BaseVerifier;
use common_verifier::signatures::{SignatureQuorum, SignatureQuorumConfig};
use soroban_sdk::{
    contract, contractimpl, symbol_short, Address, Bytes, BytesN, Env, Map, Symbol, Vec,
};
use types::{DynamicConfig, RemoteChainConfig};

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
        // TODO: implement this

        events::AllowListStateChangedEvent {
            dest_chain_selector: key,
            allowlist_enabled: true,
        }
        .publish(env);
    }
}

#[contractimpl]
impl SignatureQuorum for CommitteeVerifierContract {}

// #[contractimpl]
impl BaseVerifier for CommitteeVerifierContract {
    type RemoteChainConfig = RemoteChainConfig;

    const STORAGE_LOCATIONS: Symbol = STORAGE_LOC_ADMIN;
    const REMOTE_CHAINS: Symbol = REMOTE_CHAINS;

    fn emit_remote_chain_config_set_event(
        env: &Env,
        remote_chain_config: &Self::RemoteChainConfig,
    ) {
        events::RemoteChainConfigSetEvent {
            remote_chain_selector: remote_chain_config.remote_chain_selector,
            router: remote_chain_config.router.clone(),
            allowlist_enabled: remote_chain_config.allowlist_enabled,
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
        <Self as BaseVerifier>::init(&env, &storage_locations)?;

        env.storage()
            .instance()
            .set(&DYNAMIC_CONFIG, &dynamic_config);
        env.storage().instance().set(&STORAGE_LOC_ADMIN, &owner);

        let sig_cfgs: Map<u64, SignatureQuorumConfig> = Map::new(&env);
        env.storage()
            .persistent()
            .set(&SIGNATURE_CONFIGS, &sig_cfgs);

        // TODO: Publish ConfigSet + ownership/bootstrap events via `events.rs`.
        Ok(())
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

        // TODO: generate correct verification blob

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
        if version != BytesN::from_array(&env, &VERSION_TAG_V1_7_0) {
            return Err(CCIPError::InvalidCCVVersion);
        }

        let signature_len = <Self as SignatureQuorum>::extract_signature_len(&verifier_results)?;
        let expected = VERIFIER_VERSION_BYTES + SIGNATURE_LENGTH_BYTES + signature_len;
        if verifier_results.len() < expected {
            return Err(CCIPError::InvalidVerifierResults);
        }

        // TODO: finalize exact signed payload format with offchain signer pipeline.
        let mut signed_payload = Bytes::new(&env);
        signed_payload.append(&Bytes::from_array(&env, &VERSION_TAG_V1_7_0));
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
        // TODO: publish ConfigSet event.
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
        <Self as BaseVerifier>::apply_remote_chain_config_updates(&env, &remote_chain_config_args)?;
        Ok(())
    }

    pub fn get_remote_chain_config(
        env: Env,
        remote_chain_selector: u64,
    ) -> Result<RemoteChainConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as BaseVerifier>::get_remote_chain_config(&env, remote_chain_selector)
            .map_err(Into::into)
    }

    /// EVM-equivalent fee quote shape.
    pub fn get_fee(
        env: Env,
        dest_chain_selector: u64,
        _message: Bytes,
        _extra_args: Bytes,
        _block_confirmations: u32,
    ) -> Result<(u32, u32, u32), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as BaseVerifier>::get_fee(&env, dest_chain_selector).map_err(Into::into)
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

        // TODO: publish StorageLocationsAdminTransferRequested.
        Ok(())
    }

    pub fn accept_storage_locations_admin(_env: Env) -> Result<(), CCIPError> {
        // TODO: implement
        unimplemented!();
    }

    pub fn update_storage_locations(env: Env, new_locations: Vec<Bytes>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let admin = Self::get_storage_locations_admin(env.clone())?;
        admin.require_auth();

        env.storage()
            .instance()
            .set(&STORAGE_LOCATIONS, &new_locations);

        // TODO: publish StorageLocationsUpdated(old_locations, new_locations).
        Ok(())
    }

    // ========================================
    // Fees
    // ========================================

    pub fn withdraw_fee_tokens(env: Env, fee_tokens: Vec<Address>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let dynamic = Self::get_dynamic_config(env.clone())?;

        // TODO: integrate token transfer / fee token handler logic.
        // Fee withdrawal is permissionless in EVM and transfers to fee_aggregator.
        let _ = (dynamic.fee_aggregator, fee_tokens);
        Ok(())
    }
}

mod test;
