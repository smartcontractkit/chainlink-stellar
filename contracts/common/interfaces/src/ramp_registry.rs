//! Ramp registry interface (generated from ccip_ramp_registry.wasm; uses common_error::CCIPError).

use common_error::CCIPError;

#[soroban_sdk::contractargs(name = "RampRegistryArgs")]
#[soroban_sdk::contractclient(name = "RampRegistryClient")]
pub trait RampRegistryInterface {
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn is_owner(env: soroban_sdk::Env, addr: soroban_sdk::Address) -> bool;
    fn get_onramp(
        env: soroban_sdk::Env,
        dest_chain_selector: u64,
    ) -> Result<soroban_sdk::Address, CCIPError>;
    fn init_owner(env: soroban_sdk::Env, owner: soroban_sdk::Address) -> Result<(), CCIPError>;
    fn initialize(env: soroban_sdk::Env, owner: soroban_sdk::Address) -> Result<(), CCIPError>;
    fn is_offramp(
        env: soroban_sdk::Env,
        source_chain_selector: u64,
        offramp: soroban_sdk::Address,
    ) -> Result<bool, CCIPError>;
    fn get_onramps(env: soroban_sdk::Env) -> Result<soroban_sdk::Vec<OnRampEntry>, CCIPError>;
    fn get_offramps(env: soroban_sdk::Env) -> Result<soroban_sdk::Vec<OffRampEntry>, CCIPError>;
    fn require_owner(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn set_new_owner(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn get_pending_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn apply_onramp_updates(
        env: soroban_sdk::Env,
        updates: soroban_sdk::Vec<OnRampUpdate>,
    ) -> Result<(), CCIPError>;
    fn apply_offramp_updates(
        env: soroban_sdk::Env,
        updates: soroban_sdk::Vec<OffRampUpdate>,
    ) -> Result<(), CCIPError>;
    fn cancel_ownership_transfer(env: soroban_sdk::Env) -> Result<(), CCIPError>;
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OffRampKey {
    pub offramp: soroban_sdk::Address,
    pub source_chain_selector: u64,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OnRampEntry {
    pub dest_chain_selector: u64,
    pub onramp: soroban_sdk::Address,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OffRampEntry {
    pub offramp: soroban_sdk::Address,
    pub source_chain_selector: u64,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OnRampUpdate {
    pub dest_chain_selector: u64,
    pub onramp: Option<soroban_sdk::Address>,
}
#[soroban_sdk::contracttype(export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OffRampUpdate {
    pub enabled: bool,
    pub offramp: soroban_sdk::Address,
    pub source_chain_selector: u64,
}
#[soroban_sdk::contractevent(export = false, topics = ["ramp_reg_OnRampSet"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OnRampSetEvent {
    pub dest_chain_selector: u64,
    pub onramp: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["ramp_reg_OffRampAdded"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OffRampAddedEvent {
    pub source_chain_selector: u64,
    pub offramp: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["ramp_reg_OnRampRemoved"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OnRampRemovedEvent {
    pub dest_chain_selector: u64,
}
#[soroban_sdk::contractevent(export = false, topics = ["ramp_reg_OffRampRemoved"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OffRampRemovedEvent {
    pub source_chain_selector: u64,
    pub offramp: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["auth_RoleGranted"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleGrantedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["auth_RoleRevoked"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct RoleRevokedEvent {
    pub role: soroban_sdk::Symbol,
    pub account: soroban_sdk::Address,
    pub sender: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["auth_CallerAdded"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AuthorizedCallerAddedEvent {
    pub caller: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["auth_CallerRemoved"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AuthorizedCallerRemovedEvent {
    pub caller: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(export = false, topics = ["auth_OwnerTransferStart"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipTransferStartedEvent {
    pub previous_owner: soroban_sdk::Address,
    pub new_owner: soroban_sdk::Address,
}
