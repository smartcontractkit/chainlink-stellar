#![no_std]
//! A CRE receiver that always rejects `on_report` by returning `Err`.
//!
//! Used by the Stellar write regression suite to exercise the forwarder's
//! `TransmissionState::Failed` (retryable) dispatch path — distinct from the
//! `InvalidReceiver` (terminal) path exercised by targeting a contract without
//! `on_report`. The forwarder maps `Ok(Err(_))` to `Failed`, allowing retries;
//! a regression that maps it to `InvalidReceiver` would silently stop retries.
use soroban_sdk::{contract, contracterror, contractimpl, Address, Bytes, Env};

#[contracterror]
#[derive(Copy, Clone, Eq, PartialEq, Debug)]
#[repr(u32)]
pub enum ReceiverError {
    Rejected = 1,
}

#[contract]
pub struct RejectingReceiver;

#[contractimpl]
impl RejectingReceiver {
    /// Always returns Err — the forwarder records TransmissionState::Failed.
    pub fn on_report(_env: Env, _sender: Address, _metadata: Bytes, _payload: Bytes) -> Result<(), ReceiverError> {
        Err(ReceiverError::Rejected)
    }
}
