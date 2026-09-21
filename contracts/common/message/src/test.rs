#![cfg(test)]

use soroban_sdk::{testutils::Address as _, vec, xdr::ToXdr, Address, Bytes, BytesN, Env, Vec};

use crate::{
    CcipMessageV1, CcipTokenTransferV1, FromBytes, GenericExtraArgsV3, MessageIdCompute,
    StellarToAnyMessage, ToBytes, TokenAmount, MESSAGE_V1_VERSION,
};
use common_error::CCIPError;

// ============================================================
// TokenAmount Tests
// ============================================================

#[test]
fn test_token_amount_validate_positive() {
    let env = Env::default();
    let ta = TokenAmount {
        token: Address::generate(&env),
        amount: 100,
    };
    assert_eq!(ta.validate(), Ok(()));
}

#[test]
fn test_token_amount_validate_zero() {
    let env = Env::default();
    let ta = TokenAmount {
        token: Address::generate(&env),
        amount: 0,
    };
    assert_eq!(ta.validate(), Ok(()));
}

#[test]
fn test_token_amount_validate_negative() {
    let env = Env::default();
    let ta = TokenAmount {
        token: Address::generate(&env),
        amount: -1,
    };
    assert_eq!(ta.validate(), Err(CCIPError::InvalidTokenAmount));
}

#[test]
fn test_token_amount_to_bytes() {
    let env = Env::default();
    let ta = TokenAmount {
        token: Address::generate(&env),
        amount: 100,
    };
    let bytes = ta.to_bytes(&env).unwrap();
    // Address XDR + 16-byte big-endian i128
    let addr_xdr_len = ta.token.clone().to_xdr(&env).len();
    assert_eq!(bytes.len(), addr_xdr_len + 16);
}

// ============================================================
// StellarToAnyMessage Tests
// ============================================================

fn make_message(env: &Env, tokens: Vec<TokenAmount>) -> StellarToAnyMessage {
    StellarToAnyMessage {
        receiver: Bytes::from_array(env, &[0u8; 20]),
        data: Bytes::from_array(env, &[1, 2, 3]),
        token_amounts: tokens,
        fee_token: Address::generate(env),
        extra_args: Bytes::new(env),
    }
}

#[test]
fn test_validate_no_tokens() {
    let env = Env::default();
    let msg = make_message(&env, Vec::new(&env));
    assert_eq!(msg.validate(), Ok(()));
}

#[test]
fn test_validate_one_token() {
    let env = Env::default();
    let ta = TokenAmount {
        token: Address::generate(&env),
        amount: 50,
    };
    let msg = make_message(&env, vec![&env, ta]);
    assert_eq!(msg.validate(), Ok(()));
}

#[test]
fn test_validate_two_tokens_fails() {
    let env = Env::default();
    let ta1 = TokenAmount {
        token: Address::generate(&env),
        amount: 10,
    };
    let ta2 = TokenAmount {
        token: Address::generate(&env),
        amount: 20,
    };
    let msg = make_message(&env, vec![&env, ta1, ta2]);
    assert_eq!(
        msg.validate(),
        Err(CCIPError::CanOnlySendOneTokenPerMessage)
    );
}

#[test]
fn test_validate_negative_token_amount_fails() {
    let env = Env::default();
    let ta = TokenAmount {
        token: Address::generate(&env),
        amount: -5,
    };
    let msg = make_message(&env, vec![&env, ta]);
    assert_eq!(msg.validate(), Err(CCIPError::InvalidTokenAmount));
}

#[test]
fn test_to_bytes_deterministic() {
    let env = Env::default();
    let msg = make_message(&env, Vec::new(&env));
    let b1 = msg.to_bytes(&env).unwrap();
    let b2 = msg.to_bytes(&env).unwrap();
    assert_eq!(b1, b2);
}

#[test]
fn test_compute_message_id_deterministic() {
    let env = Env::default();
    let msg = make_message(&env, Vec::new(&env));
    let id1 = msg.compute_message_id(&env).unwrap();
    let id2 = msg.compute_message_id(&env).unwrap();
    assert_eq!(id1, id2);
    assert_eq!(id1.len(), 32);
}

#[test]
fn test_compute_message_id_differs_on_data_change() {
    let env = Env::default();
    let mut msg1 = make_message(&env, Vec::new(&env));
    let msg2 = make_message(&env, Vec::new(&env));

    msg1.data = Bytes::from_array(&env, &[99, 98, 97]);
    let id1 = msg1.compute_message_id(&env).unwrap();
    let id2 = msg2.compute_message_id(&env).unwrap();
    assert_ne!(id1, id2);
}

