#![no_std]
//! A minimal CRE receiver for Local-CRE write tests.
//!
//! The KeystoneForwarder dispatches `on_report(metadata, payload)` to the target
//! receiver. This contract records the last payload and a monotonically increasing
//! report count so a test can assert that a write was actually delivered on-chain
//! (read back via `last_payload` / `report_count`). It intentionally performs no
//! authorization — it is test infrastructure, not a production receiver.
use soroban_sdk::{contract, contractimpl, symbol_short, Bytes, Env, Symbol};

const LAST_PAYLOAD: Symbol = symbol_short!("payload");
const COUNT: Symbol = symbol_short!("count");

#[contract]
pub struct CreReceiver;

#[contractimpl]
impl CreReceiver {
    /// Invoked by the forwarder's route/dispatch. Records the report payload.
    pub fn on_report(env: Env, _metadata: Bytes, payload: Bytes) {
        env.storage().instance().set(&LAST_PAYLOAD, &payload);
        let count: u32 = env.storage().instance().get(&COUNT).unwrap_or(0);
        env.storage().instance().set(&COUNT, &(count + 1));
    }

    /// Returns the most recently received report payload (empty if none).
    pub fn last_payload(env: Env) -> Bytes {
        env.storage()
            .instance()
            .get(&LAST_PAYLOAD)
            .unwrap_or_else(|| Bytes::new(&env))
    }

    /// Returns how many reports have been received.
    pub fn report_count(env: Env) -> u32 {
        env.storage().instance().get(&COUNT).unwrap_or(0)
    }
}
