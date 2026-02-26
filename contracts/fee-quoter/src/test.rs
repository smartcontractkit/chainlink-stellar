#![cfg(test)]

use super::*;
use common_message::{StellarToAnyMessage, TokenAmount};
use soroban_sdk::{
    testutils::{Address as _, Ledger},
    Address, Bytes, Env, Vec,
};
use types::{
    DestChainConfig, DestChainConfigArgs, GasPriceUpdate, PriceUpdates, StaticConfig,
    TokenFeeConfigArgs, TokenFeeConfigRemoveArgs, TokenPriceUpdate, TokenTransferFeeConfig,
};

fn setup_env() -> (Env, Address, Address, Address, Address) {
    let env = Env::default();
    env.mock_all_auths();

    // Set ledger timestamp to a non-zero value (required for price validation)
    env.ledger().with_mut(|li| {
        li.timestamp = 1000;
    });

    let owner = Address::generate(&env);
    let link_token = Address::generate(&env);
    let price_updater = Address::generate(&env);
    let contract_id = env.register(FeeQuoterContract, ());

    (env, contract_id, owner, link_token, price_updater)
}

fn create_static_config(link_token: Address) -> StaticConfig {
    StaticConfig {
        max_fee_juels_per_msg: 1_000_000_000_000_000_000, // 1e18
        link_token,
    }
}

fn create_dest_chain_config() -> DestChainConfig {
    DestChainConfig {
        is_enabled: true,
        max_data_bytes: 50000,
        max_per_msg_gas_limit: 4_000_000,
        dest_gas_overhead: 350_000,
        dest_gas_per_payload_byte: 16,
        default_token_fee_usd: 50, // $0.50
        default_token_dest_gas: 50_000,
        default_tx_gas_limit: 200_000,
        network_fee_usd_cents: 100, // $1.00
        link_premium_percent: 90,   // 10% discount
    }
}

#[test]
fn test_initialize() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token.clone());
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater.clone());

    // Initialize
    client.initialize(&owner, &static_config, &authorized_callers);

    // Verify owner
    // assert_eq!(<FeeQuoterContract as Ownable>::get_owner(&env), owner);

    // Verify static config
    let stored_config = client.get_static_config();
    assert_eq!(stored_config.link_token, link_token);
    assert_eq!(
        stored_config.max_fee_juels_per_msg,
        static_config.max_fee_juels_per_msg
    );

    // Verify authorized callers
    // let callers = client.get_authorized_callers();
    // assert_eq!(callers.len(), 1);
    // assert_eq!(callers.get(0).unwrap(), price_updater);
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")]
fn test_initialize_already_initialized() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token);
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater);

    client.initialize(&owner, &static_config, &authorized_callers);
    client.initialize(&owner, &static_config, &authorized_callers); // Should fail
}

#[test]
fn test_update_prices() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token.clone());
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater.clone());

    client.initialize(&owner, &static_config, &authorized_callers);

    // Create price updates
    let token1 = Address::generate(&env);
    let mut token_updates: Vec<TokenPriceUpdate> = Vec::new(&env);
    token_updates.push_back(TokenPriceUpdate {
        token: token1.clone(),
        usd_per_token: 3_000_000_000_000_000_000_000, // $3000 (ETH-like)
    });
    token_updates.push_back(TokenPriceUpdate {
        token: link_token.clone(),
        usd_per_token: 15_000_000_000_000_000_000, // $15 (LINK)
    });

    let mut gas_updates: Vec<GasPriceUpdate> = Vec::new(&env);
    gas_updates.push_back(GasPriceUpdate {
        dest_chain_selector: 1,
        usd_per_unit_gas: 50_000_000_000, // 50 gwei equivalent
    });

    let price_updates = PriceUpdates {
        token_price_updates: token_updates,
        gas_price_updates: gas_updates,
    };

    // Update prices (as authorized caller)
    client.update_prices(&price_updates);

    // Verify token prices
    let token1_price = client.get_token_price(&token1);
    assert_eq!(token1_price.value, 3_000_000_000_000_000_000_000);
    assert!(token1_price.timestamp > 0);

    let link_price = client.get_validated_token_price(&link_token);
    assert_eq!(link_price, 15_000_000_000_000_000_000);

    // Verify gas price
    let gas_price = client.get_dest_chain_gas_price(&1);
    assert_eq!(gas_price.value, 50_000_000_000);

    // Verify fee tokens were added
    let fee_tokens = client.get_fee_tokens();
    assert_eq!(fee_tokens.len(), 2);
}

