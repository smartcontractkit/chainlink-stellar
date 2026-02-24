#![no_std]

mod events;
pub mod types;

use soroban_sdk::{contract, contractimpl, symbol_short, Address, BytesN, Env, Map, Symbol, Vec};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;

use events::{ConfigSetEvent, CursedEvent, UncursedEvent};
use types::{Config, Signer};

// ============================================================
// Storage Keys
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const CONFIG: Symbol = symbol_short!("CONFIG");
const CONFIG_CNT: Symbol = symbol_short!("CFGCNT");
const SIGNERS: Symbol = symbol_short!("SIGNERS");
const CURSED: Symbol = symbol_short!("CURSED");
const CHAIN_SEL: Symbol = symbol_short!("CHAINSEL");

// ============================================================
// Constants
// ============================================================

/// Global curse subject — an active curse on this subject causes `is_cursed()` to return true.
/// Equivalent to EVM `GLOBAL_CURSE_SUBJECT = 0x01000000000000000000000000000001`.
const GLOBAL_CURSE_SUBJECT: [u8; 16] = [
    0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
];

// ============================================================
// Contract
// ============================================================

/// RMN Remote contract for Stellar/Soroban.
///
/// Port of the EVM `RMNRemote.sol` contract. Provides:
///   - **Cursing**: owner can curse/uncurse subjects to emergency-halt message flows
///   - **Configuration**: manages the set of trusted RMN signers and threshold
#[contract]
pub struct RmnRemoteContract;

#[contractimpl(contracttrait)]
impl Ownable for RmnRemoteContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl]
impl Initializable for RmnRemoteContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl]
impl RmnRemoteContract {
    // ========================================
    // Initialization
    // ========================================

    /// Initialize the RMN Remote contract.
    ///
    /// # Arguments
    /// * `owner` - The owner address (can set config and curse/uncurse)
    /// * `local_chain_selector` - The chain selector of the chain this contract is deployed on
    pub fn initialize(
        env: Env,
        owner: Address,
        local_chain_selector: u64,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;

        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;

        env.storage()
            .instance()
            .set(&CHAIN_SEL, &local_chain_selector);
        env.storage().instance().set(&CONFIG_CNT, &0u32);

        // Initialize empty cursed subjects map (subject -> true)
        let cursed: Map<BytesN<16>, bool> = Map::new(&env);
        env.storage().instance().set(&CURSED, &cursed);

        // Initialize empty signers map (pubkey -> true)
        let signers: Map<BytesN<32>, bool> = Map::new(&env);
        env.storage().instance().set(&SIGNERS, &signers);

        Ok(())
    }

    // ========================================
    // Configuration
    // ========================================

    /// Set the signer configuration. Only callable by owner.
    ///
    /// Mirrors EVM `RMNRemote.setConfig()`. Validates:
    /// - `config_digest` is non-zero
    /// - Signers are in ascending order of `node_index`
    /// - At least `2 * f_sign + 1` signers are configured
    /// - No duplicate public keys
    pub fn set_config(env: Env, new_config: Config) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let zero_digest = BytesN::from_array(&env, &[0u8; 32]);
        if new_config.rmn_home_config_digest == zero_digest {
            return Err(CCIPError::ZeroValueNotAllowed);
        }

        // Validate minimum signer count: need 2f+1
        if (new_config.signers.len() as u64) < 2 * new_config.f_sign + 1 {
            return Err(CCIPError::NotEnoughSigners);
        }

        // Validate signer ordering (strictly increasing node_index)
        new_config
            .signers
            .iter()
            .try_fold(None, |prev: Option<u64>, signer| {
                if prev.is_some_and(|p| p >= signer.node_index) {
                    return Err(CCIPError::InvalidSignerOrder);
                }
                Ok(Some(signer.node_index))
            })?;

        // Build new signers map, checking for duplicate public keys
        let mut new_signers: Map<BytesN<32>, bool> = Map::new(&env);
        for i in 0..new_config.signers.len() {
            let signer: Signer = new_config.signers.get(i).unwrap();
            if new_signers
                .get(signer.onchain_pub_key.clone())
                .unwrap_or(false)
            {
                return Err(CCIPError::DuplicateOnchainPublicKey);
            }
            new_signers.set(signer.onchain_pub_key, true);
        }

        env.storage().instance().set(&SIGNERS, &new_signers);
        env.storage().instance().set(&CONFIG, &new_config);

