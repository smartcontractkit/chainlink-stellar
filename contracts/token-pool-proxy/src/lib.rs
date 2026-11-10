#![no_std]

//! # Token Pool Proxy
//!
//! SAC admin proxy that lets CCIP token pools (or any other allowlisted
//! contract / account) mint a Stellar Asset without holding the asset's admin
//! role themselves.
//!
//! ## Problem
//!
//! A Stellar Asset Contract (SAC) authorizes `mint` / `clawback` /
//! `set_authorized` / `set_admin` against exactly **one** address: its admin
//! (the issuer by default). A burn-mint token pool cannot be handed that role,
//! so it cannot mint inbound cross-chain transfers directly.
//!
//! ## Pattern
//!
//! This contract is the standard Stellar "SAC admin wrapper" (OpenZeppelin
//! `stellar_tokens::fungible::sac_admin_wrapper`: the issuer calls `sac.set_admin(proxy)`,
//! and the proxy exposes the [`SACAdminWrapper`] ABI — each privileged function takes an
//! extra `operator` argument naming the address that authorizes the call.
//!
//! - **Owner** ([`Ownable`], two-step transfer): manages the caller allowlist
//!   and gates `set_admin` / `set_authorized` / `clawback`.
//! - **Allowlisted callers**: may `mint` (and `burn`). Each caller carries an
//!   optional **mint allowance** (Circle FiatToken `minterAllowance`
//!   semantics): every mint decrements it, the owner tops it up by
//!   re-applying the caller with a new value, and `None` means unlimited.
//!   Burns do not touch the allowance.
//!
//! ## Deployment caveats
//!
//! - The issuer account can still mint / claw back via classic operations
//!   while it is unlocked. Lock the issuer (master weight 0) **after**
//!   `set_admin(proxy)` — locking it first makes the admin unchangeable.
//! - `set_admin` does not validate the new admin address; a wrong address
//!   permanently loses SAC administration.

mod events;

use soroban_sdk::{
    contract, contractimpl, contracttype, symbol_short, Address, BytesN, Env, String, Symbol, Vec,
};
use stellar_tokens::fungible::sac_admin_wrapper::{self, SACAdminWrapper};

use common_authorization::{Ownable, Upgradeable};
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use events::{
    AssetAdminChangedEvent, BurnedEvent, CallerRemovedEvent, CallerUpdatedEvent, MintedEvent,
};

// ============================================================
// Storage keys
// ============================================================

const INIT: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
/// Listing index of allowlisted callers (instance storage). Membership itself
/// is the presence of a [`DataKey::Caller`] entry.
const CALLERS: Symbol = symbol_short!("CALLERS");

/// Persistent-entry TTL threshold (~7 days at 5s ledgers), matching MCMS.
const LEDGER_THRESHOLD: u32 = 120_960;

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum DataKey {
    /// Per-caller config (persistent storage).
    Caller(Address),
}

// ============================================================
// Types
// ============================================================

/// Configuration of one allowlisted caller.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CallerConfig {
    /// Remaining amount this caller may mint. `None` = unlimited.
    pub mint_allowance: Option<i128>,
}

/// Owner-supplied add / overwrite of an allowlisted caller.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CallerUpdate {
    pub caller: Address,
    /// New remaining mint allowance (replaces any previous value). `None` = unlimited.
    pub mint_allowance: Option<i128>,
}

// ============================================================
// Contract
// ============================================================

#[contract]
pub struct TokenPoolProxy;

#[contractimpl]
impl Initializable for TokenPoolProxy {
    const INITIALIZED: Symbol = INIT;
}

#[contractimpl(contracttrait)]
impl Ownable for TokenPoolProxy {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl(contracttrait)]
impl Upgradeable for TokenPoolProxy {}

/// The OpenZeppelin SAC admin ABI. Methods return `()` (fixed by the trait),
/// so failures panic with a typed [`CCIPError`] (the `bnm-token` SAC-ABI style).
#[contractimpl]
impl SACAdminWrapper for TokenPoolProxy {
    /// Moves the asset's SAC admin role from this proxy to `new_admin`
    /// (owner-only; `operator` must be the owner). This is the migration
    /// escape hatch — after it, this proxy can no longer mint.
    ///
    /// The SAC does **not** validate `new_admin`; a wrong address permanently
    /// loses SAC administration.
    fn set_admin(e: Env, new_admin: Address, operator: Address) {
        require_owner_operator(&e, &operator);
        sac_admin_wrapper::set_admin(&e, &new_admin);
        AssetAdminChangedEvent { new_admin }.publish(&e);
    }

