#![no_std]
//! A minimal CRE receiver for Local-CRE write tests.
//!
//! The KeystoneForwarder dispatches `on_report(sender, metadata, payload)` to the
//! target receiver. This contract records the last payload and a monotonically
//! increasing report count so a test can assert that a write was actually
//! delivered on-chain (read back via `last_payload` / `report_count`). It
//! intentionally performs no authorization — it is test infrastructure, not a
//! production receiver.
use soroban_sdk::{contract, contractimpl, symbol_short, Address, Bytes, Env, Symbol};

const LAST_PAYLOAD: Symbol = symbol_short!("payload");
const COUNT: Symbol = symbol_short!("count");
const LAST_VALUE: Symbol = symbol_short!("lvalue");

#[contract]
pub struct Receiver;

#[contractimpl]
impl Receiver {
    /// Invoked by the forwarder's route/dispatch. Records the report payload.
    pub fn on_report(env: Env, _sender: Address, _metadata: Bytes, payload: Bytes) {
        env.storage().instance().set(&LAST_PAYLOAD, &payload);
        let count: u32 = env.storage().instance().get(&COUNT).unwrap_or(0);
        env.storage().instance().set(&COUNT, &(count + 1));

        // Store the first 8 bytes of the payload as a little-endian u64 so a
        // write→read roundtrip test can assert a deterministic on-chain value.
        // If the payload is shorter than 8 bytes, the value is zero-padded.
        let mut buf: [u8; 8] = [0u8; 8];
        let len = core::cmp::min(payload.len() as usize, 8);
        let _ = payload.slice(..len as u32).copy_into_slice(&mut buf[..len]);
        let value = u64::from_le_bytes(buf);
        env.storage().instance().set(&LAST_VALUE, &value);
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

    /// Returns the first 8 bytes of the most recent report payload as a
    /// little-endian u64 (0 if no report received or payload < 8 bytes).
    /// Used by the write→read roundtrip test to assert payload integrity.
    pub fn last_value_u64(env: Env) -> u64 {
        env.storage()
            .instance()
            .get::<_, u64>(&LAST_VALUE)
            .unwrap_or(0u64)
    }
}
