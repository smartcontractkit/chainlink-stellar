use soroban_sdk::{contractevent, BytesN, Vec};

/// Emitted when the signer configuration is updated via `set_config`.
#[contractevent(topics = ["rmn_ConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ConfigSetEvent {
    pub version: u32,
    pub num_signers: u32,
    pub f_sign: u64,
}

/// Emitted when one or more subjects are cursed.
#[contractevent(topics = ["rmn_Cursed"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CursedEvent {
    pub subjects: Vec<BytesN<16>>,
}

/// Emitted when one or more subjects are uncursed.
#[contractevent(topics = ["rmn_Uncursed"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct UncursedEvent {
    pub subjects: Vec<BytesN<16>>,
}
