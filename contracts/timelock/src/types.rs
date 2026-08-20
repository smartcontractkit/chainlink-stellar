//! On-chain types for the Stellar MCMS timelock v2.

use soroban_sdk::{contracttype, symbol_short, Address, Bytes, BytesN, Symbol, Vec};

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum TimelockDataKey {
    OpTime(BytesN<32>),
    RoleMember(Symbol, Address),
    RoleMembers(Symbol),
    BlockedFunction(Address, Symbol),
}

pub const DONE_TIMESTAMP: u64 = 1;

pub const ADMIN_ROLE: Symbol = symbol_short!("ADMIN");
pub const PROPOSER_ROLE: Symbol = symbol_short!("PROPOSER");
pub const CANCELLER_ROLE: Symbol = symbol_short!("CANCELLER");
pub const BYPASSER_ROLE: Symbol = symbol_short!("BYPASSER");

/// A canonical Soroban invocation. `args_xdr` encodes only `Vec<Val>` arguments.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Call {
    pub target: Address,
    pub function: Symbol,
    pub args_xdr: Bytes,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Calls {
    pub inner: Vec<Call>,
}

/// Target-scoped scheduling block. The same function can remain schedulable on other contracts.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BlockedFunction {
    pub target: Address,
    pub function: Symbol,
}
