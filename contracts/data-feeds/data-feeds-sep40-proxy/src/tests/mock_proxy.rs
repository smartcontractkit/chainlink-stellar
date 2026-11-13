use soroban_sdk::{contract, contractimpl, contracttype, panic_with_error, Env};

use data_feeds_cache::CacheError;
use data_feeds_proxy::{DataId, ProxyReadError, Round};

/// Stands in for the Chainlink proxy. Each feed can be told to return a round, return a
/// `ProxyReadError`, or panic the way the real proxy does for a frozen feed.
#[contract]
pub(crate) struct MockProxy;

/// `ProxyReadError` is a `contracterror`, which cannot sit inside a `contracttype`, so the
/// configured error is stored as its numeric code.
#[contracttype]
enum Mode {
    Answer(Round),
    Fail(u32),
    Frozen,
}

fn error_from_code(code: u32) -> ProxyReadError {
    match code {
        x if x == ProxyReadError::InvalidDecimals as u32 => ProxyReadError::InvalidDecimals,
        x if x == ProxyReadError::RoundsToZero as u32 => ProxyReadError::RoundsToZero,
        _ => ProxyReadError::NoDataPresent,
    }
}

#[contracttype]
enum MockKey {
    Mode(DataId),
    LastDecimals,
}

#[contractimpl]
impl MockProxy {
    pub fn __constructor(_env: Env) {}

    pub fn set_round(env: Env, data_id: DataId, round: Round) {
        set_mode(&env, data_id, Mode::Answer(round));
    }

    pub fn set_error(env: Env, data_id: DataId, error: ProxyReadError) {
        set_mode(&env, data_id, Mode::Fail(error as u32));
    }

    pub fn set_frozen(env: Env, data_id: DataId) {
        set_mode(&env, data_id, Mode::Frozen);
    }

    pub fn last_decimals(env: Env) -> Option<u32> {
        env.storage().instance().get(&MockKey::LastDecimals)
    }

    pub fn latest_round(env: Env, data_id: DataId, decimals: u32) -> Result<Round, ProxyReadError> {
        env.storage()
            .instance()
            .set(&MockKey::LastDecimals, &decimals);
        match env.storage().instance().get(&MockKey::Mode(data_id)) {
            Some(Mode::Answer(round)) => Ok(round),
            Some(Mode::Fail(code)) => Err(error_from_code(code)),
            Some(Mode::Frozen) => panic_with_error!(&env, CacheError::FeedFrozen),
            None => Err(ProxyReadError::NoDataPresent),
        }
    }
}

fn set_mode(env: &Env, data_id: DataId, mode: Mode) {
    env.storage().instance().set(&MockKey::Mode(data_id), &mode);
}

#[contract]
pub(crate) struct MissingLatestRound;

#[contractimpl]
impl MissingLatestRound {
    pub fn ping(_env: Env) -> bool {
        true
    }
}

#[contract]
pub(crate) struct MalformedLatestRound;

#[contractimpl]
impl MalformedLatestRound {
    pub fn latest_round(_env: Env, _data_id: DataId, _decimals: u32) -> u32 {
        123
    }
}
