#![no_std]

mod events;
pub mod types;

use common_interfaces::{
    committee_verifier::FeeResponse,
    fee_quoter::FeeQuoterClient,
    token_admin_registry::TokenAdminRegistryClient,
    token_pool::{LockOrBurnIn, TokenPoolClient},
    versioned_verifier_resolver::VersionedVerifierResolverClient,
};
use soroban_sdk::{
    contract, contractimpl, symbol_short, token,
    xdr::{FromXdr, ToXdr},
    Address, Bytes, BytesN, Env, IntoVal, Map, Symbol, Vec,
};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::{initializable::Initializable, ReentrancyGuard};
use common_helpers::{curse_checkable::CurseCheckable, validation::Validatable};
use common_message::{
    CcipMessageV1, CcipTokenTransferV1, GenericExtraArgsV3, MessageIdCompute, StellarToAnyMessage,
    ToBytes, MESSAGE_V1_VERSION,
};
use events::{CCIPMessageSentEvent, ConfigSetEvent, DestChainConfigSetEvent};
use types::{DestChainConfig, DestChainConfigArgs, DynamicConfig, Receipt, StaticConfig};

// ============================================================
// Storage Keys
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const STATIC_CONFIG: Symbol = symbol_short!("STATIC");
const DYNAMIC_CONFIG: Symbol = symbol_short!("DYNAMIC");
const DEST_CHAINS: Symbol = symbol_short!("DESTCHNS");
const RMN_PROXY: Symbol = symbol_short!("RMN_PROXY");

// ============================================================
// Contract
// ============================================================

#[contract]
pub struct OnRampContract;

#[contractimpl]
impl Initializable for OnRampContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for OnRampContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl(contracttrait)]
impl CurseCheckable for OnRampContract {
    const RMN_PROXY: Symbol = RMN_PROXY;
}

#[contractimpl]
impl OnRampContract {
    // ========================================
    // Initialization
    // ========================================

