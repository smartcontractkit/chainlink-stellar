//! Centralizes the CCV signed-payload + version-tag invariant shared by all verifiers.
//!
//! Mirrors EVM `CommitteeVerifier.verifyMessage` (and the `i_versionTag`/`s_allowedFinalityConfig`
//! framing of `BaseVerifier`): the `bytes4` version tag that a `VersionedVerifierResolver`
//! dispatches on is the *same* bytes prepended to the signed payload
//! `keccak256(version_tag || message_id)`. EVM signs `keccak256(bytes.concat(verifierVersion,
//! messageHash))` and reverts `InvalidCCVVersion` if the blob's tag ≠ `versionTag()`.
//!
//! Keeping this in one place prevents the #72-bug class (version-tag / wire-format mismatches)
//! from drifting as more verifiers are added.
#![allow(dead_code)]

use common_error::CCIPError;
use soroban_sdk::{Bytes, BytesN, Env};

/// Length of the leading `bytes4` version tag in a verifier-results blob.
pub const VERSION_BYTES: u32 = 4;
/// Length of the big-endian `u16` signature-payload length that follows the version tag.
pub const SIGNATURE_LENGTH_BYTES: u32 = 2;

/// Reads the leading `bytes4` version tag from a verifier-results blob.
///
/// EVM: `bytes4 verifierVersion = bytes4(verifierResults[:4])`.
pub fn extract_version_tag(env: &Env, verifier_results: &Bytes) -> Result<BytesN<4>, CCIPError> {
    if verifier_results.len() < VERSION_BYTES {
        return Err(CCIPError::InvalidVerifierResults);
    }
    let mut out = [0u8; VERSION_BYTES as usize];
    let mut i = 0u32;
    while i < VERSION_BYTES {
        out[i as usize] = verifier_results
            .get(i)
            .ok_or(CCIPError::InvalidVerifierResults)?;
        i += 1;
    }
    Ok(BytesN::from_array(env, &out))
}

/// Enforces that the version tag carried in the blob equals the verifier's configured tag.
///
/// EVM `CommitteeVerifier.verifyMessage`: `if (verifierVersion != versionTag()) revert
/// InvalidCCVVersion(verifierVersion)`. The version must be signed (it is part of the signed
/// payload) so that it cannot be swapped post-signatures to route to a different verifier.
pub fn ensure_version_tag_matches(
    expected: &BytesN<4>,
    actual: &BytesN<4>,
) -> Result<(), CCIPError> {
    if expected != actual {
        return Err(CCIPError::InvalidCCVVersion);
    }
    Ok(())
}

/// Computes the signed payload hash `keccak256(version_tag || message_id)` — the digest every
/// committee-style CCV signs on source and validates on destination.
///
/// EVM: `keccak256(bytes.concat(verifierVersion, messageHash))` (a 36-byte preimage).
pub fn signed_payload_hash(
    env: &Env,
    version_tag: &BytesN<4>,
    message_id: &BytesN<32>,
) -> BytesN<32> {
    let mut payload = Bytes::new(env);
    payload.append(&Bytes::from_slice(env, &version_tag.to_array()));
    payload.append(&Bytes::from_array(env, &message_id.to_array()));
    env.crypto().keccak256(&payload).into()
}
