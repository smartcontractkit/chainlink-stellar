#![no_std]

use common_error::CCIPError;
use soroban_sdk::{contracttype, xdr::ToXdr, Address, Bytes, BytesN, Env, Vec};

// ============================================================
// MessageIdCompute Trait
// ============================================================

/// Trait for types that can be serialized to Bytes.
/// Unlike `Into<Bytes>`, this takes `&Env` which is required by Soroban's `Bytes` type.
///
/// Encoding is fallible: the canonical CCIP v1 wire format prefixes variable
/// fields with a 1-byte (max 255) or 2-byte (max 65535) length. A field whose
/// length exceeds the prefix width cannot be encoded faithfully — the `as u8` /
/// `as u16` cast would silently wrap and produce a corrupt message ID. Such
/// inputs are rejected with `CCIPError::MessageTooLarge` (INV-ENC-11) rather
/// than mis-encoded.
pub trait ToBytes {
    fn to_bytes(&self, env: &Env) -> Result<Bytes, CCIPError>;
}

/// Trait for deserializing from Bytes.
pub trait FromBytes: Sized {
    fn from_bytes(env: &Env, bytes: &Bytes) -> Result<Self, CCIPError>;
}

/// Trait for computing CCIP message IDs.
/// Implementors of this trait provide the logic for generating deterministic
/// message identifiers based on message content.
pub trait MessageIdCompute: ToBytes {
    /// Computes the message ID for a CCIP message.
    fn compute_message_id(&self, env: &Env) -> Result<BytesN<32>, CCIPError> {
        let bytes = self.to_bytes(env)?;
        let hash = env.crypto().keccak256(&bytes);
        Ok(hash.into())
    }

    fn compute_message_id_from_bytes(env: &Env, bytes: &Bytes) -> BytesN<32> {
        let hash = env.crypto().keccak256(bytes);
        hash.into()
    }
}

/// INV-ENC-11: length-prefix cast helpers that fail closed instead of silently
/// wrapping. The CCIP v1 format encodes address-field lengths in 1 byte and
/// data/blob lengths in 2 bytes; an oversized field cannot be represented and
/// must be rejected with `MessageTooLarge` rather than truncated.
fn len_as_u8(len: u32) -> Result<u8, CCIPError> {
    if len > u8::MAX as u32 {
        return Err(CCIPError::MessageTooLarge);
    }
    Ok(len as u8)
}

fn len_as_u16(len: u32) -> Result<u16, CCIPError> {
    if len > u16::MAX as u32 {
        return Err(CCIPError::MessageTooLarge);
    }
    Ok(len as u16)
}

// ============================================================
// TokenAmount
// ============================================================

/// Token amount struct for message token transfers.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TokenAmount {
    /// Token contract address
    pub token: Address,
    /// Amount to transfer - we use i128 to match SACs which does it to simplify arithmetic operations
    pub amount: i128,
}

impl TokenAmount {
    pub fn validate(&self) -> Result<(), CCIPError> {
        if self.amount < 0 {
            return Err(CCIPError::InvalidTokenAmount);
        }
        Ok(())
    }
}

impl ToBytes for TokenAmount {
    fn to_bytes(&self, env: &Env) -> Result<Bytes, CCIPError> {
        let mut bytes = Bytes::new(env);
        // Convert Address to its XDR byte representation
        bytes.append(&self.token.clone().to_xdr(env));
        // Convert i128 to big-endian bytes (16 bytes)
        bytes.append(&Bytes::from_array(env, &self.amount.to_be_bytes()));
        Ok(bytes)
    }
}

// ============================================================
// GenericExtraArgsV3
// ============================================================

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct GenericExtraArgsV3 {
    pub gas_limit: u32,
    pub block_confirmations: u32,
    pub ccvs: Vec<Address>,
    pub ccv_args: Vec<Bytes>,
    pub executor: Address,
    pub executor_args: Bytes,
    pub token_receiver: Bytes,
    pub token_args: Bytes,
}