#[test]
fn test_dest_chain_config() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token);
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater);

    client.initialize(&owner, &static_config, &authorized_callers);

    // Add destination chain config
    let config = create_dest_chain_config();
    let mut config_args: Vec<DestChainConfigArgs> = Vec::new(&env);
    config_args.push_back(DestChainConfigArgs {
        dest_chain_selector: 1,
        config: config.clone(),
    });

    client.apply_dest_chain_configs(&config_args);

    // Verify config
    let stored_config = client.get_dest_chain_config(&1);
    assert_eq!(stored_config.is_enabled, true);
    assert_eq!(stored_config.max_data_bytes, 50000);
    assert_eq!(stored_config.network_fee_usd_cents, 100);

    // Verify get all configs
    let (selectors, configs) = client.get_all_dest_configs();
    assert_eq!(selectors.len(), 1);
    assert_eq!(configs.len(), 1);
}

#[test]
fn test_quote_gas_for_exec() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token.clone());
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater);

    client.initialize(&owner, &static_config, &authorized_callers);

    // Setup destination chain config
    let config = create_dest_chain_config();
    let mut config_args: Vec<DestChainConfigArgs> = Vec::new(&env);
    config_args.push_back(DestChainConfigArgs {
        dest_chain_selector: 1,
        config,
    });
    client.apply_dest_chain_configs(&config_args);

    // Setup prices
    let mut token_updates: Vec<TokenPriceUpdate> = Vec::new(&env);
    token_updates.push_back(TokenPriceUpdate {
        token: link_token.clone(),
        usd_per_token: 15_000_000_000_000_000_000, // $15
    });

    let mut gas_updates: Vec<GasPriceUpdate> = Vec::new(&env);
    gas_updates.push_back(GasPriceUpdate {
        dest_chain_selector: 1,
        usd_per_unit_gas: 1_000_000_000_000, // 1e12 (0.000001 USD per gas)
    });

    client.update_prices(&PriceUpdates {
        token_price_updates: token_updates,
        gas_price_updates: gas_updates,
    });

    // Quote gas
    let result = client.quote_gas_for_exec(&1, &100_000, &1000, &link_token);

    // total_gas = 100_000 + 1000 * 16 = 116_000
    assert_eq!(result.total_gas, 116_000);
    assert!(result.gas_cost_usd_cents > 0);
    assert_eq!(result.fee_token_price, 15_000_000_000_000_000_000);
    assert_eq!(result.premium_multiplier, 90); // LINK discount
}

#[test]
fn test_token_transfer_fee_config() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token);
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater);

    client.initialize(&owner, &static_config, &authorized_callers);

    // Setup destination chain config first
    let config = create_dest_chain_config();
    let mut config_args: Vec<DestChainConfigArgs> = Vec::new(&env);
    config_args.push_back(DestChainConfigArgs {
        dest_chain_selector: 1,
        config,
    });
    client.apply_dest_chain_configs(&config_args);

    // Add token transfer fee config
    let token = Address::generate(&env);
    let fee_config = TokenTransferFeeConfig {
        fee_usd_cents: 100, // $1.00
        dest_gas_overhead: 75_000,
        dest_bytes_overhead: 64,
        is_enabled: true,
    };

    let mut fee_args: Vec<TokenFeeConfigArgs> = Vec::new(&env);
    fee_args.push_back(TokenFeeConfigArgs {
        dest_chain_selector: 1,
        token: token.clone(),
        config: fee_config.clone(),
    });

    let remove_args: Vec<TokenFeeConfigRemoveArgs> = Vec::new(&env);
    client.apply_token_fee_configs(&fee_args, &remove_args);

    // Verify token-specific config
    let result = client.get_token_transfer_fee(&1, &token);
    assert_eq!(result.fee_usd_cents, 100);
    assert_eq!(result.dest_gas_overhead, 75_000);
    assert_eq!(result.dest_bytes_overhead, 64);

    // Verify fallback to defaults for unknown token
    let unknown_token = Address::generate(&env);
    let default_result = client.get_token_transfer_fee(&1, &unknown_token);
    assert_eq!(default_result.fee_usd_cents, 50); // Default from dest chain config
}

