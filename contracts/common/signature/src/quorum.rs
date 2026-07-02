use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use soroban_sdk::{
    contracttrait, contracttype, symbol_short, Bytes, BytesN, Env, Map, Symbol, Vec,
};

use crate::scheme::{Secp256k1EthAddress, SignatureScheme};

/// EIP-2098 compact ECDSA signature: r(32) + yParityAndS(32) = 64 bytes.
pub const ECDSA_COMPACT_SIG_BYTES: u32 = 64;
pub const PER_SIGNATURE_BYTES: u32 = ECDSA_COMPACT_SIG_BYTES;

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SignatureQuorumConfig {
    pub source_chain_selector: u64,
    pub threshold: u32,
    /// Signer identifiers (32 bytes each), stored in ascending byte-lexicographic
    /// order. Each entry is a left-zero-padded 20-byte Ethereum address derived
    /// from the signer's secp256k1 public key: `keccak256(pubkey[1..])[12..32]`.
    pub signers: Vec<BytesN<32>>,
}

fn read_compact_sig(_env: &Env, data: &Bytes, offset: u32) -> Result<([u8; 64], u32), CCIPError> {
    let mut raw = [0u8; 64];
    let mut i = 0u32;
    while i < ECDSA_COMPACT_SIG_BYTES {
        raw[i as usize] = data.get(offset + i).ok_or(CCIPError::InvalidSignature)?;
        i += 1;
    }
    // EIP-2098: bit 255 of yParityAndS carries the recovery ID (0 or 1).
    let recovery_id = (raw[32] >> 7) as u32;
    raw[32] &= 0x7F; // clear parity bit to recover clean s
    Ok((raw, recovery_id))
}

#[contracttrait]
pub trait SignatureQuorum: Initializable + Ownable {
    const SIGNATURE_CONFIGS: Symbol = symbol_short!("SIGCFGS");

    const VERIFIER_VERSION_BYTES: u32 = 4;
    const SIGNATURE_LENGTH_BYTES: u32 = 2;

    fn extract_version_tag(env: &Env, verifier_results: &Bytes) -> Result<BytesN<4>, CCIPError> {
        if verifier_results.len() < Self::VERIFIER_VERSION_BYTES {
            return Err(CCIPError::InvalidVerifierResults);
        }
        let mut out = [0u8; 4];
        let mut i = 0u32;
        while i < Self::VERIFIER_VERSION_BYTES {
            out[i as usize] = verifier_results
                .get(i)
                .ok_or(CCIPError::InvalidVerifierResults)?;
            i += 1;
        }
        Ok(BytesN::from_array(env, &out))
    }

    fn extract_signature_len(verifier_results: &Bytes) -> Result<u32, CCIPError> {
        if verifier_results.len() < Self::VERIFIER_VERSION_BYTES + Self::SIGNATURE_LENGTH_BYTES {
            return Err(CCIPError::InvalidVerifierResults);
        }
        let b0 = verifier_results
            .get(Self::VERIFIER_VERSION_BYTES)
            .ok_or(CCIPError::InvalidVerifierResults)?;
        let b1 = verifier_results
            .get(Self::VERIFIER_VERSION_BYTES + 1)
            .ok_or(CCIPError::InvalidVerifierResults)?;
        Ok(((b0 as u32) << 8) | (b1 as u32))
    }

