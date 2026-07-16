use soroban_sdk::{contracttype, BytesN, Env, IntoVal, Symbol, Val, Vec};

#[contracttype]
pub enum SignatureVerificationConfig {
    Threshold(u32),
    Failure(u32),
}

impl SignatureVerificationConfig {
    /// Minimum number of valid signatures required to reach quorum.
    ///
    /// `Threshold(t)` is that count directly; `Failure(f)` tolerates `f` faulty
    /// signers and therefore requires `f + 1` valid signatures.
    pub fn threshold(&self) -> u32 {
        match self {
            SignatureVerificationConfig::Threshold(t) => *t,
            SignatureVerificationConfig::Failure(f) => f.saturating_add(1),
        }
    }
}

#[contracttype]
pub struct SignatureConfig {
    pub signers: Vec<BytesN<32>>,
    pub verification: SignatureVerificationConfig,
}

pub trait SignatureConfigManager {
    const SIGNATURE_CONFIGS: Symbol;
    const IS_PERSISTENT: bool;
    type Error;
    type DataKey: IntoVal<Env, Val>;

    // To be implemented by the concrete contract
    fn validate_config(env: &Env, config: &SignatureConfig) -> Result<(), Self::Error>;

    fn get_config(env: &Env, key: &Self::DataKey) -> Option<SignatureConfig> {
        match Self::IS_PERSISTENT {
            true => env.storage().persistent().get(key),
            _ => env.storage().instance().get(key),
        }
    }

    fn set_config(
        env: &Env,
        key: &Self::DataKey,
        value: &SignatureConfig,
    ) -> Result<(), Self::Error> {
        Self::validate_config(env, value)?;
        match Self::IS_PERSISTENT {
            true => env.storage().persistent().set(key, value),
            _ => env.storage().instance().set(key, value),
        };

        Ok(())
    }

    fn remove_config(env: &Env, key: &Self::DataKey) {
        match Self::IS_PERSISTENT {
            true => env.storage().persistent().remove(key),
            _ => env.storage().instance().remove(key),
        }
    }
}
