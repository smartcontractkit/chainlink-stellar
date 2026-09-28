//! Upgradeable trait with default implementation.
//!
//! Owner-gated in-place self-upgrade — the Stellar/Soroban analogue of an EVM
//! proxy's `upgradeTo`, without the proxy indirection (Soroban contracts
//! self-upgrade in place via `env.deployer().update_current_contract_wasm`).
//!
//! Security: the default body gates on `Ownable::require_owner`, which calls
//! `owner.require_auth()`. The upgrade is therefore authorized by whoever
//! `owner()` returns — an EOA in devenv, or MCMS through the MCMS→timelock
//! execute path in production. No other path can swap the code. The contract
//! address and all instance/persistent storage are preserved across the swap;
//! the constructor is NOT re-run, so any schema migration must be a lazy/explicit
//! `migrate()` in the new code (same constraint as EVM storage-layout discipline
//! across `upgradeTo`).
//!
//! This is the CCIP-core `Upgradeable` (gated by `common_authorization::Ownable`),
//! deliberately separate from the data-feeds `Upgradeable` trait which gates on
//! `stellar_access::ownable::enforce_owner_auth` — a different owner model that
//! would introduce a second owner store. See `docs/upgradeability.md`.

use common_error::CCIPError;
use soroban_sdk::{contractevent, contracttrait, BytesN, Env};

use crate::Ownable;

/// Emitted when a contract's executable is swapped in place via `upgrade`.
/// The contract address and all instance/persistent storage are unchanged;
/// only the Wasm code backing the contract is replaced.
#[contractevent(topics = ["Upgraded"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Upgraded {
    /// Hash of the new Wasm the contract now runs (uploaded beforehand via
    /// `env.deployer().upload_contract_wasm`).
    pub new_wasm_hash: BytesN<32>,
}

/// Owner-gated in-place self-upgrade.
///
/// Implement this trait for contracts that already `impl Ownable` (the
/// supertrait) and need an `upgrade` entrypoint. Use the default implementation
/// by providing an empty impl:
/// ```ignore
/// #[contractimpl(contracttrait)]
/// impl Upgradeable for MyContract {}
/// ```
///
/// `upgrade` returns `Result<(), CCIPError>` (not `()`) deliberately so the Go
/// binding generator emits an `Upgrade` method without a void-function allowlist
/// entry — see `docs/upgradeability.md`.
#[contracttrait]
pub trait Upgradeable: Ownable {
    /// Replaces the contract's executable with the Wasm identified by
    /// `new_wasm_hash`. Only the current owner may upgrade.
    fn upgrade(env: Env, new_wasm_hash: BytesN<32>) -> Result<(), CCIPError> {
        <Self as Ownable>::require_owner(&env)?;
        env.deployer()
            .update_current_contract_wasm(new_wasm_hash.clone());
        Upgraded { new_wasm_hash }.publish(&env);
        Ok(())
    }
}
