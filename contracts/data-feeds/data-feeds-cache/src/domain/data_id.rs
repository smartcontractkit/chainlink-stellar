//! `DataId` layout.
//!
//! A `DataId` addresses a feed *and* the scale its answer is read at: byte
//! [`DECIMALS_BYTE`] carries the decimals, offset by [`DECIMALS_OFFSET`]. One
//! feed is therefore reachable at several scales - `0x..32..` reads 18 decimals
//! and `0x..28..` reads the same answer downscaled to 8 - without writing the
//! feed twice.
//!
//! Storage keys clear that byte, see [`CanonicalId`], so every scale of a feed
//! resolves to the single answer the workflow wrote.

use soroban_sdk::{BytesN, Env};

use crate::interface::DataId;

/// Position of the decimals byte inside a `DataId`.
const DECIMALS_BYTE: usize = 7;

/// Offset applied to the decimals byte, so `0x20` reads as zero decimals.
const DECIMALS_OFFSET: u8 = 0x20;

/// Largest decimals byte accepted, `0x60`, i.e. 64 decimals.
const DECIMALS_BYTE_MAX: u8 = 0x60;

/// A `DataId` with the decimals byte cleared: the storage key shared by every
/// scale of one feed.
#[derive(Clone, Debug, Eq, PartialEq)]
pub(crate) struct CanonicalId(BytesN<16>);

impl CanonicalId {
    pub(crate) fn new(env: &Env, id: &DataId) -> Self {
        let mut bytes = id.to_array();
        bytes[DECIMALS_BYTE] = 0;
        Self(BytesN::from_array(env, &bytes))
    }

    /// The masked bytes, for building storage keys.
    pub(crate) fn as_bytes(&self) -> &BytesN<16> {
        &self.0
    }
}

/// Decimals addressed by `id`, or `None` when the decimals byte is out of range.
pub(crate) fn decimals_of(id: &DataId) -> Option<u32> {
    let byte = id.to_array()[DECIMALS_BYTE];
    if byte > DECIMALS_BYTE_MAX {
        return None;
    }
    byte.checked_sub(DECIMALS_OFFSET).map(u32::from)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn data_id(env: &Env, decimals_byte: u8) -> DataId {
        let mut bytes = [0xAB; 16];
        bytes[DECIMALS_BYTE] = decimals_byte;
        BytesN::from_array(env, &bytes)
    }

    #[test]
    fn canonical_id_clears_the_decimals_byte() {
        let env = Env::default();
        let canonical = CanonicalId::new(&env, &data_id(&env, 0x32));
        assert_eq!(canonical.as_bytes().to_array()[DECIMALS_BYTE], 0);
    }

    #[test]
    fn canonical_id_is_shared_by_every_scale_of_a_feed() {
        let env = Env::default();
        let eighteen = CanonicalId::new(&env, &data_id(&env, 0x32));
        let eight = CanonicalId::new(&env, &data_id(&env, 0x28));
        assert_eq!(eighteen, eight);
    }

    #[test]
    fn canonical_id_keeps_the_feed_identity() {
        let env = Env::default();
        let id = data_id(&env, 0x32);
        let canonical = CanonicalId::new(&env, &id);
        let (original, masked) = (id.to_array(), canonical.as_bytes().to_array());
        for (i, (o, m)) in original.iter().zip(masked.iter()).enumerate() {
            if i != DECIMALS_BYTE {
                assert_eq!(o, m, "byte {i} changed");
            }
        }
    }

    #[test]
    fn canonical_id_separates_different_feeds() {
        let env = Env::default();
        let mut other = [0xAB; 16];
        other[0] = 0xCD;
        other[DECIMALS_BYTE] = 0x32;
        let a = CanonicalId::new(&env, &data_id(&env, 0x32));
        let b = CanonicalId::new(&env, &BytesN::from_array(&env, &other));
        assert_ne!(a, b);
    }

    #[test]
    fn decimals_of_reads_the_offset_byte() {
        let env = Env::default();
        assert_eq!(decimals_of(&data_id(&env, 0x20)), Some(0));
        assert_eq!(decimals_of(&data_id(&env, 0x26)), Some(6));
        assert_eq!(decimals_of(&data_id(&env, 0x28)), Some(8));
        assert_eq!(decimals_of(&data_id(&env, 0x32)), Some(18));
        assert_eq!(decimals_of(&data_id(&env, DECIMALS_BYTE_MAX)), Some(64));
    }

    #[test]
    fn decimals_of_rejects_a_byte_below_the_offset() {
        let env = Env::default();
        assert_eq!(decimals_of(&data_id(&env, 0x00)), None);
        assert_eq!(decimals_of(&data_id(&env, 0x1F)), None);
    }

    #[test]
    fn decimals_of_rejects_a_byte_above_the_maximum() {
        let env = Env::default();
        assert_eq!(decimals_of(&data_id(&env, 0x61)), None);
        assert_eq!(decimals_of(&data_id(&env, 0xFF)), None);
    }
}
