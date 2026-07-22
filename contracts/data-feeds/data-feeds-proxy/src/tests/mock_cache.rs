use soroban_sdk::{contract, contractimpl, contracttype, Env, String, Vec, I256};

use data_feeds_cache::{Bound, CacheError, DataFeedsCacheReader, RoundData};

use crate::DataId;

#[contract]
pub(crate) struct MockCache;

#[contracttype]
enum MockKey {
    Rounds(DataId),
    Err,
}

#[contractimpl]
impl MockCache {
    pub fn __constructor(_env: Env) {}
    pub fn inject(env: Env, data_id: DataId, rounds: Vec<RoundData>) {
        env.storage()
            .instance()
            .set(&MockKey::Rounds(data_id), &rounds);
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
            .get(&MockKey::Rounds(data_id))
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
            .get(&MockKey::Rounds(data_id))
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
    fn decimals(env: Env, data_id: DataId) -> Result<u32, CacheError> {
        if let Some(e) = env.storage().instance().get(&MockKey::Err) {
            return Err(e);
        }
        Ok(match data_id.to_array()[7] {
            b @ 0x20..=0x60 => (b - 0x20) as u32,
            _ => 0,
        })
    }
    fn description(env: Env, _data_id: DataId) -> Result<String, CacheError> {
        if let Some(e) = env.storage().instance().get(&MockKey::Err) {
            return Err(e);
        }
        Ok(String::from_str(&env, "MOCK"))
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
