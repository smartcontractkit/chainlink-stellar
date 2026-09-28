use common_error::CCIPError;
use soroban_sdk::{Address, Env, Vec};

/// The conventional Stellar analogues of EVM `address(0)`, used as degenerate
/// "no address" markers in the role-2 rejection/skip guards (authorized-callers,
/// allowlist, fee-recipient). Soroban `Address` has no first-class empty/zero
/// variant — every `Address` is a valid 32-byte contract hash or a valid ed25519
/// account — so these are the *nearest representable* zero values: the ed25519
/// pubkey `0^32` (account strkey `G…`) and the contract hash `0^32` (contract
/// strkey `C…`). Neither can act as a signatory, so they are unusable as a real
/// fee-recipient / CCV / allowed caller — exactly the property EVM relies on
/// when it rejects `address(0)`.
///
/// These strkeys are the base32(RFC4648) of `version || 32×0x00 || crc16-xmodem`
/// with the CRC stored little-endian:
/// - account (version `0x30`): `GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF`
/// - contract (version `0x10`): `CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4`
///
/// This is **role-2** (reject/skip a degenerate address in a guard). It is
/// distinct from **role-1** — the CCV-list "include defaults" sentinel, which
/// this port replaces with the explicit `include_defaults: bool` flag (see
/// `assert_ccv_set_valid`'s INV-CFG-6 note: config-list validation intentionally
/// does *not* reject zero, because the sentinel role is handled by the bool and
/// no degenerate entry is expected there).
pub const ZERO_ACCOUNT_STRKEY: &str = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF";
pub const ZERO_CONTRACT_STRKEY: &str = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4";

/// True iff `addr` is the zero Stellar account OR the zero Stellar contract —
/// the type-agnostic EVM `address(0)` parity check for role-2 guards. EVM's
/// `address(0)` is type-agnostic (a 20-byte zero is a zero regardless of
/// whether the slot expects an EOA or a contract), so this checks both strkey
/// kinds rather than only the account.
///
/// Implementation note: the most SDK-idiomatic inspection —
/// `Address::to_payload() -> AddressPayload` matched against all-zero
/// `BytesN<32>` — is `#![cfg(any(test, feature = "hazmat-address"))]`-gated and
/// thus unavailable in a production wasm build (only `timelock` enables
/// `hazmat-address`). `Address → ScAddress` byte conversion is likewise
/// `#[cfg(not(target_family = "wasm"))]`-gated. Strkey comparison is the
/// lowest-friction production-feasible mechanism and matches the pre-existing
/// `is_zero_fee_recipient` convention; the two `Address::from_str` allocations
/// sit on a cold admin/validation path. Consolidates the four previously
/// duplicated per-crate helpers (advanced-pool-hooks, executor,
/// committee-verifier, versioned-verifier-resolver) into one source of truth.
pub fn is_zero_address(env: &Env, addr: &Address) -> bool {
    addr == &Address::from_str(env, ZERO_ACCOUNT_STRKEY)
        || addr == &Address::from_str(env, ZERO_CONTRACT_STRKEY)
}

/// A trait to define abstract behavior for validating a type.
pub trait Validatable {
    fn validate(&self) -> Result<(), CCIPError>;
}

/// H-11 / INV-CFG-5 + INV-CFG-7: validate a configured CCV set — the pair of
/// `default_ccvs` and `lane_mandated_ccvs` — at config-apply time. Shared by the
/// OnRamp `DestChainConfigArgs::validate` and OffRamp
/// `SourceChainConfigArgs::validate` so both ramps enforce the same shape
/// (EVM `CCVConfigValidation._assertNoDuplicates`).
///
/// - Within-list duplicates (INV-CFG-7) → `DuplicateCCVNotAllowed` (#320).
/// - Cross-list overlap (a CCV present in BOTH default and mandated) → the
///   caller-supplied error, since OnRamp and OffRamp surface distinct config
///   error codes (`InvalidConfig` / `InvalidSourceChainConfig`).
/// - INV-CFG-6 (zero-value CCV rejection): Soroban `Address` has no zero/empty
///   representation — every `Address` is a valid 32-byte contract id or a valid
///   ed25519 account — so this is satisfied by construction and needs no
///   explicit check (EVM rejects `address(0)`; Stellar has no analog).
///
/// Empty lists are permitted here; the caller's own emptiness invariant
/// (OnRamp: at least one of the two non-empty; OffRamp: non-empty `default_ccvs`
/// per INV-CFG-5) is enforced before calling.
pub fn assert_ccv_set_valid(
    defaults: &Vec<Address>,
    mandated: &Vec<Address>,
    cross_overlap_error: CCIPError,
) -> Result<(), CCIPError> {
    assert_no_duplicate_ccvs(defaults)?;
    assert_no_duplicate_ccvs(mandated)?;

    // A CCV must not be classified as both a default and lane-mandated.
    for i in 0..defaults.len() {
        let d = defaults.get(i).unwrap();
        for j in 0..mandated.len() {
            if d == mandated.get(j).unwrap() {
                return Err(cross_overlap_error);
            }
        }
    }

    Ok(())
}

/// INV-CFG-7: reject the first duplicate within a single CCV list (O(n²)),
/// mirroring the OnRamp runtime `assert_no_duplicate_ccvs`.
fn assert_no_duplicate_ccvs(ccvs: &Vec<Address>) -> Result<(), CCIPError> {
    let n = ccvs.len();
    for i in 0..n {
        for j in (i + 1)..n {
            if ccvs.get(i).unwrap() == ccvs.get(j).unwrap() {
                return Err(CCIPError::DuplicateCCVNotAllowed);
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use soroban_sdk::{testutils::Address as AddressTestutils, Address, Env};

    /// Both zero strkeys must parse and be recognized; a generated address must
    /// not. Guards the strkey constants against a typo (a wrong CRC suffix would
    /// make `Address::from_str` panic, failing this test loudly).
    #[test]
    fn test_is_zero_address_recognizes_both_strkeys() {
        let env = Env::default();
        assert!(is_zero_address(
            &env,
            &Address::from_str(&env, ZERO_ACCOUNT_STRKEY)
        ));
        assert!(is_zero_address(
            &env,
            &Address::from_str(&env, ZERO_CONTRACT_STRKEY)
        ));
        // A real generated address is neither zero kind.
        assert!(!is_zero_address(&env, &Address::generate(&env)));
        // The two zero addresses are distinct from each other (different strkey kinds).
        assert_ne!(
            Address::from_str(&env, ZERO_ACCOUNT_STRKEY),
            Address::from_str(&env, ZERO_CONTRACT_STRKEY)
        );
    }
}