impl GenericExtraArgsV3 {
    pub fn new(env: &Env, executor: Address) -> Self {
        Self {
            gas_limit: 0,
            block_confirmations: 0,
            ccvs: Vec::new(env),
            ccv_args: Vec::new(env),
            executor,
            executor_args: Bytes::new(env),
            token_receiver: Bytes::new(env),
            token_args: Bytes::new(env),
        }
    }

    // ============================================================
    // Executor sentinels (EVM parity for the `executor` field)
    // ============================================================
    //
    // EVM carries two sentinel values in the extraArgs executor address field:
    //
    //   `address(0)`                  → "use the lane's defaultExecutor" (auto exec)
    //   `NO_EXECUTION_ADDRESS`         → "no auto-execution; manual"
    //     = address(bytes20(0xeba517d2))   (a non-zero, recognizable 20-byte tag)
    //
    // Soroban `Address` has no zero value, so BOTH EVM sentinels become
    // recognizable non-zero 32-byte *contract* Addresses that live in the
    // existing `executor` field (no schema change). Each is a 4-byte tag,
    // left-aligned, zero-padded to 32 bytes — the contract id. Real executor
    // contracts have hashed 32-byte ids, so collision with a fixed tag is
    // astronomically infeasible. Byte-level parity with EVM's 20-byte addresses
    // is impossible (Stellar addresses are 32 bytes); we preserve wire-FIELD-
    // shape parity instead — the sentinel occupies the executor ADDRESS field on
    // every chain (INV-NOEXEC-1), and `compute_ccv_and_executor_hash` is
    // unaffected (it hashes whatever 32 bytes the field holds).

    /// The 4-byte tag prefix of the no-execution sentinel, left-aligned in the
    /// 32-byte executor address. Mirrors EVM
    /// `NO_EXECUTION_ADDRESS = address(bytes20(0xeba517d2))`.
    pub const NO_EXECUTION_TAG: [u8; 4] = [0xeb, 0xa5, 0x17, 0xd2];

    /// The 4-byte tag prefix of the "use default executor" sentinel. Derived as
    /// `keccak256("USE_DEFAULT_EXECUTOR_TAG")[0..4]`. Mirrors EVM `address(0)`
    /// in the executor field of non-empty extraArgs — "resolve to the lane's
    /// `defaultExecutor`" without naming it.
    pub const USE_DEFAULT_TAG: [u8; 4] = [0x72, 0x06, 0x8b, 0x37];

    /// The full 32-byte contract id of the no-execution sentinel (tag ‖ 28 zero
    /// bytes). Compared against `CcipMessageV1::address_raw_bytes` to recognize
    /// the sentinel without constructing an `Address`.
    const NO_EXECUTION_ID32: [u8; 32] = [
        0xeb, 0xa5, 0x17, 0xd2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
        0, 0, 0, 0, 0, 0,
    ];

    /// The full 32-byte contract id of the "use default executor" sentinel.
    const USE_DEFAULT_ID32: [u8; 32] = [
        0x72, 0x06, 0x8b, 0x37, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
        0, 0, 0, 0, 0, 0,
    ];

    /// Stellar contract strkey (`C…`) encoding of [`Self::NO_EXECUTION_ID32`].
    /// Precomputed so the sentinel can be materialized via `Address::from_str`
    /// with no runtime strkey dependency in the wasm.
    pub const NO_EXECUTION_STRKEY: &'static str =
        "CDV2KF6SAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAEJ6";

    /// Stellar contract strkey (`C…`) encoding of [`Self::USE_DEFAULT_ID32`].
    pub const USE_DEFAULT_EXECUTOR_STRKEY: &'static str =
        "CBZANCZXAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAVP2";

    // Compile-time guarantee that the two sentinel tags never collide is
    // enforced at module scope (anonymous `const _: ()` below) — such a const
    // is not permitted inside an `impl` block.