// ============================================================
// CcipTokenTransferV1 Tests
// ============================================================

#[test]
fn test_token_transfer_v1_to_bytes_layout() {
    let env = Env::default();
    let src_pool = Bytes::from_array(&env, &[0xAA; 10]);
    let src_token = Bytes::from_array(&env, &[0xBB; 8]);
    let dest_token = Bytes::from_array(&env, &[0xCC; 12]);
    let token_receiver = Bytes::from_array(&env, &[0xDD; 6]);
    let extra_data = Bytes::from_array(&env, &[0xEE; 4]);

    let mut amount_bytes = [0u8; 32];
    amount_bytes[31] = 42;

    let tt = CcipTokenTransferV1 {
        version: 1,
        amount: BytesN::from_array(&env, &amount_bytes),
        source_pool_address: src_pool.clone(),
        source_token_address: src_token.clone(),
        dest_token_address: dest_token.clone(),
        token_receiver: token_receiver.clone(),
        extra_data: extra_data.clone(),
    };

    let encoded = tt.to_bytes(&env).unwrap();

    // version(1) + amount(32) + src_pool(1+10) + src_token(1+8)
    // + dest_token(1+12) + token_receiver(1+6) + extra_data(2+4)
    let expected_len: u32 = 1 + 32 + (1 + 10) + (1 + 8) + (1 + 12) + (1 + 6) + (2 + 4);
    assert_eq!(encoded.len(), expected_len);

    // First byte is version
    assert_eq!(encoded.get(0).unwrap(), 1u8);
}

#[test]
fn test_token_transfer_v1_empty_fields() {
    let env = Env::default();
    let amount_bytes = [0u8; 32];

    let tt = CcipTokenTransferV1 {
        version: 1,
        amount: BytesN::from_array(&env, &amount_bytes),
        source_pool_address: Bytes::new(&env),
        source_token_address: Bytes::new(&env),
        dest_token_address: Bytes::new(&env),
        token_receiver: Bytes::new(&env),
        extra_data: Bytes::new(&env),
    };

    let encoded = tt.to_bytes(&env).unwrap();
    // version(1) + amount(32) + 4 * (1+0) + (2+0) = 1 + 32 + 4 + 2 = 39
    assert_eq!(encoded.len(), 39);
}

#[test]
fn test_token_transfer_v1_to_bytes_from_bytes_roundtrip() {
    let env = Env::default();
    let src_pool = Bytes::from_array(&env, &[0xAA; 10]);
    let src_token = Bytes::from_array(&env, &[0xBB; 8]);
    let dest_token = Bytes::from_array(&env, &[0xCC; 12]);
    let token_receiver = Bytes::from_array(&env, &[0xDD; 6]);
    let extra_data = Bytes::from_array(&env, &[0xEE; 4]);

    let mut amount_bytes = [0u8; 32];
    amount_bytes[30] = 0x01;
    amount_bytes[31] = 0x23;

    let original = CcipTokenTransferV1 {
        version: 1,
        amount: BytesN::from_array(&env, &amount_bytes),
        source_pool_address: src_pool.clone(),
        source_token_address: src_token.clone(),
        dest_token_address: dest_token.clone(),
        token_receiver: token_receiver.clone(),
        extra_data: extra_data.clone(),
    };

    let encoded = original.to_bytes(&env).unwrap();
    let decoded = CcipTokenTransferV1::from_bytes(&env, &encoded).unwrap();

    assert_eq!(decoded.version, original.version);
    assert_eq!(decoded.amount.to_array(), original.amount.to_array());
    assert_eq!(decoded.source_pool_address, src_pool);
    assert_eq!(decoded.source_token_address, src_token);
    assert_eq!(decoded.dest_token_address, dest_token);
    assert_eq!(decoded.token_receiver, token_receiver);
    assert_eq!(decoded.extra_data, extra_data);
}

