use soroban_sdk::{contract, contractimpl, contracttype, BytesN, Env, String, Vec, I256};

use data_feeds_cache::{Bound, CacheError, DataFeedsCacheReader, RoundData};

use crate::DataId;

#[contract]
pub(crate) struct MockCache;

#[contracttype]
enum MockKey {
    Rounds(DataId),
    Decimals,
    Err,
}

const DEFAULT_STORED_DECIMALS: u32 = 18;

fn shared_key(env: &Env, data_id: &DataId) -> DataId {
    let mut bytes = data_id.to_array();
    bytes[7] = 0;
    BytesN::from_array(env, &bytes)
}

fn stored_decimals(env: &Env) -> u32 {
    env.storage()
        .instance()
        .get(&MockKey::Decimals)
        .unwrap_or(DEFAULT_STORED_DECIMALS)
}

#[contractimpl]
impl MockCache {
    pub fn __constructor(_env: Env) {}
    pub fn inject(env: Env, data_id: DataId, rounds: Vec<RoundData>) {
        let key = shared_key(&env, &data_id);
        env.storage().instance().set(&MockKey::Rounds(key), &rounds);
    }
    pub fn set_decimals(env: Env, decimals: u32) {
        env.storage().instance().set(&MockKey::Decimals, &decimals);
    }
    pub fn set_err(env: Env, e: CacheError) {
        env.storage().instance().set(&MockKey::Err, &e);
    }
}

#[contractimpl]
impl DataFeedsCacheReader for MockCache {
    fn latest_round(env: Env, data_id: DataId) -> Result<Option<RoundData>, CacheError> {
        if let Some(e) = env.storage().instance().get(&MockKey::Err) {
            return Err(e);
        }
        let rounds: Vec<RoundData> = env
            .storage()
            .instance()
            .get(&MockKey::Rounds(shared_key(&env, &data_id)))
            .unwrap_or(Vec::new(&env));
        Ok(rounds.iter().max_by_key(|v| v.round_id))
    }
    fn get_round(
        env: Env,
        data_id: DataId,
        round_id: u64,
    ) -> Result<Option<RoundData>, CacheError> {
        if let Some(e) = env.storage().instance().get(&MockKey::Err) {
            return Err(e);
        }
        let rounds: Vec<RoundData> = env
            .storage()
            .instance()
            .get(&MockKey::Rounds(shared_key(&env, &data_id)))
            .unwrap_or(Vec::new(&env));
        Ok(rounds.iter().find(|v| v.round_id == round_id))
    }
    fn round_range(
        _env: Env,
        _data_id: DataId,
        _from: u64,
        _to: u64,
    ) -> Result<Vec<RoundData>, CacheError> {
        unimplemented!("MockCache simulates no range reads; add real logic before testing them")
    }
    fn find_round(
        _env: Env,
        _data_id: DataId,
        _timestamp: u64,
        _bound: Bound,
    ) -> Result<Option<RoundData>, CacheError> {
        unimplemented!("MockCache simulates no search reads; add real logic before testing them")
    }
    fn decimals(env: Env, _data_id: DataId) -> Result<Option<u32>, CacheError> {
        if let Some(e) = env.storage().instance().get(&MockKey::Err) {
            return Err(e);
        }
        Ok(Some(stored_decimals(&env)))
    }
    fn is_configured(_env: Env, _data_id: DataId) -> Result<bool, CacheError> {
        unimplemented!("MockCache simulates no config reads; add real logic before testing them")
    }
    fn is_frozen(_env: Env, _data_id: DataId) -> bool {
        unimplemented!("MockCache simulates no freeze state; add real logic before testing it")
    }
    fn description(env: Env, _data_id: DataId) -> Result<Option<String>, CacheError> {
        if let Some(e) = env.storage().instance().get(&MockKey::Err) {
            return Err(e);
        }
        Ok(Some(String::from_str(&env, "MOCK")))
    }
}

const MOCK_LEDGER_SEQ: u32 = 1_000;

pub(crate) fn mock_round_data(env: &Env, (round_id, answer, ts): (u64, i128, u64)) -> RoundData {
    RoundData {
        round_id,
        answer: I256::from_i128(env, answer),
        timestamp: ts,
        ledger_seq: MOCK_LEDGER_SEQ,
        primary: true,
    }
}
