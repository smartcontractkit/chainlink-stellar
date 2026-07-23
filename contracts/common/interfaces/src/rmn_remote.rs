//! RMN Remote interface (generated from rmn_remote.wasm; uses common_error::CCIPError).

use common_error::CCIPError;

#[soroban_sdk::contractargs(name = "RmnRemoteArgs")]
#[soroban_sdk::contractclient(name = "RmnRemoteClient")]
pub trait RmnRemoteInterface {
    fn curse(
        env: soroban_sdk::Env,
        caller: soroban_sdk::Address,
        subjects: soroban_sdk::Vec<soroban_sdk::BytesN<16>>,
    ) -> Result<(), CCIPError>;
    fn owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn uncurse(
        env: soroban_sdk::Env,
        subjects: soroban_sdk::Vec<soroban_sdk::BytesN<16>>,
    ) -> Result<(), CCIPError>;
    fn is_owner(env: soroban_sdk::Env, addr: soroban_sdk::Address) -> bool;
    fn is_cursed(env: soroban_sdk::Env) -> bool;
    fn init_owner(env: soroban_sdk::Env, owner: soroban_sdk::Address) -> Result<(), CCIPError>;
    fn initialize(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        curse_admins: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<(), CCIPError>;
    fn require_owner(env: soroban_sdk::Env) -> Result<soroban_sdk::Address, CCIPError>;
    fn set_new_owner(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn accept_ownership(env: soroban_sdk::Env) -> Result<(), CCIPError>;
    fn get_curse_admins(
        env: soroban_sdk::Env,
    ) -> Result<soroban_sdk::Vec<soroban_sdk::Address>, CCIPError>;
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
    fn get_pending_owner(env: soroban_sdk::Env) -> Option<soroban_sdk::Address>;
    fn transfer_ownership(
        env: soroban_sdk::Env,
        new_owner: soroban_sdk::Address,
    ) -> Result<(), CCIPError>;
    fn get_cursed_subjects(
        env: soroban_sdk::Env,
    ) -> Result<soroban_sdk::Vec<soroban_sdk::BytesN<16>>, CCIPError>;
    fn is_cursed_by_subject(env: soroban_sdk::Env, subject: soroban_sdk::BytesN<16>) -> bool;
    fn apply_curse_admin_updates(
        env: soroban_sdk::Env,
        added_admins: soroban_sdk::Vec<soroban_sdk::Address>,
        removed_admins: soroban_sdk::Vec<soroban_sdk::Address>,
    ) -> Result<(), CCIPError>;
    fn cancel_ownership_transfer(env: soroban_sdk::Env) -> Result<(), CCIPError>;
}
#[soroban_sdk::contractevent(topics = ["auth_CallerAdded"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AuthorizedCallerAddedEvent {
    pub caller: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["auth_CallerRemoved"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct AuthorizedCallerRemovedEvent {
    pub caller: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["auth_OwnerTransferStart"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct OwnershipTransferStartedEvent {
    pub previous_owner: soroban_sdk::Address,
    pub new_owner: soroban_sdk::Address,
}
#[soroban_sdk::contractevent(topics = ["rmn_Cursed"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct CursedEvent {
    pub subjects: soroban_sdk::Vec<soroban_sdk::BytesN<16>>,
}
#[soroban_sdk::contractevent(topics = ["rmn_Uncursed"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct UncursedEvent {
    pub subjects: soroban_sdk::Vec<soroban_sdk::BytesN<16>>,
}
