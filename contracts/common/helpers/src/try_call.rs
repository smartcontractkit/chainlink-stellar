//! Shared collapse of `try_*` cross-contract results into `common_error::CCIPError`.
//!
//! Used by both ramps so a callee failure flows to the caller's own error handling
//! (e.g. OffRamp `execute`'s per-message catch, recorded as a retryable `Failure` +
//! `ExecutionStateChangedEvent`) instead of host-aborting the whole transaction —
//! EVM `_callWithGasBuffer` catch-all parity (`OffRamp.sol:244-260`).
//!
//! # Error mapping
//!
//! A `try_*` client call yields `Result<Result<T, ConversionError>, Result<E, InvokeError>>`:
//!
//! - `Ok(Ok(v))` — success.
//! - `Ok(Err(_))` — the callee's return value did not decode to `T` ⇒
//!   [`CCIPError::MessageDecodingError`].
//! - `Err(Ok(e))` — the callee returned a typed error ⇒ its code is preserved via
//!   [`ccip_error_from_sdk_error`].
//! - `Err(Err(_))` — host trap (callee missing, Wasm trap, `InvokeError::Abort`) ⇒
//!   the caller-chosen `on_trap` code.
//!
//! # Why no per-interface glue is needed
//!
//! Every interface crate error enum is generated with `#[contracterror]` and mirrors
//! `common_error::CCIPError`'s code table, so the SDK already generates
//! `From<E> for soroban_sdk::Error` (code-preserving) and
//! `TryFrom<soroban_sdk::Error> for CCIPError` (a checked match over the code table).
//! Chaining those two generated impls converts any interface error back to the common
//! error with no `unsafe` and no per-interface trait impls.

use common_error::CCIPError;
use soroban_sdk::{ConversionError, Error as SdkError, InvokeError};

/// Collapses a `try_*` cross-contract result into the common error type; see
/// [module documentation](self) for the four-way mapping.
pub fn try_call<T, E>(
    res: Result<Result<T, ConversionError>, Result<E, InvokeError>>,
    on_trap: CCIPError,
) -> Result<T, CCIPError>
where
    E: Into<SdkError>,
{
    match res {
        Ok(Ok(v)) => Ok(v),
        Ok(Err(_)) => Err(CCIPError::MessageDecodingError),
        Err(Ok(e)) => Err(ccip_error_from_sdk_error(e.into())),
        Err(Err(_)) => Err(on_trap),
    }
}

/// Converts a typed callee error back into the common error through the SDK-generated
/// code table (`TryFrom<soroban_sdk::Error>` is a checked match on the contract error
/// code — no `unsafe` transmute).
#[inline]
fn ccip_error_from_sdk_error(err: SdkError) -> CCIPError {
    // All interface error enums mirror the full `CCIPError` table, so a typed callee
    // `Err` always decodes. A code outside the table cannot be produced by an on-chain
    // contract return; map it to `MessageDecodingError` (the same class as an
    // undecodable return value) rather than relying on `transmute` of an invalid
    // discriminant.
    CCIPError::try_from(err).unwrap_or(CCIPError::MessageDecodingError)
}

#[cfg(test)]
mod tests {
    use super::*;
    use common_interfaces::token_pool::CCIPError as TokenPoolError;

    #[test]
    fn ok_value_passes_through() {
        let res: Result<Result<u32, ConversionError>, Result<TokenPoolError, InvokeError>> =
            Ok(Ok(7));
        assert_eq!(try_call(res, CCIPError::TokenHandlingError), Ok(7));
    }

    #[test]
    fn undecodable_return_maps_to_message_decoding_error() {
        let res: Result<Result<u32, ConversionError>, Result<TokenPoolError, InvokeError>> =
            Ok(Err(ConversionError));
        assert_eq!(
            try_call(res, CCIPError::TokenHandlingError),
            Err(CCIPError::MessageDecodingError)
        );
    }

    #[test]
    fn typed_callee_error_keeps_its_code() {
        let res: Result<Result<u32, ConversionError>, Result<TokenPoolError, InvokeError>> =
            Err(Ok(TokenPoolError::InsufficientPoolLiquidity));
        assert_eq!(
            try_call(res, CCIPError::TokenHandlingError),
            Err(CCIPError::InsufficientPoolLiquidity)
        );
    }

    #[test]
    fn host_trap_maps_to_on_trap() {
        let res: Result<Result<u32, ConversionError>, Result<TokenPoolError, InvokeError>> =
            Err(Err(InvokeError::Abort));
        assert_eq!(
            try_call(res, CCIPError::TokenHandlingError),
            Err(CCIPError::TokenHandlingError)
        );
    }
}