    /// Initialize the OnRamp contract with static and dynamic configuration.
    ///
    /// # Arguments
    /// * `owner` - The owner address (typically MCMS, can be the deployer initially)
    /// * `static_config` - Immutable configuration
    /// * `dynamic_config` - Mutable configuration
    ///
    /// # Errors
    /// * `AlreadyInitialized` - If contract is already initialized
    /// * `InvalidConfig` - If configuration is invalid
    pub fn initialize(
        env: Env,
        owner: Address,
        static_config: StaticConfig,
        dynamic_config: DynamicConfig,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;

        // Store owner
        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as Initializable>::init(&env)?;
        <Self as CurseCheckable>::init(&env, &static_config.rmn_proxy)?;

        // Validate static config
        if static_config.chain_selector == 0 || static_config.max_usd_cents_per_message == 0 {
            return Err(CCIPError::InvalidConfig);
        }

        // Validate dynamic config (fee_quoter cannot be zero address equivalent check)
        // Note: In Soroban, we check for valid address by ensuring it's set

        // Store static config (immutable after init)
        env.storage().instance().set(&STATIC_CONFIG, &static_config);

        // Store dynamic config
        env.storage()
            .instance()
            .set(&DYNAMIC_CONFIG, &dynamic_config);

        // Initialize empty destination chains map
        let dest_chains: Map<u64, DestChainConfig> = Map::new(&env);
        env.storage().persistent().set(&DEST_CHAINS, &dest_chains);

        // Emit config set event
        ConfigSetEvent {
            static_config,
            dynamic_config,
        }
        .publish(&env);

        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "OnRamp 1.0.0")
    }

    // ========================================
    // Core Messaging Functions
    // ========================================

    /// Get the fee for sending a message to a destination chain.
    ///
    /// This function calculates the total fee including:
    /// - Verifier fees from all CCVs
    /// - Token pool fees (if applicable)
    /// - Executor fees
    /// - Network protocol fee
    ///
    /// # Arguments
    /// * `dest_chain_selector` - The destination chain identifier
    /// * `message` - The message to be sent
    ///
    /// # Returns
    /// The total fee amount in the fee token's smallest denomination
    ///
    /// # Errors
    /// * `NotInitialized` - If contract is not initialized
    /// * `DestinationChainNotSupported` - If destination chain is not configured
    /// * `CanOnlySendOneTokenPerMessage` - If more than one token is specified
    pub fn get_fee(
        env: Env,
        dest_chain_selector: u64,
        message: StellarToAnyMessage,
    ) -> Result<i128, CCIPError> {
        Self::require_initialized(&env)?;
        <Self as CurseCheckable>::require_not_cursed(&env)?;

        message.validate()?;

        let message_bytes = message.to_bytes(&env);
        let dest_config = Self::get_dest_chain_config_internal(&env, dest_chain_selector)?;
        let dynamic_config = Self::get_dynamic_config_internal(&env)?;

        // Parse extra args with defaults
        let extra_args = if message.extra_args.len() == 0 {
            GenericExtraArgsV3::new(&env, dest_config.default_executor.clone())
        } else {
            GenericExtraArgsV3::from_xdr(&env, &message.extra_args.clone())
                .map_err(|_| CCIPError::InvalidExtraArgsData)?
        };

        // Get message fee (incl. transfer fees and network fees) with the fee token price.
        let fee_quoter = FeeQuoterClient::new(&env, &dynamic_config.fee_quoter);
        let message_fee = fee_quoter.get_message_fee(&dest_chain_selector, &message);

        let merged_ccvs = Self::merge_ccv_lists(
            &env,
            &extra_args.ccvs,
            &dest_config.lane_mandated_ccvs,
            &dest_config.default_ccvs,
        );

        // Query each CCV (via VVR resolution) for fees
        let ccv_fees_usd_cents = merged_ccvs
            .iter()
            .zip(extra_args.ccv_args.iter())
            .try_fold(0u128, |acc, (ccv, ccv_args)| {
                let vvr = VersionedVerifierResolverClient::new(&env, &ccv);
                let verifier_address =
                    vvr.get_outbound_implementation(&dest_chain_selector, &ccv_args);

                let ccv_fee_response = Self::get_ccv_fee_internal(
                    &env,
                    &verifier_address,
                    dest_chain_selector,
                    &message_bytes,
                    &ccv_args,
                    &extra_args,
                )?;

                Ok(acc
                    .checked_add(ccv_fee_response.fee as u128)
                    .ok_or(CCIPError::InvalidFeeCalculation)?)
            })?;

        let ccv_fees_in_fee_token = ccv_fees_usd_cents
            .checked_mul(10_u128.pow(16))
            .ok_or(CCIPError::InvalidFeeCalculation)?
            .checked_div(message_fee.fee_token_price)
            .ok_or(CCIPError::InvalidFeeCalculation)? as i128;

        // TODO: Query executor for fees
        // TODO: Query pool for fees (if token transfer)

        let total_fee = message_fee
            .fee_token_amount
            .checked_add(ccv_fees_in_fee_token)
            .ok_or(CCIPError::InvalidFeeCalculation)?;

        Ok(total_fee)
    }

    /// Forward a message from the Router to be sent cross-chain.
    ///
    /// This is the main entry point for sending CCIP messages. It:
    /// 1. Validates the message and caller
    /// 2. Parses extra args and applies defaults
    /// 3. Merges CCV lists (user, lane-mandated, pool-required)
    /// 4. Calculates and distributes fees
    /// 5. Locks or burns tokens (if applicable)
    /// 6. Encodes the message and computes message ID
    /// 7. Calls each verifier
    /// 8. Emits CCIPMessageSent event
    ///
    /// # Arguments
    /// * `dest_chain_selector` - Destination chain identifier
    /// * `message` - The message to send
    /// * `fee_token_amount` - Amount of fee token provided by router
    /// * `original_sender` - The original initiator of the CCIP request
    ///
    /// # Returns
    /// The unique message ID (32-byte hash)
    ///
    /// # Errors
    /// Various errors for validation failures
    pub fn forward_from_router(
        env: Env,
        dest_chain_selector: u64,
        message: StellarToAnyMessage,
        fee_token_amount: i128,
        original_sender: Address,
    ) -> Result<BytesN<32>, CCIPError> {
        // TODO(NONEVM-3946): Re-enable sender auth once the Router-to-OnRamp
        // invocation tree correctly propagates sub-invocation authorization.
        // Without this, any caller can forge the original_sender field.
        // original_sender.require_auth_for_args(Vec::from([
        //     dest_chain_selector.into_val(&env),
        //     message.to_bytes(&env).into_val(&env),
        //     fee_token_amount.into_val(&env),
        //     original_sender.into_val(&env),
        // ]))?;

        <Self as Initializable>::require_initialized(&env)?;
        <Self as CurseCheckable>::require_not_cursed(&env)?;
        message.validate()?;

        // Enter reentrancy guard (uses temporary storage)
        ReentrancyGuard::enter(&env)?;

        // Get configs
        let mut dest_config = Self::get_dest_chain_config_internal(&env, dest_chain_selector)?;
        let dynamic_config = Self::get_dynamic_config_internal(&env)?;
        let static_config = Self::get_static_config_internal(&env)?;

        // Verify caller is the router
        dest_config.router.require_auth();

        // Parse extra args; use default when empty (common for simple messages)
        let extra_args = if message.extra_args.len() == 0 {
            GenericExtraArgsV3::new(&env, dest_config.default_executor.clone())
        } else {
            GenericExtraArgsV3::from_xdr(&env, &message.extra_args.clone())
                .map_err(|_| CCIPError::InvalidExtraArgsData)?
        };

        // Get message fee (incl. transfer fees and network fees) with the fee token price.
        let fee_quoter = FeeQuoterClient::new(&env, &dynamic_config.fee_quoter);
        let message_fee = fee_quoter.get_message_fee(&dest_chain_selector, &message);

        let merged_ccvs = Self::merge_ccv_lists(
            &env,
            &extra_args.ccvs,
            &dest_config.lane_mandated_ccvs,
            &dest_config.default_ccvs,
        );

        // Lock or burn tokens via the pool (if token transfer)
        let token_transfer_bytes = if !message.token_amounts.is_empty() {
            let token_amount = message.token_amounts.get(0).unwrap();
            let pool_address =
                Self::get_pool_by_source_token_internal(&env, &static_config, &token_amount.token)?;
            let pool_client = TokenPoolClient::new(&env, &pool_address);

            // TODO: On Stellar as the source chain, `block_confirmations` will
            // always be 0 (WAIT_FOR_FINALITY) since Stellar has deterministic ~5s
            // finality and no fast confirmation rules. The pool's FTF outbound
            // branch is unreachable in practice. Consider asserting this invariant
            // or hardcoding 0 instead of threading the extra_args value.
            let lock_result = pool_client.lock_or_burn(
                &LockOrBurnIn {
                    receiver: message.receiver.clone(),
                    remote_chain_selector: dest_chain_selector,
                    original_sender: original_sender.clone(),
                    amount: token_amount.amount,
                    local_token: token_amount.token.clone(),
                },
                &extra_args.block_confirmations,
            );

            let token_transfer = CcipTokenTransferV1 {
                version: MESSAGE_V1_VERSION,
                amount: Self::i128_to_bytes32(&env, token_amount.amount),
                source_pool_address: pool_address.to_xdr(&env),
                source_token_address: token_amount.token.clone().to_xdr(&env),
                dest_token_address: lock_result.dest_token_address,
                token_receiver: extra_args.token_receiver.clone(),
                extra_data: lock_result.dest_pool_data,
            };
            token_transfer.to_bytes(&env)
        } else {
            Bytes::new(&env)
        };

        // Compute sequence number before building the canonical message
        dest_config.message_number += 1;
        let sequence_number = dest_config.message_number;

        // Build canonical MessageV1 for message ID computation and event encoding
        let ccip_msg = CcipMessageV1 {
            source_chain_selector: static_config.chain_selector,
            dest_chain_selector,
            sequence_number,
            execution_gas_limit: extra_args.gas_limit,
            ccip_receive_gas_limit: 0,
            finality: extra_args.block_confirmations,
            ccv_and_executor_hash: CcipMessageV1::compute_ccv_and_executor_hash(
                &env,
                &extra_args.ccvs,
                &extra_args.executor,
            ),
            onramp_address: env.current_contract_address().to_xdr(&env),
            offramp_address: dest_config.off_ramp.clone().to_xdr(&env),
            sender: original_sender.clone().to_xdr(&env),
            receiver: message.receiver.clone(),
            dest_blob: Bytes::new(&env),
            token_transfer: token_transfer_bytes,
            data: message.data.clone(),
        };

        let message_id = ccip_msg.compute_message_id(&env);

        // TODO: check if message ID already exists in storage for idempotency

        // Receipt ordering must match the offchain expectation: [CCV_0, ..., CCV_N, Executor, NetworkFee]
        // where Executor is at index length-2 and NetworkFee is at length-1.

        // Invoke verifiers to get verification blobs and generate receipts
        let (verifier_blobs, mut receipts) = Self::get_ccv_blobs_and_receipts_internal(
            &env,
            dest_chain_selector,
            &message_id,
            &original_sender,
            &message,
            &extra_args,
            &merged_ccvs,
            fee_token_amount,
        )?;

        // Executor receipt (always before the network fee receipt)
        receipts.push_back(Receipt {
            issuer: extra_args.executor.clone(),
            dest_gas_limit: dest_config
                .base_execution_gas_cost
                .saturating_add(extra_args.gas_limit),
            dest_bytes_overhead: 0,
            fee_token_amount: 0, // TODO: compute executor fee
            extra_args: extra_args.executor_args.clone(),
        });

        // Network fee receipt (always last)
        receipts.push_back(Receipt {
            issuer: dest_config.router.clone(),
            dest_gas_limit: 0,
            dest_bytes_overhead: 0,
            fee_token_amount: dest_config.token_network_fee_usd_cents as i128,
            extra_args: Bytes::new(&env),
        });

        // Persist updated sequence number
        Self::set_dest_chain_config(&env, dest_chain_selector, &dest_config);

        // Sum all receipt fees and validate against provided fee_token_amount.
        // CCV receipt fees are in USD cents; convert to fee token units using message_fee price.
        let mut total_ccv_usd_cents: u128 = 0;
        let ccv_receipt_count = merged_ccvs.len();
        for i in 0..ccv_receipt_count {
            if let Some(r) = receipts.get(i) {
                total_ccv_usd_cents = total_ccv_usd_cents
                    .checked_add(r.fee_token_amount as u128)
                    .ok_or(CCIPError::InvalidFeeCalculation)?;
            }
        }
        let ccv_fees_in_fee_token = total_ccv_usd_cents
            .checked_mul(10_u128.pow(16))
            .ok_or(CCIPError::InvalidFeeCalculation)?
            .checked_div(message_fee.fee_token_price)
            .ok_or(CCIPError::InvalidFeeCalculation)? as i128;

        let total_fee = message_fee
            .fee_token_amount
            .checked_add(ccv_fees_in_fee_token)
            .ok_or(CCIPError::InvalidFeeCalculation)?;

        if fee_token_amount < total_fee {
            return Err(CCIPError::InsufficientFeeTokenAmount);
        }

        // Distribute accumulated fee tokens to the fee aggregator.
        // CCV and executor fees stay in the OnRamp balance for later
        // withdrawal via `withdraw_fee_tokens`; the network fee portion is
        // transferred to fee_aggregator immediately so protocol revenue is
        // not delayed.
        if fee_token_amount > 0 {
            let fee_token_client = token::Client::new(&env, &message.fee_token);
            let onramp_address = env.current_contract_address();
            let network_fee = dest_config.token_network_fee_usd_cents as i128;
            if network_fee > 0 {
                let network_fee_tokens = (network_fee as u128)
                    .checked_mul(10_u128.pow(16))
                    .ok_or(CCIPError::InvalidFeeCalculation)?
                    .checked_div(message_fee.fee_token_price)
                    .ok_or(CCIPError::InvalidFeeCalculation)?
                    as i128;
                if network_fee_tokens > 0 {
                    fee_token_client.transfer(
                        &onramp_address,
                        &dynamic_config.fee_aggregator,
                        &network_fee_tokens,
                    );
                }
            }
        }

        // Emit CCIPMessageSent event with canonical MessageV1 encoding
        CCIPMessageSentEvent {
            dest_chain_selector,
            sequence_number,
            sender: original_sender,
            message_id: message_id.clone(),
            fee_token: message.fee_token.clone(),
            token_amount_before_fees: message
                .token_amounts
                .get(0)
                .map(|token_amount| token_amount.amount)
                .unwrap_or(0),
            encoded_message: ccip_msg.to_bytes(&env),
            receipts,
            verifier_blobs,
        }
        .publish(&env);

        // Exit reentrancy guard
        ReentrancyGuard::exit(&env);

        // TODO: keep track of message IDs in storage for idempotency?

        Ok(message_id)
    }

    /// Get the expected next message number for a destination chain.
    ///
    /// # Arguments
    /// * `dest_chain_selector` - The destination chain identifier
    ///
    /// # Returns
    /// The next message number that will be used
    pub fn get_expected_next_message_number(
        env: Env,
        dest_chain_selector: u64,
    ) -> Result<u64, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let dest_config = Self::get_dest_chain_config_internal(&env, dest_chain_selector)?;
        Ok(dest_config.message_number + 1)
    }

    // ========================================
    // Token Pool Functions
    // ========================================

    /// Get the pool address for a specific source token.
    ///
    /// # Arguments
    /// * `source_token` - The token address on this chain
    ///
    /// # Returns
    /// The pool address that handles this token
    pub fn get_pool_by_source_token(env: Env, source_token: Address) -> Result<Address, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let static_config = Self::get_static_config_internal(&env)?;
        let registry = TokenAdminRegistryClient::new(&env, &static_config.token_admin_registry);
        let pool = registry
            .get_pool(&source_token)
            .ok_or(CCIPError::UnsupportedToken)?;

        Ok(pool)
    }

    // ========================================
    // Configuration Functions
    // ========================================

    /// Get the static configuration.
    pub fn get_static_config(env: Env) -> Result<StaticConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Self::get_static_config_internal(&env)
    }

    /// Get the dynamic configuration.
    pub fn get_dynamic_config(env: Env) -> Result<DynamicConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Self::get_dynamic_config_internal(&env)
    }

    /// Set the dynamic configuration. Only callable by owner.
    ///
    /// # Arguments
    /// * `dynamic_config` - New dynamic configuration
    ///
    /// # Errors
    /// * `Unauthorized` - If caller is not owner
    /// * `InvalidConfig` - If configuration is invalid
    pub fn set_dynamic_config(env: Env, dynamic_config: DynamicConfig) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        env.storage()
            .instance()
            .set(&DYNAMIC_CONFIG, &dynamic_config);

        // Emit event
        let static_config: StaticConfig = env
            .storage()
            .instance()
            .get(&STATIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)?;

        ConfigSetEvent {
            static_config,
            dynamic_config,
        }
        .publish(&env);

        Ok(())
    }

    /// Get configuration for a specific destination chain.
    ///
    /// # Arguments
    /// * `dest_chain_selector` - The destination chain identifier
    pub fn get_dest_chain_config(
        env: Env,
        dest_chain_selector: u64,
    ) -> Result<DestChainConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Self::get_dest_chain_config_internal(&env, dest_chain_selector)
    }

    /// Get all destination chain configurations.
    ///
    /// # Returns
    /// Tuple of (chain selectors, configurations)
    pub fn get_all_dest_chain_configs(
        env: Env,
    ) -> Result<(Vec<u64>, Vec<DestChainConfig>), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let dest_chains: Map<u64, DestChainConfig> = env
            .storage()
            .persistent()
            .get(&DEST_CHAINS)
            .unwrap_or(Map::new(&env));

        let mut selectors: Vec<u64> = Vec::new(&env);
        let mut configs: Vec<DestChainConfig> = Vec::new(&env);

        for (selector, config) in dest_chains.iter() {
            selectors.push_back(selector);
            configs.push_back(config);
        }

        Ok((selectors, configs))
    }

    /// Apply destination chain configuration updates. Only callable by owner.
    ///
    /// # Arguments
    /// * `dest_chain_config_args` - Array of destination chain configurations to apply
    pub fn apply_dest_chain_config_updates(
        env: Env,
        dest_chain_config_args: Vec<DestChainConfigArgs>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let static_config: StaticConfig = env
            .storage()
            .instance()
            .get(&STATIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)?;

        let mut dest_chains: Map<u64, DestChainConfig> = env
            .storage()
            .persistent()
            .get(&DEST_CHAINS)
            .unwrap_or(Map::new(&env));

        for args in dest_chain_config_args.iter() {
            // Basic validation for non-zero configs and offramp address
            args.validate()?;

            // Validate that the message is not to self
            if args.dest_chain_selector == static_config.chain_selector {
                return Err(CCIPError::InvalidConfig);
            }

            // Get existing config or create new one
            let existing_message_number = dest_chains
                .get(args.dest_chain_selector)
                .map(|c| c.message_number)
                .unwrap_or(0);

            let new_config = DestChainConfig {
                router: args.router.clone(),
                message_number: existing_message_number,
                address_bytes_length: args.address_bytes_length,
                token_receiver_allowed: args.token_receiver_allowed,
                message_network_fee_usd_cents: args.message_network_fee_usd_cents,
                token_network_fee_usd_cents: args.token_network_fee_usd_cents,
                base_execution_gas_cost: args.base_execution_gas_cost,
                default_executor: args.default_executor.clone(),
                lane_mandated_ccvs: args.lane_mandated_ccvs.clone(),
                default_ccvs: args.default_ccvs.clone(),
                off_ramp: args.off_ramp.clone(),
            };

            dest_chains.set(args.dest_chain_selector, new_config.clone());

            // Emit event
            DestChainConfigSetEvent {
                dest_chain_selector: args.dest_chain_selector,
                message_number: existing_message_number,
                config: new_config,
            }
            .publish(&env);
        }

        env.storage().persistent().set(&DEST_CHAINS, &dest_chains);

        Ok(())
    }

    // ========================================
    // Fee Functions
    // ========================================

    /// Withdraw accumulated fee tokens to the fee aggregator.
    /// This function is permissionless as it only sends to the trusted fee aggregator.
    ///
    /// # Arguments
    /// * `fee_tokens` - List of fee token addresses to withdraw
    pub fn withdraw_fee_tokens(env: Env, fee_tokens: Vec<Address>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let dynamic_config: DynamicConfig = env
            .storage()
            .instance()
            .get(&DYNAMIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)?;

        let onramp_address = env.current_contract_address();
        for i in 0..fee_tokens.len() {
            if let Some(fee_token) = fee_tokens.get(i) {
                let token_client = token::Client::new(&env, &fee_token);
                let balance = token_client.balance(&onramp_address);
                if balance > 0 {
                    token_client.transfer(
                        &onramp_address,
                        &dynamic_config.fee_aggregator,
                        &balance,
                    );
                }
            }
        }

        Ok(())
    }

    // ========================================
    // Internal Helper Functions
    // ========================================

    fn get_dest_chain_config_internal(
        env: &Env,
        dest_chain_selector: u64,
    ) -> Result<DestChainConfig, CCIPError> {
        let dest_chains: Map<u64, DestChainConfig> =
            env.storage()
                .persistent()
                .get(&DEST_CHAINS)
                .ok_or(CCIPError::DestinationChainNotSupported)?;

        dest_chains
            .get(dest_chain_selector)
            .ok_or(CCIPError::DestinationChainNotSupported)
    }

    fn get_dynamic_config_internal(env: &Env) -> Result<DynamicConfig, CCIPError> {
        env.storage()
            .instance()
            .get(&DYNAMIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)
    }

    fn get_static_config_internal(env: &Env) -> Result<StaticConfig, CCIPError> {
        env.storage()
            .instance()
            .get(&STATIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)
    }

    fn set_dest_chain_config(env: &Env, dest_chain_selector: u64, config: &DestChainConfig) {
        let mut dest_chains: Map<u64, DestChainConfig> = env
            .storage()
            .persistent()
            .get(&DEST_CHAINS)
            .unwrap_or(Map::new(env));

        dest_chains.set(dest_chain_selector, config.clone());
        env.storage().persistent().set(&DEST_CHAINS, &dest_chains);
    }

    fn get_pool_by_source_token_internal(
        env: &Env,
        static_config: &StaticConfig,
        source_token: &Address,
    ) -> Result<Address, CCIPError> {
        let registry = TokenAdminRegistryClient::new(env, &static_config.token_admin_registry);
        registry
            .get_pool(source_token)
            .ok_or(CCIPError::UnsupportedToken)
    }

    fn i128_to_bytes32(env: &Env, value: i128) -> BytesN<32> {
        let be_bytes = value.to_be_bytes();
        let mut padded = [0u8; 32];
        padded[16..].copy_from_slice(&be_bytes);
        BytesN::from_array(env, &padded)
    }

    /// Merge CCV lists: user-specified + lane-mandated (deduped), falling back to
    /// default_ccvs when no user CCVs are provided.
    fn merge_ccv_lists(
        _env: &Env,
        user_ccvs: &Vec<Address>,
        lane_mandated_ccvs: &Vec<Address>,
        default_ccvs: &Vec<Address>,
    ) -> Vec<Address> {
        if user_ccvs.is_empty() && lane_mandated_ccvs.is_empty() {
            return default_ccvs.clone();
        }

        let mut merged = user_ccvs.clone();

        for i in 0..lane_mandated_ccvs.len() {
            if let Some(ccv) = lane_mandated_ccvs.get(i) {
                let mut already_present = false;
                for j in 0..merged.len() {
                    if merged.get(j) == Some(ccv.clone()) {
                        already_present = true;
                        break;
                    }
                }
                if !already_present {
                    merged.push_back(ccv);
                }
            }
        }

        if merged.is_empty() {
            return default_ccvs.clone();
        }

        merged
    }

    fn get_ccv_blobs_and_receipts_internal(
        env: &Env,
        dest_chain_selector: u64,
        message_id: &BytesN<32>,
        original_sender: &Address,
        message: &StellarToAnyMessage,
        extra_args: &GenericExtraArgsV3,
        merged_ccvs: &Vec<Address>,
        fee_token_amount: i128,
    ) -> Result<(Vec<Bytes>, Vec<Receipt>), CCIPError> {
        let mut receipts = Vec::new(env);
        let mut verification_blobs = Vec::new(env);
        let message_bytes = message.to_bytes(env);

        for (ccv, ccv_args) in merged_ccvs.iter().zip(extra_args.ccv_args.iter()) {
            let vvr = VersionedVerifierResolverClient::new(env, &ccv);
            let verifier_address = vvr.get_outbound_implementation(&dest_chain_selector, &ccv_args);

            let ccv_fee_response = Self::get_ccv_fee_internal(
                env,
                &verifier_address,
                dest_chain_selector,
                &message_bytes,
                &ccv_args,
                extra_args,
            )?;

            receipts.push_back(Receipt {
                issuer: ccv,
                dest_gas_limit: ccv_fee_response.dest_gas_limit,
                dest_bytes_overhead: ccv_fee_response.dest_bytes_overhead,
                // fee is in USD cents
                fee_token_amount: ccv_fee_response.fee as i128,
                extra_args: ccv_args.clone(),
            });

            let mut verifier_args = Vec::new(&env);
            verifier_args.push_back(dest_chain_selector.into_val(env));
            verifier_args.push_back(original_sender.into_val(env));
            verifier_args.push_back(message_id.into_val(env));
            verifier_args.push_back(message.fee_token.into_val(env));
            verifier_args.push_back(fee_token_amount.into_val(env));
            verifier_args.push_back(ccv_args.into_val(env));

            let verification_blob = env.invoke_contract::<Result<Bytes, CCIPError>>(
                &verifier_address,
                &Symbol::new(&env, "forward_to_verifier"),
                verifier_args,
            )?;

            verification_blobs.push_back(verification_blob);
        }

        Ok((verification_blobs, receipts))
    }

    fn get_ccv_fee_internal(
        env: &Env,
        ccv_address: &Address,
        dest_chain_selector: u64,
        message_bytes: &Bytes,
        ccv_args: &Bytes,
        extra_args: &GenericExtraArgsV3,
    ) -> Result<FeeResponse, CCIPError> {
        let mut fee_args = Vec::new(env);
        fee_args.push_back(dest_chain_selector.into_val(env));
        fee_args.push_back(message_bytes.clone().into_val(env));
        fee_args.push_back(ccv_args.clone().into_val(env));
        fee_args.push_back(extra_args.block_confirmations.into_val(env));

        env.invoke_contract::<Result<FeeResponse, CCIPError>>(
            ccv_address,
            &Symbol::new(env, "get_fee"),
            fee_args,
        )
    }
}

mod test;