#[test]
fn test_token_transfer_v1_from_bytes_roundtrip_empty_variable_fields() {
    let env = Env::default();
    let original = CcipTokenTransferV1 {
        version: 1,
        amount: BytesN::from_array(&env, &[0u8; 32]),
        source_pool_address: Bytes::new(&env),
        source_token_address: Bytes::new(&env),
        dest_token_address: Bytes::new(&env),
        token_receiver: Bytes::new(&env),
        extra_data: Bytes::new(&env),
    };

    let encoded = original.to_bytes(&env).unwrap();
    let decoded = CcipTokenTransferV1::from_bytes(&env, &encoded).unwrap();
    assert_eq!(decoded.version, 1);
    assert_eq!(decoded.amount.to_array(), [0u8; 32]);
    assert_eq!(decoded.extra_data.len(), 0);
}

#[test]
fn test_token_transfer_v1_from_bytes_truncated_returns_decoding_error() {
    let env = Env::default();
    let tt = CcipTokenTransferV1 {
        version: 1,
        amount: BytesN::from_array(&env, &[1u8; 32]),
        source_pool_address: Bytes::from_array(&env, &[2u8; 5]),
        source_token_address: Bytes::new(&env),
        dest_token_address: Bytes::new(&env),
        token_receiver: Bytes::new(&env),
        extra_data: Bytes::new(&env),
    };
    let encoded = tt.to_bytes(&env).unwrap();
    let truncated = encoded.slice(0..encoded.len() - 1);

    let err = CcipTokenTransferV1::from_bytes(&env, &truncated);
    assert!(matches!(err, Err(CCIPError::MessageDecodingError)));
}

#[test]
fn test_token_transfer_v1_from_bytes_too_short_returns_decoding_error() {
    let env = Env::default();
    let short = Bytes::from_array(&env, &[0u8; 10]);
    let err = CcipTokenTransferV1::from_bytes(&env, &short);
    assert!(matches!(err, Err(CCIPError::MessageDecodingError)));
}

// ---- M-1: strict decoding (version byte + trailing bytes) ----

#[test]
fn test_token_transfer_v1_from_bytes_wrong_version_returns_decoding_error() {
    // INV-MSG-2: a token transfer carrying an unexpected version byte must be rejected.
    let env = Env::default();
    let tt = CcipTokenTransferV1 {
        version: 1,
        amount: BytesN::from_array(&env, &[1u8; 32]),
        source_pool_address: Bytes::from_array(&env, &[2u8; 5]),
        source_token_address: Bytes::new(&env),
        dest_token_address: Bytes::new(&env),
        token_receiver: Bytes::new(&env),
        extra_data: Bytes::new(&env),
    };
    let mut encoded = tt.to_bytes(&env).unwrap();
    // Overwrite the version byte (index 0) with a non-v1 version.
    encoded.set(0, MESSAGE_V1_VERSION + 1);
    let err = CcipTokenTransferV1::from_bytes(&env, &encoded);
    assert!(matches!(err, Err(CCIPError::MessageDecodingError)));
}

#[test]
fn test_token_transfer_v1_from_bytes_trailing_bytes_returns_decoding_error() {
    // INV-MSG-3/11: trailing bytes after the final field must be rejected (strict consumption).
    let env = Env::default();
    let tt = CcipTokenTransferV1 {
        version: 1,
        amount: BytesN::from_array(&env, &[1u8; 32]),
        source_pool_address: Bytes::from_array(&env, &[2u8; 5]),
        source_token_address: Bytes::new(&env),
        dest_token_address: Bytes::new(&env),
        token_receiver: Bytes::new(&env),
        extra_data: Bytes::new(&env),
    };
    let encoded = tt.to_bytes(&env).unwrap();
    let mut padded = encoded.clone();
    padded.append(&Bytes::from_array(&env, &[0xFF]));
    let err = CcipTokenTransferV1::from_bytes(&env, &padded);
    assert!(matches!(err, Err(CCIPError::MessageDecodingError)));
}

// ---- M-4: checked length casts (INV-ENC-11) ----

#[test]
fn test_token_transfer_v1_to_bytes_oversized_u8_field_rejected() {
    // INV-ENC-11: a 1-byte length-prefixed field exceeding 255 bytes must be
    // rejected with `MessageTooLarge` instead of silently wrapping the cast.
    let env = Env::default();
    let tt = CcipTokenTransferV1 {
        version: 1,
        amount: BytesN::from_array(&env, &[0u8; 32]),
        source_pool_address: Bytes::from_array(&env, &[0u8; 256]),
        source_token_address: Bytes::new(&env),
        dest_token_address: Bytes::new(&env),
        token_receiver: Bytes::new(&env),
        extra_data: Bytes::new(&env),
    };
    let err = tt.to_bytes(&env);
    assert!(matches!(err, Err(CCIPError::MessageTooLarge)));
}

