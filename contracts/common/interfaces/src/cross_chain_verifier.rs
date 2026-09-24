//! Minimal cross-chain verifier (CCV) surface, mirroring EVM `ICrossChainVerifierV1`.
//!
//! Unlike [`super::committee_verifier::CommitteeVerifierInterface`] (the *full* committee-verifier
//! surface — owner/admin/signature-config plumbing), this trait exposes only the four
//! implementation-agnostic entrypoints every CCV must provide: verify, fee-quote, outbound hook,
//! and storage-location discovery. It is the typed analog of EVM `ICrossChainVerifierV1` — the
//! interface the EVM `OffRamp` casts a resolved verifier to before calling `verifyMessage`.
//!
//! The [`OffRamp`](crate::offramp) resolves a concrete verifier address via
//! [`super::versioned_verifier_resolver::VersionedVerifierResolverClient::get_inbound_implementation`]
//! (version-tag → address) and then calls it through [`CrossChainVerifierClient`]. This retires
//! the prior magic-string `env.invoke_contract(.., &Symbol::new(env, "verify_message"), ..)`
//! dispatch in favor of a compile-time-checked, type-safe client call, while preserving the
//! implementation-agnostic boundary (any future ZK / native-interop / bridge verifier that
//! implements these four entrypoints can be routed in by registering a new version tag).
//!
//! Types are re-used from [`super::committee_verifier`] so the wire encoding stays identical.

use super::committee_verifier::{CCIPError, FeeResponse};

#[soroban_sdk::contractclient(name = "CrossChainVerifierClient")]
pub trait CrossChainVerifierInterface {
    /// Destination-side verification of a message. Any proof the CCV needs is supplied through
    /// `verifier_results` (opaque, CCV-specific). `message_hash` is the 32-byte message ID; any
    /// CCV MUST include it (or the full message) in its signed/verified payload.
    ///
    /// EVM `ICrossChainVerifierV1.verifyMessage(message, messageId, verifierResults)`.
    fn verify_message(
        env: soroban_sdk::Env,
        source_chain_selector: u64,
        message_hash: soroban_sdk::BytesN<32>,
        verifier_results: soroban_sdk::Bytes,
    ) -> Result<(), CCIPError>;

    /// Quotes the fee for a message to `dest_chain_selector`.
    ///
    /// EVM `ICrossChainVerifierV1.getFee(destChainSelector, message, extraArgs, requestedFinality)`.
    fn get_fee(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        message: soroban_sdk::Bytes,
        extra_args: soroban_sdk::Bytes,
        requested_finality: u32,
    ) -> Result<FeeResponse, CCIPError>;

    /// Source-side sending hook; returns CCV-specific data (e.g. the version tag) appended to the
    /// on-wire verifier results.
    ///
    /// EVM `ICrossChainVerifierV1.forwardToVerifier(message, messageId, feeToken, feeTokenAmount, verifierArgs)`.
    fn forward_to_verifier(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
        sender: soroban_sdk::Address,
        message_id: soroban_sdk::BytesN<32>,
        fee_token: soroban_sdk::Address,
        fee_token_amount: i128,
        verifier_args: soroban_sdk::Bytes,
    ) -> Result<soroban_sdk::Bytes, CCIPError>;

    /// Returns the off-chain storage-location identifiers the executor(s) read proof data from.
    ///
    /// EVM `ICrossChainVerifierV1.getStorageLocations()`.
    fn get_storage_locations(
        env: soroban_sdk::Env,
    ) -> Result<soroban_sdk::Vec<soroban_sdk::Bytes>, CCIPError>;
}
