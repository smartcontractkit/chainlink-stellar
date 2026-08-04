#[soroban_sdk::contractargs(name = "DataFeedsProxyArgs")]
#[soroban_sdk::contractclient(name = "DataFeedsProxyClient")]
pub trait DataFeedsProxyInterface {
    fn upgrade(env: soroban_sdk::Env, new_wasm_hash: soroban_sdk::BytesN<32>);
    fn version(env: soroban_sdk::Env) -> u32;
    fn decimals(
        env: soroban_sdk::Env,
        data_id: soroban_sdk::BytesN<16>,
    ) -> Result<u32, ProxyReadError>;
    fn get_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn get_round(
        env: soroban_sdk::Env,
        data_id: soroban_sdk::BytesN<16>,
        round_id: u64,
    ) -> Result<Round, ProxyReadError>;
    fn set_cache(env: soroban_sdk::Env, cache: soroban_sdk::Address);
    fn description(
        env: soroban_sdk::Env,
        data_id: soroban_sdk::BytesN<16>,
    ) -> Result<soroban_sdk::String, ProxyReadError>;
    fn latest_round(
        env: soroban_sdk::Env,
        data_id: soroban_sdk::BytesN<16>,
    ) -> Result<Round, ProxyReadError>;
    fn __constructor(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        cache: soroban_sdk::Address,
    );
    fn recover_tokens(
        env: soroban_sdk::Env,
        token: soroban_sdk::Address,
        to: soroban_sdk::Address,
        amount: i128,
    );
    fn accept_ownership(env: soroban_sdk::Env);
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn renounce_ownership(env: soroban_sdk::Env);
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
        live_until_ledger: u32,
    );
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Round {
    pub answer: soroban_sdk::I256,
    pub round_id: u64,
    pub timestamp: u64,
}
#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum ProxyReadError {
    NoDataPresent = 50,
}
#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum RoleTransferError {
    NoPendingTransfer = 2200,
    InvalidLiveUntilLedger = 2201,
    InvalidPendingAccount = 2202,
    TransferExpired = 2203,
}
#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum OwnableError {
    OwnerNotSet = 2100,
    TransferInProgress = 2101,
    OwnerAlreadySet = 2102,
}
#[soroban_sdk::contractevent(export = false, topics = ["CacheSet"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CacheSet {
    pub old_cache: soroban_sdk::Address,
    pub new_cache: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["Upgraded"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct Upgraded {
    pub new_wasm_hash: soroban_sdk::BytesN<32>,
}
#[soroban_sdk::contractevent(export = false, topics = ["TokenRecovered"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TokenRecovered {
    pub token: soroban_sdk::Address,
    pub to: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(export = false, topics = ["ownership_transfer"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipTransfer {
    pub old_owner: soroban_sdk::Address,
    pub new_owner: soroban_sdk::Address,
    pub live_until_ledger: u32,
}
#[soroban_sdk::contractevent(export = false, topics = ["ownership_renounced"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipRenounced {
    pub old_owner: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["ownership_transfer_completed"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipTransferCompleted {
    pub new_owner: soroban_sdk::Address,
}
