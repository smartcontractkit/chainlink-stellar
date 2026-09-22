//! Fee-unit conversions that mirror the EVM CCIP v2 fee math.
//!
//! Stellar stores token prices in the same convention as EVM's
//! `USDPriceWith18Decimals` (`Internal.Price.usdPerToken`):
//! `usd_per_token = USD × 1e18` **per `1e18` smallest token units** — i.e. the
//! price already accounts for the token's decimals. Examples (see
//! `chains/evm/contracts/libraries/Internal.sol`):
//!
//! | token            | decimals | USD/token | registered price              |
//! |------------------|----------|-----------|-------------------------------|
//! | LINK             | 18       | $15       | `15e18` (`15 × 10^(36-18)`)   |
//! | USDC             | 6        | $1        | `1e30`  (`1  × 10^(36-6)`)    |
//! | 7-decimal SAC    | 7        | $1        | `1e29`  (`1  × 10^(36-7)`)    |
//!
//! Converting a USD-cents amount into fee-token smallest units is therefore:
//!
//! `amount = usd_cents × 1e34 / fee_token_price`
//!
//! (cents = USD × 1e2; price = USD × 1e18 per 1e18 smallest units; so
//! `USD × 1e18 / (price/1e18) = USD × 1e36 / price = cents × 1e34 / price`.)
//!
//! EVM evaluates this in `uint256`, which never overflows for realistic values.
//! Stellar only has `u128` (max ≈ 3.4e38), so `usd_cents × 1e34` overflows for
//! fees above ~$340 *or* whenever the intermediate product exceeds `u128::MAX`
//! — including high-decimal fee tokens (e.g. a 7-decimal $15 token prices at
//! `1.5e30`; converting 100k cents gives `1e39`, above `u128::MAX`, even though
//! the final result is only `666_666_666`). A naive `split × 1e24 / 1e10`
//! decomposition does **not** fix this: when `usd_cents × 1e24 < price` the
//! "high" term is zero and the remainder term is `usd_cents × 1e24 × 1e10` —
//! the exact overflowing product. Instead we compute the full 256-bit product
//! `usd_cents × 1e34` (two `u128` limbs) and long-divide it by the price, so the
//! overflowing intermediate is never materialized. The release profile sets
//! `overflow-checks = true` with `panic = abort`; all limb arithmetic uses
//! wrapping/checked ops so degenerate inputs revert with `InvalidFeeCalculation`
//! instead of aborting the contract.

use common_error::CCIPError;

/// Convert USD cents into fee-token smallest units.
///
/// Matches EVM `usdCents * 1e34 / feeTokenPrice`
/// (`onRamp/OnRamp.sol`, `libraries/USDPriceWith18Decimals.sol`). `fee_token_price`
/// follows the `USDPriceWith18Decimals` convention (USD × 1e18 per 1e18 smallest
/// units, i.e. already scaled by the token's decimals — see the module docs).
/// Returns `FeeTokenNotSupported` if `fee_token_price == 0` and
/// `InvalidFeeCalculation` if the quotient does not fit in `i128`.
pub fn usd_cents_to_fee_token(usd_cents: u128, fee_token_price: u128) -> Result<i128, CCIPError> {
    if fee_token_price == 0 {
        return Err(CCIPError::FeeTokenNotSupported);
    }
    const SCALE: u128 = 10_u128.pow(34);
    // Full-width floor(usd_cents * 1e34 / fee_token_price). The product can
    // exceed u128::MAX for realistic fees/prices, so mul_div carries it as a
    // 256-bit value and long-divides — it never materializes the overflowing
    // product. Returns None only if the quotient itself exceeds u128 (impossible
    // for prices that fit a real token, but defended here).
    let amount =
        mul_div(usd_cents, SCALE, fee_token_price).ok_or(CCIPError::InvalidFeeCalculation)?;
    // Stellar token amounts are i128; a fee too large to represent must revert
    // rather than wrap via `as i128`.
    if amount > i128::MAX as u128 {
        return Err(CCIPError::InvalidFeeCalculation);
    }
    Ok(amount as i128)
}

