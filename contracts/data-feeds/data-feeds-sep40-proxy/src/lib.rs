#![no_std]

pub mod interface;

mod contract;
mod events;
mod storage;

pub use contract::{DataFeedsSep40Proxy, DataFeedsSep40ProxyClient, DECIMALS};
pub use interface::{Asset, PriceData, PriceFeedClient, PriceFeedTrait, Sep40ProxyError};

#[cfg(test)]
mod tests;
