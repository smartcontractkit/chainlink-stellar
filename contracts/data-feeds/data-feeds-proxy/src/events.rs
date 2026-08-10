use soroban_sdk::{contractevent, Address};

#[contractevent(topics = ["CacheSet"])]
#[derive(Clone, Debug)]
pub struct CacheSet {
    pub old_cache: Address,
    pub new_cache: Address,
}