    /// The no-execution sentinel `Address`. Place in the `executor` field of
    /// [`GenericExtraArgsV3`] to request manual (non-auto) execution: the OnRamp
    /// leaves it in place, zeroes the executor flat fee and execution-gas cost,
    /// and still emits the executor receipt for accounting (M-7 / INV-NOEXEC-1/2).
    pub fn no_execution_address(env: &Env) -> Address {
        Address::from_str(env, Self::NO_EXECUTION_STRKEY)
    }

    /// The "use default executor" sentinel `Address`. Place in the `executor`
    /// field of non-empty extraArgs to request the lane's `defaultExecutor`
    /// without naming it — the OnRamp resolves it to `dest_config.default_executor`
    /// before hashing and before `Executor::get_fee` (EVM `address(0)→default`
    /// parity; M-5 / INV-ENC-5).
    pub fn use_default_executor_address(env: &Env) -> Address {
        Address::from_str(env, Self::USE_DEFAULT_EXECUTOR_STRKEY)
    }

    /// True iff `addr` is the no-execution sentinel. Comparison is on the raw
    /// 32-byte address key (via [`CcipMessageV1::address_raw_bytes`]), so it
    /// recognizes the sentinel regardless of whether the caller materialized it
    /// via [`Self::no_execution_address`] or off-chain strkey construction.
    pub fn is_no_execution_address(env: &Env, addr: &Address) -> bool {
        CcipMessageV1::address_raw_bytes(env, addr.clone())
            == Bytes::from_array(env, &Self::NO_EXECUTION_ID32)
    }

    /// True iff `addr` is the "use default executor" sentinel.
    pub fn is_use_default_executor_address(env: &Env, addr: &Address) -> bool {
        CcipMessageV1::address_raw_bytes(env, addr.clone())
            == Bytes::from_array(env, &Self::USE_DEFAULT_ID32)
    }
}

// Compile-time guarantee that the two executor sentinel tags never collide.
const _: () = assert!(
    GenericExtraArgsV3::USE_DEFAULT_TAG[0] != GenericExtraArgsV3::NO_EXECUTION_TAG[0]
        && GenericExtraArgsV3::USE_DEFAULT_TAG[1] != GenericExtraArgsV3::NO_EXECUTION_TAG[1]
        && GenericExtraArgsV3::USE_DEFAULT_TAG[2] != GenericExtraArgsV3::NO_EXECUTION_TAG[2]
        && GenericExtraArgsV3::USE_DEFAULT_TAG[3] != GenericExtraArgsV3::NO_EXECUTION_TAG[3]
);

// ============================================================
// StellarToAnyMessage
// ============================================================

/// CCIP Message structure for sending cross-chain messages.
/// This represents the message from the sender's perspective.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StellarToAnyMessage {
    /// Receiver address on destination chain (raw bytes)
    pub receiver: Bytes,
    /// Arbitrary data payload to deliver
    pub data: Bytes,
    /// Tokens to transfer (max 1 in CCIP 1.7)
    pub token_amounts: Vec<TokenAmount>,
    /// Fee token address
    pub fee_token: Address,
    /// Extra arguments (encoded ExtraArgsV3 or legacy format)
    pub extra_args: Bytes,
}

impl StellarToAnyMessage {
    pub fn validate(&self) -> Result<(), CCIPError> {
        if self.token_amounts.len() > 1 {
            return Err(CCIPError::CanOnlySendOneTokenPerMessage);
        }

        for token_amount in self.token_amounts.iter() {
            token_amount.validate()?;
        }

        // NOTE: the destination `receiver` length is NOT checked here because
        // `StellarToAnyMessage` does not know the destination chain's
        // `address_bytes_length`. That check is enforced on the OnRamp
        // (`OnRamp::validate_dest_address`, mirroring EVM `OnRamp._validateDestChainAddress`)
        // in both `get_fee` and `forward_from_router`, where `DestChainConfig` is in scope.

        Ok(())
    }
}

