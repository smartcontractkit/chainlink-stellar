//! Soroban contract-call payload decoding for governance-style `Bytes` (`data`).
//!
//! Off-chain tools and MCMS/timelock encode invocations as XDR for an `ScVal::Vec` whose first
//! element is the function [`Symbol`] and whose tail elements are arguments, matching
//! [`soroban_sdk::contract`] invoke conventions.
//!
//! # Recoverable errors vs host traps
//!
//! [`decode_invoke_payload`] returns [`SorobanInvokeDecodeError::InvalidPayload`] when:
//! - `data` is empty,
//! - the host’s [`Vec::from_xdr`](soroban_sdk::Vec::from_xdr) returns `Err` (host rejected the
//!   bytes as a `Vec<Val>` for this environment),
//! - the decoded vector is empty, or
//! - the first element is not a [`Symbol`].
//!
//! **Some malformed XDR byte strings do not return `Err`:** the Soroban host may **trap** while
//! deserializing. In that case the **entire contract invocation fails** at the host; there is no
//! `Result::Err` to map. In the Rust test harness this often appears as a **panic** (see
//! `garbage_bytes_may_cause_host_trap`). In production, the Stellar transaction fails and no
//! contract storage updates are committed (same failure class as an EVM subcall revert).
//!
//! Callers that need a *partial* decode (e.g. only the function name) and use the same `from_xdr`
//! path inherit the same trap behavior for garbage bytes.

use soroban_sdk::xdr::{FromXdr, ToXdr};
use soroban_sdk::{Bytes, Env, Symbol, TryFromVal, Val, Vec};

/// Recoverable failure while decoding a Soroban invoke payload.
///
/// Does not cover host traps; see [module documentation](self).
#[derive(Copy, Clone, Debug, Eq, PartialEq)]
pub enum SorobanInvokeDecodeError {
    /// Empty `data`, failed `from_xdr`, empty decoded vec, or first `Val` is not a `Symbol`
    /// (only when the host returns `Err` for these cases).
    InvalidPayload,
}

/// Decode `data` as XDR for `ScVec([ScSymbol(fn_name), arg0, arg1, …])`.
///
/// See the [module](self) for when this returns `Err` vs when the host may trap.
pub fn decode_invoke_payload(
    env: &Env,
    data: &Bytes,
) -> Result<(Symbol, Vec<Val>), SorobanInvokeDecodeError> {
    if data.len() == 0 {
        return Err(SorobanInvokeDecodeError::InvalidPayload);
    }

    let payload =
        Vec::<Val>::from_xdr(env, data).map_err(|_| SorobanInvokeDecodeError::InvalidPayload)?;

    if payload.is_empty() {
        return Err(SorobanInvokeDecodeError::InvalidPayload);
    }

    let fn_sym = Symbol::try_from_val(env, &payload.get(0).unwrap())
        .map_err(|_| SorobanInvokeDecodeError::InvalidPayload)?;

    let mut args: Vec<Val> = Vec::new(env);
    let mut i = 1u32;
    while i < payload.len() {
        args.push_back(payload.get(i).unwrap());
        i += 1;
    }

    Ok((fn_sym, args))
}

/// Decode canonical XDR for a Soroban argument vector.
///
/// Unlike [`decode_invoke_payload`], the function symbol is supplied separately and every decoded
/// value is an invocation argument. Re-encoding must reproduce the input exactly so alternative
/// or trailing encodings are rejected at the governance boundary.
pub fn decode_invoke_args(
    env: &Env,
    args_xdr: &Bytes,
) -> Result<Vec<Val>, SorobanInvokeDecodeError> {
    if args_xdr.len() == 0 {
        return Err(SorobanInvokeDecodeError::InvalidPayload);
    }

    let args = Vec::<Val>::from_xdr(env, args_xdr)
        .map_err(|_| SorobanInvokeDecodeError::InvalidPayload)?;
    if args.clone().to_xdr(env) != args_xdr.clone() {
        return Err(SorobanInvokeDecodeError::InvalidPayload);
    }
    Ok(args)
}

#[cfg(test)]
mod tests {
    use super::*;
    use soroban_sdk::xdr::ToXdr;
    use soroban_sdk::{Bytes, Env, IntoVal, TryFromVal, Val};

