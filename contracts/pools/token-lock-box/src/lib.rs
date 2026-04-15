#![no_std]

//! Stellar analogue of EVM [`ERC20LockBox`](https://github.com/smartcontractkit/chainlink-ccip/blob/develop/chains/evm/contracts/pools/ERC20LockBox.sol).
//!
//! Per-token lockbox that holds Stellar token liquidity so **pools can be upgraded
//! without migrating funds**. Only allowed callers (typically token pool contracts)
//! can deposit / withdraw; the owner manages the allowlist.
//!
//! Key differences from EVM:
//! - Soroban has no `msg.sender`; caller identity is established via `require_auth`.
//! - `deposit` expects the **caller** to have approved this lockbox as spender
//!   for `amount` (short-lived `approve` is enough). The lockbox pulls via
//!   `token::transfer_from(lockbox, caller, lockbox, amount)` so the allowance
//!   is consumed and is not left outstanding after a successful deposit.
//! - `withdraw` transfers **from the lockbox** to `recipient`; the lockbox itself
//!   authorises via `env.current_contract_address()`.

mod events;

use soroban_sdk::{contract, contractimpl, symbol_short, token, Address, Env, Symbol, Vec};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use events::{DepositEvent, WithdrawalEvent};

const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
/// The single token this lockbox manages.
const TOKEN: Symbol = symbol_short!("TOKEN");
/// Authorised callers (pool addresses) that may deposit / withdraw.
const CALLERS: Symbol = symbol_short!("CALLERS");

#[contract]
pub struct TokenLockBox;

#[contractimpl]
impl Initializable for TokenLockBox {
    const INITIALIZED: Symbol = symbol_short!("INIT");
}

#[contractimpl(contracttrait)]
impl Ownable for TokenLockBox {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl]
impl TokenLockBox {
    /// One-time setup. `owner` governs the allowlist; `token` is the single
    /// asset this lockbox holds (EVM `i_token`).
    pub fn initialize(env: Env, owner: Address, token: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;
        env.storage().instance().set(&TOKEN, &token);
        let empty: Vec<Address> = Vec::new(&env);
        env.storage().instance().set(&CALLERS, &empty);
        Ok(())
    }

    // ------------------------------------------------------------------
    // Caller management (owner-only)
    // ------------------------------------------------------------------

    pub fn add_allowed_callers(env: Env, callers: Vec<Address>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        let mut current = load_callers(&env);
        for c in callers.iter() {
            if !vec_contains(&current, &c) {
                current.push_back(c);
            }
        }
        env.storage().instance().set(&CALLERS, &current);
        Ok(())
    }

    pub fn remove_allowed_callers(env: Env, callers: Vec<Address>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        let current = load_callers(&env);
        let mut out: Vec<Address> = Vec::new(&env);
        for existing in current.iter() {
            if !vec_contains(&callers, &existing) {
                out.push_back(existing);
            }
        }
        env.storage().instance().set(&CALLERS, &out);
        Ok(())
    }

    pub fn get_allowed_callers(env: Env) -> Vec<Address> {
        load_callers(&env)
    }

    // ------------------------------------------------------------------
    // Core operations
    // ------------------------------------------------------------------

    /// Deposit tokens into this lockbox (EVM `ILockBox.deposit`).
    ///
    /// `caller` must be an allowed address and must have authorised this
    /// invocation. The caller must have `approve`d this lockbox contract for
    /// at least `amount`; funds are pulled with `transfer_from` so allowance is
    /// reduced (typically to zero when the approved amount equals `amount`).
    pub fn deposit(env: Env, caller: Address, amount: i128) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        require_allowed_caller(&env, &caller)?;
        if amount <= 0 {
            return Err(CCIPError::InvalidTokenAmount);
        }
        let tok = get_token(&env)?;
        let token_client = token::Client::new(&env, &tok);
        let lockbox = env.current_contract_address();
        token_client.transfer_from(&lockbox, &caller, &lockbox, &amount);
        DepositEvent {
            token: tok,
            depositor: caller,
            amount,
        }
        .publish(&env);
        Ok(())
    }

    /// Withdraw tokens to `recipient` (EVM `ILockBox.withdraw`).
    ///
    /// `caller` must be an allowed address. The lockbox transfers from its
    /// own balance to `recipient`.
    pub fn withdraw(
        env: Env,
        caller: Address,
        amount: i128,
        recipient: Address,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        require_allowed_caller(&env, &caller)?;
        if amount <= 0 {
            return Err(CCIPError::InvalidTokenAmount);
        }
        let tok = get_token(&env)?;
        let token_client = token::Client::new(&env, &tok);
        let balance = token_client.balance(&env.current_contract_address());
        if amount > balance {
            return Err(CCIPError::InsufficientPoolLiquidity);
        }
        token_client.transfer(&env.current_contract_address(), &recipient, &amount);
        WithdrawalEvent {
            token: tok,
            recipient,
            amount,
        }
        .publish(&env);
        Ok(())
    }

    // ------------------------------------------------------------------
    // View helpers
    // ------------------------------------------------------------------

    pub fn get_token(env: Env) -> Result<Address, CCIPError> {
        get_token(&env)
    }

    pub fn is_token_supported(env: Env, token: Address) -> Result<bool, CCIPError> {
        let tok = get_token(&env)?;
        Ok(tok == token)
    }
}

// ============================================================
// Internal helpers
// ============================================================

fn get_token(env: &Env) -> Result<Address, CCIPError> {
    env.storage()
        .instance()
        .get(&TOKEN)
        .ok_or(CCIPError::NotInitialized)
}

fn load_callers(env: &Env) -> Vec<Address> {
    env.storage()
        .instance()
        .get(&CALLERS)
        .unwrap_or_else(|| Vec::new(env))
}

fn vec_contains(v: &Vec<Address>, needle: &Address) -> bool {
    for i in 0..v.len() {
        if v.get(i).unwrap() == *needle {
            return true;
        }
    }
    false
}

fn require_allowed_caller(env: &Env, caller: &Address) -> Result<(), CCIPError> {
    caller.require_auth();
    if !vec_contains(&load_callers(env), caller) {
        return Err(CCIPError::CallerNotAuthorized);
    }
    Ok(())
}

#[cfg(test)]
mod test;
