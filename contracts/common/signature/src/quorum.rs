use common_error::CCIPError;
use soroban_sdk::{BytesN, Env, Vec};

use crate::{
    config::SignatureConfigManager,
    scheme::{Secp256k1EthAddress, SignatureScheme},
};

/// EIP-2098 compact ECDSA signature: r(32) + yParityAndS(32) = 64 bytes.
pub const ECDSA_COMPACT_SIG_BYTES: u32 = 64;
pub const PER_SIGNATURE_BYTES: u32 = ECDSA_COMPACT_SIG_BYTES;

/// Decode an EIP-2098 compact signature into raw `r || s` bytes plus recovery id.
///
/// Bit 255 of `yParityAndS` (the high bit of byte 32) carries the recovery id;
/// it is cleared to recover the canonical `s`.
fn decode_compact_sig(sig: &BytesN<64>) -> ([u8; 64], u32) {
    let mut raw = sig.to_array();
    let recovery_id = (raw[32] >> 7) as u32;
    raw[32] &= 0x7F;
    (raw, recovery_id)
}

/// Internal (non-`contracttrait`) mixin providing signature-quorum verification
/// on top of [`SignatureConfigManager`]. Its methods are called by the concrete
/// contract and are not exported as contract entrypoints.
pub trait SignatureQuorum: SignatureConfigManager<Error = CCIPError> {
    /// Verify that `signatures` contains at least the configured `threshold` of
    /// valid ECDSA (secp256k1) signatures over `signed_hash`, produced by
    /// distinct signers from the set configured under `key`.
    ///
    /// Each entry is an EIP-2098 compact signature (`r(32) || yParityAndS(32)`).
    /// The caller is responsible for extracting these from its own wire format
    /// (see `CommitteeVerifierContract::verify_message`) and for supplying the
    /// storage `key` identifying the signer set; this method only validates the
    /// signatures and enforces quorum.
    ///
    /// Recovered signer addresses must appear in strictly ascending
    /// byte-lexicographic order. This prevents duplicates and makes the
    /// ordering deterministic.
    fn validate_signatures(
        env: &Env,
        key: &Self::DataKey,
        signed_hash: BytesN<32>,
        signatures: Vec<BytesN<64>>,
    ) -> Result<(), CCIPError> {
        let cfg = <Self as SignatureConfigManager>::get_config(env, key)
            .ok_or(CCIPError::SourceSignersNotConfigured)?;

        let threshold = cfg.verification.threshold();
        if threshold == 0 {
            return Err(CCIPError::SourceSignersNotConfigured);
        }

        if signatures.len() < threshold {
            return Err(CCIPError::ThresholdNotMet);
        }

        let mut prev_address: Option<BytesN<32>> = None;

        for sig in signatures.iter() {
            let (sig_bytes, recovery_id) = decode_compact_sig(&sig);
            let sig = BytesN::<64>::from_array(env, &sig_bytes);
            let recovered_address =
                Secp256k1EthAddress::identify_signer(env, &signed_hash, &sig, recovery_id);

            if let Some(ref prev) = prev_address {
                if *prev >= recovered_address {
                    return Err(CCIPError::OutOfOrderSignatures);
                }
            }

            let mut found = false;
            for signer in cfg.signers.iter() {
                if signer == recovered_address {
                    found = true;
                    break;
                }
            }
            if !found {
                return Err(CCIPError::UnexpectedSigner);
            }

            prev_address = Some(recovered_address);
        }

        Ok(())
    }
}
