#![no_std]

//! # LINK Token (bridged)
//!
//! A custom Soroban token implementing the full `token::StellarAssetInterface`
//! (the 21-method SAC superset) plus an owner/minter split with multiple
//! minters, so the existing `BurnMintTokenPool` can mint on inbound bridge
//! messages and burn on outbound ones, and a second pool can be minting
//! during a zero-downtime pool migration. Full design:
//! `docs/token-ownership-and-minters.md`.
//!
//! LINK is natively issued and minted on Ethereum only. On Stellar it is the
//! **remotely-issued burn-mint** representation: no initial supply is minted at
//! deploy time, and supply grows/shrinks 1:1 with bridge flow (mint on inbound,
//! burn on outbound).
//!
//! ## Owner / minters split (EVM parity)
//!
//! EVM `CrossChainToken` splits mint authority (`MINTER_ROLE` on the pool)
//! from the power to re-grant it (`BURN_MINT_ADMIN_ROLE` / two-step
//! `DEFAULT_ADMIN_ROLE` → MCMS). This contract mirrors that split with the
//! fleet's shared `Ownable` trait:
//!
//! - **Owner** — the deployer initially, MCMS after the two-step
//!   `transfer_ownership` → `accept_ownership`. Gates `set_admin`,
//!   `add_minter`, `remove_minter`, `set_authorized`.
//! - **Minters** — an ordered `MINTERS: Vec<Address>` set; `MINTERS[0]` is the
//!   **primary minter**, which the SAC read `admin()` returns. The burn-mint
//!   pool after the `set_admin` handoff (was: the deployer, who mints the
//!   pre-funding before the handoff). Secondary minters exist for the pool
//!   migration overlap window.
//! - `mint(to, amount)` keeps the fixed SAC ABI and authorizes the primary
//!   minter only — with no caller argument the token can authenticate exactly
//!   one stored address. `mint_as(caller, to, amount)` is the multi-minter
//!   path: the caller passes itself explicitly, `caller.require_auth()` proves
//!   it is in the call chain, and membership in `MINTERS` is then checked (the
//!   `advanced-pool-hooks` explicit-caller pattern).
//! - `set_admin(new)` is **owner-gated** (was admin-gated): `new` becomes
//!   `MINTERS[0]`, the old primary is demoted out of the set, and `new` is
//!   repositioned to the front if it was already a secondary minter.
//!
//! ## No local faucet
//!
//! LINK is deliberately **minted only via the minters set** (the burn-mint
//! pool, after the `set_admin` handoff) — never out of thin air on Stellar.
//! Unlike the BnM test token there is no `drip` (or any other permissionless
//! mint) entrypoint: every LINK on Stellar exists because a verified inbound
//! bridge message caused the pool to mint it, and every outbound transfer
//! burns it.
//!
//! ## Divergences from EVM LINK (deliberate)
//!
//! - **Decimals = 7**, the Stellar SAC convention (EVM LINK is 18). Cross-chain
//!   amounts are scaled by the pool's `calculate_local_amount` via
//!   `dest_pool_data`.
//! - **No transfer authorization gating.** EVM LINK is a plain ERC20 (no
//!   `set_authorized` transfer lock); this contract keeps `set_authorized` /
//!   `authorized` for SAC interface fidelity but does **not** gate transfers on
//!   it (`authorized` always returns `true`).
//! - **No clawback.** LINK is a plain ERC20-style token (matching EVM LINK,
//!   which has no clawback). The `clawback` entrypoint exists only because the
//!   `StellarAssetInterface` ABI requires it; it traps with
//!   `UnsupportedOperation` for every caller, owner included — nobody can ever
//!   seize LINK balances.

mod events;

use events::{
    ApproveEvent, BurnEvent, MintEvent, SetAdminEvent, SetAuthorizedEvent, TransferEvent,
};
use soroban_sdk::{
    contract, contracterror, contractimpl, contracttype, symbol_short, token, Address, Env,
    MuxedAddress, String, Symbol, Vec,
};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;

// ============================================================
// Storage keys
// ============================================================

/// One-shot initialization guard (instance storage).
const INIT: Symbol = symbol_short!("INIT");
/// Token owner — gates `set_admin` / `add_minter` / `remove_minter` /
/// `set_authorized` (instance storage; the `Ownable` trait's key).
const OWNER: Symbol = symbol_short!("OWNER");
/// Pending owner during the two-step ownership transfer (instance storage).
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
/// Ordered minters set (instance storage). `MINTERS[0]` is the primary minter
/// — what the SAC read `admin()` returns. Secondary minters (migration overlap
/// window) follow.
const MINTERS: Symbol = symbol_short!("MINTERS");
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
pub enum LinkError {
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
    /// A credit/debit entrypoint (`mint` / `approve` / `transfer` / `burn`)
    /// was given a negative amount.
    NegativeAmount = 5,
    /// A read entrypoint was called before `initialize`.
    NotInitialized = 6,
    /// An intentionally-disabled entrypoint was invoked — clawback is not
    /// supported (LINK is a plain ERC20-style token, matching EVM LINK,
    /// which has no clawback).
    UnsupportedOperation = 7,
    /// `mint_as` invoked by an address that is not in the minters set.
    NotMinter = 8,
    /// `add_minter` on an address already in the minters set.
    MinterAlreadyExists = 9,
    /// `remove_minter` on an address not in the minters set.
    MinterNotFound = 10,
    /// `remove_minter` on the primary minter — use `set_admin` instead (the
    /// old primary must be demoted by replacing the primary, not removed as
    /// a secondary).
    CannotRemovePrimaryMinter = 11,
}

