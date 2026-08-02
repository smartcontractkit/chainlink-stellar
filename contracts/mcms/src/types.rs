//! On-chain structs for MCMS-Stellar (see `docs/mcms-stellar-plan.md`).

use soroban_sdk::{contracttype, Address, Bytes, BytesN, Symbol};

pub const NUM_GROUPS: u32 = 32;
pub const MAX_NUM_SIGNERS: u32 = 200;

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Signer {
    /// Ethereum address, left-padded to 32 bytes (Solidity `address` ABI layout).
    pub addr: BytesN<32>,
    pub index: u32,
    pub group: u32,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Config {
    pub signers: soroban_sdk::Vec<Signer>,
    pub group_quorums: BytesN<32>,
    pub group_parents: BytesN<32>,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ExpiringRootAndOpCount {
    pub root: BytesN<32>,
    pub valid_until: u32,
    pub op_count: u64,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StellarRootMetadata {
    pub network_id: BytesN<32>,
    pub multisig: Address,
    pub pre_op_count: u64,
    pub post_op_count: u64,
    pub override_previous_root: bool,
    pub config_version: u64,
    pub encoding_version: u32,
}

/// Typed Stellar operation leaf. Function and argument XDR are deliberately separate.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StellarOp {
    pub network_id: BytesN<32>,
    pub multisig: Address,
    pub nonce: u64,
    pub target: Address,
    pub function: Symbol,
    pub args_xdr: Bytes,
    pub encoding_version: u32,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Signature {
    pub v: u32,
    pub r: BytesN<32>,
    pub s: BytesN<32>,
}

/// Wrapper so exported contract methods avoid `Vec<BytesN<32>>` (restricted by Soroban ABI).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SignerAddresses {
    pub inner: soroban_sdk::Vec<BytesN<32>>,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SignerGroups {
    pub inner: soroban_sdk::Vec<u32>,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct MerkleProof {
    pub inner: soroban_sdk::Vec<BytesN<32>>,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SignatureVec {
    pub inner: soroban_sdk::Vec<Signature>,
}
