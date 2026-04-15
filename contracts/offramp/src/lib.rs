#![no_std]

mod events;
pub mod types;

use common_interfaces::{
    token_admin_registry::TokenAdminRegistryClient,
    token_pool::{ReleaseOrMintIn, TokenPoolClient},
    versioned_verifier_resolver::VersionedVerifierResolverClient,
};
use soroban_sdk::{
    contract, contractimpl, symbol_short, xdr::ToXdr, Address, Bytes, BytesN, Env, Executable,
    IntoVal, Map, Symbol, Vec,
};
use stellar_strkey::Contract as StrkeyContract;

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::{initializable::Initializable, ReentrancyGuard};
use common_helpers::{curse_checkable::CurseCheckable, validation::Validatable};
use common_message::{
    AnyToStellarMessage, CcipMessageV1, CcipTokenTransferV1, FromBytes, MessageIdCompute,
    TokenAmount,
};
use events::{ExecutionStateChangedEvent, SourceChainConfigSetEvent, StaticConfigSetEvent};
use types::{
    DataKey, MessageExecutionState, SourceChainConfig, SourceChainConfigArgs, StaticConfig,
};

// ============================================================
// Storage Keys
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const STATIC_CONFIG: Symbol = symbol_short!("STATIC");
const SOURCE_CHAINS: Symbol = symbol_short!("SRCCHNS");
const RMN_PROXY: Symbol = symbol_short!("RMN_PROXY");

// Extend persistent entry TTL if it drops below ~30 days (at 5s/ledger)
const TTL_THRESHOLD: u32 = 518_400;
// Extend to ~180 days (at 5s/ledger)
const TTL_EXTEND_TO: u32 = 3_110_400;

// ============================================================
// Contract
// ============================================================

#[contract]
pub struct OffRampContract;

#[contractimpl]
impl Initializable for OffRampContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for OffRampContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl(contracttrait)]
impl CurseCheckable for OffRampContract {
    const RMN_PROXY: Symbol = RMN_PROXY;
}

#[contractimpl]
impl OffRampContract {
    // ========================================
    // Initialization
    // ========================================

