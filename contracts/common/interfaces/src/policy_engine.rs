//! Policy engine interface — Stellar analogue of EVM
//! [`IPolicyEngine`](https://github.com/smartcontractkit/ace/blob/main/packages/policy-management/src/interfaces/IPolicyEngine.sol).
//!
//! A contract a target (e.g. `AdvancedPoolHooks`) attaches to so that ACE
//! policies can evaluate that target's operations. The target builds a
//! [`Payload`] and calls [`PolicyEngineInterface::run`]; the engine looks up the
//! extractor registered for `payload.selector`, decodes `payload.data` into named
//! [`Parameter`]s, and runs the configured policy chain — reverting to reject
//! the operation (which, for pool hooks, blocks the transfer, mirroring EVM
//! `PolicyRunRejected` propagation).
//!
//! The engine is **generic across all attached targets**: it never inspects
//! `payload.data` itself, only passes it to the target-specific extractor. To
//! keep that genericity while staying typed (Soroban cross-contract calls pass
//! typed `Val`s, so there is no EVM-style raw-calldata blob to carry opaquely),
//! `data` is a [`PolicyData`] union — one variant per target family. New target
//! families add a variant (additive, non-breaking for the engine).
//!
//! This trait is the seam the policy-engine team implements against. It is
//! hand-written (not generated) because no engine contract exists yet; it
//! survives `make generate-interfaces` like `pool_hooks.rs` does.

use crate::pool_hooks::PoolHooksPayloadData;
use common_error::CCIPError;
use soroban_sdk::{contractclient, contracttype, Address, Bytes, Symbol};

/// The components a policy chain operates on (EVM `IPolicyEngine.Payload`).
///
/// Field-by-field mapping from EVM:
/// - `selector` (EVM `bytes4 msg.sig`) -> [`Symbol`]: identifies the target
///   method being invoked (e.g. `preflight_check` / `postflight_check`). Soroban
///   identifies functions by `Symbol`, so the keccak-4-byte scheme is replaced
///   by the function-name `Symbol` it denotes — same semantic, Soroban-idiomatic.
/// - `sender` (EVM `address msg.sender`) -> [`Address`]: the authenticated
///   immediate caller of the target (for pool hooks, the invoking pool).
/// - `data` (EVM `bytes msg.data[4:]`) -> [`PolicyData`]: the op args the
///   extractor decodes. See [`PolicyData`] for why this is typed, not opaque.
/// - `context` (EVM `bytes`) -> [`Bytes`]: side-channel passed alongside
///   (preflight: `token_args`; postflight: `offchain_token_data`). The extractor
///   skips it, as on EVM.
#[contracttype(export = false)]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Payload {
    pub selector: Symbol,
    pub sender: Address,
    pub data: PolicyData,
    pub context: Bytes,
}

/// Typed, engine-agnostic replacement for EVM's opaque `bytes` `Payload.data`.
///
/// EVM's `data` is opaque `bytes` because the engine is generic and the
/// extractor re-decodes the raw calldata. Soroban passes typed `Val`s, so the
/// carrier is a tagged union: the engine carries it without inspection, each
/// target's extractor downcasts its own variant. Today only pool hooks is
/// defined; future target families add variants.
#[contracttype(export = false)]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum PolicyData {
    PoolHooks(PoolHooksPayloadData),
}

/// A named, typed value a policy reads (EVM `IPolicyEngine.Parameter`).
///
/// EVM models `name` as `bytes32 keccak256(paramName)` and `value` as
/// `bytes abi.encode(...)`. Soroban-idiomatic equivalents: `name` as the
/// [`Symbol`] denoting the parameter, and `value` as a typed [`PolicyValue`]
/// (no raw-bytes encode/decode round-trip — the extractor emits the value in
/// its native type, the policy matches on it).
#[contracttype(export = false)]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Parameter {
    pub name: Symbol,
    pub value: PolicyValue,
}

/// Typed value of a [`Parameter`] (replaces EVM's `bytes abi.encode(value)`).
///
/// Variants cover the value types the pool-hooks extractor emits: sender / token
/// (`Address`), amounts (`I128`), chain selector (`U64`), finality config
/// (`U32`), and raw byte fields (`Bytes` — receiver, source pool address/data).
#[contracttype(export = false)]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum PolicyValue {
    Address(Address),
    I128(i128),
    U64(u64),
    U32(u32),
    Bytes(Bytes),
}

/// Outcome of a policy run (EVM `IPolicyEngine.PolicyResult`).
///
/// A policy that wants to reject MUST revert (Stellar: return `Err` / abort),
/// so `None` / `Continue` mean "did not reject; keep going" and `Allowed` means
/// "explicitly allow and stop the chain".
#[contracttype(export = false)]
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum PolicyResult {
    None,
    Allowed,
    Continue,
}

/// Cross-contract client interface for a policy engine (EVM `IPolicyEngine`).
///
/// `attach` / `detach` take the target address explicitly with `require_auth`,
/// because Soroban gives the callee no `msg.sender` to read — the same
/// explicit-caller pattern used by `PoolHooksInterface::preflight_check`.
#[contractclient(name = "PolicyEngineClient")]
pub trait PolicyEngineInterface {
    /// Run the policy chain for `payload`. Reverts (returns `Err` / aborts) when
    /// a policy rejects — for pool hooks this blocks the transfer (EVM parity).
    fn run(env: soroban_sdk::Env, payload: Payload) -> Result<(), CCIPError>;

    /// Register the calling target with this engine (EVM `attach`).
    /// `target` is the attaching contract's own address; it must authenticate.
    fn attach(env: soroban_sdk::Env, target: Address) -> Result<(), CCIPError>;

    /// Unregister the calling target from this engine (EVM `detach`).
    fn detach(env: soroban_sdk::Env, target: Address) -> Result<(), CCIPError>;

    /// Type-and-version string for the engine.
    fn type_and_version(env: soroban_sdk::Env) -> soroban_sdk::String;
}
