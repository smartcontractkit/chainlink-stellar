use crate::finality_codec::{self, BLOCK_DEPTH_MASK, WAIT_FOR_FINALITY_FLAG, WAIT_FOR_SAFE_FLAG};
use common_error::CCIPError;

// ================================================================
//  is_fast_finality
// ================================================================

#[test]
fn is_ftf_zero_is_false() {
    assert!(!finality_codec::is_fast_finality(WAIT_FOR_FINALITY_FLAG));
}

#[test]
fn is_ftf_safe_flag_is_true() {
    assert!(finality_codec::is_fast_finality(WAIT_FOR_SAFE_FLAG));
}

#[test]
fn is_ftf_block_depth_is_true() {
    assert!(finality_codec::is_fast_finality(5));
}

// ================================================================
//  validate_requested_finality
// ================================================================

#[test]
fn validate_zero_is_ok() {
    assert!(finality_codec::validate_requested_finality(WAIT_FOR_FINALITY_FLAG).is_ok());
}

#[test]
fn validate_pure_block_depth_is_ok() {
    assert!(finality_codec::validate_requested_finality(10).is_ok());
}

#[test]
fn validate_safe_flag_is_ok() {
    assert!(finality_codec::validate_requested_finality(WAIT_FOR_SAFE_FLAG).is_ok());
}

#[test]
fn validate_flag_plus_depth_rejected() {
    let combined = WAIT_FOR_SAFE_FLAG | 5;
    let err = finality_codec::validate_requested_finality(combined).unwrap_err();
    assert_eq!(err, CCIPError::RequestedFinalityCanOnlyHaveOneMode);
}

#[test]
fn validate_multiple_flags_rejected() {
    let two_flags = (1u32 << 16) | (1u32 << 17);
    let err = finality_codec::validate_requested_finality(two_flags).unwrap_err();
    assert_eq!(err, CCIPError::RequestedFinalityCanOnlyHaveOneMode);
}

#[test]
fn validate_max_depth_ok() {
    assert!(finality_codec::validate_requested_finality(BLOCK_DEPTH_MASK).is_ok());
}

// ================================================================
//  ensure_requested_finality_allowed
// ================================================================

#[test]
fn allowed_finality_zero_always_ok() {
    assert!(finality_codec::ensure_requested_finality_allowed(WAIT_FOR_FINALITY_FLAG, 0).is_ok());
    assert!(finality_codec::ensure_requested_finality_allowed(
        WAIT_FOR_FINALITY_FLAG,
        WAIT_FOR_SAFE_FLAG
    )
    .is_ok());
}

#[test]
fn allowed_flag_matches_requested_flag() {
    let allowed = WAIT_FOR_SAFE_FLAG | 10;
    assert!(finality_codec::ensure_requested_finality_allowed(WAIT_FOR_SAFE_FLAG, allowed).is_ok());
}

#[test]
fn allowed_block_depth_permits_deeper() {
    let allowed = 5u32; // allow depth >= 5
    assert!(finality_codec::ensure_requested_finality_allowed(10, allowed).is_ok());
    assert!(finality_codec::ensure_requested_finality_allowed(5, allowed).is_ok());
}

#[test]
fn allowed_block_depth_rejects_shallower() {
    let allowed = 10u32;
    let err = finality_codec::ensure_requested_finality_allowed(5, allowed).unwrap_err();
    assert_eq!(err, CCIPError::InvalidRequestedFinality);
}

#[test]
fn no_allowed_depth_rejects_requested_depth() {
    let allowed = WAIT_FOR_SAFE_FLAG; // flag only, no depth
    let err = finality_codec::ensure_requested_finality_allowed(5, allowed).unwrap_err();
    assert_eq!(err, CCIPError::InvalidRequestedFinality);
}

#[test]
fn flag_not_in_allowed_rejected() {
    let allowed = 5u32; // depth only
    let err =
        finality_codec::ensure_requested_finality_allowed(WAIT_FOR_SAFE_FLAG, allowed).unwrap_err();
    assert_eq!(err, CCIPError::InvalidRequestedFinality);
}