#[test]
fn test_remove_token_transfer_fee_config() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token);
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater);

    client.initialize(&owner, &static_config, &authorized_callers);

    // Setup destination chain config
    let config = create_dest_chain_config();
    let mut config_args: Vec<DestChainConfigArgs> = Vec::new(&env);
    config_args.push_back(DestChainConfigArgs {
        dest_chain_selector: 1,
        config,
    });
    client.apply_dest_chain_configs(&config_args);

    // Add token transfer fee config
    let token = Address::generate(&env);
    let fee_config = TokenTransferFeeConfig {
        fee_usd_cents: 100,
        dest_gas_overhead: 75_000,
        dest_bytes_overhead: 64,
        is_enabled: true,
    };

    let mut fee_args: Vec<TokenFeeConfigArgs> = Vec::new(&env);
    fee_args.push_back(TokenFeeConfigArgs {
        dest_chain_selector: 1,
        token: token.clone(),
        config: fee_config,
    });
    client.apply_token_fee_configs(&fee_args, &Vec::new(&env));

    // Remove the config
    let mut remove_args: Vec<TokenFeeConfigRemoveArgs> = Vec::new(&env);
    remove_args.push_back(TokenFeeConfigRemoveArgs {
        dest_chain_selector: 1,
        token: token.clone(),
    });
    client.apply_token_fee_configs(&Vec::new(&env), &remove_args);

    // Verify falls back to defaults
    let result = client.get_token_transfer_fee(&1, &token);
    assert_eq!(result.fee_usd_cents, 50); // Default
}

#[test]
fn test_convert_token_amount() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token.clone());
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater);

    client.initialize(&owner, &static_config, &authorized_callers);

    // Setup token prices
    let eth_token = Address::generate(&env);
    let mut token_updates: Vec<TokenPriceUpdate> = Vec::new(&env);
    token_updates.push_back(TokenPriceUpdate {
        token: eth_token.clone(),
        usd_per_token: 3_000_000_000_000_000_000_000, // $3000
    });
    token_updates.push_back(TokenPriceUpdate {
        token: link_token.clone(),
        usd_per_token: 15_000_000_000_000_000_000, // $15
    });

    client.update_prices(&PriceUpdates {
        token_price_updates: token_updates,
        gas_price_updates: Vec::new(&env),
    });

    // Convert 1 ETH to LINK: 1 * 3000 / 15 = 200 LINK
    let result = client.convert_token_amount(
        &eth_token,
        &1_000_000_000_000_000_000, // 1 ETH (1e18)
        &link_token,
    );

    // Expected: 200e18 LINK
    assert_eq!(result, 200_000_000_000_000_000_000);
}

#[test]
fn test_remove_fee_tokens() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token.clone());
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater);

    client.initialize(&owner, &static_config, &authorized_callers);

    // Add some token prices
    let token1 = Address::generate(&env);
    let mut token_updates: Vec<TokenPriceUpdate> = Vec::new(&env);
    token_updates.push_back(TokenPriceUpdate {
        token: token1.clone(),
        usd_per_token: 100_000_000_000_000_000_000,
    });
    token_updates.push_back(TokenPriceUpdate {
        token: link_token.clone(),
        usd_per_token: 15_000_000_000_000_000_000,
    });

    client.update_prices(&PriceUpdates {
        token_price_updates: token_updates,
        gas_price_updates: Vec::new(&env),
    });

    assert_eq!(client.get_fee_tokens().len(), 2);

    // Remove token1
    let mut tokens_to_remove: Vec<Address> = Vec::new(&env);
    tokens_to_remove.push_back(token1.clone());
    client.remove_fee_tokens(&tokens_to_remove);

    assert_eq!(client.get_fee_tokens().len(), 1);

    // Verify price is also removed
    let price = client.get_token_price(&token1);
    assert_eq!(price.value, 0);
    assert_eq!(price.timestamp, 0);
}

#[test]
#[should_panic(expected = "Error(Contract, #22)")]
fn test_get_validated_token_price_not_set() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token);
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater);

    client.initialize(&owner, &static_config, &authorized_callers);

    let unknown_token = Address::generate(&env);
    client.get_validated_token_price(&unknown_token); // Should fail
}

