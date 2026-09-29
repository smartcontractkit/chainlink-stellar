#![no_std]

//! # CCIP BnM Token
//!
//! A custom Soroban token implementing the full `token::StellarAssetInterface`
//! (the 21-method SAC superset), so the existing `BurnMintTokenPool` can mint
//! on inbound bridge messages and burn on outbound ones with **no pool-side
//! changes** — the pool calls `token::StellarAssetClient::mint` /
//! `token::Client::burn`, both of which this contract exposes.
//!
//! BnM is CCIP's cross-chain **test token** (EVM `BurnMintERC20` /
//! `BurnMintERC677`). On Stellar it is the **remotely-issued burn-mint** leg of
//! a token natively issued elsewhere: no initial supply is minted at deploy
//! time, and supply grows/shrinks 1:1 with bridge flow (mint on inbound, burn
//! on outbound).
//!
//! ## Divergences from EVM BnM (deliberate)
//!
//! - **Decimals = 7**, the Stellar SAC convention (EVM BnM is 18).
//! - **`drip(to)` mints `0.1` token** (`10⁶` at 7 decimals) to the explicit
//!   `to` address, permissionlessly. EVM `BurnMintERC20WithDrip.drip(to)` mints
//!   `1` token; the `0.1` amount is per the Stellar spec. The signature
//!   (`drip(to)`, no auth, explicit recipient) matches EVM.
//! - **No transfer authorization gating.** EVM BnM is a plain ERC20 (no
//!   `set_authorized` transfer lock); this contract keeps `set_authorized` /
//!   `authorized` for SAC interface fidelity but does **not** gate transfers on
//!   it (`authorized` always returns `true`).
//!
//! ## Burn-mint mint authority
//!
//! The pool can only call `mint` once it is the token's admin. The deployer
//! initializes the token as admin, then calls `set_admin(pool)` (admin-gated)
//! to hand mint authority to the burn-mint pool — exactly the SAC
//! `set_admin` handoff used in `burn-mint-pool` tests.

mod events;

use events::{
    ApproveEvent, BurnEvent, ClawbackEvent, DripEvent, MintEvent, SetAdminEvent,
    SetAuthorizedEvent, TransferEvent,
};
use soroban_sdk::{
    contract, contracterror, contractimpl, contracttype, symbol_short, token, Address, Env,
    MuxedAddress, String, Symbol,
};

// ============================================================
// Storage keys
// ============================================================

/// One-shot initialization guard (instance storage).
const INIT: Symbol = symbol_short!("INIT");
/// Current token admin — the only address that may mint / clawback / set
/// authorized / re-assign admin (instance storage).
const ADMIN: Symbol = symbol_short!("ADMIN");
/// Token name (instance storage).
const NAME: Symbol = symbol_short!("NAME");
/// Token symbol (instance storage).
const SYMBOL: Symbol = symbol_short!("SYMBOL");
/// Token decimals (instance storage).
const DECIMALS: Symbol = symbol_short!("DECIMALS");

/// Per-address keys live in persistent storage (balances/allowances/authorized
/// can grow unboundedly and must not crowd the 32 KiB instance footprint).
#[contracttype]
pub enum DataKey {
    Balance(Address),
    /// `(from, spender)` allowance amount. The expiry ledger is tracked
    /// separately so an expired entry reads as `0` without a write.
    Allowance(Address, Address),
    AllowanceExpiry(Address, Address),
    Authorized(Address),
}

// ============================================================
// Errors
// ============================================================

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq, PartialOrd, Ord)]
#[repr(u32)]
pub enum BnmError {
    /// `initialize` called on an already-initialized contract.
    AlreadyInitialized = 1,
    /// A required-admin entrypoint was invoked by a non-admin caller.
    NotAdmin = 2,
    /// `transfer` / `transfer_from` / `burn` with `amount` exceeding the
    /// available balance.
    InsufficientBalance = 3,
    /// `transfer_from` / `burn_from` with `amount` exceeding the spender's
    /// allowance.
    InsufficientAllowance = 4,
    /// A debit (transfer / burn / clawback) was given a negative amount.
    NegativeAmount = 5,
    /// A read entrypoint was called before `initialize`.
    NotInitialized = 6,
}

// ============================================================
// Contract
// ============================================================

#[contract]
pub struct BnmTokenContract;

#[contractimpl(contracttrait)]
impl token::StellarAssetInterface for BnmTokenContract {
    fn allowance(env: Env, from: Address, spender: Address) -> i128 {
        // An expired allowance reads as 0 (SAC semantics).
        if allowance_expired(&env, &from, &spender) {
            return 0;
        }
        env.storage()
            .persistent()
            .get(&DataKey::Allowance(from, spender))
            .unwrap_or(0)
    }

