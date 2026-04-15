use soroban_sdk::{contractevent, BytesN};

/// Emitted when this example receiver handles an inbound CCIP message via `ccip_receive`.
///
/// Includes scalars only (no unbounded `Bytes` in the event) so payloads stay small on-chain.
#[contractevent(topics = ["example_CcipMessageReceived"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CcipMessageReceivedEvent {
    pub message_id: BytesN<32>,
    pub source_chain_selector: u64,
    pub data_len: u32,
    pub sender_len: u32,
    pub dest_token_transfers: u32,
}

#[contractevent(topics = ["example_RemChCfg"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CcipRemoteChainConfiguredEvent {
    pub dest_chain_selector: u64,
    pub extra_args_len: u32,
    /// FinalityCodec-style policy (EVM `bytes4`); `0` when disabled or unset.
    pub allowed_finality_config: u32,
}

#[contractevent(topics = ["example_CcvCfg"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CcipCcvConfigSetEvent {
    pub source_chain_selector: u64,
    pub required_len: u32,
    pub optional_len: u32,
    pub optional_threshold: u32,
}
