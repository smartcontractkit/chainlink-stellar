//! Curve-agnostic single-signature verification primitive.
//!
//! [`SignatureScheme`] abstracts "verify one signature over a 32-byte digest and
//! return the signer's 32-byte on-chain identity". It is a plain internal Rust
//! trait (not a `#[contracttrait]`): contracts keep their own quorum, storage, and
//! error handling, and reuse only this per-signature crypto step.
//!
//! Two curves are provided:
//! - [`Secp256k1EthAddress`] — the signer is *recovered* from the signature and its
//!   Ethereum-address identity derived (used by the committee verifier).
//! - [`Ed25519`] — ed25519 cannot recover a signer, so the signature is *verified*
//!   against a caller-supplied public key, which is then returned as the identity.

use soroban_sdk::{crypto::Hash, Bytes, BytesN, Env};

/// Ethereum address length (last 20 bytes of keccak256(uncompressed_pubkey[1..])).
pub const ETH_ADDRESS_BYTES: u32 = 20;

/// Offset within a `BytesN<32>` where the 20-byte Ethereum address is stored
/// (left-padded with 12 zero bytes to match Solidity's `abi.encode(address)` layout).
pub const ETH_ADDRESS_OFFSET: u32 = 32 - ETH_ADDRESS_BYTES;

/// Verifies a single signature over a 32-byte digest and yields the signer's
/// 32-byte on-chain identity.
pub trait SignatureScheme {
    /// Curve-specific data carried with the 64-byte signature to identify the signer.
    /// - [`Secp256k1EthAddress`]: the recovery id (`u32`) — signer is *recovered*.
    /// - [`Ed25519`]: the claimed public key (`BytesN<32>`) — signer is *verified*.
    type SignerInput;

    /// Return the signer's 32-byte on-chain identity. Traps (host panic) on an
    /// invalid signature, matching the SDK crypto host behavior.
    fn identify_signer(
        env: &Env,
        digest: &BytesN<32>,
        signature: &BytesN<64>,
        input: Self::SignerInput,
    ) -> BytesN<32>;
}

/// secp256k1 ECDSA with recovery: the signer's identity is the left-zero-padded
/// 20-byte Ethereum address `keccak256(recovered_pubkey[1..])[12..]`.
pub struct Secp256k1EthAddress;

impl SignatureScheme for Secp256k1EthAddress {
    /// The ECDSA recovery id (0 or 1).
    type SignerInput = u32;

    fn identify_signer(
        env: &Env,
        digest: &BytesN<32>,
        signature: &BytesN<64>,
        recovery_id: u32,
    ) -> BytesN<32> {
        // SAFETY: Hash<32> is #[repr(transparent)] over BytesN<32>, so the
        // reference reinterpret is layout-compatible. The caller guarantees
        // `digest` is a secure-hash digest (e.g. keccak-256), satisfying the
        // SDK's "secure hash" invariant for `secp256k1_recover`.
        let hash_ref: &Hash<32> = unsafe { &*(digest as *const BytesN<32> as *const Hash<32>) };
        let uncompressed_pubkey: BytesN<65> =
            env.crypto().secp256k1_recover(hash_ref, signature, recovery_id);

        // keccak256(pubkey[1..65]) — skip the 0x04 prefix byte.
        let pubkey_body = Bytes::from_slice(env, &uncompressed_pubkey.to_array()[1..]);
        let hash: BytesN<32> = env.crypto().keccak256(&pubkey_body).into();
        let hash_arr = hash.to_array();

        // The Ethereum address is the last 20 bytes, left-padded to 32 bytes.
        let mut padded = [0u8; 32];
        padded[ETH_ADDRESS_OFFSET as usize..]
            .copy_from_slice(&hash_arr[ETH_ADDRESS_OFFSET as usize..]);
        BytesN::from_array(env, &padded)
    }
}

/// ed25519: verifies the signature against the caller-supplied public key (which
/// doubles as the signer identity, since ed25519 has no recovery). `ed25519_verify`
/// traps on an invalid signature.
pub struct Ed25519;

impl SignatureScheme for Ed25519 {
    /// The claimed ed25519 public key; returned as the signer identity on success.
    type SignerInput = BytesN<32>;

    fn identify_signer(
        env: &Env,
        digest: &BytesN<32>,
        signature: &BytesN<64>,
        public_key: BytesN<32>,
    ) -> BytesN<32> {
        // ed25519 signs the 32-byte digest as its message. Traps if invalid.
        let message = Bytes::from_array(env, &digest.to_array());
        env.crypto().ed25519_verify(&public_key, &message, signature);
        public_key
    }
}
