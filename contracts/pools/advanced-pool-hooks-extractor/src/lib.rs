#![no_std]

//! # Advanced Pool Hooks Extractor
//!
//! Stellar analogue of EVM
//! [`AdvancedPoolHooksExtractor`](https://github.com/smartcontractkit/chainlink-ccip/blob/develop/chains/evm/contracts/pools/extractors/AdvancedPoolHooksExtractor.sol).
//!
//! A policy extractor for the `advanced-pool-hooks` target. The policy engine
//! routes a [`Payload`] whose `selector` is `preflight_check` / `postflight_check`
//! to this contract's [`IExtractor::extract`], which projects the typed
//! [`PoolHooksPayloadData`] into the canonical named [`Parameter`] array a policy
//! chain evaluates.
//!
//! ## Why a separate contract
//!
//! On EVM the extractor is its own contract because the pool-hooks contract is
//! already near the deploy size limit. The same separation is preserved here:
//! the extractor is a standalone crate/contract, registered with the (future)
//! policy engine, not linked into the hooks contract.
//!
//! ## Typed projection vs EVM byte decoding
//!
//! EVM's `Payload.data` is the raw calldata (`msg.data[4:]`), so the extractor
//! ABI-decodes it. Soroban cross-contract calls pass typed `Val`s, so
//! [`Payload::data`] is already a typed [`PolicyData::PoolHooks`](
//! `crate::PolicyData::PoolHooks`) carrying a [`PoolHooksPayloadData`]. This
//! extractor therefore *projects* fields to `Parameter[]` instead of decoding
//! bytes — same output parameters, canonical names, and `context`-skip as EVM,
//! without the encode/decode round-trip that only exists because of EVM's
//! raw-calldata boundary.
//!
//! ## Canonical parameter names
//!
//! Preflight (7): `from`, `to`, `amount`, `amount_post_fee`,
//! `remote_chain_selector`, `token`, `requested_finality`.
//!
//! Postflight (9): `from`, `to`, `amount`, `remote_chain_selector`, `token`,
//! `requested_finality`, `source_pool_address`, `source_pool_data`,
//! `source_denominated_amount`.
//!
//! `context` (`token_args` / `offchain_token_data`) is skipped, exactly as EVM
//! skips it — it is a side-channel, not policy input.

use common_error::CCIPError;
use common_interfaces::extractor::IExtractor;
use common_interfaces::policy_engine::{Parameter, Payload, PolicyData, PolicyValue};
use common_interfaces::pool_hooks::{PoolHooksPayloadData, PostflightPayload, PreflightPayload};
use soroban_sdk::{contract, contractimpl, symbol_short, vec, Env, String, Symbol, Vec};

#[contract]
pub struct AdvancedPoolHooksExtractorContract;

/// Canonical parameter-name keys (EVM `PARAM_*`, as `Symbol` instead of
/// `keccak256(name)`). The short ones use `symbol_short!` (≤9 chars); the rest
/// are built with `Symbol::new` at call time in [`param`].
const PARAM_FROM: Symbol = symbol_short!("from");
const PARAM_TO: Symbol = symbol_short!("to");
const PARAM_AMOUNT: Symbol = symbol_short!("amount");
const PARAM_TOKEN: Symbol = symbol_short!("token");

#[contractimpl]
impl IExtractor for AdvancedPoolHooksExtractorContract {
    fn extract(env: Env, payload: Payload) -> Result<Vec<Parameter>, CCIPError> {
        // The engine routes by `selector` (EVM `bytes4 msg.sig`); the typed
        // `data` is the variant that selector denotes. Dispatch on `selector` —
        // EVM-faithful — and defensively confirm the `data` variant matches it,
        // so a malformed payload (selector/data mismatch) is rejected rather than
        // silently mis-projected. Anything unhandled => `UnsupportedSelector`.
        let preflight_sel = Symbol::new(&env, "preflight_check");
        let postflight_sel = Symbol::new(&env, "postflight_check");
        match payload.data {
            PolicyData::PoolHooks(data) => match payload.selector {
                s if s == preflight_sel => match data {
                    PoolHooksPayloadData::Preflight(p) => Ok(Self::preflight_params(&env, &p)),
                    _ => Err(CCIPError::UnsupportedSelector),
                },
                s if s == postflight_sel => match data {
                    PoolHooksPayloadData::Postflight(p) => Ok(Self::postflight_params(&env, &p)),
                    _ => Err(CCIPError::UnsupportedSelector),
                },
                _ => Err(CCIPError::UnsupportedSelector),
            },
        }
    }

    fn type_and_version(env: Env) -> String {
        String::from_str(&env, "AdvancedPoolHooksExtractor 2.0.0")
    }
}