    /// Forwards SAC `set_authorized` (owner-only; `operator` must be the owner).
    fn set_authorized(e: Env, id: Address, authorize: bool, operator: Address) {
        require_owner_operator(&e, &operator);
        sac_admin_wrapper::set_authorized(&e, &id, authorize);
    }

    /// Mints `amount` of the asset to `to` on behalf of the allowlisted
    /// `operator`, consuming its mint allowance when one is set.
    ///
    /// `operator` passes itself explicitly; `operator.require_auth()` proves it
    /// authorized this invocation (a calling contract satisfies this
    /// implicitly as the direct invoker).
    ///
    /// # Panics
    /// * `InvalidTokenAmount` — `amount <= 0`
    /// * `CallerNotAuthorized` — `operator` is not allowlisted
    /// * `MintAllowanceExceeded` — `amount` exceeds the remaining allowance
    /// * host auth error — `operator` did not authorize this invocation
    fn mint(e: Env, to: Address, amount: i128, operator: Address) {
        unwrap_or_panic(&e, <Self as Initializable>::require_initialized(&e));
        if amount <= 0 {
            e.panic_with_error(CCIPError::InvalidTokenAmount);
        }
        // Membership before auth, so unknown callers get a typed error instead
        // of a host auth failure.
        let config = unwrap_or_panic(&e, require_allowed_caller(&e, &operator));

        let remaining_allowance = match config.mint_allowance {
            Some(allowance) => {
                if amount > allowance {
                    e.panic_with_error(CCIPError::MintAllowanceExceeded);
                }
                let remaining = allowance - amount;
                write_caller(
                    &e,
                    &operator,
                    &CallerConfig {
                        mint_allowance: Some(remaining),
                    },
                );
                Some(remaining)
            }
            None => None,
        };

        sac_admin_wrapper::mint(&e, &to, amount);

        MintedEvent {
            caller: operator,
            to,
            amount,
            remaining_allowance,
        }
        .publish(&e);
    }

    /// Forwards SAC `clawback` (owner-only; `operator` must be the owner).
    /// Only works if the asset has clawback enabled.
    fn clawback(e: Env, from: Address, amount: i128, operator: Address) {
        require_owner_operator(&e, &operator);
        sac_admin_wrapper::clawback(&e, &from, amount);
    }
}

#[contractimpl]
impl TokenPoolProxy {
    /// One-time setup. `owner` governs the allowlist and SAC admin functions;
    /// `asset` is the SAC this proxy administers.
    ///
    /// Does not require the proxy to already be the SAC admin: the issuer runs
    /// `sac.set_admin(proxy)` after deployment, and mints fail until it does.
    pub fn initialize(env: Env, owner: Address, asset: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;
        sac_admin_wrapper::set_sac_address(&env, &asset);
        env.storage()
            .instance()
            .set(&CALLERS, &Vec::<Address>::new(&env));
        Ok(())
    }

    /// Burns `amount` of the asset from `from` on behalf of the allowlisted
    /// `operator`. Does not affect the mint allowance.
    ///
    /// A SAC burn needs no admin rights — the SAC requires `from` to authorize
    /// `burn(from, amount)`. This entrypoint exists so callers get a single
    /// mint/burn surface; `from`'s authorization tree must cover the nested
    /// SAC `burn` invocation.
    ///
    /// # Errors
    /// * `InvalidTokenAmount` — `amount <= 0`
    /// * `CallerNotAuthorized` — `operator` is not allowlisted
    pub fn burn(env: Env, operator: Address, from: Address, amount: i128) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        if amount <= 0 {
            return Err(CCIPError::InvalidTokenAmount);
        }
        require_allowed_caller(&env, &operator)?;

        sac_admin_wrapper::get_sac_client(&env).burn(&from, &amount);

