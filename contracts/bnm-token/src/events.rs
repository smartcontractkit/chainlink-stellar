//! Events emitted by the BnM token contract.
//!
//! The CCIP `gen_interfaces.sh` / `gen_bindings.sh` event-prefix convention
//! prefixes each topic with a short contract tag. BnM is a standalone test
//! token (not a CCIP ramp), so it uses the bare Stellar token-interface topic
//! names (`transfer`, `mint`, `burn`, `set_admin`, `set_authorized`,
//! `clawback`, `approve`) emitted by the standard SAC interface, plus a
//! `drip` topic for the permissionless faucet entrypoint. The `bnm_` prefix
//! is reserved for any future BnM-specific admin events.

use soroban_sdk::{contractevent, Address};

/// `transfer(from, to, amount)` — matches the SAC `["transfer", from, to]`
/// topic shape. Emitted on user transfers and `transfer_from`.
#[contractevent(topics = ["transfer"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TransferEvent {
    pub from: Address,
    pub to: Address,
    pub amount: i128,
}

/// `mint(to, amount)` — matches the SAC `["mint", to]` topic shape. Emitted by
/// the admin `mint` entrypoint and by the permissionless `drip` faucet.
#[contractevent(topics = ["mint"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct MintEvent {
    pub to: Address,
    pub amount: i128,
}

/// `burn(from, amount)` — matches the SAC `["burn", from]` topic shape.
/// Emitted by `burn` and `burn_from`.
#[contractevent(topics = ["burn"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BurnEvent {
    pub from: Address,
    pub amount: i128,
}

/// `set_admin(admin, new_admin)` — matches the SAC `["set_admin", admin]`
/// topic shape. The burn-mint token pool becomes admin via this entrypoint so
/// it can mint on inbound bridge messages.
#[contractevent(topics = ["set_admin"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SetAdminEvent {
    pub admin: Address,
    pub new_admin: Address,
}

/// `set_authorized(id, authorize)` — matches the SAC `["set_authorized", id]`
/// topic shape. BnM does not gate transfers on authorization (ERC20-like, same
/// as EVM `BurnMintERC20`), but the entrypoint is retained for interface
/// fidelity.
#[contractevent(topics = ["set_authorized"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SetAuthorizedEvent {
    pub id: Address,
    pub authorize: bool,
}

/// `clawback(admin, from, amount)` — matches the SAC `["clawback", admin,
/// from]` topic shape. Required by the interface ABI; unused by CCIP.
#[contractevent(topics = ["clawback"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ClawbackEvent {
    pub admin: Address,
    pub from: Address,
    pub amount: i128,
}

/// `approve(from, spender, amount, expiration_ledger)` — matches the SAC
/// `["approve", from, spender]` topic shape.
#[contractevent(topics = ["approve"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ApproveEvent {
    pub from: Address,
    pub spender: Address,
    pub amount: i128,
    pub expiration_ledger: u32,
}

/// `drip(to, amount)` — BnM-specific faucet event. Emitted by the
/// permissionless `drip(to)` entrypoint which mints `0.1` token (10⁶ at 7
/// decimals) to `to`.
#[contractevent(topics = ["drip"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DripEvent {
    pub to: Address,
    pub amount: i128,
}