impl AdvancedPoolHooksExtractorContract {
    /// Preflight → 7 named params (EVM `_extractPreflightParameters`).
    ///
    /// - `from`   = `lock_or_burn_in.original_sender` (EVM `originalSender`, address)
    /// - `to`     = `lock_or_burn_in.receiver`        (EVM `receiver`, bytes)
    /// - `amount` = `lock_or_burn_in.amount`
    /// - `amount_post_fee` = `PreflightPayload.amount_post_fee` (EVM `amountPostFee`)
    /// - `remote_chain_selector` = `lock_or_burn_in.remote_chain_selector`
    /// - `token`  = `lock_or_burn_in.local_token`     (EVM `localToken`, address)
    /// - `requested_finality` = `PreflightPayload.requested_finality`
    fn preflight_params(env: &Env, p: &PreflightPayload) -> Vec<Parameter> {
        let lob = &p.lock_or_burn_in;
        let rcs = Symbol::new(env, "remote_chain_selector");
        let apf = Symbol::new(env, "amount_post_fee");
        let rf = Symbol::new(env, "requested_finality");
        vec![
            env,
            Parameter {
                name: PARAM_FROM,
                value: PolicyValue::Address(lob.original_sender.clone()),
            },
            Parameter {
                name: PARAM_TO,
                value: PolicyValue::Bytes(lob.receiver.clone()),
            },
            Parameter {
                name: PARAM_AMOUNT,
                value: PolicyValue::I128(lob.amount),
            },
            Parameter {
                name: apf,
                value: PolicyValue::I128(p.amount_post_fee),
            },
            Parameter {
                name: rcs,
                value: PolicyValue::U64(lob.remote_chain_selector),
            },
            Parameter {
                name: PARAM_TOKEN,
                value: PolicyValue::Address(lob.local_token.clone()),
            },
            Parameter {
                name: rf,
                value: PolicyValue::U32(p.requested_finality),
            },
        ]
    }

    /// Postflight → 9 named params (EVM `_extractPostflightParameters`).
    ///
    /// - `from`   = `release_or_mint_in.original_sender` (EVM `originalSender`, bytes)
    /// - `to`     = `release_or_mint_in.receiver`        (EVM `receiver`, address)
    /// - `amount` = `PostflightPayload.local_amount`     (EVM `localAmount` — the scalar
    ///   minted/released locally, NOT `release_or_mint_in.amount`)
    /// - `remote_chain_selector` = `release_or_mint_in.remote_chain_selector`
    /// - `token`  = `release_or_mint_in.local_token`     (EVM `localToken`, address)
    /// - `requested_finality` = `PostflightPayload.requested_finality`
    /// - `source_pool_address` = `release_or_mint_in.source_pool_address` (bytes)
    /// - `source_pool_data`    = `release_or_mint_in.source_pool_data`    (bytes)
    /// - `source_denominated_amount` = `release_or_mint_in.amount`
    ///   (EVM `sourceDenominatedAmount` — Stellar's `ReleaseOrMintIn.amount` *is*
    ///   the source-denominated amount, per `token_pool.rs`.)
    fn postflight_params(env: &Env, p: &PostflightPayload) -> Vec<Parameter> {
        let rom = &p.release_or_mint_in;
        let rcs = Symbol::new(env, "remote_chain_selector");
        let rf = Symbol::new(env, "requested_finality");
        let spa = Symbol::new(env, "source_pool_address");
        let spd = Symbol::new(env, "source_pool_data");
        let sda = Symbol::new(env, "source_denominated_amount");
        vec![
            env,
            Parameter {
                name: PARAM_FROM,
                value: PolicyValue::Bytes(rom.original_sender.clone()),
            },
            Parameter {
                name: PARAM_TO,
                value: PolicyValue::Address(rom.receiver.clone()),
            },
            Parameter {
                name: PARAM_AMOUNT,
                value: PolicyValue::I128(p.local_amount),
            },
            Parameter {
                name: rcs,
                value: PolicyValue::U64(rom.remote_chain_selector),
            },
            Parameter {
                name: PARAM_TOKEN,
                value: PolicyValue::Address(rom.local_token.clone()),
            },
            Parameter {
                name: rf,
                value: PolicyValue::U32(p.requested_finality),
            },
            Parameter {
                name: spa,
                value: PolicyValue::Bytes(rom.source_pool_address.clone()),
            },
            Parameter {
                name: spd,
                value: PolicyValue::Bytes(rom.source_pool_data.clone()),
            },
            Parameter {
                name: sda,
                value: PolicyValue::I128(rom.amount),
            },
        ]
    }
}

#[cfg(test)]
mod test;