/// Convert USD cents into fee-token smallest units with the LINK
/// premium/discount applied.
///
/// Matches EVM `receipt.feeTokenAmount *= feeMultiplier` where
/// `feeMultiplier = percentMultiplier * 1e32 / feeTokenPrice`
/// (`onRamp/OnRamp.sol` `_getReceipts`, L1086-1090) — the per-receipt scaling
/// EVM applies to every non-execution-cost receipt (CCV, pool, executor flat,
/// network). `premium_percent = 100` ⇒ no change and is **bit-identical** to
/// [`usd_cents_to_fee_token`] (since `100 * 1e32 == 1e34`); `< 100` ⇒ the LINK
/// discount; `> 100` ⇒ a surcharge. Execution-gas cost must NOT use this — EVM
/// adds `execCost * 1e34 / price` without the multiplier (L1095-1097), so callers
/// price exec cost with the bare [`usd_cents_to_fee_token`].
///
/// Same error contract as [`usd_cents_to_fee_token`]: `FeeTokenNotSupported`
/// when `fee_token_price == 0`, `InvalidFeeCalculation` if the quotient does not
/// fit in `i128` (or `premium_percent` is pathologically large).
pub fn usd_cents_to_fee_token_with_premium(
    usd_cents: u128,
    premium_percent: u32,
    fee_token_price: u128,
) -> Result<i128, CCIPError> {
    if fee_token_price == 0 {
        return Err(CCIPError::FeeTokenNotSupported);
    }
    // percentMultiplier * 1e32 (EVM L1086). Realistic percents are ≤ a few
    // hundred ⇒ ≤ ~1e34, far under u128::MAX; a misconfigured value reverts
    // cleanly via `InvalidFeeCalculation` rather than aborting.
    let scaled_percent = (premium_percent as u128)
        .checked_mul(10_u128.pow(32))
        .ok_or(CCIPError::InvalidFeeCalculation)?;
    // floor(usd_cents * scaled_percent / fee_token_price) = floor(usd_cents *
    // percentMultiplier * 1e32 / price), the EVM feeMultiplier product. Full
    // 256-bit so the overflowing intermediate is never materialized.
    let amount = mul_div(usd_cents, scaled_percent, fee_token_price)
        .ok_or(CCIPError::InvalidFeeCalculation)?;
    if amount > i128::MAX as u128 {
        return Err(CCIPError::InvalidFeeCalculation);
    }
    Ok(amount as i128)
}

/// Compute `floor(a * b / denom)` for `u128` inputs without materializing the
/// 256-bit product as a single value that could overflow `u128`.
///
/// `a * b` is formed as two `u128` limbs (`mul_128x128`) and long-divided by
/// `denom` (`div_256x128`). Returns `None` if `denom == 0` or the quotient
/// exceeds `u128::MAX`.
fn mul_div(a: u128, b: u128, denom: u128) -> Option<u128> {
    let (hi, lo) = mul_128x128(a, b);
    div_256x128(hi, lo, denom).map(|(q, _)| q)
}