        BurnedEvent {
            caller: operator,
            from,
            amount,
        }
        .publish(&env);
        Ok(())
    }

    /// Owner-only allowlist maintenance. `removes` are applied first, then
    /// `adds`. Adding an already-allowlisted caller **overwrites** its mint
    /// allowance (this is how the owner tops a caller up). Removing an unknown
    /// caller is a no-op.
    ///
    /// # Errors
    /// * `InvalidConfig` — an add carries a negative mint allowance
    /// * `NotOwner` — owner not set
    pub fn apply_caller_updates(
        env: Env,
        adds: Vec<CallerUpdate>,
        removes: Vec<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut callers = load_callers(&env);

        for caller in removes.iter() {
            let key = DataKey::Caller(caller.clone());
            if env.storage().persistent().has(&key) {
                env.storage().persistent().remove(&key);
                if let Some(idx) = callers.first_index_of(&caller) {
                    callers.remove(idx);
                }
                CallerRemovedEvent { caller }.publish(&env);
            }
        }

        for update in adds.iter() {
            if let Some(allowance) = update.mint_allowance {
                if allowance < 0 {
                    return Err(CCIPError::InvalidConfig);
                }
            }
            write_caller(
                &env,
                &update.caller,
                &CallerConfig {
                    mint_allowance: update.mint_allowance,
                },
            );
            if !callers.contains(&update.caller) {
                callers.push_back(update.caller.clone());
            }
            CallerUpdatedEvent {
                caller: update.caller,
                mint_allowance: update.mint_allowance,
            }
            .publish(&env);
        }

        env.storage().instance().set(&CALLERS, &callers);
        Ok(())
    }

    // ------------------------------------------------------------------
    // Views
    // ------------------------------------------------------------------

    /// Config of `caller`, or `None` if it is not allowlisted.
    pub fn get_caller_config(env: Env, caller: Address) -> Option<CallerConfig> {
        read_caller(&env, &caller)
    }

    pub fn is_allowed_caller(env: Env, caller: Address) -> bool {
        read_caller(&env, &caller).is_some()
    }

    pub fn get_allowed_callers(env: Env) -> Vec<Address> {
        load_callers(&env)
    }

    /// The SAC this proxy administers.
    pub fn get_asset(env: Env) -> Result<Address, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Ok(sac_admin_wrapper::get_sac_address(&env))
    }

    pub fn type_and_version(env: Env) -> String {
        String::from_str(&env, "TokenPoolProxy 1.0.0")
    }
}

// ============================================================
// Internal helpers
// ============================================================

fn unwrap_or_panic<T>(env: &Env, res: Result<T, CCIPError>) -> T {
    match res {
        Ok(v) => v,
        Err(e) => env.panic_with_error(e),
    }
}

/// `operator` must be the stored owner and must have authorized this call.
fn require_owner_operator(env: &Env, operator: &Address) {
    unwrap_or_panic(
        env,
        <TokenPoolProxy as Initializable>::require_initialized(env),
    );
    if !<TokenPoolProxy as Ownable>::is_owner(env, operator) {
        env.panic_with_error(CCIPError::NotOwner);
    }
    operator.require_auth();
}

/// Membership check first (typed error), then `require_auth` on `caller`.
fn require_allowed_caller(env: &Env, caller: &Address) -> Result<CallerConfig, CCIPError> {
    let config = read_caller(env, caller).ok_or(CCIPError::CallerNotAuthorized)?;
    caller.require_auth();
    Ok(config)
}

fn read_caller(env: &Env, caller: &Address) -> Option<CallerConfig> {
    let key = DataKey::Caller(caller.clone());
    let config: Option<CallerConfig> = env.storage().persistent().get(&key);
    if config.is_some() {
        env.storage()
            .persistent()
            .extend_ttl(&key, LEDGER_THRESHOLD, env.storage().max_ttl());
    }
    config
}

fn write_caller(env: &Env, caller: &Address, config: &CallerConfig) {
    let key = DataKey::Caller(caller.clone());
    env.storage().persistent().set(&key, config);
    env.storage()
        .persistent()
        .extend_ttl(&key, LEDGER_THRESHOLD, env.storage().max_ttl());
}

fn load_callers(env: &Env) -> Vec<Address> {
    env.storage()
        .instance()
        .get(&CALLERS)
        .unwrap_or_else(|| Vec::new(env))
}

#[cfg(test)]
mod test;