#[test]
fn test_get_message_fee() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token.clone());
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater);

    client.initialize(&owner, &static_config, &authorized_callers);

    // Setup destination chain config
    let config = create_dest_chain_config();
    let mut config_args: Vec<DestChainConfigArgs> = Vec::new(&env);
    config_args.push_back(DestChainConfigArgs {
        dest_chain_selector: 1,
        config,
    });
    client.apply_dest_chain_configs(&config_args);

    // Setup prices (use a gas price high enough to produce a non-zero fee in token units)
    let mut token_updates: Vec<TokenPriceUpdate> = Vec::new(&env);
    token_updates.push_back(TokenPriceUpdate {
        token: link_token.clone(),
        usd_per_token: 15_000_000_000_000_000_000, // $15
    });

    let mut gas_updates: Vec<GasPriceUpdate> = Vec::new(&env);
    gas_updates.push_back(GasPriceUpdate {
        dest_chain_selector: 1,
        usd_per_unit_gas: 100_000_000_000_000, // 1e14 (higher gas price for meaningful fee)
    });

    client.update_prices(&PriceUpdates {
        token_price_updates: token_updates,
        gas_price_updates: gas_updates,
    });

    // Build a CCIP message
    let message = StellarToAnyMessage {
        receiver: Bytes::from_slice(
            &env,
            &[
                1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20,
            ],
        ),
        data: Bytes::from_slice(&env, &[0u8; 100]), // 100 bytes of data
        token_amounts: Vec::new(&env),
        fee_token: link_token.clone(),
        extra_args: Bytes::new(&env),
    };

    // Get message fee
    let fee = client.get_message_fee(&1, &message);

    // Fee should be positive (gas cost + network fee, with LINK discount applied)
    // Gas: (350000 + 100*16) * 1e14 = 351600 * 1e14 gas cost
    // gas_cost_usd_cents ~= 3516, + network_fee 100 = 3616
    // With 90% LINK discount: 3254 cents
    // In LINK: 3254 * 1e16 / 15e18 = ~2 LINK units
    assert!(fee.fee_token_amount > 0);
}

#[test]
fn test_get_message_fee_with_token_transfer() {
    let (env, contract_id, owner, link_token, price_updater) = setup_env();
    let client = FeeQuoterContractClient::new(&env, &contract_id);

    let static_config = create_static_config(link_token.clone());
    let mut authorized_callers: Vec<Address> = Vec::new(&env);
    authorized_callers.push_back(price_updater);

    client.initialize(&owner, &static_config, &authorized_callers);

    // Setup destination chain config
    let config = create_dest_chain_config();
    let mut config_args: Vec<DestChainConfigArgs> = Vec::new(&env);
    config_args.push_back(DestChainConfigArgs {
        dest_chain_selector: 1,
        config,
    });
    client.apply_dest_chain_configs(&config_args);

    // Setup prices (use higher gas price for meaningful fee calculation)
    let token = Address::generate(&env);
    let mut token_updates: Vec<TokenPriceUpdate> = Vec::new(&env);
    token_updates.push_back(TokenPriceUpdate {
        token: link_token.clone(),
        usd_per_token: 15_000_000_000_000_000_000, // $15
    });
    token_updates.push_back(TokenPriceUpdate {
        token: token.clone(),
        usd_per_token: 1_000_000_000_000_000_000, // $1
    });

    let mut gas_updates: Vec<GasPriceUpdate> = Vec::new(&env);
    gas_updates.push_back(GasPriceUpdate {
        dest_chain_selector: 1,
        usd_per_unit_gas: 100_000_000_000_000, // 1e14
    });

    client.update_prices(&PriceUpdates {
        token_price_updates: token_updates,
        gas_price_updates: gas_updates,
    });

    // Add a custom token fee config with a large enough fee to be visible after
    // integer division (fee token is $15/token = minimum step of 1500 cents)
    let fee_config = TokenTransferFeeConfig {
        fee_usd_cents: 5000, // $50 - large enough to show up after division by $15 token price
        dest_gas_overhead: 75_000,
        dest_bytes_overhead: 64,
        is_enabled: true,
    };
    let mut fee_args: Vec<TokenFeeConfigArgs> = Vec::new(&env);
    fee_args.push_back(TokenFeeConfigArgs {
        dest_chain_selector: 1,
        token: token.clone(),
        config: fee_config,
    });
    client.apply_token_fee_configs(&fee_args, &Vec::new(&env));

    // Build a message without token transfer
    let message_no_token = StellarToAnyMessage {
        receiver: Bytes::from_slice(&env, &[1u8; 20]),
        data: Bytes::from_slice(&env, &[0u8; 100]),
        token_amounts: Vec::new(&env),
        fee_token: link_token.clone(),
        extra_args: Bytes::new(&env),
    };

    // Build a message with token transfer
    let mut token_amounts: Vec<TokenAmount> = Vec::new(&env);
    token_amounts.push_back(TokenAmount {
        token: token.clone(),
        amount: 1_000_000,
    });

    let message_with_token = StellarToAnyMessage {
        receiver: Bytes::from_slice(&env, &[1u8; 20]),
        data: Bytes::from_slice(&env, &[0u8; 100]),
        token_amounts,
        fee_token: link_token.clone(),
        extra_args: Bytes::new(&env),
    };

    let fee_no_token = client.get_message_fee(&1, &message_no_token);
    let fee_with_token = client.get_message_fee(&1, &message_with_token);

    // Fee with token transfer should be higher than without
    // (the $50 token fee pushes total over the $15/token resolution threshold)
    assert!(fee_with_token.fee_token_amount > fee_no_token.fee_token_amount);
}
