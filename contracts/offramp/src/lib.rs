#![no_std]

mod events;
pub mod types;

use common_interfaces::{
    ccip_receiver::CcvsAndFinalityConfig,
    token_admin_registry::TokenAdminRegistryClient,
    token_pool::{MessageDirection, ReleaseOrMintIn, TokenPoolClient},
    versioned_verifier_resolver::VersionedVerifierResolverClient,
};
// `finality_codec` lives in `common-pool` (the inbound pools already use it in `release_or_mint`);
// OffRamp reuses it for the receiver allowed-finality check (H-7) so the rule stays identical.
use common_pool::finality_codec;
use soroban_sdk::{
    contract, contractimpl, symbol_short, xdr::ToXdr, Address, Bytes, BytesN, Env, Executable,
    IntoVal, InvokeError, Map, Symbol, Vec,
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
        soroban_sdk::String::from_str(&_env, "OffRamp-dev 2.0.0")
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
        static_config: &StaticConfig,
        _gas_limit_override: u32,
    ) -> Result<(), CCIPError> {
        // --- CCV Verification ---
        Self::verify_ccv_quorum(
            env,
            message.source_chain_selector,
            message_id,
            message,
            ccvs,
            verifier_results,
            source_config,
            static_config,
        )?;

        // --- Token Handling ---
        let dest_token_amounts: Vec<TokenAmount> = if message.token_transfer.len() > 0 {
            Self::release_or_mint_single_token(
                env,
                &message.token_transfer,
                &message.sender,
                &message.receiver,
                message.source_chain_selector,
                message.finality,
                static_config,
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

    /// Pool-required CCVs for an inbound token transfer (EVM `OffRamp._getCCVsFromPool`).
    /// Flattens [`PoolRequiredCCVs`] into a single `Vec<Address>` by appending `default_ccvs`
    /// when the pool reports `include_defaults = true` (or returned no CCVs at all, matching
    /// EVM's "empty list ⇒ use defaults" fallback).
    fn get_inbound_pool_required_ccvs(
        env: &Env,
        source_chain_selector: u64,
        requested_finality: u32,
        token_transfer_bytes: &Bytes,
        static_config: &StaticConfig,
        default_ccvs: &Vec<Address>,
    ) -> Result<Vec<Address>, CCIPError> {
        if token_transfer_bytes.is_empty() {
            return Ok(Vec::new(env));
        }

        let token_transfer = CcipTokenTransferV1::from_bytes(env, token_transfer_bytes)?;
        let registry = TokenAdminRegistryClient::new(env, &static_config.token_admin_registry);
        let dest_token = Self::address_from_token_bytes(env, &token_transfer.dest_token_address)?;
        let pool_address = registry
            .get_pool(&dest_token)
            .ok_or(CCIPError::UnsupportedToken)?;
        let pool_client = TokenPoolClient::new(env, &pool_address);
        let amount = Self::bytes32_to_i128(env, &token_transfer.amount)?;
        let required = pool_client.get_required_ccvs(
            &dest_token,
            &source_chain_selector,
            &amount,
            &requested_finality,
            &token_transfer.extra_data,
            &MessageDirection::Inbound,
        );

        let mut flattened: Vec<Address> = required.ccvs.clone();

        if required.include_defaults {
            for i in 0..default_ccvs.len() {
                if let Some(ccv) = default_ccvs.get(i) {
                    if !Self::is_in_list(&ccv, &flattened) {
                        flattened.push_back(ccv);
                    }
                }
            }
        }

        Ok(flattened)
    }

    /// Public read-only view mirroring EVM `OffRamp.getCCVsForMessage(encodedMessage)`. Exists for
    /// **off-chain** use — the CCV executor/aggregator calls it to gather attestations for exactly
    /// the CCVs that on-chain `execute` will enforce, so the off-chain report and on-chain quorum
    /// cannot drift (see C-1: the receiver consult added receiver-required/optional CCVs that the
    /// old off-chain `GetCCVSForMessage` — lane-mandated + defaults only — never gathered). Calling
    /// this on-chain is not gas-efficient (it re-runs the receiver/pool consult); the on-chain path
    /// uses [`Self::get_ccvs_for_message_internal`] directly.
    ///
    /// Returns `(required, optional, optional_threshold)`:
    /// - **token-only** (`data` empty AND `ccip_receive_gas_limit == 0`): `required` = lane-mandated,
    ///   `optional` = lane defaults, threshold 1 when defaults exist — matching the existing off-chain
    ///   reader output and the token-only "≥1 default when no lane-mandated" floor. Receiver
    ///   consultation does NOT apply to token-only (mirroring EVM `_isTokenOnlyTransfer`).
    /// - **non-token-only**: identical to what `verify_ccv_quorum` enforces — the receiver's
    ///   `get_ccvs_and_finality_config` resolved + merged with pool-required + lane-mandated (+ lane
    ///   defaults via the empty-config sentinel), and the receiver's optional/threshold. (`allowed_finality`
    ///   is intentionally NOT returned here — EVM's view returns only the CCV triple; finality is
    ///   enforced on-chain via H-7.)
    pub fn get_ccvs_for_message(
        env: Env,
        encoded_message: Bytes,
    ) -> Result<(Vec<Address>, Vec<Address>, u32), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let static_config = Self::get_static_config_internal(&env)?;
        let message = CcipMessageV1::from_bytes(&env, &encoded_message)?;
        let source_config =
            Self::get_source_chain_config_internal(&env, message.source_chain_selector)?;

        // EVM `_isTokenOnlyTransfer` (`OffRamp.sol:426`):
        //   `(dataLength == 0 && ccipReceiveGasLimit == 0) || receiver.code.length == 0
        //    || !supportsInterface(IAny2EVMMessageReceiver)`
        // This **view** is the off-chain source of truth for which CCVs the DON must gather before
        // it can transmit. EVM's `getCCVsForMessage` returns token-only defaults when the receiver is
        // not a contract (`receiver.code.length == 0`), so aggregation/transmission is never blocked
        // by an undeliverable receiver — the message is still transmitted and `_executeSingleMessage`
        // decides its fate on-chain. The previous Stellar view omitted the `receiver.code.length == 0`
        // clause and instead fell through to `get_ccvs_for_message_internal`, which fail-fasts with
        // `ReceiverDoesNotExist` — so the view reverted, the DON could never gather CCVs, `execute`
        // was never called, and the test timed out waiting for an execution event.
        //
        // `receiver.code.length == 0` ⇔ Soroban `executable()` returning a non-`Wasm` variant (or
        // `None` for a contract that was never deployed). Soroban has no cheap `supportsInterface`
        // probe; a Wasm contract that does not implement `ccip_receive` is caught at delivery (its
        // invocation traps ⇒ `Failure`) rather than pre-classified as token-only here.
        //
        // INTENTIONAL VIEW↔EXECUTE DIVERGENCE for a non-Wasm receiver: this view returns lane
        // defaults (gatherable) so the DON transmits, while the on-chain `execute` path
        // (`verify_ccv_quorum` → `get_ccvs_for_message_internal`) keeps the require-V2 fail-fast and
        // rejects with `ReceiverDoesNotExist`/`ReceiverNotWasmContract` (recorded as `Failure`,
        // retryable). The divergence is benign: `execute` rejects at the pre-quorum existence check
        // (`get_ccvs_for_message_internal`, before `ensure_quorum_present`), so the CCVs the DON
        // gathered from the defaults never affect the outcome. This is the C-1 require-V2 policy,
        // preserved on-chain; the view simply mirrors EVM in not blocking transmission.
        let no_payload = message.data.is_empty() && message.ccip_receive_gas_limit == 0;
        let receiver_not_wasm = match Self::ccip_receiver_contract_address(&env, &message.receiver)
        {
            Ok(addr) => !matches!(addr.executable(), Some(Executable::Wasm(_))),
            // Malformed receiver (not 32 bytes): EVM `getCCVsForMessage` reverts `InvalidEVMAddress`.
            Err(e) => return Err(e),
        };
        let is_token_only = no_payload || receiver_not_wasm;

        if is_token_only {
            // Token-only quorum (EVM `_getCCVsForMessage` token-only arm): lane-mandated required,
            // lane defaults optional (≥1 when present). Receiver consultation does not apply.
            let required = source_config.lane_mandated_ccvs.clone();
            let optional = source_config.default_ccvs.clone();
            let threshold = if optional.len() > 0 { 1 } else { 0 };
            return Ok((required, optional, threshold));
        }

        // Non-token-only with a Wasm receiver: same resolution `verify_ccv_quorum` enforces
        // (C-1 receiver consult + merge), so off-chain gathering cannot drift from on-chain
        // enforcement on the only path where the receiver is actually consulted.
        let (required, optional, threshold, _allowed_finality) =
            Self::get_ccvs_for_message_internal(&env, &message, &source_config, &static_config)?;
        Ok((required, optional, threshold))
    }

    /// Resolve the required/optional CCVs and allowed-finality for a **non-token-only**
    /// message by consulting the receiver, mirroring EVM `OffRamp._getCCVsFromReceiver` +
    /// the merge in `_getCCVsForMessage`.
    ///
    /// **Require-V2, fail-fast policy.** A non-contract receiver is rejected *before* any
    /// consultation (the Wasm-existence check below) — Stellar, unlike EVM, does not treat a
    /// non-contract receiver as token-only, and already rejects non-Wasm receivers at delivery
    /// (`execute_single_message`). For a Wasm receiver, `get_ccvs_and_finality_config` is invoked
    /// via `try_invoke_contract`; a receiver that *returns* a `CCIPError` has it propagated
    /// unchanged, and **any other consult failure** (missing symbol / trap / non-convertible
    /// result) fails the message with `ReceiverError` (recorded as `Failure`, retryable) — it does
    /// **not** fall back to defaults. EVM splits these two cases (non-V2 ⇒ defaults via the
    /// `supportsInterface` staticcall that swallows reverts; V2-trap ⇒ revert propagates ⇒
    /// `Failure`), but Soroban's `InvokeError` cannot distinguish "symbol absent" from "trapped"
    /// (both are `Abort`) and there is no symbol-presence probe short of invoking, so the two are
    /// collapsed to the fail branch. Conformant V2 receivers with empty config still receive lane
    /// defaults via the success-path sentinel in `merge_receiver_ccvs`.
    ///
    /// **Purity strategy** (Soroban has no `staticcall`; `try_invoke_contract` is a writable call,
    /// and `execute` wraps inner errors as `Failure` + outer `Ok`, so a callee's writes can persist
    /// on a later-rejected message):
    /// (a) the fail-fast Wasm pre-check below means non-contracts are never invoked;
    /// (b) this runs inside `verify_ccv_quorum`, which precedes token release in
    /// `execute_single_message`, so a rejected message moves no tokens — the only possible
    /// persistent side effect is the receiver's own storage;
    /// (c) Soroban rolls back a *trapping* invocation's writes, and under this policy a trap also
    /// fails the message, so a trapping receiver leaves no persisted writes;
    /// (d) residual, accepted: a malicious *conformant-V2* receiver that successfully returns config
    /// yet writes storage, where the message is then rejected, persists those writes. This cannot
    /// be prevented without a Soroban read-only-call primitive; documented as a known chain-specific
    /// divergence. Trust boundary: the receiver is the sender-chosen destination, already fully
    /// empowered at delivery (`ccip_receive`), so the marginal exposure is "view-writes on a doomed
    /// message" only.
    ///
    /// Returns `(required, optional, optional_threshold, allowed_finality)`.
    fn get_ccvs_for_message_internal(
        env: &Env,
        message: &CcipMessageV1,
        source_config: &SourceChainConfig,
        static_config: &StaticConfig,
    ) -> Result<(Vec<Address>, Vec<Address>, u32, u32), CCIPError> {
        let receiver = Self::ccip_receiver_contract_address(env, &message.receiver)?;

        // Fail fast: a non-token-only message must be deliverable to a Wasm contract that can
        // implement `ccip_receive` (and, for CCV consultation, `get_ccvs_and_finality_config`).
        // Reject before the pool cross-contract call and before invoking the receiver.
        match receiver.executable() {
            Some(Executable::Wasm(_)) => {}
            None => return Err(CCIPError::ReceiverDoesNotExist),
            Some(Executable::Account) | Some(Executable::StellarAsset) => {
                return Err(CCIPError::ReceiverNotWasmContract);
            }
        }

        let pool_required = Self::get_inbound_pool_required_ccvs(
            env,
            message.source_chain_selector,
            message.finality,
            &message.token_transfer,
            static_config,
            &source_config.default_ccvs,
        )?;

        // `get_ccvs_and_finality_config(source_chain_selector, sender)`: the second arg is the
        // message sender on the source chain (EVM `bytes sender`, forwarded by `OffRamp._getCCVsFromReceiver`).
        // Receivers may key required/optional CCVs or allowed finality off the sender, so forward the real
        // `message.sender` — not an empty `Bytes`, which would silently bypass sender-dependent policies.
        let mut args = soroban_sdk::Vec::new(env);
        args.push_back(message.source_chain_selector.into_val(env));
        args.push_back(message.sender.clone().into_val(env));

        // Every non-success arm below returns early, so these are assigned exactly once on the
        // only fall-through path (the `Ok(Ok(Ok(config)))` arm) — no pre-init / dead store.
        let required: Vec<Address>;
        let optional: Vec<Address>;
        let optional_threshold: u32;
        let allowed_finality: u32;

        match env.try_invoke_contract::<Result<CcvsAndFinalityConfig, CCIPError>, InvokeError>(
            &receiver,
            &Symbol::new(env, "get_ccvs_and_finality_config"),
            args,
        ) {
            Ok(Ok(Ok(config))) => {
                allowed_finality = config.allowed_finality_config;
                let (req, opt, thr) =
                    Self::merge_receiver_ccvs(env, &config, &pool_required, source_config)?;
                required = req;
                optional = opt;
                optional_threshold = thr;
            }
            // Receiver returned a typed CCIPError ⇒ propagate (do not silently default).
            Ok(Ok(Err(e))) => return Err(e),
            // Wasm receiver that is not a V2 CCIP receiver (missing `get_ccvs_and_finality_config`
            // symbol) or that trapped / returned a non-convertible value. EVM splits "non-V2 ⇒
            // defaults" from "V2-trap ⇒ Failure", but Soroban's `InvokeError` cannot tell them
            // apart (both surface as `Abort`) and there is no symbol-presence probe short of
            // invoking. Per the require-V2 policy we fail the message (`Failure`, retryable) —
            // mirroring EVM's no-catch trap behavior — rather than silently defaulting (which would
            // also drop `pool_required`, see the purity/pool-merge rationale in the fn doc).
            _ => return Err(CCIPError::ReceiverError),
        }

        Ok((required, optional, optional_threshold, allowed_finality))
    }

    /// Reject within-list duplicate CCVs (EVM `CCVConfigValidation._assertNoDuplicates`). Returns
    /// `InvalidConfig` on the first duplicate. Cross-list (required↔optional) overlap is intentionally
    /// NOT rejected here — EVM handles that by removing an optional that is also required and
    /// decrementing the threshold (see the optional-minus-required loop below).
    fn assert_no_duplicates(list: &Vec<Address>) -> Result<(), CCIPError> {
        for i in 0..list.len() {
            if let Some(a) = list.get(i) {
                for j in (i + 1)..list.len() {
                    if let Some(b) = list.get(j) {
                        if a == b {
                            return Err(CCIPError::InvalidConfig);
                        }
                    }
                }
            }
        }
        Ok(())
    }

    /// Append each address from `src` to `dst` unless already present (dedup via `is_in_list`).
    fn dedup_append(dst: &mut Vec<Address>, src: &Vec<Address>) {
        for i in 0..src.len() {
            if let Some(a) = src.get(i) {
                if !Self::is_in_list(&a, dst) {
                    dst.push_back(a);
                }
            }
        }
    }

    /// Pure merge of a receiver-reported `CcvsAndFinalityConfig` with the pool-required and
    /// lane CCV lists (EVM `_getCCVsFromReceiver` validation + the `_getCCVsForMessage` merge).
    /// Factored out of `get_ccvs_for_message` so the merge/threshold/defaults logic is unit-
    /// testable without a deployed receiver contract.
    ///
    /// Returns `(required, optional, optional_threshold)`.
    fn merge_receiver_ccvs(
        env: &Env,
        config: &CcvsAndFinalityConfig,
        pool_required: &Vec<Address>,
        source_config: &SourceChainConfig,
    ) -> Result<(Vec<Address>, Vec<Address>, u32), CCIPError> {
        // EVM `_getCCVsFromReceiver` calls `CCVConfigValidation._assertNoDuplicates` on both the
        // required and optional lists before the threshold check. Without this, a duplicate optional
        // (e.g. optional=[A,A], threshold=2) lets `ensure_quorum_present` count one attestation twice,
        // accepting a 2-of-2 policy with a single CCV. Reuse `InvalidConfig` (no dedicated code yet;
        // the EVM `DuplicateCCVNotAllowed` slot, 119, stays reserved for a future FIX-GROUP-D pass).
        Self::assert_no_duplicates(&config.required_ccvs)?;
        Self::assert_no_duplicates(&config.optional_ccvs)?;

        // EVM `_getCCVsFromReceiver`: optionalThreshold must be ≤ optionalCCVs.length.
        if config.optional_threshold > config.optional_ccvs.len() {
            return Err(CCIPError::InvalidOptionalThreshold);
        }

        // required = receiver.required + pool-required + lane-mandated, deduped.
        let mut required: Vec<Address> = Vec::new(env);
        Self::dedup_append(&mut required, &config.required_ccvs);
        Self::dedup_append(&mut required, pool_required);
        Self::dedup_append(&mut required, &source_config.lane_mandated_ccvs);

        // Stellar "include defaults" sentinel (EVM uses an `address(0)` marker): an empty
        // receiver-required list with threshold 0 ⇒ fold in the lane default CCVs.
        if config.required_ccvs.is_empty() && config.optional_threshold == 0 {
            Self::dedup_append(&mut required, &source_config.default_ccvs);
        }

        // optional = receiver.optional minus any entry already in required; for each removed
        // optional, decrement the threshold (≥0) — EVM :589-614.
        let mut optional: Vec<Address> = Vec::new(env);
        let mut optional_threshold = config.optional_threshold;
        for i in 0..config.optional_ccvs.len() {
            if let Some(opt) = config.optional_ccvs.get(i) {
                if Self::is_in_list(&opt, &required) {
                    optional_threshold = optional_threshold.saturating_sub(1);
                } else {
                    optional.push_back(opt);
                }
            }
        }

        Ok((required, optional, optional_threshold))
    }

    /// Pure quorum-presence check: every `required` CCV must be in `ccvs` (`RequiredCCVMissing`),
    /// and at least `optional_threshold` of `optional` must be present (`OptionalCCVQuorumNotReached`).
    /// Factored out of `verify_ccv_quorum` so the quorum logic is unit-testable without a live
    /// verifier. Does not invoke any verifier (the `verify_message` loop runs separately, after).
    fn ensure_quorum_present(
        required: &Vec<Address>,
        optional: &Vec<Address>,
        optional_threshold: u32,
        ccvs: &Vec<Address>,
    ) -> Result<(), CCIPError> {
        for i in 0..required.len() {
            if let Some(req) = required.get(i) {
                if !Self::is_in_list(&req, ccvs) {
                    return Err(CCIPError::RequiredCCVMissing);
                }
            }
        }

        let mut optional_present: u32 = 0;
        for i in 0..optional.len() {
            if let Some(opt) = optional.get(i) {
                if Self::is_in_list(&opt, ccvs) {
                    optional_present += 1;
                }
            }
        }
        if optional_present < optional_threshold {
            return Err(CCIPError::OptionalCCVQuorumNotReached);
        }

        Ok(())
    }

    /// Verify that the CCV quorum is met for a message (EVM `OffRamp._getCCVsForMessage` +
    /// `_ensureCCVQuorumIsReached`).
    ///
    /// **Token-only** messages (`data` empty AND `ccip_receive_gas_limit == 0`) take the original
    /// path unchanged: pool-required present, all CCVs verified, all lane-mandated verified, and a
    /// "≥1 default CCV" floor when no lane-mandated CCVs exist. (H-10 — dropping that floor for
    /// token-only — and M-12 — ignoring extra CCVs — are deferred; token-only behavior is
    /// intentionally left as-is in this fix.)
    ///
    /// **Non-token-only** messages consult the receiver (`get_ccvs_for_message`, C-1) for
    /// required/optional/threshold + allowed-finality, enforce the receiver's finality config
    /// (H-7), require every required CCV present (`RequiredCCVMissing`), require ≥`optional_threshold`
    /// optional CCVs present (`OptionalCCVQuorumNotReached`), then verify every attested CCV. A
    /// non-contract receiver, or a Wasm receiver whose consult fails (missing symbol / trap / typed
    /// error), is rejected up front with `ReceiverDoesNotExist` / `ReceiverNotWasmContract` /
    /// `ReceiverError` (recorded as `Failure`, retryable) — there is **no** defaults fallback (see
    /// `get_ccvs_for_message` for the require-V2 policy and purity strategy).
    fn verify_ccv_quorum(
        env: &Env,
        source_chain_selector: u64,
        message_id: &BytesN<32>,
        message: &CcipMessageV1,
        ccvs: &Vec<Address>,
        verifier_results: &Vec<Bytes>,
        source_config: &SourceChainConfig,
        static_config: &StaticConfig,
    ) -> Result<(), CCIPError> {
        let is_token_only = message.data.is_empty() && message.ccip_receive_gas_limit == 0;

        if is_token_only {
            // === Token-only path (UNCHANGED — H-10/M-12 deferred) ===
            if ccvs.is_empty() {
                return Err(CCIPError::CCVQuorumNotMet);
            }

            let pool_required = Self::get_inbound_pool_required_ccvs(
                env,
                source_chain_selector,
                message.finality,
                &message.token_transfer,
                static_config,
                &source_config.default_ccvs,
            )?;
            for i in 0..pool_required.len() {
                if let Some(req) = pool_required.get(i) {
                    if !Self::is_in_list(&req, ccvs) {
                        return Err(CCIPError::RequiredCCVMissing);
                    }
                }
            }

            // Track which mandated CCVs have been verified
            let mut mandated_verified = 0u32;
            let mut default_verified = 0u32;

            for i in 0..ccvs.len() {
                let ccv = ccvs.get(i).ok_or(CCIPError::CCVLengthMismatch)?;
                let result = verifier_results
                    .get(i)
                    .ok_or(CCIPError::CCVLengthMismatch)?;

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

            return Ok(());
        }

        // === Non-token-only path: consult receiver (C-1) + receiver finality (H-7) ===
        let (required, optional, optional_threshold, allowed_finality) =
            Self::get_ccvs_for_message_internal(env, message, source_config, static_config)?;

        // H-7: enforce the receiver's allowed-finality config. When the receiver is not V2 /
        // not a contract, `get_ccvs_for_message` returned `WAIT_FOR_FINALITY_FLAG` (always allowed).
        finality_codec::ensure_requested_finality_allowed(message.finality, allowed_finality)?;

        // Every required CCV present (`RequiredCCVMissing`); ≥`optional_threshold` optional
        // present (`OptionalCCVQuorumNotReached`).
        Self::ensure_quorum_present(&required, &optional, optional_threshold, ccvs)?;

        // Verify every attested CCV. (M-12 — verifying only required + counted-optional and
        // ignoring extras — is deferred; an extra CCV that fails `verify_message` still rejects.)
        for i in 0..ccvs.len() {
            let ccv = ccvs.get(i).ok_or(CCIPError::CCVLengthMismatch)?;
            let result = verifier_results
                .get(i)
                .ok_or(CCIPError::CCVLengthMismatch)?;

            let vvr = VersionedVerifierResolverClient::new(env, &ccv);
            let verifier_address = vvr.get_inbound_implementation(&result);

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
    ///
    /// `message_receiver` is the message-level receiver (a 32-byte Stellar contract
    /// id hash on this destination chain). When the transfer's `token_receiver` is
    /// empty, tokens are released/minted to `message_receiver` — EVM parity
    /// (INV-TR-3): EVM defaults an empty `tokenReceiver` to `message.receiver`
    /// outbound (`OnRamp.sol:311`); this defends inbound for sources that don't
    /// default. `message_receiver` is in the exact 32-byte format
    /// `address_from_token_bytes` resolves, so the fallback yields the same
    /// `Address` as `ccip_receiver_contract_address`.
    fn release_or_mint_single_token(
        env: &Env,
        token_transfer_bytes: &Bytes,
        original_sender: &Bytes,
        message_receiver: &Bytes,
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

        // EVM parity (INV-TR-3): empty tokenReceiver ⇒ message receiver.
        let receiver_bytes = if token_transfer.token_receiver.len() != 0 {
            &token_transfer.token_receiver
        } else {
            message_receiver
        };
        let receiver_address = Self::address_from_token_bytes(env, receiver_bytes)?;

        let release_result = pool_client.release_or_mint(
            &env.current_contract_address(),
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
        // INV-MSG-8: any dropped prefix must be all-zero. Stellar destination addresses are the
        // raw 32-byte contract hash (offset 0 ⇒ no prefix ⇒ trivially satisfied); a longer input is
        // only acceptable when the leading bytes are zero padding, matching EVM's ABI-padded
        // address check (`if (word >> (addressBytesLength*8) != 0) revert InvalidDestChainAddress`,
        // OnRamp.sol:483). A non-zero discriminant/prefix is rejected rather than silently dropped.
        for i in 0..offset {
            if bytes.get(i).ok_or(CCIPError::InvalidReceiverLength)? != 0 {
                return Err(CCIPError::InvalidReceiverAddress);
            }
        }
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
