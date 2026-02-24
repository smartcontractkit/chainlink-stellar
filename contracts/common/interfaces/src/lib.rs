//! Cross-contract client interfaces for the CCIP Stellar workspace.
//!
//! This crate defines shared interface traits using `#[contractclient]`,
//! following the [Stellar workspace pattern](https://developers.stellar.org/docs/build/smart-contracts/example-contracts/workspace).
//!
//! Each module defines a trait for a contract's cross-contract interface and
//! auto-generates a typed client via `#[contractclient]`. Other contracts
//! (e.g. the Router) import these clients to make cross-contract calls
//! without depending on the full contract implementation — avoiding WASM
//! export symbol collisions.
//!
//! To regenerate or verify these interfaces against compiled contracts:
//! ```sh
//! make generate-interfaces
//! ```
#![no_std]

pub mod committee_verifier;
pub mod fee_quoter;
pub mod onramp;
pub mod rmn_proxy;
pub mod rmn_remote;
pub mod versioned_verifier_resolver;
