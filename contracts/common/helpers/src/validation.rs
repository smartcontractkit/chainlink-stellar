use common_error::CCIPError;
use soroban_sdk::{Address, Vec};

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
