#![no_std]

mod events;
pub mod types;

use common_authorization::{Ownable, Upgradeable};
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_helpers::{finality_codec, validation::Validatable};
use soroban_sdk::{
    contract, contractimpl, symbol_short, token, Address, BytesN, Env, Map, Symbol, Vec,
};
use types::{DynamicConfig, RemoteChainConfig, RemoteChainConfigArgs};

// ============================================================
// Storage Keys
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const DYNAMIC_CONFIG: Symbol = symbol_short!("DYNCFG");
const REMOTE_CHAINS: Symbol = symbol_short!("RCHAINS");
const ALLOWED_CCVS: Symbol = symbol_short!("ALWCCVS");
/// Immutable per-message CCV cap, set at `initialize` (EVM `i_maxCCVsPerMsg`).
const MAX_CCVS_PER_MSG: Symbol = symbol_short!("MAXCCVS");

// ============================================================
// Contract
// ============================================================

/// Executor — source-side fee/policy surface for the off-chain CCIP execution
/// service. Mirrors EVM `Executor.sol`. NOT the on-chain executor of the
/// receiver (destination execution is always `OffRamp → Router → receiver`);
/// this contract only quotes a flat USD-cent `get_fee` per destination, enforces
/// the executor layer of the 5-layer FTF opt-in matrix via
/// `allowed_finality_config`, gates CCVs through an optional allowlist + an
/// immutable per-message cap, and custody/sweeps accumulated fees.
///
/// Divergence from EVM: Soroban `Address` has no zero value, so the two EVM
/// executor sentinels (`address(0)` and `NO_EXECUTION_ADDRESS`) become
/// recognizable non-zero 32-byte sentinel Addresses in the OnRamp's
/// `GenericExtraArgsV3.executor` field (see `common_message::GenericExtraArgsV3`).
/// This contract itself is sentinel-agnostic — it is only ever called for
/// *concrete* executors; sentinel resolution happens in the OnRamp.
///
/// Divergence from `CommitteeVerifier`: this contract omits `CurseCheckable`
/// (EVM `Executor.sol` has no RMN dependency).
#[contract]
pub struct ExecutorContract;

#[contractimpl]
impl Initializable for ExecutorContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for ExecutorContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl(contracttrait)]
impl Upgradeable for ExecutorContract {}

