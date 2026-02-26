#![no_std]

mod events;
pub mod types;

use soroban_sdk::{contract, contractimpl, symbol_short, Address, Env, Map, Symbol, Vec};

use common_authorization::{AuthorizedCallers, Ownable};
use common_error::CCIPError;
use common_guard::{initializable::Initializable, ReentrancyGuard};
use common_message::StellarToAnyMessage;
use events::{
    DestChainAddedEvent, DestChainConfigUpdatedEvent, FeeTokenAddedEvent, FeeTokenRemovedEvent,
    TokenFeeConfigDeletedEvent, TokenFeeConfigUpdatedEvent, UsdPerTokenUpdatedEvent,
    UsdPerUnitGasUpdatedEvent,
};
use types::{
    DestChainConfig, DestChainConfigArgs, GasQuoteResult, PriceUpdates, StaticConfig,
    TimestampedPrice, TokenFeeConfigArgs, TokenFeeConfigRemoveArgs, TokenTransferFeeConfig,
    TokenTransferFeeResult,
};

use crate::types::MessageFeeResult;

// ============================================================
// Storage Keys
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const STATIC_CFG: Symbol = symbol_short!("STATIC");
const TOKEN_PRC: Symbol = symbol_short!("TOKENPRC");
const GAS_PRC: Symbol = symbol_short!("GASPRC");
const FEE_TKNS: Symbol = symbol_short!("FEETKNS");
const DEST_CFG: Symbol = symbol_short!("DESTCFG");
const TKN_FEES: Symbol = symbol_short!("TKNFEES");

/// Minimum bytes overhead for token pool return data (matching EVM Pool.CCIP_LOCK_OR_BURN_V1_RET_BYTES).
const CCIP_LOCK_OR_BURN_V1_RET_BYTES: u32 = 32;

// ============================================================
// Contract
// ============================================================

#[contract]
pub struct FeeQuoterContract;

#[contractimpl]
impl Initializable for FeeQuoterContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for FeeQuoterContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl]
impl FeeQuoterContract {
    // ========================================
    // Initialization
    // ========================================

    /// Initialize the FeeQuoter contract.
    ///
    /// # Arguments
    /// * `owner` - The owner address (typically MCMS)
    /// * `static_config` - Static configuration (immutable after init)
    /// * `authorized_callers` - Initial list of authorized price updaters
    ///
    /// # Errors
    /// * `AlreadyInitialized` - If contract is already initialized
    /// * `InvalidStaticConfig` - If static config is invalid
    pub fn initialize(
        env: Env,
        owner: Address,
        static_config: StaticConfig,
        authorized_callers: Vec<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;

        // Validate static config
        if static_config.max_fee_juels_per_msg <= 0 {
            return Err(CCIPError::InvalidStaticConfig);
        }

        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;

        // Initialize authorized callers (common-authorization)
        // TODO: remove this in favor of using the AllowListable trait.
        AuthorizedCallers::init(&env, authorized_callers);

        // Store static config
        env.storage().instance().set(&STATIC_CFG, &static_config);

        // Initialize empty maps
        let token_prices: Map<Address, TimestampedPrice> = Map::new(&env);
        env.storage().persistent().set(&TOKEN_PRC, &token_prices);

        let gas_prices: Map<u64, TimestampedPrice> = Map::new(&env);
        env.storage().persistent().set(&GAS_PRC, &gas_prices);

        let fee_tokens: Vec<Address> = Vec::new(&env);
        env.storage().persistent().set(&FEE_TKNS, &fee_tokens);

        // Store dest configs as a Vec of tuples to avoid complex Map type issues
        let dest_selectors: Vec<u64> = Vec::new(&env);
        let dest_configs: Vec<DestChainConfig> = Vec::new(&env);
        env.storage()
            .persistent()
            .set(&DEST_CFG, &(dest_selectors, dest_configs));

        // Token transfer fees use a nested structure
        // We store as Vec of tuples: (dest_chain_selector, token, config)
        let token_fees: Vec<(u64, Address, TokenTransferFeeConfig)> = Vec::new(&env);
        env.storage().persistent().set(&TKN_FEES, &token_fees);

        // Mark as initialized
        env.storage().instance().set(&INITIALIZED, &true);

        Ok(())
    }

