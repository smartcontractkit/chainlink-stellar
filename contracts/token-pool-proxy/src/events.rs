//! Events emitted by the token pool proxy. Topics use the `tpproxy_` prefix
//! (the `gen_interfaces.sh` / `gen_bindings.sh` per-contract event-prefix
//! convention, e.g. `lockbox_` for the token lock box).

use soroban_sdk::{contractevent, Address};

/// An allowlisted caller minted `amount` of the asset to `to`.
/// `remaining_allowance` is `None` for an unlimited caller.
#[contractevent(topics = ["tpproxy_Minted"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct MintedEvent {
    pub caller: Address,
    pub to: Address,
    pub amount: i128,
    pub remaining_allowance: Option<i128>,
}

/// An allowlisted caller burned `amount` of the asset from `from`.
#[contractevent(topics = ["tpproxy_Burned"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BurnedEvent {
    pub caller: Address,
    pub from: Address,
    pub amount: i128,
}

/// A caller was added to the allowlist or had its mint allowance overwritten.
/// `mint_allowance` is `None` for an unlimited caller.
#[contractevent(topics = ["tpproxy_CallerUpdated"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CallerUpdatedEvent {
    pub caller: Address,
    pub mint_allowance: Option<i128>,
}

/// A caller was removed from the allowlist.
#[contractevent(topics = ["tpproxy_CallerRemoved"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CallerRemovedEvent {
    pub caller: Address,
}

/// The owner moved the asset's SAC admin role away from this proxy.
#[contractevent(topics = ["tpproxy_AssetAdminChanged"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AssetAdminChangedEvent {
    pub new_admin: Address,
}
