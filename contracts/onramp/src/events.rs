use soroban_sdk::{contractevent, Address, Bytes, BytesN, Vec};

use crate::{DestChainConfig, DynamicConfig, Receipt, StaticConfig};

/// Event data for CCIPMessageSent
#[contractevent(topics = ["onramp_1_7_CCIPMessageSent"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CCIPMessageSentEvent {
    /// Destination chain selector
    pub dest_chain_selector: u64,
    /// Sequence number for this message to the destination chain
    pub sequence_number: u64,
    /// Original sender address
    pub sender: Address,
    /// Unique message ID (hash of encoded message)
    pub message_id: BytesN<32>,
    /// Fee token used for payment
    pub fee_token: Address,
    /// Token amount before pool fees (0 if no tokens)
    pub token_amount_before_fees: i128,
    /// Full encoded message (MessageV1 format)
    pub encoded_message: Bytes,
    /// Receipts for all components
    pub receipts: Vec<Receipt>,
    /// Blobs from each verifier
    pub verifier_blobs: Vec<Bytes>,
}

#[contractevent(topics = ["onramp_1_7_ConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ConfigSetEvent {
    pub static_config: StaticConfig,
    pub dynamic_config: DynamicConfig,
}

#[contractevent(topics = ["onramp_1_7_DestChainConfigSet"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DestChainConfigSetEvent {
    pub dest_chain_selector: u64,
    pub message_number: u64,
    pub config: DestChainConfig,
}

#[contractevent(topics = ["onramp_1_7_OwnershipTransferred"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct OwnershipTransferredEvent {
    pub new_owner: Address,
}

/// Emitted when the OnRamp's executable is swapped in place via `upgrade`.
/// The contract address and all instance/persistent storage are unchanged;
/// only the Wasm code backing the contract is replaced.
#[contractevent(topics = ["onramp_1_7_Upgraded"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Upgraded {
    /// Hash of the new Wasm the contract now runs (uploaded beforehand via
    /// `env.deployer().upload_contract_wasm`).
    pub new_wasm_hash: BytesN<32>,
}

/// Emitted by `forward_from_router` ONLY when the `e2e-upgrade-marker` cargo
/// feature is enabled. The default (feature-off) shipped Wasm never emits this,
/// so the event's presence after a send is unambiguous evidence that the
/// OnRamp's executable was swapped to a feature-enabled Wasm via `upgrade` and
/// that the upgraded `forward_from_router` code path actually ran — stronger
/// than a `peek`-style probe, which only proves "a function now exists."
///
/// Used by the upgrade tests (see `docs/upgradeability.md`): they send a
/// message before upgrading (marker absent), upgrade to the feature-enabled
/// Wasm, send the same message again (marker present), and assert the
/// difference. The feature adds this single event publish and touches no
/// storage, fees, receipts, or message-id derivation, so storage layout is
/// identical across the upgrade.
#[cfg(feature = "e2e-upgrade-marker")]
#[contractevent(topics = ["onramp_1_7_E2EUpgradeMarker"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct E2EUpgradeMarker {
    /// Fixed sentinel so the test can confirm it found the right event, not a
    /// coincidental same-topic event. Value: 0xE2E0_0001.
    pub marker: u32,
}
