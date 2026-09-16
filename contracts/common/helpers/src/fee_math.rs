//! Fee-unit conversions that mirror the EVM CCIP v2 fee math.
//!
//! Stellar stores token prices in the same convention as EVM's
//! `USDPriceWith18Decimals`: `usd_per_token = USD × 1e18` per `1e18` smallest
//! token units (e.g. LINK @ $15 → `15e18`, USDC @ $1 → `1e18`). Converting a
//! USD-cents amount into fee-token smallest units is therefore:
//!
//! `amount = usd_cents × 1e34 / fee_token_price`
//!
//! (cents = USD × 1e2; price = USD × 1e18 per 1e18 units; so
//! `USD × 1e18 / (price/1e18) = USD × 1e36 / price = cents × 1e34 / price`.)
//!
//! EVM evaluates this in `uint256`, which never overflows for realistic values.
//! Stellar only has `u128` (max ≈ 3.4e38), so `usd_cents × 1e34` overflows for
//! fees above ~$340 — a realistic edge for large messages on expensive
//! destination chains. The release profile sets `overflow-checks = true` with
//! `panic = abort`, so a bare `*` would abort the contract on overflow. To stay
//! within `u128` we split the scaling (`1e34 = 1e24 × 1e10`) and carry the
//! division remainder, which reproduces EVM's `floor(cents × 1e34 / price)`
//! bit-for-bit without an overflowing intermediate. All arithmetic uses
//! `checked_*` so degenerate inputs revert with `InvalidFeeCalculation` instead
//! of aborting.

use common_error::CCIPError;

/// Convert USD cents into fee-token smallest units.
///
/// Matches EVM `usdCents * 1e34 / feeTokenPrice`
/// (`onRamp/OnRamp.sol`, `libraries/USDPriceWith18Decimals.sol`). `fee_token_price`
/// follows the `USDPriceWith18Decimals` convention (USD × 1e18 per 1e18 smallest
/// units). Returns `FeeTokenNotSupported` if `fee_token_price == 0` and
/// `InvalidFeeCalculation` on `u128` overflow of degenerate inputs.
pub fn usd_cents_to_fee_token(usd_cents: u128, fee_token_price: u128) -> Result<i128, CCIPError> {
    if fee_token_price == 0 {
        return Err(CCIPError::FeeTokenNotSupported);
    }
    // amount = usd_cents * 1e34 / fee_token_price
    //        = ((usd_cents * 1e24) / fee_token_price) * 1e10
    //          + ((usd_cents * 1e24) % fee_token_price) * 1e10 / fee_token_price
    // The remainder term carries the low-order bits so the result equals
    // floor(usd_cents * 1e34 / fee_token_price) exactly.
    const HI: u128 = 10_u128.pow(24);
    const LO: u128 = 10_u128.pow(10);

    let scaled = usd_cents
        .checked_mul(HI)
        .ok_or(CCIPError::InvalidFeeCalculation)?;
    let high = scaled / fee_token_price;
    let rem = scaled % fee_token_price;
    // rem < fee_token_price, so rem * LO only overflows for absurd price/fee
    // magnitudes; checked_mul keeps it from aborting the contract.
    let rem_lo = rem
        .checked_mul(LO)
        .ok_or(CCIPError::InvalidFeeCalculation)?;
    let amount = high
        .checked_mul(LO)
        .and_then(|a| a.checked_add(rem_lo / fee_token_price))
        .ok_or(CCIPError::InvalidFeeCalculation)?;
    // Stellar token amounts are i128; a fee too large to represent must revert
    // rather than wrap via `as i128`. Realistic fee tokens (price >= ~1e15 for
    // a >=$0.001 token) keep `amount` far below this bound.
    if amount > i128::MAX as u128 {
        return Err(CCIPError::InvalidFeeCalculation);
    }
    Ok(amount as i128)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_zero_price_rejected() {
        assert_eq!(
            usd_cents_to_fee_token(1_000, 0).unwrap_err(),
            CCIPError::FeeTokenNotSupported
        );
    }

    #[test]
    fn test_link_parity_with_evm() {
        // EVM: 3254 cents * 1e34 / 15e18 = 2169333333333333333
        let amt = usd_cents_to_fee_token(3254, 15_000_000_000_000_000_000).unwrap();
        assert_eq!(amt, 2_169_333_333_333_333_333);
    }

    #[test]
    fn test_usdc_parity_with_evm() {
        // USDC (6 decimals) at $1: USDPriceWith18Decimals stores 1e30, because the
        // price is "USD × 1e18 per 1e18 smallest units" and 1e18 USDC-smallest =
        // 1e12 USDC = $1e12, so the per-1e18-unit price = $1e12 × 1e18 = 1e30.
        // EVM: 5000 cents * 1e34 / 1e30 = 50_000_000 (= 50 USDC in smallest units).
        let amt = usd_cents_to_fee_token(5000, 1_000_000_000_000_000_000_000_000_000_000).unwrap();
        assert_eq!(amt, 50_000_000);
    }

    #[test]
    fn test_no_overflow_above_340_dollars() {
        // $1000 fee (100_000 cents) in LINK ($15): naive `cents * 1e34` would
        // overflow u128, but the split form must succeed and match EVM.
        // EVM: 100_000 * 1e34 / 15e18 = floor(1e21 / 15) = 66666666666666666666
        let amt = usd_cents_to_fee_token(100_000, 15_000_000_000_000_000_000).unwrap();
        assert_eq!(amt, 66_666_666_666_666_666_666);
    }

    #[test]
    fn test_bit_exact_vs_naive_reference() {
        // Brute-force comparison against a u128-computed floor(cents*1e34/price)
        // for cases where cents*1e34 itself fits in u128 (cents <= 34000).
        for cents in [1u128, 99, 100, 3254, 10_000, 34_000] {
            for price in [
                1_000_000_000_000_000_000u128, // $1
                15_000_000_000_000_000_000,    // $15
                3_000_000_000_000_000_000_000, // $3000
            ] {
                let naive = cents * 10_u128.pow(34) / price;
                let got = usd_cents_to_fee_token(cents, price).unwrap();
                assert_eq!(got as u128, naive, "cents={cents} price={price}");
            }
        }
    }
}