// ============================================================
// Contract
// ============================================================

#[contract]
pub struct LinkTokenContract;

/// One-shot `INIT` guard shared by the token's own `initialize` and the
/// `Ownable` trait's `init_owner` (the token's `initialize` seeds both).
#[contractimpl]
impl Initializable for LinkTokenContract {
    const INITIALIZED: Symbol = INIT;
}

/// Owner/minter split (EVM `DEFAULT_ADMIN_ROLE` parity): two-step
/// `transfer_ownership` → `accept_ownership` moves control to MCMS while the
/// minters set (the pool) is left untouched.
#[contractimpl(contracttrait)]
impl Ownable for LinkTokenContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl(contracttrait)]
impl token::StellarAssetInterface for LinkTokenContract {
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

        if amount < 0 {
            env.panic_with_error(LinkError::NegativeAmount);
        }

        // SAC rule: expiration may not be in the past unless clearing to 0.
        if amount != 0 && expiration_ledger < env.ledger().sequence() {
            env.panic_with_error(LinkError::NegativeAmount);
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
            env.panic_with_error(LinkError::InsufficientAllowance);
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
            env.panic_with_error(LinkError::InsufficientAllowance);
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
        require_owner(&env);
        let previous = primary_minter(&env);

        // `new_admin` becomes MINTERS[0]; the old primary is demoted out of
        // the set (preserving LINK's "no faucet post-handoff" property — the
        // deployer cannot mint after set_admin(pool)). A `new_admin` that was
        // already a secondary minter (the migration overlap flow) is
        // repositioned to the front, not duplicated.
        let current = minters(&env);
        let mut updated: Vec<Address> = Vec::new(&env);
        updated.push_back(new_admin.clone());
        for m in current.iter() {
            if m != new_admin && m != previous {
                updated.push_back(m);
            }
        }
        env.storage().instance().set(&MINTERS, &updated);

        SetAdminEvent {
            admin: previous,
            new_admin,
        }
        .publish(&env);
    }

    fn admin(env: Env) -> Address {
        require_initialized(&env);
        primary_minter(&env)
    }

    fn set_authorized(env: Env, id: Address, authorize: bool) {
        require_owner(&env);
        env.storage()
            .persistent()
            .set(&DataKey::Authorized(id.clone()), &authorize);
        SetAuthorizedEvent { id, authorize }.publish(&env);
    }

    fn authorized(_env: Env, _id: Address) -> bool {
        // LINK is a plain ERC20-style token (matches EVM LINK, which has no
        // auth lock): every holder is always authorized. The `set_authorized`
        // entrypoint is retained for SAC interface fidelity but does not gate
        // transfers.
        true
    }

    fn mint(env: Env, to: Address, amount: i128) {
        let primary = primary_minter(&env);
        primary.require_auth();
        credit(&env, &to, amount);
        MintEvent { to, amount }.publish(&env);
    }

