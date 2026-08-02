use soroban_sdk::{contractevent, Address, BytesN, Symbol};

use crate::types::{Config, StellarRootMetadata};

/// Emitted when signer configuration is updated (mirrors Solidity `ConfigSet`).
#[contractevent(topics = ["mcms_ConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ConfigSetEvent {
    pub config: Config,
    pub config_version: u64,
    pub is_root_cleared: bool,
}

/// Emitted when a new Merkle root is accepted (mirrors Solidity `NewRoot`).
#[contractevent(topics = ["mcms_NewRoot"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct NewRootEvent {
    pub root: BytesN<32>,
    pub valid_until: u32,
    pub metadata: StellarRootMetadata,
}

/// Emitted when a governance op is successfully executed (mirrors Solidity `OpExecuted`).
#[contractevent(topics = ["mcms_OpExecuted"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct OpExecutedEvent {
    pub nonce: u64,
    pub target: Address,
    pub function: Symbol,
    pub args_hash: BytesN<32>,
}
