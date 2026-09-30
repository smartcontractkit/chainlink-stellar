//! Events emitted by the LINK token contract.
//!
//! The CCIP `gen_interfaces.sh` / `gen_bindings.sh` event-prefix convention
//! prefixes each topic with a short contract tag. LINK is a standalone token
//! contract (not a CCIP ramp), so it uses the bare Stellar token-interface
//! topic names (`transfer`, `mint`, `burn`, `set_admin`, `set_authorized`,
//! `approve`) emitted by the standard SAC interface. There is no faucet event
//! — LINK mints only via the admin (the burn-mint pool) on inbound bridge
//! messages, never out of thin air on Stellar — and no `clawback` event: the
//! clawback entrypoint is unsupported and traps.

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
/// the admin `mint` entrypoint (the burn-mint pool, on inbound bridge messages).
#[contractevent(topics = ["mint"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct MintEvent {
    pub to: Address,
    pub amount: i128,
}

/// `burn(from, amount)` — matches the SAC `["burn", from]` topic shape.
/// Emitted by `burn` (the burn-mint pool's outbound `lock_or_burn`) and
/// `burn_from`.
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
/// topic shape. LINK does not gate transfers on authorization (ERC20-like, same
/// as EVM LINK), but the entrypoint is retained for interface fidelity.
#[contractevent(topics = ["set_authorized"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SetAuthorizedEvent {
    pub id: Address,
    pub authorize: bool,
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