#[test]
fn test_token_transfer_v1_to_bytes_oversized_u16_field_rejected() {
    // INV-ENC-11: a 2-byte length-prefixed field exceeding 65535 bytes must be
    // rejected with `MessageTooLarge` instead of silently wrapping the cast.
    let env = Env::default();
    let tt = CcipTokenTransferV1 {
        version: 1,
        amount: BytesN::from_array(&env, &[0u8; 32]),
        source_pool_address: Bytes::new(&env),
        source_token_address: Bytes::new(&env),
        dest_token_address: Bytes::new(&env),
        token_receiver: Bytes::new(&env),
        extra_data: Bytes::from_array(&env, &[0u8; 65536]),
    };
    let err = tt.to_bytes(&env);
    assert!(matches!(err, Err(CCIPError::MessageTooLarge)));
}

// ============================================================
// CcipMessageV1 Tests
// ============================================================

fn make_ccip_message_v1(env: &Env) -> CcipMessageV1 {
    CcipMessageV1 {
        source_chain_selector: 1001,
        dest_chain_selector: 2002,
        sequence_number: 42,
        execution_gas_limit: 200_000,
        ccip_receive_gas_limit: 100_000,
        finality: 12,
        ccv_and_executor_hash: BytesN::from_array(env, &[0xAB; 32]),
        onramp_address: Bytes::from_array(env, &[1u8; 32]),
        offramp_address: Bytes::from_array(env, &[2u8; 20]),
        sender: Bytes::from_array(env, &[3u8; 32]),
        receiver: Bytes::from_array(env, &[4u8; 20]),
        dest_blob: Bytes::from_array(env, &[5u8; 10]),
        token_transfer: Bytes::from_array(env, &[6u8; 39]),
        data: Bytes::from_array(env, &[7u8; 100]),
    }
}

#[test]
fn test_message_v1_to_bytes_layout() {
    let env = Env::default();
    let msg = make_ccip_message_v1(&env);
    let encoded = msg.to_bytes(&env).unwrap();

    // Fixed portion: version(1) + src_chain(8) + dst_chain(8) + seq(8) +
    //   exec_gas(4) + recv_gas(4) + finality(4) + ccv_hash(32) = 69
    // Variable: onramp(1+32) + offramp(1+20) + sender(1+32) + receiver(1+20)
    //   + dest_blob(2+10) + token_transfer(2+39) + data(2+100)
    let expected: u32 =
        69 + (1 + 32) + (1 + 20) + (1 + 32) + (1 + 20) + (2 + 10) + (2 + 39) + (2 + 100);
    assert_eq!(encoded.len(), expected);

    // First byte must be MESSAGE_V1_VERSION
    assert_eq!(encoded.get(0).unwrap(), MESSAGE_V1_VERSION);
}

// ---- M-1: strict decoding for CcipMessageV1 (trailing bytes) ----

#[test]
fn test_message_v1_from_bytes_roundtrip() {
    let env = Env::default();
    let msg = make_ccip_message_v1(&env);
    let encoded = msg.to_bytes(&env).unwrap();
    let decoded = CcipMessageV1::from_bytes(&env, &encoded).unwrap();
    assert_eq!(decoded.source_chain_selector, msg.source_chain_selector);
    assert_eq!(decoded.dest_chain_selector, msg.dest_chain_selector);
    assert_eq!(decoded.sequence_number, msg.sequence_number);
    assert_eq!(decoded.execution_gas_limit, msg.execution_gas_limit);
    assert_eq!(decoded.ccip_receive_gas_limit, msg.ccip_receive_gas_limit);
    assert_eq!(decoded.finality, msg.finality);
    assert_eq!(
        decoded.ccv_and_executor_hash.to_array(),
        msg.ccv_and_executor_hash.to_array()
    );
    assert_eq!(decoded.onramp_address, msg.onramp_address);
    assert_eq!(decoded.offramp_address, msg.offramp_address);
    assert_eq!(decoded.sender, msg.sender);
    assert_eq!(decoded.receiver, msg.receiver);
    assert_eq!(decoded.dest_blob, msg.dest_blob);
    assert_eq!(decoded.token_transfer, msg.token_transfer);
    assert_eq!(decoded.data, msg.data);
}