    fn approve(env: Env, from: Address, spender: Address, amount: i128, expiration_ledger: u32) {
        from.require_auth();
        require_initialized(&env);

        // SAC rule: expiration may not be in the past unless clearing to 0.
        if amount != 0 && expiration_ledger < env.ledger().sequence() {
            env.panic_with_error(BnmError::NegativeAmount);
        }

        env.storage()
            .persistent()
            .set(&DataKey::Allowance(from.clone(), spender.clone()), &amount);
        env.storage().persistent().set(
            &DataKey::AllowanceExpiry(from.clone(), spender.clone()),
            &expiration_ledger,
        );

        ApproveEvent {
            from,
            spender,
            amount,
            expiration_ledger,
        }
        .publish(&env);
    }

    fn balance(env: Env, id: Address) -> i128 {
        env.storage()
            .persistent()
            .get(&DataKey::Balance(id))
            .unwrap_or(0)
    }

    fn transfer(env: Env, from: Address, to: MuxedAddress, amount: i128) {
        from.require_auth();
        let to = to.address();
        do_transfer(&env, &from, &to, amount);
    }

    fn transfer_from(env: Env, spender: Address, from: Address, to: Address, amount: i128) {
        spender.require_auth();
        require_initialized(&env);

        // Consume the allowance first (expired allowance == 0).
        let allowed = if allowance_expired(&env, &from, &spender) {
            0
        } else {
            env.storage()
                .persistent()
                .get(&DataKey::Allowance(from.clone(), spender.clone()))
                .unwrap_or(0)
        };
        if amount > allowed {
            env.panic_with_error(BnmError::InsufficientAllowance);
        }
        env.storage().persistent().set(
            &DataKey::Allowance(from.clone(), spender.clone()),
            &(allowed - amount),
        );

        do_transfer(&env, &from, &to, amount);
    }

    fn burn(env: Env, from: Address, amount: i128) {
        from.require_auth();
        do_burn(&env, &from, amount);
    }

    fn burn_from(env: Env, spender: Address, from: Address, amount: i128) {
        spender.require_auth();
        require_initialized(&env);

        let allowed = if allowance_expired(&env, &from, &spender) {
            0
        } else {
            env.storage()
                .persistent()
                .get(&DataKey::Allowance(from.clone(), spender.clone()))
                .unwrap_or(0)
        };
        if amount > allowed {
            env.panic_with_error(BnmError::InsufficientAllowance);
        }
        env.storage().persistent().set(
            &DataKey::Allowance(from.clone(), spender.clone()),
            &(allowed - amount),
        );

        do_burn(&env, &from, amount);
    }

    fn decimals(env: Env) -> u32 {
        require_initialized(&env);
        env.storage().instance().get(&DECIMALS).unwrap_or(0)
    }

    fn name(env: Env) -> String {
        require_initialized(&env);
        env.storage()
            .instance()
            .get(&NAME)
            .unwrap_or_else(|| String::from_str(&env, ""))
    }

    fn symbol(env: Env) -> String {
        require_initialized(&env);
        env.storage()
            .instance()
            .get(&SYMBOL)
            .unwrap_or_else(|| String::from_str(&env, ""))
    }

    fn set_admin(env: Env, new_admin: Address) {
        let admin = require_admin(&env);
        admin.require_auth();
        env.storage().instance().set(&ADMIN, &new_admin);
        SetAdminEvent { admin, new_admin }.publish(&env);
    }

    fn admin(env: Env) -> Address {
        require_initialized(&env);
        env.storage()
            .instance()
            .get(&ADMIN)
            .unwrap_or_else(|| env.panic_with_error(BnmError::NotInitialized))
    }

    fn set_authorized(env: Env, id: Address, authorize: bool) {
        let admin = require_admin(&env);
        admin.require_auth();
        env.storage()
            .persistent()
            .set(&DataKey::Authorized(id.clone()), &authorize);
        SetAuthorizedEvent { id, authorize }.publish(&env);
    }

    fn authorized(_env: Env, _id: Address) -> bool {
        // BnM is a plain ERC20-style test token: every holder is always
        // authorized (matches EVM `BurnMintERC20`, which has no auth lock).
        // The `set_authorized` entrypoint is retained for SAC interface
        // fidelity but does not gate transfers.
        true
    }

    fn mint(env: Env, to: Address, amount: i128) {
        let admin = require_admin(&env);
        admin.require_auth();
        credit(&env, &to, amount);
        MintEvent { to, amount }.publish(&env);
    }

    fn clawback(env: Env, from: Address, amount: i128) {
        let admin = require_admin(&env);
        admin.require_auth();
        if amount < 0 {
            env.panic_with_error(BnmError::NegativeAmount);
        }
        debit(&env, &from, amount);
        ClawbackEvent {
            admin,
            from,
            amount,
        }
        .publish(&env);
    }