    fn clawback(env: Env, _from: Address, _amount: i128) {
        // Not supported: LINK is a plain ERC20-style token (matching EVM LINK,
        // which has no clawback). The entrypoint exists only because the
        // StellarAssetInterface ABI requires it; every caller — owner included
        // — traps with UnsupportedOperation. Nobody can ever seize LINK
        // balances.
        env.panic_with_error(LinkError::UnsupportedOperation);
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
impl LinkTokenContract {
    /// One-time initialization. Sets the token owner **and** primary minter
    /// (the deployer, who later hands off to the burn-mint pool via the
    /// owner-gated `set_admin`) and the ERC20 metadata. No initial supply is
    /// minted — LINK is remotely-issued; supply tracks bridge flow, and the
    /// only minters are the pool(s) in `MINTERS`.
    pub fn initialize(
        env: Env,
        admin: Address,
        name: String,
        symbol: String,
        decimals: u32,
    ) -> Result<(), LinkError> {
        if env.storage().instance().has(&INIT) {
            return Err(LinkError::AlreadyInitialized);
        }

        admin.require_auth();
        env.storage().instance().set(&INIT, &true);
        if let Err(e) = <Self as Ownable>::init_owner(&env, &admin) {
            env.panic_with_error(e);
        }
        let mut initial_minters: Vec<Address> = Vec::new(&env);
        initial_minters.push_back(admin.clone());
        env.storage().instance().set(&MINTERS, &initial_minters);
        env.storage().instance().set(&NAME, &name);
        env.storage().instance().set(&SYMBOL, &symbol);
        env.storage().instance().set(&DECIMALS, &decimals);
        Ok(())
    }

    /// Human-readable version tag (EVM `typeAndVersion` analogue).
    pub fn type_and_version(_env: Env) -> String {
        String::from_str(&_env, "LinkToken 1.0.0")
    }

    /// Multi-minter mint path (the pool's path). `caller` passes itself
    /// explicitly; `caller.require_auth()` proves it is in the call chain
    /// (only the invoking contract can satisfy that), and membership in
    /// `MINTERS` is then checked — the `advanced-pool-hooks`
    /// explicit-caller pattern. Any minter (primary or secondary) may mint
    /// through this entrypoint; there is no owner gate.
    pub fn mint_as(env: Env, caller: Address, to: Address, amount: i128) {
        require_initialized(&env);
        // Membership first, so non-minters get the typed NotMinter error
        // instead of a host auth failure.
        if !is_minter(&env, &caller) {
            env.panic_with_error(LinkError::NotMinter);
        }
        caller.require_auth();
        credit(&env, &to, amount);
        MintEvent { to, amount }.publish(&env);
    }

    /// Adds a **secondary** minter (owner-gated) — the migration overlap
    /// window: a new pool can be minting while the old one still is, before
    /// `set_admin(newPool)` re-points the primary. Mirrors EVM
    /// `grantMintAndBurnRoles` under `BURN_MINT_ADMIN_ROLE`.
    pub fn add_minter(env: Env, minter: Address) -> Result<(), LinkError> {
        require_owner(&env);
        if is_minter(&env, &minter) {
            return Err(LinkError::MinterAlreadyExists);
        }
        let mut updated = minters(&env);
        updated.push_back(minter);
        env.storage().instance().set(&MINTERS, &updated);
        Ok(())
    }

    /// Removes a **secondary** minter (owner-gated). The primary minter is
    /// demoted via `set_admin` instead (replacing the primary), not removed
    /// here.
    pub fn remove_minter(env: Env, minter: Address) -> Result<(), LinkError> {
        require_owner(&env);
        if minter == primary_minter(&env) {
            return Err(LinkError::CannotRemovePrimaryMinter);
        }
        let current = minters(&env);
        let mut updated: Vec<Address> = Vec::new(&env);
        let mut found = false;
        for m in current.iter() {
            if m == minter {
                found = true;
            } else {
                updated.push_back(m);
            }
        }
        if !found {
            return Err(LinkError::MinterNotFound);
        }
        env.storage().instance().set(&MINTERS, &updated);
        Ok(())
    }

    /// Read: the ordered minters set, primary (`MINTERS[0]`) first.
    pub fn get_minters(env: Env) -> Vec<Address> {
        require_initialized(&env);
        minters(&env)
    }
}

// ============================================================
// Internal helpers
// ============================================================

fn require_initialized(env: &Env) {
    if !env.storage().instance().has(&INIT) {
        env.panic_with_error(LinkError::NotInitialized);
    }
}

/// Requires the stored owner's authorization. Panics with `CCIPError::NotOwner`
/// (the `Ownable` trait's error) if unset, or a host auth failure if the owner
/// did not authorize this invocation.
fn require_owner(env: &Env) {
    if let Err(e) = <LinkTokenContract as Ownable>::require_owner(env) {
        env.panic_with_error(e);
    }
}

/// Returns the ordered minters set, panicking with `NotInitialized` if unset.
fn minters(env: &Env) -> Vec<Address> {
    env.storage()
        .instance()
        .get(&MINTERS)
        .unwrap_or_else(|| env.panic_with_error(LinkError::NotInitialized))
}

/// Returns the primary minter (`MINTERS[0]`) — what the SAC read `admin()`
/// returns and the only address the fixed-ABI `mint` authorizes.
fn primary_minter(env: &Env) -> Address {
    minters(env)
        .get(0)
        .unwrap_or_else(|| env.panic_with_error(LinkError::NotInitialized))
}

/// True iff `addr` is in the minters set (primary or secondary).
fn is_minter(env: &Env, addr: &Address) -> bool {
    for m in minters(env).iter() {
        if m == *addr {
            return true;
        }
    }
    false
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

/// Credits `amount` to `to`. Mints supply (no debit source). Panics on a
/// negative amount — a negative mint must not silently debit the recipient
/// (the guard here makes `mint` (and any future caller) negative-safe).
fn credit(env: &Env, to: &Address, amount: i128) {
    if amount < 0 {
        env.panic_with_error(LinkError::NegativeAmount);
    }
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
        env.panic_with_error(LinkError::NegativeAmount);
    }
    let bal: i128 = env
        .storage()
        .persistent()
        .get(&DataKey::Balance(from.clone()))
        .unwrap_or(0);
    if amount > bal {
        env.panic_with_error(LinkError::InsufficientBalance);
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