/// Schoolbook 128×128 → 256-bit multiply. Returns `(hi, lo)` where the product
/// is `hi * 2^128 + lo`. Each partial product of two `u64` limbs fits in `u128`,
/// so the only additions that can exceed `u128` are the cross-term sum and the
/// low-half accumulation — both use `wrapping_add` with an explicit carry bit.
/// The final `hi` provably fits in `u128` (max product `(2^128-1)^2` ⇒
/// `hi ≤ 2^128-2`), so plain `+` cannot overflow there.
fn mul_128x128(a: u128, b: u128) -> (u128, u128) {
    let a_lo = a as u64 as u128;
    let a_hi = (a >> 64) as u64 as u128;
    let b_lo = b as u64 as u128;
    let b_hi = (b >> 64) as u64 as u128;

    let ll = a_lo * b_lo; // bits   0..127
    let lh = a_lo * b_hi; // bits  64..191
    let hl = a_hi * b_lo; // bits  64..191
    let hh = a_hi * b_hi; // bits 128..255

    // Cross terms occupy bits 64..191; their sum can carry into bit 192.
    let mid = lh.wrapping_add(hl);
    let mid_carry = (mid < lh) as u128;

    // Low half = ll + (low 64 bits of mid placed at bits 64..127).
    let lo = ll.wrapping_add(mid << 64);
    let lo_carry = (lo < ll) as u128;

    // High half = hh + (high 64 bits of mid at bits 128..191) + carry at bit 192
    // + carry from the low-half addition at bit 128.
    let hi = hh + (mid >> 64) + (mid_carry << 64) + lo_carry;
    (hi, lo)
}