#[test]
fn test_message_v1_from_bytes_trailing_bytes_returns_decoding_error() {
    // INV-MSG-3: trailing bytes after the final `data` field must be rejected (strict consumption),
    // mirroring EVM `MessageV1Codec` `MESSAGE_FINAL_OFFSET` (MessageV1Codec.sol:512).
    let env = Env::default();
    let msg = make_ccip_message_v1(&env);
    let encoded = msg.to_bytes(&env).unwrap();
    let mut padded = encoded.clone();
    padded.append(&Bytes::from_array(&env, &[0xFF]));
    let err = CcipMessageV1::from_bytes(&env, &padded);
    assert!(matches!(err, Err(CCIPError::MessageDecodingError)));
}

#[test]
fn test_message_v1_empty_variable_fields() {
    let env = Env::default();
    let msg = CcipMessageV1 {
        source_chain_selector: 0,
        dest_chain_selector: 0,
        sequence_number: 0,
        execution_gas_limit: 0,
        ccip_receive_gas_limit: 0,
        finality: 0,
        ccv_and_executor_hash: BytesN::from_array(&env, &[0u8; 32]),
        onramp_address: Bytes::new(&env),
        offramp_address: Bytes::new(&env),
        sender: Bytes::new(&env),
        receiver: Bytes::new(&env),
        dest_blob: Bytes::new(&env),
        token_transfer: Bytes::new(&env),
        data: Bytes::new(&env),
    };

    let encoded = msg.to_bytes(&env).unwrap();
    // 69 (fixed) + 4*(1+0) + 3*(2+0) = 69 + 4 + 6 = 79
    assert_eq!(encoded.len(), 79);
}

#[test]
fn test_message_v1_compute_message_id() {
    let env = Env::default();
    let msg = make_ccip_message_v1(&env);
    let id = msg.compute_message_id(&env).unwrap();

    let expected: BytesN<32> = env.crypto().keccak256(&msg.to_bytes(&env).unwrap()).into();
    assert_eq!(id, expected);
}

#[test]
fn test_message_v1_different_fields_different_id() {
    let env = Env::default();
    let msg1 = make_ccip_message_v1(&env);

    let mut msg2 = make_ccip_message_v1(&env);
    msg2.sequence_number = 999;

    assert_ne!(
        msg1.compute_message_id(&env).unwrap(),
        msg2.compute_message_id(&env).unwrap()
    );
}

// ---- M-4: checked length casts for CcipMessageV1 (INV-ENC-11) ----

#[test]
fn test_message_v1_to_bytes_oversized_u8_field_rejected() {
    // INV-ENC-11: a 1-byte length-prefixed address field exceeding 255 bytes
    // must be rejected with `MessageTooLarge` instead of silently wrapping.
    let env = Env::default();
    let mut msg = make_ccip_message_v1(&env);
    msg.receiver = Bytes::from_array(&env, &[0u8; 256]);
    let err = msg.to_bytes(&env);
    assert!(matches!(err, Err(CCIPError::MessageTooLarge)));
}

#[test]
fn test_message_v1_to_bytes_oversized_u16_field_rejected() {
    // INV-ENC-11: a 2-byte length-prefixed `data` field exceeding 65535 bytes
    // (FeeQuoter `max_data_bytes` is operator-configurable above this width)
    // must be rejected with `MessageTooLarge` instead of silently wrapping.
    let env = Env::default();
    let mut msg = make_ccip_message_v1(&env);
    msg.data = Bytes::from_array(&env, &[0u8; 65536]);
    let err = msg.to_bytes(&env);
    assert!(matches!(err, Err(CCIPError::MessageTooLarge)));
}

#[test]
fn test_message_v1_compute_message_id_propagates_oversize_error() {
    // INV-ENC-11: an un-encodable message must surface `MessageTooLarge` from
    // `compute_message_id` rather than hashing a silently-truncated encoding.
    let env = Env::default();
    let mut msg = make_ccip_message_v1(&env);
    msg.data = Bytes::from_array(&env, &[0u8; 65536]);
    let err = msg.compute_message_id(&env);
    assert!(matches!(err, Err(CCIPError::MessageTooLarge)));
}

// ============================================================
// CcvAndExecutorHash Tests
// ============================================================

