use common_error::CCIPError;
use common_helpers::validation::Validatable;
use soroban_sdk::{contracttype, Address, BytesN, Vec};

/// Public signer-set configuration for a source chain, used by the
/// `apply_signature_configs` / `get_signature_config` entrypoints.
///
/// Persisted internally via `SignatureConfigManager` as a `SignatureConfig`;
/// this is the flat, caller-facing shape (threshold expressed directly).
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

/// Dynamic config mirrored from EVM CommitteeVerifier.DynamicConfig.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DynamicConfig {
    /// Destination for withdrawn fee tokens.
    pub fee_aggregator: Option<Address>,
    /// Optional allowlist admin, owner still has full access.
    pub allowlist_admin: Option<Address>,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RemoteChainConfig {
    pub remote_chain_selector: u64,
    pub router: Option<Address>,
    pub allowlist_enabled: bool,
    pub fee_usd_cents: u32,
    pub gas_for_verification: u32,
    pub payload_size_bytes: u32,
}

impl Validatable for RemoteChainConfig {
    fn validate(&self) -> Result<(), CCIPError> {
        if self.remote_chain_selector == 0 || self.gas_for_verification == 0 {
            return Err(CCIPError::InvalidConfig);
        }

        if self.router.is_none() && self.allowlist_enabled {
            return Err(CCIPError::InvalidConfig);
        }

        // TODO: add other validation rules here

        Ok(())
    }
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FeeResponse {
    pub fee: u32, // in USD cents
    pub dest_gas_limit: u32,
    pub dest_bytes_overhead: u32,
}
