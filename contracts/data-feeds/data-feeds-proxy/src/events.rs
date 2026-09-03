use soroban_sdk::{contractevent, Address, BytesN};

#[contractevent(topics = ["CacheSet"])]
#[derive(Clone, Debug)]
pub struct CacheSet {
    pub old_cache: Address,
    pub new_cache: Address,
}

#[contractevent(topics = ["MinDecimalsSet"])]
#[derive(Clone, Debug)]
pub struct MinDecimalsSet {
    #[topic]
    pub data_id: BytesN<32>,
    pub min: u32,
}
