#![no_std]

use common_error::CCIPError;
use soroban_sdk::{
    contracttype,
    xdr::{FromXdr, ToXdr},
    Address, Bytes, BytesN, Env, TryFromVal, Vec,
};

// ============================================================
// MessageIdCompute Trait
// ============================================================

/// Trait for types that can be serialized to Bytes.
/// Unlike `Into<Bytes>`, this takes `&Env` which is required by Soroban's `Bytes` type.
pub trait ToBytes {
    fn to_bytes(&self, env: &Env) -> Bytes;
}

/// Trait for computing CCIP message IDs.
/// Implementors of this trait provide the logic for generating deterministic
/// message identifiers based on message content.
pub trait MessageIdCompute: ToBytes {
    /// Computes the message ID for a CCIP message.
    fn compute_message_id(&self, env: &Env) -> BytesN<32> {
        let bytes = self.to_bytes(env);
        let hash = env.crypto().keccak256(&bytes);
        hash.into()
    }
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
    fn to_bytes(&self, env: &Env) -> Bytes {
        let mut bytes = Bytes::new(env);
        // Convert Address to its XDR byte representation
        bytes.append(&self.token.clone().to_xdr(env));
        // Convert i128 to big-endian bytes (16 bytes)
        bytes.append(&Bytes::from_array(env, &self.amount.to_be_bytes()));
        bytes
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
}

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

        // TODO: add other validations
        // if self.receiver.len() != 32 {
        //     return Err(CCIPError::InvalidReceiverAddress);
        // }

        Ok(())
    }
}

impl ToBytes for StellarToAnyMessage {
    fn to_bytes(&self, env: &Env) -> Bytes {
        let mut bytes = Bytes::new(env);
        bytes.append(&self.receiver);
        bytes.append(&self.data);
        for token_amount in self.token_amounts.iter() {
            bytes.append(&token_amount.to_bytes(env));
        }
        bytes.append(&self.fee_token.clone().to_xdr(env));
        bytes.append(&self.extra_args);
        bytes
    }
}

impl MessageIdCompute for StellarToAnyMessage {}

// ============================================================
// AnyToStellarMessage
// ============================================================

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AnyToStellarMessage {
    // TODO: add fields
    pub placeholder: u64,
}

impl AnyToStellarMessage {
    pub fn validate(&self) -> Result<(), CCIPError> {
        // TODO: add validations
        Ok(())
    }
}

impl ToBytes for AnyToStellarMessage {
    fn to_bytes(&self, env: &Env) -> Bytes {
        unimplemented!()
    }
}

impl MessageIdCompute for AnyToStellarMessage {}