    fn trust(env: Env, addr: Address) {
        // Custom Soroban tokens have no classic trustlines; `trust` is a
        // SAC-interface no-op that still records the caller's authorization
        // (so an unauthorized invocation fails loudly, matching SAC behavior).
        addr.require_auth();
        let _ = env.current_contract_address();
    }
}

#[contractimpl]
impl BnmTokenContract {
    /// One-time initialization. Sets the token admin (the deployer, who later
    /// hands off to the burn-mint pool via `set_admin`) and the ERC20 metadata.
    /// No initial supply is minted — BnM is remotely-issued; supply tracks
    /// bridge flow.
    pub fn initialize(
        env: Env,
        admin: Address,
        name: String,
        symbol: String,
        decimals: u32,
    ) -> Result<(), BnmError> {
        if env.storage().instance().has(&INIT) {
            return Err(BnmError::AlreadyInitialized);
        }
        env.storage().instance().set(&INIT, &true);
        env.storage().instance().set(&ADMIN, &admin);
        env.storage().instance().set(&NAME, &name);
        env.storage().instance().set(&SYMBOL, &symbol);
        env.storage().instance().set(&DECIMALS, &decimals);
        Ok(())
    }

    /// Human-readable version tag (EVM `typeAndVersion` analogue).
    pub fn type_and_version(_env: Env) -> String {
        String::from_str(&_env, "BnmToken 1.0.0")
    }

    /// Permissionless faucet: mints `0.1` token (`10⁶` at 7 decimals) to the
    /// explicit `to` address. No `require_auth` — anyone may call it, matching
    /// EVM `BurnMintERC20WithDrip.drip(to)`. The amount (0.1, not EVM's 1) is
    /// the deliberate Stellar-spec divergence.
    pub fn drip(env: Env, to: Address) {
        require_initialized(&env);
        const DRIP_AMOUNT: i128 = 1_000_000; // 0.1 * 10^7
        credit(&env, &to, DRIP_AMOUNT);
        DripEvent {
            to,
            amount: DRIP_AMOUNT,
        }
        .publish(&env);
    }
}

// ============================================================
// Internal helpers
// ============================================================

fn require_initialized(env: &Env) {
    if !env.storage().instance().has(&INIT) {
        env.panic_with_error(BnmError::NotInitialized);
    }
}

/// Returns the current admin, panicking with `NotInitialized` if unset.
fn require_admin(env: &Env) -> Address {
    require_initialized(env);
    env.storage()
        .instance()
        .get(&ADMIN)
        .unwrap_or_else(|| env.panic_with_error(BnmError::NotInitialized))
}

/// True iff the `(from, spender)` allowance entry has expired (and so reads as
/// 0). An entry with no recorded expiry is treated as non-expired.
fn allowance_expired(env: &Env, from: &Address, spender: &Address) -> bool {
    match env
        .storage()
        .persistent()
        .get::<_, u32>(&DataKey::AllowanceExpiry(from.clone(), spender.clone()))
    {
        Some(expiry) => expiry < env.ledger().sequence(),
        None => false,
    }
}

/// Credits `amount` to `to`. Mints supply (no debit source). `amount` may be
/// any non-negative i128.
fn credit(env: &Env, to: &Address, amount: i128) {
    let bal: i128 = env
        .storage()
        .persistent()
        .get(&DataKey::Balance(to.clone()))
        .unwrap_or(0);
    env.storage()
        .persistent()
        .set(&DataKey::Balance(to.clone()), &(bal + amount));
}

/// Debits `amount` from `from`, panicking on negative amount or insufficient
/// balance.
fn debit(env: &Env, from: &Address, amount: i128) {
    require_initialized(env);
    if amount < 0 {
        env.panic_with_error(BnmError::NegativeAmount);
    }
    let bal: i128 = env
        .storage()
        .persistent()
        .get(&DataKey::Balance(from.clone()))
        .unwrap_or(0);
    if amount > bal {
        env.panic_with_error(BnmError::InsufficientBalance);
    }
    env.storage()
        .persistent()
        .set(&DataKey::Balance(from.clone()), &(bal - amount));
}

/// `transfer` core: debit `from`, credit `to`, emit. Rejects negative amounts.
fn do_transfer(env: &Env, from: &Address, to: &Address, amount: i128) {
    debit(env, from, amount);
    credit(env, to, amount);
    TransferEvent {
        from: from.clone(),
        to: to.clone(),
        amount,
    }
    .publish(env);
}

/// `burn` core: debit `from`, emit. Rejects negative amounts.
fn do_burn(env: &Env, from: &Address, amount: i128) {
    debit(env, from, amount);
    BurnEvent {
        from: from.clone(),
        amount,
    }
    .publish(env);
}

#[cfg(test)]
mod test;