impl ToBytes for StellarToAnyMessage {
    fn to_bytes(&self, env: &Env) -> Result<Bytes, CCIPError> {
        let mut bytes = Bytes::new(env);
        bytes.append(&self.receiver);
        bytes.append(&self.data);
        for token_amount in self.token_amounts.iter() {
            bytes.append(&token_amount.to_bytes(env)?);
        }
        bytes.append(&self.fee_token.clone().to_xdr(env));
        bytes.append(&self.extra_args);
        Ok(bytes)
    }
}

impl MessageIdCompute for StellarToAnyMessage {}

// ============================================================
// AnyToStellarMessage
// ============================================================

/// Decoded inbound CCIP message for Stellar receivers.
/// This is the Stellar analog of the EVM's `Client.Any2EVMMessage`.
/// Built by the OffRamp from the canonical `CcipMessageV1` after verification.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AnyToStellarMessage {
    pub message_id: BytesN<32>,
    pub source_chain_selector: u64,
    /// Sender address on the source chain (raw bytes, chain-family dependent encoding)
    pub sender: Bytes,
    /// Arbitrary data payload
    pub data: Bytes,
    /// Token amounts released/minted on this chain
    pub dest_token_amounts: Vec<TokenAmount>,
}

// ============================================================
// CCIP MessageV1 Canonical Format
// ============================================================
//
// These types implement the chain-agnostic CCIP v1.7 canonical message
// encoding, matching the protocol.Message.Encode() and
// protocol.TokenTransfer.Encode() formats used by offchain services.
//
// The OnRamp constructs a CcipMessageV1 from user message fields plus
// routing metadata, then calls compute_message_id() to derive the
// deterministic message ID that matches the offchain computation.

/// Current message format version for CCIP v1.7.
pub const MESSAGE_V1_VERSION: u8 = 1;

/// Fixed framing size of a `CcipMessageV1` excluding any variable-length field
/// content. The wire layout is the chain-agnostic CCIP v1.7 format, byte-for-byte
/// identical to EVM `MessageV1Codec.MESSAGE_V1_BASE_SIZE`:
///   1 (version) + 8 (sourceChain) + 8 (destChain) + 8 (msgNum) +
///   4 (executionGasLimit) + 4 (ccipReceiveGasLimit) + 4 (finality) +
///   32 (ccvAndExecutorHash) + 1 (onRampLen) + 1 (offRampLen) +
///   1 (senderLen) + 1 (receiverLen) + 2 (destBlobLen) + 2 (tokenTransferLen) +
///   2 (dataLen) = 79.
/// The length-prefix bytes are fixed; the field contents they prefix are
/// variable (receiver/offRamp/destBlob are dest-chain-specific, data is
/// user-specified, tokenTransfer is optional) and are billed separately.
pub const MESSAGE_V1_BASE_SIZE: u32 = 1 + 8 + 8 + 8 + 4 + 4 + 4 + 32 + 1 + 1 + 1 + 1 + 2 + 2 + 2;

/// Fixed on-wire byte overhead a Stellar-source `CcipMessageV1` always carries,
/// mirroring EVM `MessageV1Codec.MESSAGE_V1_EVM_SOURCE_BASE_SIZE`. It is the
/// framing above plus the fixed content of the two SOURCE-side address fields
/// `sender` and `onramp`: each is a 32-byte raw Soroban address key
/// (`CcipMessageV1::address_raw_bytes`). EVM reaches the same 32+32 by
/// abi.encoding its 20-byte addresses to 32; Stellar addresses are natively 32.
/// So 79 + 32 + 32 = 143, equal to EVM's constant by derivation rather than
/// copy. The OnRamp bills this once into the executor receipt's `calldata_size`
/// (EVM `OnRamp._getReceipts` executor `destBytesOverhead` parity).
pub const MESSAGE_V1_STELLAR_SOURCE_BASE_SIZE: u32 = MESSAGE_V1_BASE_SIZE + 32 + 32;