        let mut config_count: u32 = env.storage().instance().get(&CONFIG_CNT).unwrap_or(0);
        config_count += 1;
        env.storage().instance().set(&CONFIG_CNT, &config_count);

        ConfigSetEvent {
            version: config_count,
            num_signers: new_config.signers.len(),
            f_sign: new_config.f_sign,
        }
        .publish(&env);

        Ok(())
    }

    /// Returns the current configuration and its version number.
    pub fn get_versioned_config(env: Env) -> Result<(u32, Config), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let config_count: u32 = env
            .storage()
            .instance()
            .get(&CONFIG_CNT)
            .ok_or(CCIPError::NotInitialized)?;
        let config: Config = env
            .storage()
            .instance()
            .get(&CONFIG)
            .ok_or(CCIPError::ConfigNotSet)?;
        Ok((config_count, config))
    }

    /// Returns the local chain selector set at initialization.
    pub fn get_local_chain_selector(env: Env) -> Result<u64, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&CHAIN_SEL)
            .ok_or(CCIPError::NotInitialized)
    }

    // ========================================
    // Cursing
    // ========================================

    /// Curse one or more subjects. Only callable by owner.
    ///
    /// Reverts if any subject is already cursed.
    pub fn curse(env: Env, subjects: Vec<BytesN<16>>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut cursed: Map<BytesN<16>, bool> = env
            .storage()
            .instance()
            .get(&CURSED)
            .ok_or(CCIPError::NotInitialized)?;

        subjects.iter().try_for_each(|subject| {
            if cursed.get(subject.clone()).unwrap_or(false) {
                return Err(CCIPError::AlreadyCursed);
            }
            cursed.set(subject, true);
            Ok(())
        })?;

        env.storage().instance().set(&CURSED, &cursed);

        CursedEvent { subjects }.publish(&env);

        Ok(())
    }

    /// Uncurse one or more subjects. Only callable by owner.
    ///
    /// Reverts if any subject is not currently cursed.
    pub fn uncurse(env: Env, subjects: Vec<BytesN<16>>) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut cursed: Map<BytesN<16>, bool> = env
            .storage()
            .instance()
            .get(&CURSED)
            .ok_or(CCIPError::NotInitialized)?;

        subjects.iter().try_for_each(|subject| {
            if !cursed.get(subject.clone()).unwrap_or(false) {
                return Err(CCIPError::NotCursed);
            }
            cursed.remove(subject);
            Ok(())
        })?;

        env.storage().instance().set(&CURSED, &cursed);

        UncursedEvent { subjects }.publish(&env);

        Ok(())
    }

    /// Returns all currently cursed subjects.
    pub fn get_cursed_subjects(env: Env) -> Result<Vec<BytesN<16>>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let cursed: Map<BytesN<16>, bool> = env
            .storage()
            .instance()
            .get(&CURSED)
            .ok_or(CCIPError::NotInitialized)?;

        let mut result: Vec<BytesN<16>> = Vec::new(&env);
        for key in cursed.keys() {
            result.push_back(key);
        }
        Ok(result)
    }

    /// Check if the network is globally cursed.
    ///
    /// Returns `true` if the `GLOBAL_CURSE_SUBJECT` is in the cursed set.
    /// This is the function called by the RMN Proxy (via `RmnProxyClient::is_cursed()`).
    pub fn is_cursed(env: Env) -> bool {
        let cursed: Map<BytesN<16>, bool> = match env.storage().instance().get(&CURSED) {
            Some(c) => c,
            None => return false,
        };

        if cursed.is_empty() {
            return false;
        }

        let global = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
        cursed.get(global).unwrap_or(false)
    }

    /// Check if a specific subject is cursed.
    ///
    /// Returns `true` if `subject` OR the `GLOBAL_CURSE_SUBJECT` is cursed.
    pub fn is_cursed_by_subject(env: Env, subject: BytesN<16>) -> bool {
        let cursed: Map<BytesN<16>, bool> = match env.storage().instance().get(&CURSED) {
            Some(c) => c,
            None => return false,
        };

        if cursed.is_empty() {
            return false;
        }

        let global = BytesN::from_array(&env, &GLOBAL_CURSE_SUBJECT);
        cursed.get(subject).unwrap_or(false) || cursed.get(global).unwrap_or(false)
    }
}

#[cfg(test)]
mod test;
