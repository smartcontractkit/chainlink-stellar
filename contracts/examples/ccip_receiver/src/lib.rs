#![no_std]

mod events;

/// Minimal Stellar CCIP **application** receiver — analogue of Solidity [`CCIPReceiver`](https://github.com/smartcontractkit/chainlink-ccip/blob/develop/chains/evm/contracts/applications/CCIPReceiver.sol).
///
/// - The CCIP **Router** is the only address allowed to call `ccip_receive` (enforced via
///   [`Address::require_auth_for_args`](soroban_sdk::Address::require_auth_for_args), same role as
///   `onlyRouter` on EVM).
/// - Implement your application logic by reacting to [`AnyToStellarMessage`] (payload, sender bytes,
///   optional `dest_token_amounts` after OffRamp token handling is complete).
use soroban_sdk::{contract, contractimpl, symbol_short, Address, BytesN, Env, IntoVal, Symbol};

use common_error::CCIPError;
use common_message::AnyToStellarMessage;
use events::CcipMessageReceivedEvent;

const INIT: Symbol = symbol_short!("INIT");
const ROUTER: Symbol = symbol_short!("ROUTER");
const LAST_MID: Symbol = symbol_short!("LASTMID");

#[contract]
pub struct ExampleCcipReceiver;

#[contractimpl]
impl ExampleCcipReceiver {
    /// One-time setup: stores the Router address used for `onlyRouter`-style checks.
    pub fn initialize(env: Env, router: Address) -> Result<(), CCIPError> {
        if env.storage().instance().has(&INIT) {
            return Err(CCIPError::AlreadyInitialized);
        }
        env.storage().instance().set(&ROUTER, &router);
        env.storage().instance().set(&INIT, &true);
        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "ExampleCcipReceiver 1.0.0")
    }

    pub fn get_router(env: Env) -> Result<Address, CCIPError> {
        env.storage()
            .instance()
            .get(&ROUTER)
            .ok_or(CCIPError::NotInitialized)
    }

    /// Entry point invoked by the Router after OffRamp verification (EVM `ccipReceive` analogue).
    pub fn ccip_receive(env: Env, message: AnyToStellarMessage) -> Result<(), CCIPError> {
        let router: Address = env
            .storage()
            .instance()
            .get(&ROUTER)
            .ok_or(CCIPError::NotInitialized)?;

        router.require_auth_for_args(soroban_sdk::vec![&env, message.clone().into_val(&env)]);

        CcipMessageReceivedEvent {
            message_id: message.message_id.clone(),
            source_chain_selector: message.source_chain_selector,
            data_len: message.data.len(),
            sender_len: message.sender.len(),
            dest_token_transfers: message.dest_token_amounts.len(),
        }
        .publish(&env);

        // Demo: persist last message id for integration tests / debugging.
        env.storage().instance().set(&LAST_MID, &message.message_id);

        Ok(())
    }

    pub fn last_message_id(env: Env) -> Result<BytesN<32>, CCIPError> {
        env.storage()
            .instance()
            .get(&LAST_MID)
            .ok_or(CCIPError::NotInitialized)
    }
}

mod test;