// Compile-time guarantee that the derived Stellar source base matches the EVM
// constant it mirrors (`MESSAGE_V1_EVM_SOURCE_BASE_SIZE == 143`).
const _: () = assert!(MESSAGE_V1_STELLAR_SOURCE_BASE_SIZE == 143);

/// Canonical token transfer encoding for CCIP v1.7.
///
/// Matches protocol.TokenTransfer.Encode() byte layout:
///   version(1) | amount(32) | src_pool(1+N) | src_token(1+N) |
///   dest_token(1+N) | token_receiver(1+N) | extra_data(2+N)
#[derive(Clone)]
pub struct CcipTokenTransferV1 {
    pub version: u8,
    /// Transfer amount as 32 big-endian bytes (uint256).
    pub amount: BytesN<32>,
    pub source_pool_address: Bytes,
    pub source_token_address: Bytes,
    pub dest_token_address: Bytes,
    pub token_receiver: Bytes,
    pub extra_data: Bytes,
}

impl ToBytes for CcipTokenTransferV1 {
    fn to_bytes(&self, env: &Env) -> Result<Bytes, CCIPError> {
        let mut buf = Bytes::new(env);

        buf.append(&Bytes::from_array(env, &[self.version]));
        buf.append(&Bytes::from_slice(env, &self.amount.to_array()));

        // INV-ENC-11: 1-byte length-prefixed fields must fit in u8 (≤ 255).
        buf.append(&Bytes::from_array(
            env,
            &[len_as_u8(self.source_pool_address.len())?],
        ));
        buf.append(&self.source_pool_address);

        buf.append(&Bytes::from_array(
            env,
            &[len_as_u8(self.source_token_address.len())?],
        ));
        buf.append(&self.source_token_address);

        buf.append(&Bytes::from_array(
            env,
            &[len_as_u8(self.dest_token_address.len())?],
        ));
        buf.append(&self.dest_token_address);

        buf.append(&Bytes::from_array(
            env,
            &[len_as_u8(self.token_receiver.len())?],
        ));
        buf.append(&self.token_receiver);

        // INV-ENC-11: 2-byte length-prefixed field must fit in u16 (≤ 65535).
        buf.append(&Bytes::from_array(
            env,
            &len_as_u16(self.extra_data.len())?.to_be_bytes(),
        ));
        buf.append(&self.extra_data);

        Ok(buf)
    }
}

impl FromBytes for CcipTokenTransferV1 {
    fn from_bytes(env: &Env, bytes: &Bytes) -> Result<Self, CCIPError> {
        let len = bytes.len();
        if len < 34 {
            return Err(CCIPError::MessageDecodingError);
        }
        let mut pos: u32 = 0;

        let version = bytes.get(pos).ok_or(CCIPError::MessageDecodingError)?;
        pos += 1;
        // INV-MSG-2: reject token transfers carrying an unexpected version byte.
        // Mirrors `CcipMessageV1::from_bytes` below and EVM `MessageV1Codec._decodeTokenTransferV1`
        // (`if (version != 1) revert InvalidEncodingVersion`, MessageV1Codec.sol:269).
        if version != MESSAGE_V1_VERSION {
            return Err(CCIPError::MessageDecodingError);
        }

        let mut amount_arr = [0u8; 32];
        for i in 0..32u32 {
            amount_arr[i as usize] = bytes.get(pos + i).ok_or(CCIPError::MessageDecodingError)?;
        }
        let amount = BytesN::from_array(env, &amount_arr);
        pos += 32;

        // 1-byte length-prefixed fields
        let read_field_1 = |bytes: &Bytes, pos: &mut u32| -> Result<Bytes, CCIPError> {
            let field_len = bytes.get(*pos).ok_or(CCIPError::MessageDecodingError)? as u32;
            *pos += 1;
            if *pos + field_len > len {
                return Err(CCIPError::MessageDecodingError);
            }
            let field = bytes.slice(*pos..*pos + field_len);
            *pos += field_len;
            Ok(field)
        };

        let source_pool_address = read_field_1(bytes, &mut pos)?;
        let source_token_address = read_field_1(bytes, &mut pos)?;
        let dest_token_address = read_field_1(bytes, &mut pos)?;
        let token_receiver = read_field_1(bytes, &mut pos)?;

        // 2-byte length-prefixed extra_data
        if pos + 2 > len {
            return Err(CCIPError::MessageDecodingError);
        }
        let ed_hi = bytes.get(pos).ok_or(CCIPError::MessageDecodingError)? as u32;
        let ed_lo = bytes.get(pos + 1).ok_or(CCIPError::MessageDecodingError)? as u32;
        let ed_len = (ed_hi << 8) | ed_lo;
        pos += 2;
        if pos + ed_len > len {
            return Err(CCIPError::MessageDecodingError);
        }
        let extra_data = bytes.slice(pos..pos + ed_len);

        // INV-MSG-3/11: reject trailing bytes — the entire payload must be consumed.
        // Mirrors EVM `MessageV1Codec` (`if (offset != encoded.length) revert ... MESSAGE_FINAL_OFFSET`,
        // MessageV1Codec.sol:512) and the sub-field end check at :499.
        if pos + ed_len != len {
            return Err(CCIPError::MessageDecodingError);
        }

        Ok(CcipTokenTransferV1 {
            version,
            amount,
            source_pool_address,
            source_token_address,
            dest_token_address,
            token_receiver,
            extra_data,
        })
    }
}