/// Long-divide a 256-bit dividend `(hi:lo)` by a 128-bit `denom`, returning
/// `(quotient, remainder)` with the quotient fitting in `u128`.
///
/// Restoring binary long division, MSB first: each step shifts the remainder
/// left and brings in one dividend bit, then subtracts `denom` when the shifted
/// remainder is `>= denom`. The invariant `remainder < denom` is maintained, so
/// the shifted value is always `< 2*denom + 1 ≤ 2^129 - 1`; when the shift would
/// set bit 128 (`rem_msb`) the true value is `≥ 2^128 > denom` (since
/// `denom ≤ 2^128-1`), so the quotient bit is 1 and `wrapping_sub` yields the
/// correct 128-bit remainder (the true difference is `< denom < 2^128`).
///
/// Returns `None` if `denom == 0` or `hi >= denom` (quotient would overflow
/// `u128`). The caller guarantees a non-zero `denom`; the `hi >= denom` guard
/// makes overflow impossible for any valid price.
fn div_256x128(hi: u128, lo: u128, denom: u128) -> Option<(u128, u128)> {
    if denom == 0 {
        return None;
    }
    if hi >= denom {
        // Quotient has bits above 128 → does not fit u128.
        return None;
    }
    let mut rem: u128 = 0;
    let mut quot: u128 = 0;
    // Process the 256 dividend bits from MSB (bit 255) to LSB (bit 0). Because
    // `hi < denom`, the true quotient fits in 128 bits, so the high 128 quotient
    // bits produced first are all zero and shifting them out loses nothing.
    for i in (0..256).rev() {
        let bit = if i >= 128 {
            (hi >> (i - 128)) & 1
        } else {
            (lo >> i) & 1
        };
        let rem_msb = rem >> 127;
        // `rem << 1` discards the top bit by construction (shift, not add), so
        // no overflow panic; the dropped bit is accounted for via `rem_msb`.
        rem = (rem << 1) | bit;
        if rem_msb != 0 || rem >= denom {
            rem = rem.wrapping_sub(denom);
            quot = (quot << 1) | 1;
        } else {
            quot <<= 1;
        }
    }
    Some((quot, rem))
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
        // overflow u128, but the full-width form must succeed and match EVM.
        // EVM: 100_000 * 1e34 / 15e18 = floor(1e21 / 15) = 66666666666666666666
        let amt = usd_cents_to_fee_token(100_000, 15_000_000_000_000_000_000).unwrap();
        assert_eq!(amt, 66_666_666_666_666_666_666);
    }

    #[test]
    fn test_high_decimal_price_no_overflow() {
        // Regression for the split-form overflow: a 7-decimal token at $15
        // prices at 15 × 10^(36-7) = 1.5e30. The old split form had `high = 0`
        // (since usd_cents*1e24 = 1e29 < 1.5e30) and then formed
        // `rem * 1e10 = usd_cents * 1e34 = 1e39`, overflowing u128::MAX and
        // aborting the contract — even though the final result fits i128.
        // Full-width form: floor(100_000 * 1e34 / 1.5e30) = floor(1e39 / 1.5e30)
        // = floor(6.666…e8) = 666_666_666.
        let price = 15_u128 * 10_u128.pow(29); // 1.5e30
        let amt = usd_cents_to_fee_token(100_000, price).unwrap();
        assert_eq!(amt, 666_666_666);
    }

    #[test]
    fn test_seven_decimal_one_dollar_50_cents() {
        // A 7-decimal $1 SAC prices at 1 × 10^(36-7) = 1e29. A 50-cent fee must
        // be quoted as 0.5 token = 5_000_000 smallest units — NOT the 5e17
        // (50 billion tokens) that the decimals-agnostic 1e18 price would yield.
        let price = 10_u128.pow(29);
        let amt = usd_cents_to_fee_token(50, price).unwrap();
        assert_eq!(amt, 5_000_000);
    }

    #[test]
    fn test_bit_exact_vs_naive_reference() {
        // Brute-force comparison against a u128-computed floor(cents*1e34/price)
        // for cases where cents*1e34 itself fits in u128 (cents <= ~34000).
        for cents in [1u128, 99, 100, 3254, 10_000, 34_000] {
            for price in [
                1_000_000_000_000_000_000u128, // $1 (18-dec token)
                15_000_000_000_000_000_000,    // $15 LINK
                3_000_000_000_000_000_000_000, // $3000 (18-dec token)
                10_u128.pow(29),               // $1 (7-dec token)
                15_u128 * 10_u128.pow(29),     // $15 (7-dec token)
            ] {
                let naive = cents * 10_u128.pow(34) / price;
                let got = usd_cents_to_fee_token(cents, price).unwrap();
                assert_eq!(got as u128, naive, "cents={cents} price={price}");
            }
        }
    }

    #[test]
    fn test_premium_100_is_bit_identical_to_bare() {
        // percentMultiplier = 100 ⇒ 100 * 1e32 == 1e34 ⇒ exactly the bare helper.
        for cents in [1u128, 99, 150, 3254, 10_000, 100_000] {
            for price in [
                1_000_000_000_000_000_000u128, // $1 (18-dec)
                15_000_000_000_000_000_000,    // $15 LINK
                10_u128.pow(29),               // $1 (7-dec)
                15_u128 * 10_u128.pow(29),     // $15 (7-dec)
            ] {
                assert_eq!(
                    usd_cents_to_fee_token_with_premium(cents, 100, price).unwrap(),
                    usd_cents_to_fee_token(cents, price).unwrap(),
                    "cents={cents} price={price}"
                );
            }
        }
    }

    #[test]
    fn test_premium_link_discount_matches_evm_fee_multiplier() {
        // EVM: feeTokenAmount = usdCents * percentMultiplier * 1e32 / price.
        // LINK @ $15, 90% (10% discount), $1.50 fee (150 cents):
        //   150 * 90 * 1e32 / 15e18 = 150 * 90 / 15 * 1e14 = 900 * 1e14 = 9e16.
        let price = 15_000_000_000_000_000_000u128;
        let amt = usd_cents_to_fee_token_with_premium(150, 90, price).unwrap();
        assert_eq!(amt, 90_000_000_000_000_000); // 0.09 LINK (vs 0.10 at full price)
                                                 // Cross-check: exactly 90% of the bare (full-price) amount, floor-aligned
                                                 // because 150 * 1e34 / 15e18 = 1e17 is exact ⇒ 90% = 9e16 exact.
        let bare = usd_cents_to_fee_token(150, price).unwrap();
        assert_eq!(amt, bare * 90 / 100);

        // Non-exact case (3254 cents @ $15, 90%). EVM computes the feeMultiplier
        // product as ONE full-width division: floor(3254 * 90 * 1e32 / 15e18) =
        // floor(292860e32 / 15e18) = 195240 * 1e13 = 1952400000000000000 (exact
        // here, 292860 = 1.5 * 195240). Note this is NOT `bare * 90 / 100`, which
        // floors twice and yields one fewer (1952399999999999999) — EVM never
        // re-floors an already-floored bare amount.
        assert_eq!(
            usd_cents_to_fee_token_with_premium(3254, 90, price).unwrap(),
            1_952_400_000_000_000_000
        );
    }

    #[test]
    fn test_premium_zero_price_rejected() {
        assert_eq!(
            usd_cents_to_fee_token_with_premium(1_000, 90, 0).unwrap_err(),
            CCIPError::FeeTokenNotSupported
        );
    }

    #[test]
    fn test_premium_pathological_percent_reverts() {
        // percentMultiplier so large that percent * 1e32 overflows u128 reverts
        // cleanly (InvalidFeeCalculation), not an abort. u32::MAX * 1e32 ≫ u128::MAX.
        assert_eq!(
            usd_cents_to_fee_token_with_premium(100, u32::MAX, 15_000_000_000_000_000_000)
                .unwrap_err(),
            CCIPError::InvalidFeeCalculation
        );
    }

    #[test]
    fn test_premium_surcharge_above_100() {
        // percentMultiplier = 110 ⇒ 10% surcharge. $1 (100 cents) @ $1 18-dec token:
        // bare = 100 * 1e34 / 1e18 = 1e18; 110% = 1.1e18.
        let price = 1_000_000_000_000_000_000u128;
        assert_eq!(
            usd_cents_to_fee_token_with_premium(100, 110, price).unwrap(),
            1_100_000_000_000_000_000
        );
    }

    #[test]
    fn test_mul_div_remainder_and_identity() {
        // mul_div must floor and keep the remainder exact.
        assert_eq!(mul_div(7, 6, 4), Some(10)); // 42/4 = 10
        assert_eq!(mul_div(0, 1_000, 7), Some(0));
        assert_eq!(mul_div(1, 1, 1), Some(1));
        // denom == 0 → None.
        assert_eq!(mul_div(1, 1, 0), None);
        // Quotient overflow: product's high limb >= denom. (2^128-1)^2 has
        // hi = 2^128-2; dividing by 1 ⇒ quotient ≈ 2^128-1 overflows u128 only
        // when denom < hi. With denom=1 (< hi) → None.
        let max = u128::MAX;
        assert_eq!(mul_div(max, max, 1), None);
        // With denom = max the quotient = max*max/max = max (fits u128 exactly,
        // remainder 0); hi = max-1 < denom = max so the division proceeds.
        assert_eq!(mul_div(max, max, max), Some(max));
    }

    #[test]
    fn test_mul_128x128_vs_known_products() {
        // Small values: low limb only.
        assert_eq!(mul_128x128(0, 0), (0, 0));
        assert_eq!(mul_128x128(1, 0), (0, 0));
        assert_eq!(mul_128x128(1, 1), (0, 1));
        assert_eq!(mul_128x128(2, 3), (0, 6));
        // Cross-limb: 2^64 * 2^64 = 2^128 → hi=1, lo=0.
        assert_eq!(mul_128x128(1u128 << 64, 1u128 << 64), (1, 0));
        // 2^128 - 1 squared: hi = 2^128 - 2, lo = 1.
        let max = u128::MAX;
        let (hi, lo) = mul_128x128(max, max);
        assert_eq!(hi, max - 1);
        assert_eq!(lo, 1);
        // 1e39 (reviewer's intermediate): a=1e5, b=1e34. The product itself
        // overflows u128, so compute the expected low limb via wrapping_mul
        // (== 1e39 mod 2^128) rather than materializing 1e39.
        let (hi, lo) = mul_128x128(100_000, 10_u128.pow(34));
        // 1e39 = hi*2^128 + lo. 2^128 ≈ 3.4028e38 → hi = floor(1e39 / 2^128) = 2.
        assert_eq!(hi, 2);
        let expected_lo = (100_000u128).wrapping_mul(10_u128.pow(34));
        assert_eq!(lo, expected_lo);
    }

    // ----- Extreme-value coverage for the full-width math. The whole point of
    // mul_128x128 / div_256x128 is that inputs which overflow naive u128
    // arithmetic must still produce the correct floor (or revert cleanly) rather
    // than abort the contract (release profile sets overflow-checks = true). -----

    #[test]
    fn test_zero_cents_yields_zero() {
        // A free message quotes as 0 fee-token units for any supported price.
        assert_eq!(
            usd_cents_to_fee_token(0, 15_000_000_000_000_000_000).unwrap(),
            0
        );
        assert_eq!(usd_cents_to_fee_token(0, 10_u128.pow(29)).unwrap(), 0);
        assert_eq!(usd_cents_to_fee_token(0, u128::MAX).unwrap(), 0);
    }

    #[test]
    fn test_amount_above_i128_max_reverts() {
        // price = 1 is a degenerate "worthless token" price (USD × 1e18 per 1e18
        // units ⇒ USD/token = 1e-18), so amount = cents × 1e34. For cents = 20_000
        // the quotient is 2e38: it fits u128 (MAX ≈ 3.4e38) so mul_div succeeds,
        // but exceeds i128::MAX (≈ 1.7e38) ⇒ InvalidFeeCalculation, not a wrap via
        // `as i128`. Verifies the i128 ceiling is enforced after the u128 division.
        assert_eq!(
            usd_cents_to_fee_token(20_000, 1).unwrap_err(),
            CCIPError::InvalidFeeCalculation
        );
    }

    #[test]
    fn test_quotient_overflow_u128_reverts() {
        // cents = u128::MAX, price = 1 ⇒ quotient ≈ 3.4e72 ≫ u128::MAX. The
        // 256-bit product's high limb (1e34 - 1) ≥ denom = 1, so div_256x128
        // returns None ⇒ InvalidFeeCalculation. Must not abort.
        assert_eq!(
            usd_cents_to_fee_token(u128::MAX, 1).unwrap_err(),
            CCIPError::InvalidFeeCalculation
        );
        // Same with a realistic LINK price: max × 1e34 / 15e18 ≈ 2.2e53 ≫ u128.
        assert_eq!(
            usd_cents_to_fee_token(u128::MAX, 15_000_000_000_000_000_000).unwrap_err(),
            CCIPError::InvalidFeeCalculation
        );
    }

    #[test]
    fn test_both_inputs_at_u128_max() {
        // usd_cents = price = u128::MAX ⇒ amount = floor(MAX × 1e34 / MAX) = 1e34
        // exactly (MAX divides MAX × 1e34 evenly, remainder 0). The product's high
        // limb is 1e34 - 1 < MAX so the division proceeds; 1e34 fits i128.
        assert_eq!(
            usd_cents_to_fee_token(u128::MAX, u128::MAX).unwrap(),
            10_u128.pow(34) as i128
        );
    }

    #[test]
    fn test_price_at_u128_max_rounds_to_zero() {
        // A $1 fee (100 cents) for a token priced at u128::MAX (effectively
        // priceless): 100 × 1e34 = 1e36 ≪ MAX ⇒ quot = floor(1e36 / MAX) = 0.
        assert_eq!(usd_cents_to_fee_token(100, u128::MAX).unwrap(), 0);
    }

    #[test]
    fn test_div_256x128_high_limb_set() {
        // dividend = 2^128 (hi=1, lo=0). Exercises bits above 127 of the dividend.
        // 2^128 = 3·q + 1 (2^128 mod 3 = 1) ⇒ q = (2^128-1)/3 and q·3 = u128::MAX
        // exactly (u128::MAX = 2^128-1 is divisible by 3).
        let (q, r) = div_256x128(1, 0, 3).unwrap();
        assert_eq!(r, 1);
        assert_eq!(q * 3, u128::MAX);

        // denom = 2: 2^128 / 2 = 2^127, remainder 0.
        let (q, r) = div_256x128(1, 0, 2).unwrap();
        assert_eq!(q, 1u128 << 127);
        assert_eq!(r, 0);

        // denom = u128::MAX = 2^128-1: 2^128 / (2^128-1) = 1 rem 1.
        let (q, r) = div_256x128(1, 0, u128::MAX).unwrap();
        assert_eq!((q, r), (1, 1));
    }

    #[test]
    fn test_div_256x128_rem_msb_path() {
        // dividend = 2^128 + (2^128-1) = 2^129 - 1 (hi=1, lo=u128::MAX), denom =
        // u128::MAX = 2^128-1. The remainder climbs to ≈ 2^128-2 (bit 127 set)
        // during the shift, exercising the `rem_msb` branch where `rem << 1`
        // would set bit 128. 2^129-1 = 2·(2^128-1) + 1 ⇒ quotient 2, remainder 1.
        let (q, r) = div_256x128(1, u128::MAX, u128::MAX).unwrap();
        assert_eq!((q, r), (2, 1));
    }

    #[test]
    fn test_div_256x128_rejects_quotient_overflow() {
        // hi >= denom ⇒ quotient has bits above 128 ⇒ does not fit u128 ⇒ None.
        assert!(div_256x128(2, 0, 1).is_none());
        // hi == denom exactly: quotient would be exactly 2^128 (one past u128::MAX).
        assert!(div_256x128(5, 0, 5).is_none());
        // denom == 0 ⇒ None (caller guards, but the helper must be total).
        assert!(div_256x128(0, 123, 0).is_none());
    }

    #[test]
    fn test_div_256x128_invariant_extremes() {
        // For hi < denom: quot·denom + rem == hi·2^128 + lo, with rem < denom.
        // Reconstruct the 256-bit product with mul_128x128 and add rem with carry,
        // so no expected-quotient literals are needed — the invariant is the oracle.
        fn check(hi: u128, lo: u128, denom: u128) {
            assert!(hi < denom, "case must satisfy hi < denom");
            let (q, r) = div_256x128(hi, lo, denom).expect("hi < denom ⇒ Some");
            assert!(r < denom, "remainder must be < denom");
            let (ph, pl) = mul_128x128(q, denom);
            let (pl, carry) = pl.overflowing_add(r);
            let ph = ph.wrapping_add(carry as u128);
            assert_eq!((ph, pl), (hi, lo), "q*denom + r != dividend");
        }
        check(0, 0, 7);
        check(0, 1, 7);
        check(0, u128::MAX, 7);
        check(0, u128::MAX, u128::MAX);
        check(1, 0, 3);
        check(1, 0, 2);
        check(1, u128::MAX, u128::MAX);
        check(1, 1u128 << 100, (1u128 << 100) + 1);
        check(u128::MAX - 1, u128::MAX, u128::MAX);
    }

    #[test]
    fn test_mul_128x128_extremes() {
        let max = u128::MAX;
        // max × 1 = max ⇒ fits the low limb.
        assert_eq!(mul_128x128(max, 1), (0, max));
        assert_eq!(mul_128x128(1, max), (0, max));
        // max × 2 = 2^129 - 2 = 1·2^128 + (2^128 - 2) ⇒ hi=1, lo=max-1.
        assert_eq!(mul_128x128(max, 2), (1, max - 1));
        // 3 × 2^127 = 2^128 + 2^127 ⇒ straddles the limb boundary, hi=1, lo=2^127.
        assert_eq!(mul_128x128(1u128 << 127, 3), (1, 1u128 << 127));
        // (2^64 - 1)^2 = 2^128 - 2^65 + 1 < 2^128 ⇒ hi=0; verify lo without
        // materializing the literal via wrapping_mul (== value mod 2^128).
        let m = (1u128 << 64) - 1;
        let (hi, lo) = mul_128x128(m, m);
        assert_eq!(hi, 0);
        assert_eq!(lo, m.wrapping_mul(m));
    }
}
