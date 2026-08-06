use soroban_sdk::{BytesN, Env};

pub const DECIMALS_BYTE: usize = 7;

const DECIMALS_OFFSET: u8 = 0x20;

const DECIMALS_BYTE_MAX: u8 = 0x60;

pub fn decimals_of(id: &BytesN<16>) -> Option<u32> {
    let byte = id.to_array()[DECIMALS_BYTE];
    if byte > DECIMALS_BYTE_MAX {
        return None;
    }
    byte.checked_sub(DECIMALS_OFFSET).map(u32::from)
}

pub fn to_canonical(env: &Env, id: &BytesN<16>) -> BytesN<16> {
    let mut bytes = id.to_array();
    bytes[DECIMALS_BYTE] = 0;
    BytesN::from_array(env, &bytes)
}

#[cfg(test)]
mod tests {
    use super::*;
    use soroban_sdk::Env;

    fn data_id(env: &Env, decimals_byte: u8) -> BytesN<16> {
        let mut bytes = [0xAB; 16];
        bytes[DECIMALS_BYTE] = decimals_byte;
        BytesN::from_array(env, &bytes)
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

    #[test]
    fn every_scale_maps_to_one_unaddressable_key() {
        let env = Env::default();
        let key = to_canonical(&env, &data_id(&env, 0x20));
        for byte in [0x20u8, 0x26, 0x28, 0x32, DECIMALS_BYTE_MAX] {
            assert_eq!(
                to_canonical(&env, &data_id(&env, byte)),
                key,
                "scale {byte:#x} must map to the same storage key, zero decimals included"
            );
        }
        assert_eq!(key.to_array()[DECIMALS_BYTE], 0);
        assert_eq!(decimals_of(&key), None, "a storage key is never addressable");
    }
}