/// CCIP MessageV1 canonical encoding, matching protocol.Message.Encode().
///
/// This is the chain-agnostic message format used for computing message IDs
/// and for the `encoded_message` field in CCIPMessageSent events.
///
/// Byte layout:
///   version(1) | src_chain(8) | dst_chain(8) | seq_num(8) |
///   exec_gas(4) | recv_gas(4) | finality(4) | ccv_exec_hash(32) |
///   onramp(1+N) | offramp(1+N) | sender(1+N) | receiver(1+N) |
///   dest_blob(2+N) | token_transfer(2+N) | data(2+N)
///
/// All multi-byte integers are big-endian. Address fields use a 1-byte
/// length prefix (max 255 bytes). Data/blob fields use a 2-byte length
/// prefix (max 65535 bytes).
#[derive(Clone)]
pub struct CcipMessageV1 {
    pub source_chain_selector: u64,
    pub dest_chain_selector: u64,
    pub sequence_number: u64,
    pub execution_gas_limit: u32,
    pub ccip_receive_gas_limit: u32,
    pub finality: u32,
    pub ccv_and_executor_hash: BytesN<32>,
    pub onramp_address: Bytes,
    pub offramp_address: Bytes,
    pub sender: Bytes,
    pub receiver: Bytes,
    pub dest_blob: Bytes,
    /// Pre-encoded token transfer bytes (from CcipTokenTransferV1::to_bytes).
    pub token_transfer: Bytes,
    pub data: Bytes,
}

