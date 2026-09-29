#[soroban_sdk::contractargs(name = "BnmTokenArgs")]
#[soroban_sdk::contractclient(name = "BnmTokenClient")]
pub trait BnmTokenInterface {
    fn burn(env: soroban_sdk::Env, from: soroban_sdk::Address, amount: i128);
    fn drip(env: soroban_sdk::Env, to: soroban_sdk::Address);
    fn mint(env: soroban_sdk::Env, to: soroban_sdk::Address, amount: i128);
    fn name(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn admin(env: soroban_sdk::Env) -> soroban_sdk::Address;
    fn trust(env: soroban_sdk::Env, addr: soroban_sdk::Address);
    fn symbol(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn approve(
        env: soroban_sdk::Env,
        from: soroban_sdk::Address,
        spender: soroban_sdk::Address,
        amount: i128,
        expiration_ledger: u32,
    );
    fn balance(env: soroban_sdk::Env, id: soroban_sdk::Address) -> i128;
    fn clawback(env: soroban_sdk::Env, from: soroban_sdk::Address, amount: i128);
    fn decimals(env: soroban_sdk::Env) -> u32;
    fn transfer(
        env: soroban_sdk::Env,
        from: soroban_sdk::Address,
        to: soroban_sdk::MuxedAddress,
        amount: i128,
    );
    fn allowance(
        env: soroban_sdk::Env,
        from: soroban_sdk::Address,
        spender: soroban_sdk::Address,
    ) -> i128;
    fn burn_from(
        env: soroban_sdk::Env,
        spender: soroban_sdk::Address,
        from: soroban_sdk::Address,
        amount: i128,
    );
    fn set_admin(env: soroban_sdk::Env, new_admin: soroban_sdk::Address);
    fn authorized(env: soroban_sdk::Env, id: soroban_sdk::Address) -> bool;
    fn initialize(
        env: soroban_sdk::Env,
        admin: soroban_sdk::Address,
        name: soroban_sdk::String,
        symbol: soroban_sdk::String,
        decimals: u32,
    ) -> Result<(), BnmError>;
    fn transfer_from(
        env: soroban_sdk::Env,
        spender: soroban_sdk::Address,
        from: soroban_sdk::Address,
        to: soroban_sdk::Address,
        amount: i128,
    );
    fn set_authorized(env: soroban_sdk::Env, id: soroban_sdk::Address, authorize: bool);
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
}
#[soroban_sdk::contracterror(export = false)]
#[derive(Debug, Copy, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub enum BnmError {
    AlreadyInitialized = 1,
    NotAdmin = 2,
    InsufficientBalance = 3,
    InsufficientAllowance = 4,
    NegativeAmount = 5,
    NotInitialized = 6,
}
#[soroban_sdk::contractevent(topics = ["burn"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct BurnEvent {
    pub from: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["drip"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct DripEvent {
    pub to: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["mint"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct MintEvent {
    pub to: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["approve"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ApproveEvent {
    pub from: soroban_sdk::Address,
    pub spender: soroban_sdk::Address,
    pub amount: i128,
    pub expiration_ledger: u32,
}
#[soroban_sdk::contractevent(topics = ["clawback"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ClawbackEvent {
    pub admin: soroban_sdk::Address,
    pub from: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["set_admin"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SetAdminEvent {
    pub admin: soroban_sdk::Address,
    pub new_admin: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["transfer"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct TransferEvent {
    pub from: soroban_sdk::Address,
    pub to: soroban_sdk::Address,
    pub amount: i128,
}
#[soroban_sdk::contractevent(topics = ["set_authorized"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct SetAuthorizedEvent {
    pub id: soroban_sdk::Address,
    pub authorize: bool,
}