#[test]
fn test_compute_ccv_and_executor_hash_single_ccv() {
    let env = Env::default();
    let ccv = Address::generate(&env);
    let executor = Address::generate(&env);
    let ccvs = vec![&env, ccv.clone()];

    let hash = CcipMessageV1::compute_ccv_and_executor_hash(&env, &ccvs, &executor);
    assert_eq!(hash.len(), 32);

    // Recomputing with same inputs produces the same hash
    let hash2 = CcipMessageV1::compute_ccv_and_executor_hash(&env, &ccvs, &executor);
    assert_eq!(hash, hash2);
}

#[test]
fn test_compute_ccv_and_executor_hash_multiple_ccvs() {
    let env = Env::default();
    let ccv_a = Address::generate(&env);
    let ccv_b = Address::generate(&env);
    let executor = Address::generate(&env);

    let hash_ab = CcipMessageV1::compute_ccv_and_executor_hash(
        &env,
        &vec![&env, ccv_a.clone(), ccv_b.clone()],
        &executor,
    );
    let hash_ba = CcipMessageV1::compute_ccv_and_executor_hash(
        &env,
        &vec![&env, ccv_b.clone(), ccv_a.clone()],
        &executor,
    );

    // Swapping CCV order must produce a different hash
    assert_ne!(hash_ab, hash_ba);
}

#[test]
fn test_address_raw_bytes_contract() {
    let env = Env::default();
    let addr = Address::generate(&env);
    let raw = CcipMessageV1::address_raw_bytes(&env, addr.clone());
    assert_eq!(raw.len(), 32);
}

#[test]
fn test_address_raw_bytes_deterministic() {
    let env = Env::default();
    let addr = Address::generate(&env);
    let raw1 = CcipMessageV1::address_raw_bytes(&env, addr.clone());
    let raw2 = CcipMessageV1::address_raw_bytes(&env, addr.clone());
    assert_eq!(raw1, raw2);
}

// ============================================================
// GenericExtraArgsV3 Tests
// ============================================================

#[test]
fn test_extra_args_v3_new_defaults() {
    let env = Env::default();
    let executor = Address::generate(&env);
    let args = GenericExtraArgsV3::new(&env, executor.clone());

    assert_eq!(args.gas_limit, 0);
    assert_eq!(args.block_confirmations, 0);
    assert_eq!(args.ccvs.len(), 0);
    assert_eq!(args.ccv_args.len(), 0);
    assert_eq!(args.executor, executor);
    assert_eq!(args.executor_args.len(), 0);
    assert_eq!(args.token_receiver.len(), 0);
    assert_eq!(args.token_args.len(), 0);
}

// ============================================================
// Executor sentinel tests
// ============================================================

#[test]
fn test_no_execution_sentinel_round_trip() {
    let env = Env::default();
    let sentinel = GenericExtraArgsV3::no_execution_address(&env);
    // Recognized via the raw 32-byte key comparison.
    assert!(GenericExtraArgsV3::is_no_execution_address(&env, &sentinel));
    // Not mistaken for the "use default" sentinel.
    assert!(!GenericExtraArgsV3::is_use_default_executor_address(
        &env, &sentinel
    ));
    // The leading 4 bytes of the raw key are the EVM NO_EXECUTION tag.
    let raw = CcipMessageV1::address_raw_bytes(&env, sentinel.clone());
    let mut tag = [0u8; 4];
    for i in 0..4 {
        tag[i as usize] = raw.get(i).unwrap();
    }
    assert_eq!(tag, GenericExtraArgsV3::NO_EXECUTION_TAG);
}

#[test]
fn test_use_default_sentinel_round_trip() {
    let env = Env::default();
    let sentinel = GenericExtraArgsV3::use_default_executor_address(&env);
    assert!(GenericExtraArgsV3::is_use_default_executor_address(
        &env, &sentinel
    ));
    assert!(!GenericExtraArgsV3::is_no_execution_address(
        &env, &sentinel
    ));
    let raw = CcipMessageV1::address_raw_bytes(&env, sentinel.clone());
    let mut tag = [0u8; 4];
    for i in 0..4 {
        tag[i as usize] = raw.get(i).unwrap();
    }
    assert_eq!(tag, GenericExtraArgsV3::USE_DEFAULT_TAG);
}

#[test]
fn test_sentinels_distinct_from_real_addresses() {
    let env = Env::default();
    // A generated contract/account address is neither sentinel.
    let real = Address::generate(&env);
    assert!(!GenericExtraArgsV3::is_no_execution_address(&env, &real));
    assert!(!GenericExtraArgsV3::is_use_default_executor_address(
        &env, &real
    ));
}
