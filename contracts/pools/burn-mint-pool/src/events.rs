// use soroban_sdk::{contractevent, Address};

// This is done to re-export the events from the `common/pool` crate.
#[allow(unused_imports)]
pub use common_pool::events::*;

// #[contractevent(topics = ["pool_Burned"])]
// #[derive(Clone, Debug, Eq, PartialEq)]
// pub struct BurnedEvent {
//     pub sender: Address,
//     pub amount: i128,
// }

// #[contractevent(topics = ["pool_Minted"])]
// #[derive(Clone, Debug, Eq, PartialEq)]
// pub struct MintedEvent {
//     pub sender: Address,
//     pub recipient: Address,
//     pub amount: i128,
// }
