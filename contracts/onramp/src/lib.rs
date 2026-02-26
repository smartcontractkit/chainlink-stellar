#![no_std]

mod events;
pub mod types;

use common_interfaces::{
    committee_verifier::FeeResponse,
    fee_quoter::{FeeQuoterClient, GasQuoteResult},
    versioned_verifier_resolver::VersionedVerifierResolverClient,
};
use soroban_sdk::{
    contract, contractimpl, symbol_short, xdr::FromXdr, Address, Bytes, BytesN, Env, IntoVal, Map,
    Symbol, Vec,
};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::{initializable::Initializable, ReentrancyGuard};
use common_helpers::{curse_checkable::CurseCheckable, validation::Validatable};
use common_message::{GenericExtraArgsV3, MessageIdCompute, StellarToAnyMessage, ToBytes};
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

        // TODO: Get CCVs for pool (if token transfer)
        // TODO: Merge CCV lists

        // TODO: add the defualt ccv from dest config if no user-specified CCVs

        // Query each CCV for fees
        let ccv_fees_usd_cents = extra_args
            .ccvs
            .iter()
            .zip(extra_args.ccv_args.iter())
            .try_fold(0u128, |acc, (ccv, ccv_args)| {
                let ccv_fee_response = Self::get_ccv_fee_internal(
                    &env,
                    &ccv,
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
        // Verify the original sender is authorized to send the message
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

        // Get destination chain config
        let mut dest_config = Self::get_dest_chain_config_internal(&env, dest_chain_selector)?;
        let dynamic_config = Self::get_dynamic_config_internal(&env)?;

        // Verify caller is the router
        dest_config.router.require_auth();

        // Parse extra args; use default when empty (common for simple messages)
        let extra_args = if message.extra_args.len() == 0 {
            // TODO: is a completely empty extra args value a valid case?
            GenericExtraArgsV3::new(&env, dest_config.default_executor.clone())
        } else {
            GenericExtraArgsV3::from_xdr(&env, &message.extra_args.clone())
                .map_err(|_| CCIPError::InvalidExtraArgsData)?
        };

        // Get message fee (incl. transfer fees and network fees) with the fee token price.
        let fee_quoter = FeeQuoterClient::new(&env, &dynamic_config.fee_quoter);
        let _message_fee = fee_quoter.get_message_fee(&dest_chain_selector, &message);

        // Get pool CCVs if token transfer
        // Merge CCV lists

        // Generate message ID (keccak256(messageBytes))
        let message_id = message.compute_message_id(&env);

        // Invoke verifiers to get verification blobs and generate receipts
        let (verifier_blobs, mut receipts) = Self::get_ccv_blobs_and_receipts_internal(
            &env,
            dest_chain_selector,
            &message_id,
            &original_sender,
            &message,
            &extra_args,
            fee_token_amount,
        )?;

        // Router's receipt to represent the network's fee
        receipts.push_back(Receipt {
            issuer: dest_config.router.clone(),
            dest_gas_limit: 0,
            dest_bytes_overhead: 0,
            fee_token_amount: dest_config.token_network_fee_usd_cents as i128,
            extra_args: Bytes::new(&env),
        });

        // TODO: add executor fee to receipts
        // receipts.push_back(Receipt {
        //     issuer: extra_args.executor.clone(),
        //     dest_gas_limit: dest_config
        //         .base_execution_gas_cost
        //         .saturating_add(extra_args.gas_limit),
        //     dest_bytes_overhead: 0,
        //     fee_token_amount,
        //     extra_args: extra_args.executor_args.clone(),
        // });

        // Increment message number (sequence number for this destination)
        dest_config.message_number += 1;
        let sequence_number = dest_config.message_number;
        Self::set_dest_chain_config(&env, dest_chain_selector, &dest_config);

        // TODO: sum all fees and validate
        // TODO: Distribute fees
        // TODO: Lock or burn tokens

        // Emit CCIPMessageSent event
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
            encoded_message: message.to_bytes(&env),
            receipts,
            verifier_blobs,
        }
        .publish(&env);

        // Exit reentrancy guard
        ReentrancyGuard::exit(&env);

        // TODO: Placeholder to use fee_token_amount
        let _ = fee_token_amount;

        // TODO: do we need to keep track of message IDs in storage for idempotency?

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

        let static_config: StaticConfig = env
            .storage()
            .instance()
            .get(&STATIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)?;

        // TODO: Call TokenAdminRegistry.getPool(sourceToken)
        // For now, return placeholder
        let _ = static_config.token_admin_registry;
        let _ = source_token;

        Err(CCIPError::UnsupportedToken)
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

        // TODO: Implement fee token withdrawal
        // For each token:
        // 1. Get contract's balance
        // 2. Transfer to fee_aggregator
        let _ = fee_tokens;
        let _ = dynamic_config.fee_aggregator;

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

    fn get_ccv_blobs_and_receipts_internal(
        env: &Env,
        dest_chain_selector: u64,
        message_id: &BytesN<32>,
        original_sender: &Address,
        message: &StellarToAnyMessage,
        extra_args: &GenericExtraArgsV3,
        fee_token_amount: i128,
    ) -> Result<(Vec<Bytes>, Vec<Receipt>), CCIPError> {
        let mut receipts = Vec::new(env);
        let mut verification_blobs = Vec::new(env);
        let message_bytes = message.to_bytes(env);

        for (ccv, ccv_args) in extra_args.ccvs.iter().zip(extra_args.ccv_args.iter()) {
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