#[contractimpl]
impl ExecutorContract {
    /// Initializes the Executor with `owner`, an immutable per-message CCV cap
    /// (`max_ccvs_per_msg`, EVM constructor `uint8 maxCCVsPerMsg` — widened to
    /// `u32` because Soroban has no `u8` arg type), and the initial dynamic
    /// config (fee aggregator, allowed finality, CCV allowlist flag).
    pub fn initialize(
        env: Env,
        owner: Address,
        max_ccvs_per_msg: u32,
        dynamic_config: DynamicConfig,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;

        // Mirrors EVM `if (maxCCVsPerMsg == 0) revert InvalidMaxPossibleCCVsPerMsg`.
        if max_ccvs_per_msg == 0 {
            return Err(CCIPError::InvalidConfig);
        }

        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;

        env.storage()
            .instance()
            .set(&MAX_CCVS_PER_MSG, &max_ccvs_per_msg);
        env.storage()
            .instance()
            .set(&DYNAMIC_CONFIG, &dynamic_config);
        env.storage()
            .instance()
            .set(&REMOTE_CHAINS, &Map::<u64, RemoteChainConfig>::new(&env));
        env.storage()
            .instance()
            .set(&ALLOWED_CCVS, &Vec::<Address>::new(&env));

        events::ConfigSetEvent {
            dynamic_config: dynamic_config.clone(),
        }
        .publish(&env);
        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "Executor 2.0.0-dev")
    }

    // ========================================
    // Destination chain config
    // ========================================

    /// Returns the immutable per-message CCV cap (EVM `getMaxCCVsPerMessage`).
    pub fn get_max_ccvs_per_message(env: Env) -> Result<u32, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&MAX_CCVS_PER_MSG)
            .ok_or(CCIPError::NotInitialized)
    }

    /// Returns the configured destination chains (EVM `getDestChains`).
    pub fn get_dest_chains(env: Env) -> Result<Vec<RemoteChainConfigArgs>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let remote_chains: Map<u64, RemoteChainConfig> = env
            .storage()
            .instance()
            .get(&REMOTE_CHAINS)
            .unwrap_or(Map::new(&env));

        let mut out = Vec::new(&env);
        for (selector, config) in remote_chains.iter() {
            out.push_back(RemoteChainConfigArgs {
                dest_chain_selector: selector,
                config,
            });
        }
        Ok(out)
    }

    /// Returns the config for a single destination chain. Errors if absent.
    pub fn get_dest_chain_config(
        env: Env,
        dest_chain_selector: u64,
    ) -> Result<RemoteChainConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let remote_chains: Map<u64, RemoteChainConfig> = env
            .storage()
            .instance()
            .get(&REMOTE_CHAINS)
            .unwrap_or(Map::new(&env));
        remote_chains
            .get(dest_chain_selector)
            .ok_or(CCIPError::DestinationChainNotEnabled)
    }

    /// Owner-only batch update of destination chains (EVM `applyDestChainUpdates`).
    /// Removals are applied first, then validated upserts.
    pub fn apply_dest_chain_updates(
        env: Env,
        dest_chain_selectors_to_remove: Vec<u64>,
        dest_chain_selectors_to_add: Vec<RemoteChainConfigArgs>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut remote_chains: Map<u64, RemoteChainConfig> = env
            .storage()
            .instance()
            .get(&REMOTE_CHAINS)
            .unwrap_or(Map::new(&env));

        for selector in dest_chain_selectors_to_remove.iter() {
            if remote_chains.remove(selector).is_some() {
                events::DestChainRemovedEvent {
                    dest_chain_selector: selector,
                }
                .publish(&env);
            }
        }

        for update in dest_chain_selectors_to_add.iter() {
            update.validate()?;
            remote_chains.set(update.dest_chain_selector, update.config.clone());
            events::DestChainAddedEvent {
                dest_chain_selector: update.dest_chain_selector,
                config: update.config.clone(),
            }
            .publish(&env);
        }

        env.storage().instance().set(&REMOTE_CHAINS, &remote_chains);
        Ok(())
    }

    // ========================================
    // Dynamic config
    // ========================================

    pub fn get_dynamic_config(env: Env) -> Result<DynamicConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&DYNAMIC_CONFIG)
            .ok_or(CCIPError::NotInitialized)
    }

    pub fn set_dynamic_config(env: Env, dynamic_config: DynamicConfig) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;
        env.storage()
            .instance()
            .set(&DYNAMIC_CONFIG, &dynamic_config);
        events::ConfigSetEvent {
            dynamic_config: dynamic_config.clone(),
        }
        .publish(&env);
        Ok(())
    }

    /// Returns the allowed finality config (EVM `getAllowedFinalityConfig`). This
    /// is the executor layer of the 5-layer FTF opt-in matrix.
    pub fn get_allowed_finality_config(env: Env) -> Result<u32, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Ok(Self::get_dynamic_config(env)?.allowed_finality_config)
    }

    // ========================================
    // CCV allowlist
    // ========================================

    /// Returns the CCV allowlist (EVM `getAllowedCCVs`).
    pub fn get_allowed_ccvs(env: Env) -> Vec<Address> {
        <Self as Initializable>::require_initialized(&env).unwrap();
        env.storage()
            .instance()
            .get(&ALLOWED_CCVS)
            .unwrap_or(Vec::new(&env))
    }

    /// Owner-only update of the CCV allowlist contents and enablement
    /// (EVM `applyAllowedCCVUpdates`). Removals first, then validated additions
    /// (zero-account CCVs rejected, mirroring EVM `if (ccv == address(0)) revert
    /// InvalidCCV`). Duplicates are skipped. The enablement flag is applied to
    /// the dynamic config last.
    pub fn apply_allowed_ccv_updates(
        env: Env,
        ccvs_to_remove: Vec<Address>,
        ccvs_to_add: Vec<Address>,
        ccv_allowlist_enabled: bool,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut allowed: Vec<Address> = env
            .storage()
            .instance()
            .get(&ALLOWED_CCVS)
            .unwrap_or(Vec::new(&env));

        // Removals.
        for to_remove in ccvs_to_remove.iter() {
            let mut filtered = Vec::new(&env);
            let mut removed = false;
            for i in 0..allowed.len() {
                if let Some(addr) = allowed.get(i) {
                    if addr == to_remove {
                        removed = true;
                    } else {
                        filtered.push_back(addr);
                    }
                }
            }
            if removed {
                events::CCVRemovedEvent {
                    ccv: to_remove.clone(),
                }
                .publish(&env);
            }
            allowed = filtered;
        }

        // Additions (dedup + zero-account rejection).
        for to_add in ccvs_to_add.iter() {
            if is_zero_fee_recipient(&env, &to_add) {
                return Err(CCIPError::InvalidAddress);
            }
            if !contains(&allowed, &to_add) {
                allowed.push_back(to_add.clone());
                events::CCVAddedEvent {
                    ccv: to_add.clone(),
                }
                .publish(&env);
            }
        }

        env.storage().instance().set(&ALLOWED_CCVS, &allowed);

        // Apply the enablement flag to the dynamic config.
        let mut dynamic = Self::get_dynamic_config(env.clone())?;
        if dynamic.ccv_allowlist_enabled != ccv_allowlist_enabled {
            dynamic.ccv_allowlist_enabled = ccv_allowlist_enabled;
            env.storage().instance().set(&DYNAMIC_CONFIG, &dynamic);
            events::CCVAllowlistUpdatedEvent {
                enabled: ccv_allowlist_enabled,
            }
            .publish(&env);
        }

        Ok(())
    }

    // ========================================
    // Fees
    // ========================================

    /// Validates that the executor can process the message and returns the flat
    /// USD-cent fee (EVM `IExecutor.getFee`). This is a **view** (no
    /// `require_auth`) so the OnRamp can call it cross-contract from its own
    /// `get_fee` quote path.
    ///
    /// Reverts (`DestinationChainNotEnabled`) if the dest chain is absent or
    /// disabled; reverts (`InvalidRequestedFinality`) if the requested finality
    /// is not permitted by `allowed_finality_config` (the executor layer of the
    /// FTF opt-in matrix — H-8); reverts (`CCVNotAllowed`) if the allowlist is
    /// enabled and a supplied CCV is not on it; reverts (`ExceedsMaxCCVs`) if the
    /// supplied CCV count exceeds the immutable cap.
    ///
    /// `extra_args` and `fee_token` are accepted for wire-shape parity with EVM
    /// `IExecutor.getFee` and are unused (EVM ignores them too).
    pub fn get_fee(
        env: Env,
        dest_chain_selector: u64,
        requested_finality_config: u32,
        ccv_addresses: Vec<Address>,
        _extra_args: soroban_sdk::Bytes,
        _fee_token: Address,
    ) -> Result<u32, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let cfg = Self::get_dest_chain_config(env.clone(), dest_chain_selector)?;
        if !cfg.enabled {
            return Err(CCIPError::DestinationChainNotEnabled);
        }

        let dynamic = Self::get_dynamic_config(env.clone())?;

        // H-8 / INV-FIN-EXEC-1/2: executor-layer FTF opt-in enforcement.
        finality_codec::ensure_requested_finality_allowed(
            requested_finality_config,
            dynamic.allowed_finality_config,
        )?;

        // CCV allowlist (EVM `if (s_dynamicConfig.ccvAllowlistEnabled) ... revert InvalidCCV`).
        if dynamic.ccv_allowlist_enabled {
            let allowed = env
                .storage()
                .instance()
                .get(&ALLOWED_CCVS)
                .unwrap_or(Vec::new(&env));
            for i in 0..ccv_addresses.len() {
                if let Some(ccv) = ccv_addresses.get(i) {
                    if !contains(&allowed, &ccv) {
                        return Err(CCIPError::CCVNotAllowed);
                    }
                }
            }
        }

        // Immutable per-message cap (EVM `if (ccvs.length > i_maxCCVsPerMsg) revert ExceedsMaxCCVs`).
        let max = Self::get_max_ccvs_per_message(env)?;
        if ccv_addresses.len() > max {
            return Err(CCIPError::ExceedsMaxCCVs);
        }

        Ok(cfg.usd_cents_fee)
    }

    /// Withdraws outstanding fee token balances to the configured fee aggregator.
    /// Permissionless: only transfers to the trusted aggregator address (same
    /// model as EVM `FeeTokenHandler._withdrawFeeTokens`).
    ///
    /// # Errors
    /// * [`CCIPError::ZeroFeeAggregatorNotAllowed`] — dynamic config has no fee
    ///   aggregator or it is the zero account (mirrors EVM, where a zero
    ///   aggregator intentionally reverts withdrawals).
    pub fn withdraw_fee_tokens(env: Env, fee_tokens: Vec<Address>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let dynamic = Self::get_dynamic_config(env.clone())?;
        let fee_agg = dynamic
            .fee_aggregator
            .ok_or(CCIPError::ZeroFeeAggregatorNotAllowed)?;
        if is_zero_fee_recipient(&env, &fee_agg) {
            return Err(CCIPError::ZeroFeeAggregatorNotAllowed);
        }

        let self_address = env.current_contract_address();
        for i in 0..fee_tokens.len() {
            if let Some(fee_token) = fee_tokens.get(i) {
                let token_client = token::Client::new(&env, &fee_token);
                let balance = token_client.balance(&self_address);
                if balance > 0 {
                    token_client.transfer(&self_address, &fee_agg, &balance);
                    // Mirrors EVM `FeeTokenHandler._withdrawFeeTokens` emitting
                    // `FeeTokenWithdrawn(receiver, feeToken, amount)` per sweep.
                    events::FeeTokenWithdrawnEvent {
                        receiver: fee_agg.clone(),
                        fee_token: fee_token.clone(),
                        amount: balance,
                    }
                    .publish(&env);
                }
            }
        }

        Ok(())
    }
}

// ============================================================
// Helpers
// ============================================================

/// True iff `addr` is the zero Stellar account (EVM `address(0)` parity for
/// fee-recipient and CCV rejection). Mirrors `committee_verifier::is_zero_fee_recipient`.
fn is_zero_fee_recipient(env: &Env, addr: &Address) -> bool {
    addr == &Address::from_str(
        env,
        "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
    )
}

/// Linear membership test for a `Vec<Address>` (Soroban `Vec` has no `contains`).
fn contains(haystack: &Vec<Address>, needle: &Address) -> bool {
    for i in 0..haystack.len() {
        if let Some(addr) = haystack.get(i) {
            if addr == *needle {
                return true;
            }
        }
    }
    false
}

mod test;
