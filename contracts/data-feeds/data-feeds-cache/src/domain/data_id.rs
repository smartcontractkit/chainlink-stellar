use soroban_sdk::{BytesN, Env};

use crate::interface::data_id::DECIMALS_BYTE;
use crate::interface::DataId;

#[derive(Clone, Debug, Eq, PartialEq)]
pub(crate) struct CanonicalId(BytesN<16>);

impl CanonicalId {
    pub(crate) fn new(env: &Env, id: &DataId) -> Self {
        let mut bytes = id.to_array();
        bytes[DECIMALS_BYTE] = 0;
        Self(BytesN::from_array(env, &bytes))
    }

    pub(crate) fn as_bytes(&self) -> &BytesN<16> {
        &self.0
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::interface::data_id::decimals_of;

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
    fn a_canonical_key_is_never_itself_addressable() {
        let env = Env::default();
        let masked = CanonicalId::new(&env, &data_id(&env, 0x32));

        assert_eq!(decimals_of(masked.as_bytes()), None);
    }
}
