//! Local copy of the SEP-40 oracle consumer interface. [https://github.com/stellar/stellar-protocol/blob/master/ecosystem/sep-0040.md#design-rationale]
//!
//! Mirrors the `sep-40-oracle` crate (Script3) and the SEP-0040 text exactly. The crate is not
//! used directly because its latest release pins `soroban-sdk ^25`, which cannot be mixed with
//! the `soroban-sdk` this workspace is pinned to. Soroban encodes `#[contracttype]` values by
//! variant and field name, so these types are wire-compatible with the upstream ones
//! (`tests/conformance.rs` pins the encoding).

use soroban_sdk::{contractclient, contracttype, Address, Env, Symbol, Vec};

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PriceData {
    pub price: i128,
    pub timestamp: u64,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum Asset {
    Stellar(Address),
    Other(Symbol),
}

#[contractclient(name = "PriceFeedClient")]
pub trait PriceFeedTrait {
    fn base(env: Env) -> Asset;

    fn assets(env: Env) -> Vec<Asset>;

    fn decimals(env: Env) -> u32;

    fn resolution(env: Env) -> u32;

    fn price(env: Env, asset: Asset, timestamp: u64) -> Option<PriceData>;

    fn prices(env: Env, asset: Asset, records: u32) -> Option<Vec<PriceData>>;

    fn lastprice(env: Env, asset: Asset) -> Option<PriceData>;
}
