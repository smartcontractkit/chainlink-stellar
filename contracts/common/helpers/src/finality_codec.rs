//! FinalityCodec — encoding and validation for finality parameters.
//!
//! Mirrors EVM `FinalityCodec.sol`. The finality value is a `u32` (`bytes4` on EVM)
//! with a 16-bit block depth in the lower half and flag bits in the upper half.
//!
//! Bit layout (32 bits, MSB on the left):
//!
//!   bits 31..17 = Reserved flags
//!   bit  16     = WAIT_FOR_SAFE_FLAG
//!   bits 15..0  = block depth (max 65535)
//!
//! Special values:
//!   `0x00000000` = WAIT_FOR_FINALITY (safest, default)
//!   `0x00010000` = WAIT_FOR_SAFE (bit 16 set, no depth)
//!
//! This module lives in `common-helpers` (rather than `common-pool`) so that the
//! ramps (`onramp`/`offramp`) can validate requested finality without taking a hard
//! dependency on the pool *implementation* crate — they depend only on the pool
//! *interface* (`common-interfaces`). `common-pool` re-exports it for back-compat.

use common_error::CCIPError;

pub const BLOCK_DEPTH_BITS: u32 = 16;
pub const BLOCK_DEPTH_MASK: u32 = 0x0000_FFFF;

pub const WAIT_FOR_FINALITY_FLAG: u32 = 0;
pub const WAIT_FOR_SAFE_FLAG: u32 = 1 << BLOCK_DEPTH_BITS;

/// Returns `true` when the requested finality asks for anything other than
/// default full-finality — i.e. the transfer should use the FTF rate limit
/// buckets (with fallback to default if FTF buckets are not configured).
pub fn is_fast_finality(requested_finality: u32) -> bool {
    requested_finality != WAIT_FOR_FINALITY_FLAG
}

/// Validate that `requested_finality` contains exactly one mode: either
/// a single flag bit or a pure block depth, but not a flag combined with
/// a block depth. `WAIT_FOR_FINALITY_FLAG` (0) is always valid.
///
/// Mirrors EVM `FinalityCodec._validateRequestedFinality`.
pub fn validate_requested_finality(requested_finality: u32) -> Result<(), CCIPError> {
    if requested_finality == WAIT_FOR_FINALITY_FLAG {
        return Ok(());
    }

    let has_block_depth = (requested_finality & BLOCK_DEPTH_MASK) != 0;
    let mut active_modes: u32 = if has_block_depth { 1 } else { 0 };

    let flags = requested_finality >> BLOCK_DEPTH_BITS;
    if flags != 0 {
        for i in 0..16u32 {
            if (flags & (1 << i)) != 0 {
                active_modes += 1;
            }
        }
    }

    if active_modes != 1 {
        return Err(CCIPError::RequestedFinalityCanOnlyHaveOneMode);
    }
    Ok(())
}

/// Validate that `requested_finality` is well-formed and permitted by
/// `allowed_finality`. Mirrors EVM `FinalityCodec._ensureRequestedFinalityAllowed`.
pub fn ensure_requested_finality_allowed(
    requested_finality: u32,
    allowed_finality: u32,
) -> Result<(), CCIPError> {
    if requested_finality == WAIT_FOR_FINALITY_FLAG {
        return Ok(());
    }

    validate_requested_finality(requested_finality)?;

    // If any flag bits match, request is allowed (flag-only request).
    let req_flags = requested_finality >> BLOCK_DEPTH_BITS;
    let allowed_flags = allowed_finality >> BLOCK_DEPTH_BITS;
    if (req_flags & allowed_flags) != 0 {
        return Ok(());
    }

    // Otherwise must be block-depth based.
    let requested_depth = requested_finality & BLOCK_DEPTH_MASK;
    let allowed_depth = allowed_finality & BLOCK_DEPTH_MASK;

    if allowed_depth == 0 || requested_depth < allowed_depth {
        return Err(CCIPError::InvalidRequestedFinality);
    }
    Ok(())
}
