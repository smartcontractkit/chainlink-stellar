//! Extractor interface — Stellar analogue of EVM
//! [`IExtractor`](https://github.com/smartcontractkit/ace/blob/main/packages/policy-management/src/interfaces/IExtractor.sol).
//!
//! A target-specific contract that decodes a [`Payload`](crate::policy_engine::Payload)
//! into the named [`Parameter`](crate::policy_engine::Parameter) array a policy
//! chain evaluates. The policy engine looks up the extractor registered for
//! `payload.selector` and calls [`IExtractor::extract`].
//!
//! Hand-written (not generated) and survives `make generate-interfaces`, like
//! `pool_hooks.rs`. The pool-hooks extractor contract lives at
//! `contracts/pools/advanced-pool-hooks-extractor`.
//!
//! # Canonical parameter names
//!
//! Extractors emit parameters under canonical `Symbol` names so policies are
//! portable. The pool-hooks extractor uses these (EVM `AdvancedPoolHooksExtractor`
//! `PARAM_*` keys, expressed as `Symbol` rather than `keccak256`-of-name):
//!
//! Preflight (7): `from`, `to`, `amount`, `amount_post_fee`,
//! `remote_chain_selector`, `token`, `requested_finality`.
//!
//! Postflight (9): `from`, `to`, `amount`, `remote_chain_selector`, `token`,
//! `requested_finality`, `source_pool_address`, `source_pool_data`,
//! `source_denominated_amount`.

use crate::policy_engine::{Parameter, Payload};
use common_error::CCIPError;
use soroban_sdk::{contractclient, Vec};

/// Cross-contract client interface for a policy extractor (EVM `IExtractor`).
#[contractclient(name = "ExtractorClient")]
pub trait IExtractor {
    /// Extract named parameters from `payload` for policy evaluation.
    /// Returns `Err(UnsupportedSelector)` when this extractor does not handle
    /// `payload.selector` (EVM `IPolicyEngine.UnsupportedSelector` parity — EVM
    /// reverts; Soroban surfaces it as a typed `Result`, the idiom this codebase
    /// uses everywhere, and a non-`try_` caller aborts on `Err` just as EVM
    /// propagates the revert).
    fn extract(env: soroban_sdk::Env, payload: Payload) -> Result<Vec<Parameter>, CCIPError>;

    /// Type-and-version string for the extractor.
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
}