    #[test]
    fn empty_data_is_invalid() {
        let env = Env::default();
        assert!(matches!(
            decode_invoke_payload(&env, &Bytes::new(&env)),
            Err(SorobanInvokeDecodeError::InvalidPayload)
        ));
    }

    #[test]
    fn symbol_only_payload_no_args() {
        let env = Env::default();
        let fn_sym = Symbol::new(&env, "transfer");
        let mut v: Vec<Val> = Vec::new(&env);
        v.push_back(fn_sym.clone().into_val(&env));
        let (sym, args) = decode_invoke_payload(&env, &v.to_xdr(&env)).unwrap();
        assert_eq!(sym, fn_sym);
        assert_eq!(args.len(), 0);
    }

    #[test]
    fn symbol_plus_args_roundtrips() {
        let env = Env::default();
        let fn_sym = Symbol::new(&env, "set_value");
        let mut v: Vec<Val> = Vec::new(&env);
        v.push_back(fn_sym.clone().into_val(&env));
        v.push_back(42u32.into_val(&env));
        v.push_back(true.into_val(&env));
        let (sym, args) = decode_invoke_payload(&env, &v.to_xdr(&env)).unwrap();
        assert_eq!(sym, fn_sym);
        assert_eq!(args.len(), 2);
        assert_eq!(
            u32::try_from_val(&env, &args.get(0).unwrap()).unwrap(),
            42u32
        );
        assert_eq!(
            bool::try_from_val(&env, &args.get(1).unwrap()).unwrap(),
            true
        );
    }

    #[test]
    fn empty_vec_payload_is_invalid() {
        let env = Env::default();
        let empty: Vec<Val> = Vec::new(&env);
        assert!(matches!(
            decode_invoke_payload(&env, &empty.to_xdr(&env)),
            Err(SorobanInvokeDecodeError::InvalidPayload)
        ));
    }

    #[test]
    fn non_symbol_first_element_is_invalid() {
        let env = Env::default();
        let mut v: Vec<Val> = Vec::new(&env);
        v.push_back(99u32.into_val(&env)); // integer, not a Symbol
        assert!(matches!(
            decode_invoke_payload(&env, &v.to_xdr(&env)),
            Err(SorobanInvokeDecodeError::InvalidPayload)
        ));
    }

    /// Invalid XDR bytes may cause a host trap rather than `InvalidPayload`. On chain the
    /// transaction fails; in tests this surfaces as a panic.
    #[test]
    #[should_panic]
    fn garbage_bytes_may_cause_host_trap() {
        let env = Env::default();
        let garbage = Bytes::from_array(&env, &[0xff, 0xfe, 0x00, 0x01]);
        let _ = decode_invoke_payload(&env, &garbage);
    }

    #[test]
    fn address_arg_roundtrips() {
        let env = Env::default();
        use soroban_sdk::testutils::Address as _;
        use soroban_sdk::Address;
        let addr = Address::generate(&env);
        let fn_sym = Symbol::new(&env, "set_owner");
        let mut v: Vec<Val> = Vec::new(&env);
        v.push_back(fn_sym.clone().into_val(&env));
        v.push_back(addr.clone().into_val(&env));
        let (sym, args) = decode_invoke_payload(&env, &v.to_xdr(&env)).unwrap();
        assert_eq!(sym, fn_sym);
        let decoded = Address::try_from_val(&env, &args.get(0).unwrap()).unwrap();
        assert_eq!(decoded, addr);
    }

    #[test]
    fn args_only_vector_roundtrips() {
        let env = Env::default();
        let mut args: Vec<Val> = Vec::new(&env);
        args.push_back(42u32.into_val(&env));
        args.push_back(true.into_val(&env));
        let args_xdr = args.clone().to_xdr(&env);

        let decoded = decode_invoke_args(&env, &args_xdr).unwrap();
        assert_eq!(decoded, args);
    }

    #[test]
    fn empty_args_vector_is_valid() {
        let env = Env::default();
        let args: Vec<Val> = Vec::new(&env);
        let decoded = decode_invoke_args(&env, &args.to_xdr(&env)).unwrap();
        assert!(decoded.is_empty());
    }

    #[test]
    fn empty_args_bytes_are_invalid() {
        let env = Env::default();
        assert!(matches!(
            decode_invoke_args(&env, &Bytes::new(&env)),
            Err(SorobanInvokeDecodeError::InvalidPayload)
        ));
    }
}