impl ToBytes for CcipMessageV1 {
    fn to_bytes(&self, env: &Env) -> Result<Bytes, CCIPError> {
        let mut buf = Bytes::new(env);

        // Version (1 byte)
        buf.append(&Bytes::from_array(env, &[MESSAGE_V1_VERSION]));

        // Chain selectors and sequence number (8 bytes each, big-endian)
        buf.append(&Bytes::from_array(
            env,
            &self.source_chain_selector.to_be_bytes(),
        ));
        buf.append(&Bytes::from_array(
            env,
            &self.dest_chain_selector.to_be_bytes(),
        ));
        buf.append(&Bytes::from_array(env, &self.sequence_number.to_be_bytes()));

        // Gas limits (4 bytes each, big-endian)
        buf.append(&Bytes::from_array(
            env,
            &self.execution_gas_limit.to_be_bytes(),
        ));
        buf.append(&Bytes::from_array(
            env,
            &self.ccip_receive_gas_limit.to_be_bytes(),
        ));

        // Finality (4 bytes, big-endian)
        buf.append(&Bytes::from_array(env, &self.finality.to_be_bytes()));

        // CCV and executor hash (32 bytes)
        buf.append(&Bytes::from_slice(
            env,
            &self.ccv_and_executor_hash.to_array(),
        ));

        // INV-ENC-11: 1-byte length-prefixed address fields must fit in u8 (≤ 255).
        buf.append(&Bytes::from_array(
            env,
            &[len_as_u8(self.onramp_address.len())?],
        ));
        buf.append(&self.onramp_address);

        buf.append(&Bytes::from_array(
            env,
            &[len_as_u8(self.offramp_address.len())?],
        ));
        buf.append(&self.offramp_address);

        buf.append(&Bytes::from_array(env, &[len_as_u8(self.sender.len())?]));
        buf.append(&self.sender);

        buf.append(&Bytes::from_array(env, &[len_as_u8(self.receiver.len())?]));
        buf.append(&self.receiver);

        // INV-ENC-11: 2-byte length-prefixed fields must fit in u16 (≤ 65535).
        buf.append(&Bytes::from_array(
            env,
            &len_as_u16(self.dest_blob.len())?.to_be_bytes(),
        ));
        buf.append(&self.dest_blob);

        buf.append(&Bytes::from_array(
            env,
            &len_as_u16(self.token_transfer.len())?.to_be_bytes(),
        ));
        buf.append(&self.token_transfer);

        buf.append(&Bytes::from_array(
            env,
            &len_as_u16(self.data.len())?.to_be_bytes(),
        ));
        buf.append(&self.data);

        Ok(buf)
    }
}

impl MessageIdCompute for CcipMessageV1 {}

impl CcipMessageV1 {
    /// Compute the CCV-and-executor hash from CCV addresses and executor address.
    /// Matches protocol.ComputeCCVAndExecutorHash() in Go.
    /// Format: keccak256(addressLength(1) || ccv1 || ccv2 || ... || executor)
    ///
    /// All addresses must have the same byte length (derived from the executor).
    pub fn compute_ccv_and_executor_hash(
        env: &Env,
        ccv_addresses: &Vec<Address>,
        executor: &Address,
    ) -> BytesN<32> {
        let executor_bytes = Self::address_raw_bytes(env, executor.clone());
        let addr_len = executor_bytes.len() as u8;

        let mut encoded = Bytes::new(env);
        encoded.append(&Bytes::from_array(env, &[addr_len]));

        for ccv in ccv_addresses.iter() {
            encoded.append(&Self::address_raw_bytes(env, ccv));
        }

        encoded.append(&executor_bytes);

        env.crypto().keccak256(&encoded).into()
    }

    /// Extract the raw 32-byte key from a Soroban Address.
    /// For contract addresses this is the contract ID; for account addresses
    /// it is the ed25519 public key. The raw bytes are the final 32 bytes of
    /// the ScVal XDR encoding, which holds the key for both address types.
    pub fn address_raw_bytes(env: &Env, addr: Address) -> Bytes {
        let xdr = addr.to_xdr(env);
        let len = xdr.len();
        xdr.slice((len - 32)..len)
    }
}

// ============================================================
// ByteReader — cursor-based decoder for canonical CCIP encoding
// ============================================================

/// A simple cursor-based reader over `Bytes` for decoding the canonical
/// CCIP v1.7 wire format. All reads advance the cursor and return
/// `MessageDecodingError` on underflow.
struct ByteReader<'a> {
    data: &'a Bytes,
    pos: u32,
}

impl<'a> ByteReader<'a> {
    fn new(data: &'a Bytes) -> Self {
        Self { data, pos: 0 }
    }

    fn remaining(&self) -> u32 {
        self.data.len().saturating_sub(self.pos)
    }

    fn read_u8(&mut self) -> Result<u8, CCIPError> {
        if self.remaining() < 1 {
            return Err(CCIPError::MessageDecodingError);
        }
        let val = self
            .data
            .get(self.pos)
            .ok_or(CCIPError::MessageDecodingError)?;
        self.pos += 1;
        Ok(val)
    }

