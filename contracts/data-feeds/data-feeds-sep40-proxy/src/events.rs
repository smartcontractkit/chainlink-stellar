use soroban_sdk::{contractevent, Address};

use data_feeds_proxy::DataId;

use crate::interface::Asset;

#[contractevent(topics = ["FeedAdded"])]
#[derive(Clone, Debug)]
pub struct FeedAdded {
    #[topic]
    pub asset: Asset,
    pub data_id: DataId,
}

#[contractevent(topics = ["FeedUpdated"])]
#[derive(Clone, Debug)]
pub struct FeedUpdated {
    #[topic]
    pub asset: Asset,
    pub old_data_id: DataId,
    pub new_data_id: DataId,
}

#[contractevent(topics = ["FeedRemoved"])]
#[derive(Clone, Debug)]
pub struct FeedRemoved {
    #[topic]
    pub asset: Asset,
    pub data_id: DataId,
}

#[contractevent(topics = ["ProxySet"])]
#[derive(Clone, Debug)]
pub struct ProxySet {
    pub old_proxy: Address,
    pub new_proxy: Address,
}
