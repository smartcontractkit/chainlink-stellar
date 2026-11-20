use soroban_sdk::{contractevent, Address, BytesN};

use crate::interface::Asset;

#[contractevent(topics = ["FeedAdded"])]
#[derive(Clone, Debug)]
pub struct FeedAdded {
    #[topic]
    pub asset: Asset,
    pub data_id: BytesN<32>,
}

#[contractevent(topics = ["FeedUpdated"])]
#[derive(Clone, Debug)]
pub struct FeedUpdated {
    #[topic]
    pub asset: Asset,
    pub old_data_id: BytesN<32>,
    pub new_data_id: BytesN<32>,
}

#[contractevent(topics = ["FeedRemoved"])]
#[derive(Clone, Debug)]
pub struct FeedRemoved {
    #[topic]
    pub asset: Asset,
    pub data_id: BytesN<32>,
}

#[contractevent(topics = ["ProxySet"])]
#[derive(Clone, Debug)]
pub struct ProxySet {
    pub old_proxy: Address,
    pub new_proxy: Address,
}