    /// Initialize the OffRamp contract with static configuration.
    ///
    /// # Arguments
    /// * `owner` - The owner address (typically MCMS, can be the deployer initially)
    /// * `static_config` - Immutable configuration (chain selector, RMN proxy, token admin registry)
    pub fn initialize(
        env: Env,
        owner: Address,
        static_config: StaticConfig,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;

        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as Initializable>::init(&env)?;
        <Self as CurseCheckable>::init(&env, &static_config.rmn_proxy)?;

        if static_config.chain_selector == 0 {
            return Err(CCIPError::InvalidConfig);
        }

        env.storage().instance().set(&STATIC_CONFIG, &static_config);

        let source_chains: Map<u64, SourceChainConfig> = Map::new(&env);
        env.storage().instance().set(&SOURCE_CHAINS, &source_chains);

        StaticConfigSetEvent { static_config }.publish(&env);

        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "OffRamp 1.0.0")
    }

    // ========================================
    // Core Execution
    // ========================================

    /// Execute a cross-chain message that has been committed and attested.
    ///
    /// This is permissionless — anyone can call it. Security comes from
    /// CCV attestations, not caller identity.
    ///
    /// # Arguments
    /// * `encoded_message` - Canonical CcipMessageV1 wire-format bytes
    /// * `ccvs` - CCV resolver addresses that produced the attestations
    /// * `verifier_results` - Attestation blobs from each CCV (parallel to `ccvs`)
    /// * `gas_limit_override` - If non-zero, must be >= message's ccip_receive_gas_limit
    pub fn execute(
        env: Env,
        encoded_message: Bytes,
        ccvs: Vec<Address>,
        verifier_results: Vec<Bytes>,
        gas_limit_override: u32,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        ReentrancyGuard::enter(&env)?;

        let static_config = Self::get_static_config_internal(&env)?;

        // Decode the canonical message
        let message = CcipMessageV1::from_bytes(&env, &encoded_message)?;

        // Check if the source chain is cursed (both global and source-specific curse)
        <Self as CurseCheckable>::require_chain_not_cursed(&env, message.source_chain_selector)?;

        // Source chain must be enabled
        let source_config =
            Self::get_source_chain_config_internal(&env, message.source_chain_selector)?;
        if !source_config.is_enabled {
            return Err(CCIPError::SourceChainNotEnabled);
        }

        // OnRamp must be in the allowed set
        Self::verify_onramp_allowed(
            &env,
            message.source_chain_selector,
            &message.onramp_address,
            &source_config,
        )?;

        // OffRamp address in the message must match this contract.
        // We compare only the 32-byte hash of the contract address and leave
        // out the discriminant bytes.
        let self_xdr = env.current_contract_address().to_xdr(&env);
        // TODO: is there a better way to do this rather than slicing bytes?
        let self_hash = self_xdr.slice(self_xdr.len() - 32..);
        if message.offramp_address != self_hash {
            return Err(CCIPError::InvalidOffRampAddress);
        }

        // Destination chain must match local chain selector
        if message.dest_chain_selector != static_config.chain_selector {
            return Err(CCIPError::InvalidMessageDestination);
        }

        // CCV arrays must have matching lengths
        if ccvs.len() != verifier_results.len() {
            return Err(CCIPError::CCVLengthMismatch);
        }

        // Gas limit override validation
        if gas_limit_override != 0 && gas_limit_override < message.ccip_receive_gas_limit {
            return Err(CCIPError::GasLimitOverrideTooLow);
        }

        // Compute message ID = keccak256(encoded_message)
        let message_id: BytesN<32> =
            CcipMessageV1::compute_message_id_from_bytes(&env, &encoded_message);

        // Check execution state: only UNTOUCHED or FAILURE can be (re-)executed
        let current_state = Self::get_execution_state_internal(&env, &message_id);
        match current_state {
            MessageExecutionState::Untouched | MessageExecutionState::Failure => {}
            MessageExecutionState::InProgress | MessageExecutionState::Success => {
                return Err(CCIPError::MessageAlreadyExecuted);
            }
        }

        // Set state to InProgress (replay protection)
        // TODO: is it actually necessary to set the state to InProgress? If yes, can temp storage by used instead?
        Self::set_execution_state(&env, &message_id, MessageExecutionState::InProgress);

        // Verify CCVs and execute message
        let execution_result = Self::execute_single_message(
            &env,
            &message,
            &message_id,
            &ccvs,
            &verifier_results,
            &source_config,
            &static_config,
            gas_limit_override,
        );

        // Set final state based on outcome
        let (final_state, return_data) = match execution_result {
            Ok(()) => (MessageExecutionState::Success, Bytes::new(&env)),
            Err(_e) => {
                // Capture the error code as return data for debugging
                let mut data = Bytes::new(&env);
                data.append(&Bytes::from_array(&env, &(_e as u32).to_be_bytes()));
                (MessageExecutionState::Failure, data)
            }
        };

        Self::set_execution_state(&env, &message_id, final_state.clone());

        ExecutionStateChangedEvent {
            source_chain_selector: message.source_chain_selector,
            sequence_number: message.sequence_number,
            message_id,
            state: final_state,
            return_data,
        }
        .publish(&env);

        ReentrancyGuard::exit(&env);

        env.storage()
            .instance()
            .extend_ttl(TTL_THRESHOLD, TTL_EXTEND_TO);

        Ok(())
    }

    // ========================================
    // Query Functions
    // ========================================

    /// Get the execution state for a message.
    pub fn get_execution_state(
        env: Env,
        message_id: BytesN<32>,
    ) -> Result<MessageExecutionState, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Ok(Self::get_execution_state_internal(&env, &message_id))
    }

    /// Extends the persistent TTL of the execution-state entry for `message_id`, using the same
    /// threshold and target as writes from [`Self::execute`]. Permissionless so keepers can bump rent.
    ///
    /// Soroban does not expose reading a persistent entry's `live_until_ledger_seq` from guest
    /// code, and rent can also be extended outside this contract (same ledger entry). So there is
    /// no authoritative on-chain "get TTL" — use RPC / ledger APIs on the contract-data entry instead.
    pub fn extend_execution_state_ttl(env: Env, message_id: BytesN<32>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let state_key = DataKey::ExecState(message_id.clone());
        if !env.storage().persistent().has(&state_key) {
            return Err(CCIPError::InvalidExecutionState);
        }
        env.storage()
            .persistent()
            .extend_ttl(&state_key, TTL_THRESHOLD, TTL_EXTEND_TO);
        Ok(())
    }

    /// Get the static configuration.
    pub fn get_static_config(env: Env) -> Result<StaticConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Self::get_static_config_internal(&env)
    }

    /// Get configuration for a specific source chain.
    pub fn get_source_chain_config(
        env: Env,
        source_chain_selector: u64,
    ) -> Result<SourceChainConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Self::get_source_chain_config_internal(&env, source_chain_selector)
    }

    /// Get all source chain configurations.
    pub fn get_all_source_chain_configs(
        env: Env,
    ) -> Result<(Vec<u64>, Vec<SourceChainConfig>), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let source_chains: Map<u64, SourceChainConfig> = env
            .storage()
            .instance()
            .get(&SOURCE_CHAINS)
            .unwrap_or(Map::new(&env));

        let mut selectors: Vec<u64> = Vec::new(&env);
        let mut configs: Vec<SourceChainConfig> = Vec::new(&env);

        for (selector, config) in source_chains.iter() {
            selectors.push_back(selector);
            configs.push_back(config);
        }

        Ok((selectors, configs))
    }

    // ========================================
    // Admin Functions
    // ========================================

    /// Apply source chain configuration updates. Only callable by owner.
    ///
    /// Creates or updates per-source-chain configs that control which
    /// lanes are enabled and which OnRamps/CCVs are allowed.
    pub fn apply_source_chain_cfg_updates(
        env: Env,
        source_chain_config_args: Vec<SourceChainConfigArgs>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let static_config = Self::get_static_config_internal(&env)?;

        let mut source_chains: Map<u64, SourceChainConfig> = env
            .storage()
            .instance()
            .get(&SOURCE_CHAINS)
            .unwrap_or(Map::new(&env));

        for args in source_chain_config_args.iter() {
            args.validate()?;

            if args.source_chain_selector == static_config.chain_selector {
                return Err(CCIPError::InvalidConfig);
            }

            let new_config = SourceChainConfig {
                router: args.router.clone(),
                is_enabled: args.is_enabled,
                on_ramps: args.on_ramps.clone(),
                default_ccvs: args.default_ccvs.clone(),
                lane_mandated_ccvs: args.lane_mandated_ccvs.clone(),
            };

            source_chains.set(args.source_chain_selector, new_config.clone());

            SourceChainConfigSetEvent {
                source_chain_selector: args.source_chain_selector,
                source_config: new_config,
            }
            .publish(&env);
        }

        env.storage().instance().set(&SOURCE_CHAINS, &source_chains);

        Ok(())
    }

    // ========================================
    // Internal — Execution Logic
    // ========================================

    /// Inner execution logic: verify CCVs, handle tokens, route message.
    /// Separated from `execute` so the outer function can catch errors
    /// and record them as `Failure` state.
    fn execute_single_message(
        env: &Env,
        message: &CcipMessageV1,
        message_id: &BytesN<32>,
        ccvs: &Vec<Address>,
        verifier_results: &Vec<Bytes>,
        source_config: &SourceChainConfig,
        _static_config: &StaticConfig,
        _gas_limit_override: u32,
    ) -> Result<(), CCIPError> {
        // --- CCV Verification ---
        Self::verify_ccv_quorum(
            env,
            message.source_chain_selector,
            message_id,
            ccvs,
            verifier_results,
            source_config,
        )?;

        // --- Token Handling ---
        let dest_token_amounts: Vec<TokenAmount> = if message.token_transfer.len() > 0 {
            Self::release_or_mint_single_token(
                env,
                &message.token_transfer,
                &message.sender,
                message.source_chain_selector,
                message.finality,
                _static_config,
            )?
        } else {
            Vec::new(env)
        };

        // --- Message Routing ---
        // EVM skips `_callReceiver` for token-only (no data and ccipReceiveGasLimit == 0).
        let has_data = message.data.len() > 0;
        let has_receive_gas = message.ccip_receive_gas_limit > 0;

        if has_data || has_receive_gas {
            let receiver_contract = Self::ccip_receiver_contract_address(env, &message.receiver)?;

            // Fail before touching the Router: receiver must exist on-ledger and be a Wasm contract
            // (plain accounts / Stellar asset contracts cannot implement `ccip_receive`).
            match receiver_contract.executable() {
                Some(Executable::Wasm(_)) => {}
                None => return Err(CCIPError::ReceiverDoesNotExist),
                Some(Executable::Account) | Some(Executable::StellarAsset) => {
                    return Err(CCIPError::ReceiverNotWasmContract);
                }
            }

            let any2stellar = AnyToStellarMessage {
                message_id: message_id.clone(),
                source_chain_selector: message.source_chain_selector,
                sender: message.sender.clone(),
                data: message.data.clone(),
                dest_token_amounts,
            };

            Self::route_message(
                env,
                &source_config.router,
                &env.current_contract_address(),
                message.source_chain_selector,
                &receiver_contract,
                &any2stellar,
            )?;
        }

        Ok(())
    }

    /// Decode `CcipMessageV1.receiver` bytes as a Soroban **contract** [`Address`].
    ///
    /// Stellar CCIP payloads use the 32-byte contract identifier hash (same as EVM using 20-byte
    /// `message.receiver` for `address`). Account-only receivers are not supported here.
    fn ccip_receiver_contract_address(env: &Env, receiver: &Bytes) -> Result<Address, CCIPError> {
        const STELLAR_CONTRACT_ID_LEN: u32 = 32;
        if receiver.len() != STELLAR_CONTRACT_ID_LEN {
            return Err(CCIPError::InvalidReceiverLength);
        }
        let mut hash = [0u8; 32];
        for i in 0..STELLAR_CONTRACT_ID_LEN {
            hash[i as usize] = receiver.get(i).ok_or(CCIPError::InvalidReceiverLength)?;
        }
        // `TryFromVal<Env, ScAddress>` for `Address` is not available on the Wasm target; build a
        // contract strkey (C...) and parse it via the host, matching off-chain tooling.
        let sk = StrkeyContract(hash);
        let encoded = sk.to_string();
        Ok(Address::from_str(env, encoded.as_str()))
    }

    /// Verify that the CCV quorum is met for a message.
    ///
    /// Each CCV address is resolved via VersionedVerifierResolver to get
    /// the concrete verifier implementation, then `verify_message` is called.
    ///
    /// The quorum requires that all lane-mandated CCVs have verified,
    /// plus at least one default CCV if no lane-mandated CCVs exist.
    fn verify_ccv_quorum(
        env: &Env,
        source_chain_selector: u64,
        message_id: &BytesN<32>,
        ccvs: &Vec<Address>,
        verifier_results: &Vec<Bytes>,
        source_config: &SourceChainConfig,
    ) -> Result<(), CCIPError> {
        if ccvs.is_empty() {
            return Err(CCIPError::CCVQuorumNotMet);
        }

        // Track which mandated CCVs have been verified
        let mut mandated_verified = 0u32;
        let mut default_verified = 0u32;

        for i in 0..ccvs.len() {
            let ccv = ccvs.get(i).ok_or(CCIPError::CCVLengthMismatch)?;
            let result = verifier_results
                .get(i)
                .ok_or(CCIPError::CCVLengthMismatch)?;

            // TODO: is the ccv address here referring to the verifier or resolver contract?

            // Resolve the inbound verifier implementation from the CCV resolver
            let vvr = VersionedVerifierResolverClient::new(env, &ccv);
            let verifier_address = vvr.get_inbound_implementation(&result);

            // Call verify_message on the resolved verifier
            let message_hash: BytesN<32> = message_id.clone();
            let mut verify_args = soroban_sdk::Vec::new(env);
            verify_args.push_back(source_chain_selector.into_val(env));
            verify_args.push_back(message_hash.into_val(env));
            verify_args.push_back(result.into_val(env));

            env.invoke_contract::<Result<(), CCIPError>>(
                &verifier_address,
                &Symbol::new(env, "verify_message"),
                verify_args,
            )?;

            // Check if this CCV is a mandated or default one
            if Self::is_in_list(&ccv, &source_config.lane_mandated_ccvs) {
                mandated_verified += 1;
            }
            if Self::is_in_list(&ccv, &source_config.default_ccvs) {
                default_verified += 1;
            }
        }

        // All lane-mandated CCVs must have verified
        let mandated_count = source_config.lane_mandated_ccvs.len();
        if mandated_verified < mandated_count {
            return Err(CCIPError::CCVQuorumNotMet);
        }

        // If no mandated CCVs, at least one default CCV must verify
        if mandated_count == 0 && default_verified == 0 {
            return Err(CCIPError::CCVQuorumNotMet);
        }

        Ok(())
    }

    /// Route a verified message through the Router to the receiver contract (EVM `_callReceiver` analogue).
    fn route_message(
        env: &Env,
        router: &Address,
        offramp: &Address,
        source_chain_selector: u64,
        receiver: &Address,
        message: &AnyToStellarMessage,
    ) -> Result<(), CCIPError> {
        let mut args = soroban_sdk::Vec::new(env);
        args.push_back(offramp.into_val(env));
        args.push_back(source_chain_selector.into_val(env));
        args.push_back(receiver.into_val(env));
        args.push_back(message.clone().into_val(env));

        env.invoke_contract::<Result<(), CCIPError>>(
            router,
            &Symbol::new(env, "route_message"),
            args,
        )?;
        Ok(())
    }

    // ========================================
    // Internal — Token Handling
    // ========================================

    /// Decode the token transfer, resolve the destination pool via
    /// TokenAdminRegistry, call `release_or_mint`, and return the
    /// resulting `TokenAmount` for the receiver.
    ///
    /// `requested_finality` is forwarded from the cross-chain message and drives
    /// FTF inbound rate limit bucket selection in the pool. When the source is an
    /// EVM chain, this may be non-zero (WAIT_FOR_SAFE, block depth, etc.),
    /// reflecting higher reorg risk that the pool's FTF inbound limits guard.
    fn release_or_mint_single_token(
        env: &Env,
        token_transfer_bytes: &Bytes,
        original_sender: &Bytes,
        source_chain_selector: u64,
        requested_finality: u32,
        static_config: &StaticConfig,
    ) -> Result<Vec<TokenAmount>, CCIPError> {
        let token_transfer = CcipTokenTransferV1::from_bytes(env, token_transfer_bytes)?;

        let registry = TokenAdminRegistryClient::new(env, &static_config.token_admin_registry);

        let dest_token = Self::address_from_token_bytes(env, &token_transfer.dest_token_address)?;

        let pool_address = registry
            .get_pool(&dest_token)
            .ok_or(CCIPError::UnsupportedToken)?;

        let pool_client = TokenPoolClient::new(env, &pool_address);

        let amount = Self::bytes32_to_i128(env, &token_transfer.amount)?;

        let receiver_address = Self::address_from_token_bytes(env, &token_transfer.token_receiver)?;

        let release_result = pool_client.release_or_mint(
            &ReleaseOrMintIn {
                original_sender: original_sender.clone(),
                remote_chain_selector: source_chain_selector,
                receiver: receiver_address.clone(),
                amount,
                local_token: dest_token.clone(),
                source_pool_address: token_transfer.source_pool_address,
                source_pool_data: token_transfer.extra_data,
            },
            &requested_finality,
        );

        let mut amounts = Vec::new(env);
        amounts.push_back(TokenAmount {
            token: dest_token,
            amount: release_result.destination_amount,
        });
        Ok(amounts)
    }

    /// Convert raw bytes containing a 32-byte contract hash to a Soroban `Address`.
    fn address_from_token_bytes(env: &Env, bytes: &Bytes) -> Result<Address, CCIPError> {
        if bytes.len() < 32 {
            return Err(CCIPError::InvalidReceiverLength);
        }
        // Take the last 32 bytes (XDR-encoded addresses have a discriminant prefix)
        let offset = bytes.len() - 32;
        let mut hash = [0u8; 32];
        for i in 0..32u32 {
            hash[i as usize] = bytes
                .get(offset + i)
                .ok_or(CCIPError::InvalidReceiverLength)?;
        }
        let sk = StrkeyContract(hash);
        let encoded = sk.to_string();
        Ok(Address::from_str(env, encoded.as_str()))
    }

    /// Convert a 32-byte big-endian uint256 to i128 (lower 16 bytes).
    fn bytes32_to_i128(_env: &Env, bytes: &BytesN<32>) -> Result<i128, CCIPError> {
        let arr = bytes.to_array();
        // Ensure upper 16 bytes are zero (value fits in i128)
        for b in &arr[..16] {
            if *b != 0 {
                return Err(CCIPError::TokenHandlingError);
            }
        }
        let mut amount_bytes = [0u8; 16];
        amount_bytes.copy_from_slice(&arr[16..]);
        Ok(i128::from_be_bytes(amount_bytes))
    }

    // ========================================
    // Internal — Storage Helpers
    // ========================================

    fn get_static_config_internal(env: &Env) -> Result<StaticConfig, CCIPError> {
        env.storage()
            .instance()
            .get(&STATIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)
    }

    fn get_source_chain_config_internal(
        env: &Env,
        source_chain_selector: u64,
    ) -> Result<SourceChainConfig, CCIPError> {
        let source_chains: Map<u64, SourceChainConfig> = env
            .storage()
            .instance()
            .get(&SOURCE_CHAINS)
            .ok_or(CCIPError::SourceChainNotEnabled)?;

        source_chains
            .get(source_chain_selector)
            .ok_or(CCIPError::SourceChainNotEnabled)
    }

    fn get_execution_state_internal(env: &Env, message_id: &BytesN<32>) -> MessageExecutionState {
        let key = DataKey::ExecState(message_id.clone());
        env.storage()
            .persistent()
            .get(&key)
            .unwrap_or(MessageExecutionState::Untouched)
    }

    fn set_execution_state(env: &Env, message_id: &BytesN<32>, state: MessageExecutionState) {
        let key = DataKey::ExecState(message_id.clone());
        env.storage().persistent().set(&key, &state);
        env.storage()
            .persistent()
            .extend_ttl(&key, TTL_THRESHOLD, TTL_EXTEND_TO);
    }

    /// Verify the onramp address is in the allowed set for the source chain.
    /// Compares keccak256 hashes of the onramp bytes against stored allowed hashes.
    fn verify_onramp_allowed(
        env: &Env,
        _source_chain_selector: u64,
        onramp_address: &Bytes,
        source_config: &SourceChainConfig,
    ) -> Result<(), CCIPError> {
        let onramp_hash: BytesN<32> = env.crypto().keccak256(onramp_address).into();

        for allowed_onramp in source_config.on_ramps.iter() {
            let allowed_hash: BytesN<32> = env.crypto().keccak256(&allowed_onramp).into();
            if onramp_hash == allowed_hash {
                return Ok(());
            }
        }

        Err(CCIPError::InvalidOnRampAddress)
    }

    /// Check if an address is in a list.
    fn is_in_list(addr: &Address, list: &Vec<Address>) -> bool {
        for item in list.iter() {
            if &item == addr {
                return true;
            }
        }
        false
    }
}

mod test;
