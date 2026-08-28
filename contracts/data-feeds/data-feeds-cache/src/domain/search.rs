use crate::interface::types::Bound;

/// Binary search over `lo..=hi` for the entry closest to `target` on the given side.
///
/// `probe` must be monotonic across the range: timestamps must not decrease as the
/// index rises, and any entry it cannot return must form an unbroken run at the low
/// end. A gap in the middle breaks the search and it may return the wrong entry.
pub(crate) fn boundary<T>(
    lo: u64,
    hi: u64,
    target: u64,
    bound: Bound,
    probe: impl Fn(u64) -> Option<(u64, T)>,
) -> Option<T> {
    let (mut lo, mut hi, mut found) = (lo, hi, None);
    while lo <= hi {
        let mid = lo + (hi - lo) / 2;
        let hit = probe(mid);
        let accept = match (bound, &hit) {
            (Bound::AtOrBefore, None) => true,
            (Bound::AtOrBefore, Some((t, _))) => *t <= target,
            (Bound::AtOrAfter, None) => false,
            (Bound::AtOrAfter, Some((t, _))) => *t >= target,
        };
        if accept {
            if let Some((_, v)) = hit {
                found = Some(v);
            }
        }
        if matches!(bound, Bound::AtOrAfter) == accept {
            if mid == 0 {
                break;
            }
            hi = mid - 1;
        } else {
            lo = mid + 1;
        }
    }
    found
}

#[cfg(test)]
mod tests {
    use super::*;

    fn found(ts: &[Option<u64>], target: u64, bound: Bound) -> Option<u64> {
        let head = ts.len() as u64 - 1;
        let at = |rid: u64| ts.get(rid as usize).copied().flatten().map(|t| (t, rid));
        boundary(1, head, target, bound, at)
    }

    #[test]
    fn a_zero_lower_bound_narrows_without_underflowing() {
        let at = |rid: u64| Some((rid * 10 + 10, rid));

        assert_eq!(boundary(0, 0, 5, Bound::AtOrBefore, at), None);
        assert_eq!(boundary(0, 0, 5, Bound::AtOrAfter, at), Some(0));
        assert_eq!(boundary(0, 1, 5, Bound::AtOrAfter, at), Some(0));
    }

    #[test]
    fn a_zero_midpoint_still_searches_upwards() {
        let at = |rid: u64| Some((rid * 10 + 10, rid));

        assert_eq!(boundary(0, 1, 15, Bound::AtOrBefore, at), Some(0));
        assert_eq!(boundary(0, 1, 20, Bound::AtOrBefore, at), Some(1));
        assert_eq!(boundary(0, 1, 15, Bound::AtOrAfter, at), Some(1));
    }

    #[test]
    fn between_two_entries() {
        let ts = [None, Some(10), Some(20), Some(30), Some(40), Some(50)];
        assert_eq!(found(&ts, 35, Bound::AtOrBefore), Some(3));
        assert_eq!(found(&ts, 35, Bound::AtOrAfter), Some(4));
    }

    #[test]
    fn empty_is_none() {
        let ts = [None];
        assert_eq!(found(&ts, 10, Bound::AtOrBefore), None);
        assert_eq!(found(&ts, 10, Bound::AtOrAfter), None);
    }

    #[test]
    fn masked_low_prefix_is_never_returned() {
        let ts = [None, None, None, None, Some(40), Some(50)];
        assert_eq!(found(&ts, 25, Bound::AtOrBefore), None);
        assert_eq!(found(&ts, 45, Bound::AtOrBefore), Some(4));
        assert_eq!(found(&ts, 25, Bound::AtOrAfter), Some(4));
        assert_eq!(found(&ts, 55, Bound::AtOrAfter), None);
    }

    #[test]
    fn matches_linear_scan_across_sizes_and_windows() {
        for n in [4u64, 5, 64] {
            let at = |rid: u64| (1..=n).contains(&rid).then_some((rid * 10, rid));
            for lo in 1..=n {
                for target in 0..=(n * 10 + 10) {
                    for bound in [Bound::AtOrBefore, Bound::AtOrAfter] {
                        let got = boundary(lo, n, target, bound, at);
                        let want = match bound {
                            Bound::AtOrBefore => (lo..=n).rev().find(|&i| i * 10 <= target),
                            Bound::AtOrAfter => (lo..=n).find(|&i| i * 10 >= target),
                        };
                        let label = match bound {
                            Bound::AtOrBefore => "before",
                            Bound::AtOrAfter => "after",
                        };
                        assert_eq!(got, want, "n={n} lo={lo} target={target} bound={label}");
                    }
                }
            }
        }
    }
}