    fn read_u16(&mut self) -> Result<u16, CCIPError> {
        let mut arr = [0u8; 2];
        for b in &mut arr {
            *b = self.read_u8()?;
        }
        Ok(u16::from_be_bytes(arr))
    }

    fn read_u32(&mut self) -> Result<u32, CCIPError> {
        let mut arr = [0u8; 4];
        for b in &mut arr {
            *b = self.read_u8()?;
        }
        Ok(u32::from_be_bytes(arr))
    }

    fn read_u64(&mut self) -> Result<u64, CCIPError> {
        let mut arr = [0u8; 8];
        for b in &mut arr {
            *b = self.read_u8()?;
        }
        Ok(u64::from_be_bytes(arr))
    }

    fn read_bytes(&mut self, len: u32) -> Result<Bytes, CCIPError> {
        if self.remaining() < len {
            return Err(CCIPError::MessageDecodingError);
        }
        let slice = self.data.slice(self.pos..(self.pos + len));
        self.pos += len;
        Ok(slice)
    }

    fn read_bytes32(&mut self, env: &Env) -> Result<BytesN<32>, CCIPError> {
        let b = self.read_bytes(32)?;
        let mut arr = [0u8; 32];
        for i in 0..32u32 {
            arr[i as usize] = b.get(i).ok_or(CCIPError::MessageDecodingError)?;
        }
        Ok(BytesN::from_array(env, &arr))
    }

    /// Read a 1-byte length-prefixed field.
    fn read_1lp_field(&mut self) -> Result<Bytes, CCIPError> {
        let len = self.read_u8()? as u32;
        self.read_bytes(len)
    }

    /// Read a 2-byte (big-endian) length-prefixed field.
    fn read_2lp_field(&mut self) -> Result<Bytes, CCIPError> {
        let len = self.read_u16()? as u32;
        self.read_bytes(len)
    }
}

impl FromBytes for CcipMessageV1 {
    /// Decode a `CcipMessageV1` from its canonical wire-format bytes.
    /// The byte layout is the exact reverse of `ToBytes::to_bytes()`.
    fn from_bytes(env: &Env, bytes: &Bytes) -> Result<Self, CCIPError> {
        let mut r = ByteReader::new(bytes);

        let version = r.read_u8()?;
        if version != MESSAGE_V1_VERSION {
            return Err(CCIPError::MessageDecodingError);
        }

        let source_chain_selector = r.read_u64()?;
        let dest_chain_selector = r.read_u64()?;
        let sequence_number = r.read_u64()?;
        let execution_gas_limit = r.read_u32()?;
        let ccip_receive_gas_limit = r.read_u32()?;
        let finality = r.read_u32()?;
        let ccv_and_executor_hash = r.read_bytes32(env)?;

        let onramp_address = r.read_1lp_field()?;
        let offramp_address = r.read_1lp_field()?;
        let sender = r.read_1lp_field()?;
        let receiver = r.read_1lp_field()?;

        let dest_blob = r.read_2lp_field()?;
        let token_transfer = r.read_2lp_field()?;
        let data = r.read_2lp_field()?;

        // INV-MSG-3: reject trailing bytes — the entire payload must be consumed.
        // Mirrors EVM `MessageV1Codec._decodeMessageV1`
        // (`if (offset != encoded.length) revert ... MESSAGE_FINAL_OFFSET`, MessageV1Codec.sol:512).
        if r.remaining() != 0 {
            return Err(CCIPError::MessageDecodingError);
        }

        Ok(CcipMessageV1 {
            source_chain_selector,
            dest_chain_selector,
            sequence_number,
            execution_gas_limit,
            ccip_receive_gas_limit,
            finality,
            ccv_and_executor_hash,
            onramp_address,
            offramp_address,
            sender,
            receiver,
            dest_blob,
            token_transfer,
            data,
        })
    }
}

#[cfg(test)]
mod test;
