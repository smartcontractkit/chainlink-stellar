use soroban_sdk::{contracttype, BytesN, Vec};

/// An RMN signer's on-chain configuration.
///
/// Mirrors EVM `RMNRemote.Signer`:
///   - `onchain_pub_key`: ed25519 public key used to verify report signatures
///   - `node_index`: maps to nodes in the RMN home chain config (strictly increasing)
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Signer {
    pub onchain_pub_key: BytesN<32>,
    pub node_index: u64,
}

/// Contract configuration.
///
/// Mirrors EVM `RMNRemote.Config`:
///   - `rmn_home_config_digest`: ties this config to the RMN home chain contract
///   - `signers`: authorized RMN signer set (must be sorted by ascending `node_index`)
///   - `f_sign`: max number of faulty RMN nodes; `f_sign + 1` signatures required to verify
///     a report; must configure `2 * f_sign + 1` signers total
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Config {
    pub rmn_home_config_digest: BytesN<32>,
    pub signers: Vec<Signer>,
    pub f_sign: u64,
}