    // ========================================
    // Price Query Functions
    // ========================================

    /// Get the price for a token (may be stale or zero).
    ///
    /// # Arguments
    /// * `token` - Token address
    ///
    /// # Returns
    /// Timestamped price (value may be 0 if not set)
    pub fn get_token_price(env: Env, token: Address) -> Result<TimestampedPrice, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let token_prices: Map<Address, TimestampedPrice> = env
            .storage()
            .persistent()
            .get(&TOKEN_PRC)
            .unwrap_or(Map::new(&env));

        Ok(token_prices.get(token).unwrap_or(TimestampedPrice {
            value: 0,
            timestamp: 0,
        }))
    }

    /// Get prices for multiple tokens.
    ///
    /// # Arguments
    /// * `tokens` - Vector of token addresses
    ///
    /// # Returns
    /// Vector of timestamped prices
    pub fn get_token_prices(
        env: Env,
        tokens: Vec<Address>,
    ) -> Result<Vec<TimestampedPrice>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let token_prices: Map<Address, TimestampedPrice> = env
            .storage()
            .persistent()
            .get(&TOKEN_PRC)
            .unwrap_or(Map::new(&env));

        let mut result: Vec<TimestampedPrice> = Vec::new(&env);

        for token in tokens.iter() {
            let price = token_prices.get(token).unwrap_or(TimestampedPrice {
                value: 0,
                timestamp: 0,
            });
            result.push_back(price);
        }

        Ok(result)
    }

    /// Get the validated price for a token (reverts if not set).
    ///
    /// # Arguments
    /// * `token` - Token address
    ///
    /// # Returns
    /// Price value in USD with 18 decimals
    ///
    /// # Errors
    /// * `TokenNotSupported` - If token price is not set
    pub fn get_validated_token_price(env: Env, token: Address) -> Result<u128, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let token_prices: Map<Address, TimestampedPrice> = env
            .storage()
            .persistent()
            .get(&TOKEN_PRC)
            .unwrap_or(Map::new(&env));

        let price = token_prices
            .get(token)
            .ok_or(CCIPError::TokenNotSupported)?;

        // Price must be set at least once
        if price.timestamp == 0 || price.value == 0 {
            return Err(CCIPError::TokenNotSupported);
        }

        Ok(price.value)
    }

    /// Get the gas price for a destination chain.
    ///
    /// # Arguments
    /// * `dest_chain_selector` - Destination chain selector
    ///
    /// # Returns
    /// Timestamped gas price
    pub fn get_dest_chain_gas_price(
        env: Env,
        dest_chain_selector: u64,
    ) -> Result<TimestampedPrice, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let gas_prices: Map<u64, TimestampedPrice> = env
            .storage()
            .persistent()
            .get(&GAS_PRC)
            .unwrap_or(Map::new(&env));

        Ok(gas_prices
            .get(dest_chain_selector)
            .unwrap_or(TimestampedPrice {
                value: 0,
                timestamp: 0,
            }))
    }

    // ========================================
    // Fee Token Functions
    // ========================================

    /// Get the list of fee tokens.
    pub fn get_fee_tokens(env: Env) -> Result<Vec<Address>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        Ok(env
            .storage()
            .persistent()
            .get(&FEE_TKNS)
            .unwrap_or(Vec::new(&env)))
    }

    /// Remove fee tokens (owner only).
    /// This also clears their prices.
    ///
    /// # Arguments
    /// * `tokens` - Tokens to remove from fee tokens
    pub fn remove_fee_tokens(env: Env, tokens: Vec<Address>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut fee_tokens: Vec<Address> = env
            .storage()
            .persistent()
            .get(&FEE_TKNS)
            .unwrap_or(Vec::new(&env));

        let mut token_prices: Map<Address, TimestampedPrice> = env
            .storage()
            .persistent()
            .get(&TOKEN_PRC)
            .unwrap_or(Map::new(&env));

        for token in tokens.iter() {
            // Remove from fee tokens
            let mut new_fee_tokens: Vec<Address> = Vec::new(&env);
            for ft in fee_tokens.iter() {
                if ft != token {
                    new_fee_tokens.push_back(ft);
                }
            }
            fee_tokens = new_fee_tokens;

            // Remove price
            token_prices.remove(token.clone());

            FeeTokenRemovedEvent {
                fee_token: token.clone(),
            }
            .publish(&env);
        }

        env.storage().persistent().set(&FEE_TKNS, &fee_tokens);
        env.storage().persistent().set(&TOKEN_PRC, &token_prices);

        Ok(())
    }

    // ========================================
    // Price Update Functions
    // ========================================

    /// Update token and gas prices. Only callable by authorized callers.
    /// Protected by reentrancy guard.
    ///
    /// # Arguments
    /// * `price_updates` - Token and gas price updates
    pub fn update_prices(env: Env, price_updates: PriceUpdates) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        AuthorizedCallers::require_authorized(&env)?;

        // Reentrancy protection for price updates
        ReentrancyGuard::enter(&env)?;

        let timestamp = env.ledger().timestamp();

        // Update token prices
        let mut token_prices: Map<Address, TimestampedPrice> = env
            .storage()
            .persistent()
            .get(&TOKEN_PRC)
            .unwrap_or(Map::new(&env));

        let mut fee_tokens: Vec<Address> = env
            .storage()
            .persistent()
            .get(&FEE_TKNS)
            .unwrap_or(Vec::new(&env));

        for update in price_updates.token_price_updates.iter() {
            let price = TimestampedPrice {
                value: update.usd_per_token,
                timestamp,
            };
            token_prices.set(update.token.clone(), price);

            // Add to fee tokens if not already present
            let mut found = false;
            for ft in fee_tokens.iter() {
                if ft == update.token {
                    found = true;
                    break;
                }
            }
            if !found {
                fee_tokens.push_back(update.token.clone());
                FeeTokenAddedEvent {
                    fee_token: update.token.clone(),
                }
                .publish(&env);
            }

            UsdPerTokenUpdatedEvent {
                token: update.token.clone(),
                value: update.usd_per_token,
                timestamp,
            }
            .publish(&env);
        }

        env.storage().persistent().set(&TOKEN_PRC, &token_prices);
        env.storage().persistent().set(&FEE_TKNS, &fee_tokens);

        // Update gas prices
        let mut gas_prices: Map<u64, TimestampedPrice> = env
            .storage()
            .persistent()
            .get(&GAS_PRC)
            .unwrap_or(Map::new(&env));

        for update in price_updates.gas_price_updates.iter() {
            let price = TimestampedPrice {
                value: update.usd_per_unit_gas,
                timestamp,
            };
            gas_prices.set(update.dest_chain_selector, price);

            UsdPerUnitGasUpdatedEvent {
                dest_chain_selector: update.dest_chain_selector,
                value: update.usd_per_unit_gas,
                timestamp,
            }
            .publish(&env);
        }

        env.storage().persistent().set(&GAS_PRC, &gas_prices);

        // Exit reentrancy guard
        ReentrancyGuard::exit(&env);

        Ok(())
    }

    // ========================================
    // Fee Calculation Functions
    // ========================================

    /// Quote gas for execution on a destination chain.
    ///
    /// # Arguments
    /// * `dest_chain_selector` - Destination chain selector
    /// * `non_calldata_gas` - Non-calldata gas to be used
    /// * `calldata_size` - Size of calldata in bytes
    /// * `fee_token` - Fee token address
    ///
    /// # Returns
    /// GasQuoteResult with total gas, cost, and fee token price
    pub fn quote_gas_for_exec(
        env: Env,
        dest_chain_selector: u64,
        non_calldata_gas: u32,
        calldata_size: u32,
        fee_token: Address,
    ) -> Result<GasQuoteResult, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let dest_config = Self::get_dest_chain_config_internal(&env, dest_chain_selector)?;

        if !dest_config.is_enabled {
            return Err(CCIPError::DestinationChainNotEnabled);
        }

        // Calculate total gas
        let total_gas = non_calldata_gas + calldata_size * dest_config.dest_gas_per_payload_byte;

        if total_gas > dest_config.max_per_msg_gas_limit {
            return Err(CCIPError::MessageGasLimitTooHigh);
        }

        if calldata_size > dest_config.max_data_bytes {
            return Err(CCIPError::MessageTooLarge);
        }

        // Get gas price
        let gas_prices: Map<u64, TimestampedPrice> = env
            .storage()
            .persistent()
            .get(&GAS_PRC)
            .unwrap_or(Map::new(&env));

        let gas_price = gas_prices
            .get(dest_chain_selector)
            .ok_or(CCIPError::NoGasPriceAvailable)?;

        if gas_price.timestamp == 0 {
            return Err(CCIPError::NoGasPriceAvailable);
        }

        // Gas cost in USD cents (gas_price is in 1e18 USD, we want cents which is 1e2)
        // So we divide by 1e16 to go from 1e18 to 1e2
        // Round up to ensure we never reach zero fee
        let gas_cost_usd_cents =
            ((total_gas as u128) * gas_price.value + (10_u128.pow(16) - 1)) / 10_u128.pow(16);

        // Get fee token price
        let fee_token_price = Self::get_validated_token_price(env.clone(), fee_token.clone())?;

        // Apply premium/discount based on fee token
        let static_config: StaticConfig = env
            .storage()
            .instance()
            .get(&STATIC_CFG)
            .ok_or(CCIPError::NotInitialized)?;

        let premium_multiplier = if fee_token == static_config.link_token {
            dest_config.link_premium_percent
        } else {
            100 // No discount for non-LINK tokens
        };

        Ok(GasQuoteResult {
            total_gas,
            gas_cost_usd_cents,
            fee_token_price,
            premium_multiplier,
        })
    }

    /// Get token transfer fee components.
    ///
    /// # Arguments
    /// * `dest_chain_selector` - Destination chain selector
    /// * `token` - Token address
    ///
    /// # Returns
    /// TokenTransferFeeResult with fee, gas overhead, and bytes overhead
    pub fn get_token_transfer_fee(
        env: Env,
        dest_chain_selector: u64,
        token: Address,
    ) -> Result<TokenTransferFeeResult, CCIPError> {
        Self::require_initialized(&env)?;

        // Try to get token-specific config
        let token_fees: Vec<(u64, Address, TokenTransferFeeConfig)> = env
            .storage()
            .persistent()
            .get(&TKN_FEES)
            .unwrap_or(Vec::new(&env));

        for (selector, tkn, config) in token_fees.iter() {
            if selector == dest_chain_selector && tkn == token && config.is_enabled {
                return Ok(TokenTransferFeeResult {
                    fee_usd_cents: config.fee_usd_cents,
                    dest_gas_overhead: config.dest_gas_overhead,
                    dest_bytes_overhead: config.dest_bytes_overhead,
                });
            }
        }

        // Fall back to destination chain defaults
        let dest_config = Self::get_dest_chain_config_internal(&env, dest_chain_selector)?;

        Ok(TokenTransferFeeResult {
            fee_usd_cents: dest_config.default_token_fee_usd,
            dest_gas_overhead: dest_config.default_token_dest_gas,
            dest_bytes_overhead: CCIP_LOCK_OR_BURN_V1_RET_BYTES,
        })
    }

    /// Get the fee for sending a CCIP message.
    ///
    /// This validates the message and calculates the fee based on gas costs,
    /// token transfer fees, and network fees.
    ///
    /// # Arguments
    /// * `dest_chain_selector` - Destination chain selector
    /// * `message` - The CCIP message to price
    ///
    /// # Returns
    /// Fee amount in the message's fee token denomination
    ///
    /// # Errors
    /// * `UnsupportedNumberOfTokens` - If more than 1 token transfer
    /// * `DestinationChainNotEnabled` - If destination chain is not enabled
    pub fn get_message_fee(
        env: Env,
        dest_chain_selector: u64,
        message: StellarToAnyMessage,
    ) -> Result<MessageFeeResult, CCIPError> {
        Self::require_initialized(&env)?;

        // Validate the message using common-message validation
        message.validate()?;

        // Only 1 token per message in CCIP v1.7
        if message.token_amounts.len() > 1 {
            return Err(CCIPError::UnsupportedNumberOfTokens);
        }

        let dest_config = Self::get_dest_chain_config_internal(&env, dest_chain_selector)?;

        if !dest_config.is_enabled {
            return Err(CCIPError::DestinationChainNotEnabled);
        }

        // Calculate gas cost for the message payload
        let calldata_size = message.data.len() as u32;
        let gas_quote = Self::quote_gas_for_exec(
            env.clone(),
            dest_chain_selector,
            dest_config.dest_gas_overhead,
            calldata_size,
            message.fee_token.clone(),
        )?;

        // Start with gas cost in USD cents
        let mut total_usd_cents: u128 = gas_quote.gas_cost_usd_cents;

        // Add network fee
        total_usd_cents += dest_config.network_fee_usd_cents as u128;

        // Add token transfer fees if tokens are being sent
        for token_amount in message.token_amounts.iter() {
            let token_fee = Self::get_token_transfer_fee(
                env.clone(),
                dest_chain_selector,
                token_amount.token.clone(),
            )?;
            total_usd_cents += token_fee.fee_usd_cents as u128;
        }

        // Apply premium multiplier (percentage)
        total_usd_cents = total_usd_cents * gas_quote.premium_multiplier as u128 / 100;

        // Convert from USD cents to fee token amount
        // total_usd_cents is in 1e2 (cents), fee_token_price is in 1e18 USD
        // fee_amount = total_usd_cents * 1e16 / fee_token_price (1e18 / 1e2 = 1e16)
        let fee_amount = if gas_quote.fee_token_price > 0 {
            (total_usd_cents * 10_u128.pow(16) / gas_quote.fee_token_price) as i128
        } else {
            return Err(CCIPError::FeeTokenNotSupported);
        };

        // Check against max fee
        let static_config: StaticConfig = env
            .storage()
            .instance()
            .get(&STATIC_CFG)
            .ok_or(CCIPError::NotInitialized)?;

        if fee_amount > static_config.max_fee_juels_per_msg {
            return Err(CCIPError::MessageFeeTooHigh);
        }

        Ok(MessageFeeResult {
            fee_usd_cents: total_usd_cents,
            fee_token_amount: fee_amount,
            fee_token_price: gas_quote.fee_token_price,
        })
    }

    /// Convert a token amount to another token.
    ///
    /// # Arguments
    /// * `from_token` - Source token address
    /// * `from_token_amount` - Amount in source token
    /// * `to_token` - Target token address
    ///
    /// # Returns
    /// Amount in target token
    pub fn convert_token_amount(
        env: Env,
        from_token: Address,
        from_token_amount: i128,
        to_token: Address,
    ) -> Result<i128, CCIPError> {
        Self::require_initialized(&env)?;

        let from_price = Self::get_validated_token_price(env.clone(), from_token)?;
        let to_price = Self::get_validated_token_price(env, to_token)?;

        // (fromTokenAmount * fromTokenPrice) / toTokenPrice
        let amount = from_token_amount as u128;

        if from_price >= to_price {
            let price_ratio = from_price / to_price;
            let remainder = from_price % to_price;
            let base = amount.saturating_mul(price_ratio);
            let extra = (amount / to_price).saturating_mul(remainder);
            Ok((base.saturating_add(extra)) as i128)
        } else {
            let quotient = amount / to_price;
            let remainder = amount % to_price;
            let base = quotient.saturating_mul(from_price);
            let extra = (remainder.saturating_mul(from_price)) / to_price;
            Ok((base.saturating_add(extra)) as i128)
        }
    }

    // ========================================
    // Destination Chain Config Functions
    // ========================================

    /// Get configuration for a destination chain.
    pub fn get_dest_chain_config(
        env: Env,
        dest_chain_selector: u64,
    ) -> Result<DestChainConfig, CCIPError> {
        Self::require_initialized(&env)?;
        Self::get_dest_chain_config_internal(&env, dest_chain_selector)
    }

    /// Get all destination chain configurations.
    pub fn get_all_dest_configs(env: Env) -> Result<(Vec<u64>, Vec<DestChainConfig>), CCIPError> {
        Self::require_initialized(&env)?;

        let (selectors, configs): (Vec<u64>, Vec<DestChainConfig>) = env
            .storage()
            .persistent()
            .get(&DEST_CFG)
            .unwrap_or((Vec::new(&env), Vec::new(&env)));

        Ok((selectors, configs))
    }

    /// Apply destination chain config updates (owner only).
    pub fn apply_dest_chain_configs(
        env: Env,
        config_args: Vec<DestChainConfigArgs>,
    ) -> Result<(), CCIPError> {
        Self::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let (mut selectors, mut configs): (Vec<u64>, Vec<DestChainConfig>) = env
            .storage()
            .persistent()
            .get(&DEST_CFG)
            .unwrap_or((Vec::new(&env), Vec::new(&env)));

        for args in config_args.iter() {
            // Validate config
            if args.dest_chain_selector == 0
                || args.config.default_tx_gas_limit == 0
                || args.config.default_tx_gas_limit > args.config.max_per_msg_gas_limit
            {
                return Err(CCIPError::InvalidDestChainConfig);
            }

            // Check if this is a new chain or an update
            let mut found_idx: Option<u32> = None;
            for i in 0..selectors.len() {
                if selectors.get(i).unwrap() == args.dest_chain_selector {
                    found_idx = Some(i);
                    break;
                }
            }

            match found_idx {
                Some(idx) => {
                    // Update existing
                    configs.set(idx, args.config.clone());
                    DestChainConfigUpdatedEvent {
                        dest_chain_selector: args.dest_chain_selector,
                        is_enabled: args.config.is_enabled,
                        max_data_bytes: args.config.max_data_bytes,
                    }
                    .publish(&env);
                }
                None => {
                    // Add new
                    selectors.push_back(args.dest_chain_selector);
                    configs.push_back(args.config.clone());
                    DestChainAddedEvent {
                        dest_chain_selector: args.dest_chain_selector,
                        is_enabled: args.config.is_enabled,
                        max_data_bytes: args.config.max_data_bytes,
                    }
                    .publish(&env);
                }
            }
        }

        env.storage()
            .persistent()
            .set(&DEST_CFG, &(selectors, configs));

        Ok(())
    }

    // ========================================
    // Token Transfer Fee Config Functions
    // ========================================

    /// Get token transfer fee configuration.
    pub fn get_token_fee_config(
        env: Env,
        dest_chain_selector: u64,
        token: Address,
    ) -> Result<TokenTransferFeeConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let token_fees: Vec<(u64, Address, TokenTransferFeeConfig)> = env
            .storage()
            .persistent()
            .get(&TKN_FEES)
            .unwrap_or(Vec::new(&env));

        for (selector, tkn, config) in token_fees.iter() {
            if selector == dest_chain_selector && tkn == token {
                return Ok(config);
            }
        }

        // Return default (not enabled)
        Ok(TokenTransferFeeConfig {
            fee_usd_cents: 0,
            dest_gas_overhead: 0,
            dest_bytes_overhead: 0,
            is_enabled: false,
        })
    }

    /// Apply token transfer fee config updates (owner only).
    pub fn apply_token_fee_configs(
        env: Env,
        config_args: Vec<TokenFeeConfigArgs>,
        remove_args: Vec<TokenFeeConfigRemoveArgs>,
    ) -> Result<(), CCIPError> {
        Self::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut token_fees: Vec<(u64, Address, TokenTransferFeeConfig)> = env
            .storage()
            .persistent()
            .get(&TKN_FEES)
            .unwrap_or(Vec::new(&env));

        // Apply updates
        for args in config_args.iter() {
            // Validate bytes overhead
            if args.config.dest_bytes_overhead < CCIP_LOCK_OR_BURN_V1_RET_BYTES {
                return Err(CCIPError::InvalidDestBytesOverhead);
            }

            // Find existing or add new
            let mut found_idx: Option<u32> = None;
            for i in 0..token_fees.len() {
                let (selector, tkn, _) = token_fees.get(i).unwrap();
                if selector == args.dest_chain_selector && tkn == args.token {
                    found_idx = Some(i);
                    break;
                }
            }

            match found_idx {
                Some(idx) => {
                    token_fees.set(
                        idx,
                        (
                            args.dest_chain_selector,
                            args.token.clone(),
                            args.config.clone(),
                        ),
                    );
                }
                None => {
                    token_fees.push_back((
                        args.dest_chain_selector,
                        args.token.clone(),
                        args.config.clone(),
                    ));
                }
            }

            TokenFeeConfigUpdatedEvent {
                dest_chain_selector: args.dest_chain_selector,
                token: args.token.clone(),
                fee_usd_cents: args.config.fee_usd_cents,
                dest_gas_overhead: args.config.dest_gas_overhead,
                dest_bytes_overhead: args.config.dest_bytes_overhead,
            }
            .publish(&env);
        }

        // Apply removals
        for args in remove_args.iter() {
            let mut new_token_fees: Vec<(u64, Address, TokenTransferFeeConfig)> = Vec::new(&env);
            let mut removed = false;

            for (selector, tkn, config) in token_fees.iter() {
                if selector == args.dest_chain_selector && tkn == args.token {
                    removed = true;
                } else {
                    new_token_fees.push_back((selector, tkn, config));
                }
            }

            if removed {
                TokenFeeConfigDeletedEvent {
                    dest_chain_selector: args.dest_chain_selector,
                    token: args.token.clone(),
                }
                .publish(&env);
            }

            token_fees = new_token_fees;
        }

        env.storage().persistent().set(&TKN_FEES, &token_fees);

        Ok(())
    }

    // ========================================
    // Static Config Functions
    // ========================================

    /// Get the static configuration.
    pub fn get_static_config(env: Env) -> Result<StaticConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&STATIC_CFG)
            .ok_or(CCIPError::NotInitialized)
    }

    // ========================================
    // Internal Helper Functions
    // ========================================

    fn get_dest_chain_config_internal(
        env: &Env,
        dest_chain_selector: u64,
    ) -> Result<DestChainConfig, CCIPError> {
        let (selectors, configs): (Vec<u64>, Vec<DestChainConfig>) = env
            .storage()
            .persistent()
            .get(&DEST_CFG)
            .ok_or(CCIPError::DestinationChainNotEnabled)?;

        for i in 0..selectors.len() {
            if selectors.get(i).unwrap() == dest_chain_selector {
                return configs.get(i).ok_or(CCIPError::DestinationChainNotEnabled);
            }
        }

        Err(CCIPError::DestinationChainNotEnabled)
    }
}

mod test;