    /// Verify that `signatures` contains at least `threshold` valid ECDSA
    /// (secp256k1) signatures over `signed_hash`, produced by distinct signers
    /// from the configured set for `source_chain_selector`.
    ///
    /// Signature payload format (EIP-2098 compact):
    /// `[r_0 (32B)][yParityAndS_0 (32B)][r_1 (32B)][yParityAndS_1 (32B)]...`
    ///
    /// Recovered signer addresses must appear in strictly ascending
    /// byte-lexicographic order. This prevents duplicates and makes the
    /// ordering deterministic.
    fn validate_signatures(
        env: &Env,
        source_chain_selector: u64,
        signed_hash: BytesN<32>,
        signatures: Bytes,
    ) -> Result<(), CCIPError> {
        let sig_cfgs: Map<u64, SignatureQuorumConfig> = env
            .storage()
            .persistent()
            .get(&Self::SIGNATURE_CONFIGS)
            .unwrap_or(Map::new(env));

        let cfg = sig_cfgs
            .get(source_chain_selector)
            .ok_or(CCIPError::SourceSignersNotConfigured)?;

        if cfg.threshold == 0 {
            return Err(CCIPError::SourceSignersNotConfigured);
        }

        if signatures.len() % PER_SIGNATURE_BYTES != 0 {
            return Err(CCIPError::InvalidSignatureLength);
        }

        let sig_count = signatures.len() / PER_SIGNATURE_BYTES;
        if sig_count < cfg.threshold {
            return Err(CCIPError::ThresholdNotMet);
        }

        let mut prev_address: Option<BytesN<32>> = None;

        let mut i = 0u32;
        while i < sig_count {
            let offset = i * PER_SIGNATURE_BYTES;
            let (sig_bytes, recovery_id) = read_compact_sig(env, &signatures, offset)?;
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
            i += 1;
        }

        Ok(())
    }

    /// Hook for implementors to emit a contract event when signature configs change.
    fn emit_signature_config_set(
        _env: &Env,
        _source_chain_selector: u64,
        _signers: &Vec<BytesN<32>>,
        _threshold: u32,
    ) {
    }

    fn get_signature_config(
        env: Env,
        source_chain_selector: u64,
    ) -> Result<SignatureQuorumConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let sig_cfgs: Map<u64, SignatureQuorumConfig> = env
            .storage()
            .persistent()
            .get(&Self::SIGNATURE_CONFIGS)
            .unwrap_or(Map::new(&env));
        sig_cfgs
            .get(source_chain_selector)
            .ok_or(CCIPError::SourceSignersNotConfigured)
    }

    fn get_all_signature_configs(env: Env) -> Result<Vec<SignatureQuorumConfig>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let sig_cfgs: Map<u64, SignatureQuorumConfig> = env
            .storage()
            .persistent()
            .get(&Self::SIGNATURE_CONFIGS)
            .unwrap_or(Map::new(&env));

        let mut out = Vec::new(&env);
        for (source_chain_selector, cfg) in sig_cfgs.iter() {
            out.push_back(SignatureQuorumConfig {
                source_chain_selector,
                threshold: cfg.threshold,
                signers: cfg.signers,
            });
        }
        Ok(out)
    }

    fn apply_signature_configs(
        env: Env,
        source_chains_to_remove: Vec<u64>,
        signature_configs: Vec<SignatureQuorumConfig>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut sig_cfgs: Map<u64, SignatureQuorumConfig> = env
            .storage()
            .persistent()
            .get(&Self::SIGNATURE_CONFIGS)
            .unwrap_or(Map::new(&env));

        for source_chain_selector in source_chains_to_remove.iter() {
            sig_cfgs.remove(source_chain_selector);
            let empty: Vec<BytesN<32>> = Vec::new(&env);
            Self::emit_signature_config_set(&env, source_chain_selector, &empty, 0);
        }

        for update in signature_configs.iter() {
            if update.threshold == 0 || update.threshold > update.signers.len() {
                return Err(CCIPError::InvalidSignatureThreshold);
            }

            let signers = &update.signers;
            let mut j = 1u32;
            while j < signers.len() {
                let prev = signers
                    .get(j - 1)
                    .ok_or(CCIPError::InvalidSignaturePubkey)?;
                let curr = signers.get(j).ok_or(CCIPError::InvalidSignaturePubkey)?;
                if prev == curr {
                    return Err(CCIPError::DuplicateOnchainPublicKey);
                }
                if prev > curr {
                    return Err(CCIPError::InvalidSignerOrder);
                }
                j += 1;
            }

            sig_cfgs.set(
                update.source_chain_selector,
                SignatureQuorumConfig {
                    source_chain_selector: update.source_chain_selector,
                    threshold: update.threshold,
                    signers: update.signers.clone(),
                },
            );

            Self::emit_signature_config_set(
                &env,
                update.source_chain_selector,
                &update.signers,
                update.threshold,
            );
        }

        env.storage()
            .persistent()
            .set(&Self::SIGNATURE_CONFIGS, &sig_cfgs);
        Ok(())
    }
}
