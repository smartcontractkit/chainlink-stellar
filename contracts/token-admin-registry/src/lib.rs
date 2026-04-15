#![no_std]

mod events;
pub mod types;

use soroban_sdk::{contract, contractimpl, symbol_short, Address, Env, Symbol, Vec};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use events::{
    AdminTransferRequestedEvent, AdministratorTransferredEvent, PoolSetEvent,
    RegistryModuleAddedEvent, RegistryModuleRemovedEvent,
};
use types::{DataKey, TokenConfig};

// ============================================================
// Storage Keys (instance)
// ============================================================

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
const REGISTRY_MODULES: Symbol = symbol_short!("REGMODS");
const TOKEN_COUNT: Symbol = symbol_short!("TOKNCNT");

// ============================================================
// Contract
// ============================================================

#[contract]
pub struct TokenAdminRegistryContract;

#[contractimpl]
impl Initializable for TokenAdminRegistryContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for TokenAdminRegistryContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl]
impl TokenAdminRegistryContract {
    // ========================================
    // Initialization
    // ========================================

    pub fn initialize(env: Env, owner: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Initializable>::init(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;

        env.storage()
            .instance()
            .set(&REGISTRY_MODULES, &Vec::<Address>::new(&env));
        env.storage().instance().set(&TOKEN_COUNT, &0u32);

        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "TokenAdminRegistry 1.0.0")
    }

    // ========================================
    // View Functions
    // ========================================

    /// Returns the pool address for a token, or None if not configured.
    pub fn get_pool(env: Env, token: Address) -> Result<Option<Address>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let config = Self::get_token_config_internal(&env, &token);
        Ok(config.token_pool)
    }

    /// Returns pool addresses for multiple tokens.
    pub fn get_pools(env: Env, tokens: Vec<Address>) -> Result<Vec<Option<Address>>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let mut pools = Vec::new(&env);
        for token in tokens.iter() {
            let config = Self::get_token_config_internal(&env, &token);
            pools.push_back(config.token_pool);
        }
        Ok(pools)
    }

    /// Returns the full configuration for a token.
    pub fn get_token_config(env: Env, token: Address) -> Result<TokenConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Ok(Self::get_token_config_internal(&env, &token))
    }

    /// Returns a paginated list of all configured tokens.
    /// Tokens are never removed from this list; setting a pool to None
    /// delists the token from CCIP but keeps the registry entry.
    pub fn get_all_configured_tokens(
        env: Env,
        start_index: u32,
        max_count: u32,
    ) -> Result<Vec<Address>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let total: u32 = env.storage().instance().get(&TOKEN_COUNT).unwrap_or(0u32);

        if start_index >= total {
            return Ok(Vec::new(&env));
        }

        let mut count = max_count;
        if count + start_index > total {
            count = total - start_index;
        }

        let mut tokens = Vec::new(&env);
        for i in 0..count {
            let idx = start_index + i;
            let token: Address = env
                .storage()
                .persistent()
                .get(&DataKey::TokenIndex(idx))
                .unwrap();
            tokens.push_back(token);
        }

        Ok(tokens)
    }

    /// Checks if an address is the administrator of the given token.
    pub fn is_administrator(
        env: Env,
        local_token: Address,
        administrator: Address,
    ) -> Result<bool, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let config = Self::get_token_config_internal(&env, &local_token);
        Ok(config.administrator == Some(administrator))
    }

    /// Checks if an address is a registered registry module.
    pub fn is_registry_module(env: Env, module: Address) -> Result<bool, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Ok(Self::is_registry_module_internal(&env, &module))
    }

    // ========================================
    // Administrator Functions
    // ========================================

    /// Sets the pool for a token. Setting pool to None delists the token from CCIP.
    /// Can only be called by the token's administrator.
    pub fn set_pool(
        env: Env,
        local_token: Address,
        pool: Option<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let mut config = Self::get_token_config_internal(&env, &local_token);
        let admin = config
            .administrator
            .as_ref()
            .ok_or(CCIPError::OnlyAdministrator)?;

        // Ensure that the caller is the administrator of the token.
        admin.require_auth();

        // TODO: When pool contracts are implemented, validate that the pool
        // supports this token via pool.is_supported_token(local_token).

        let previous_pool = config.token_pool.clone();
        config.token_pool = pool.clone();

        if previous_pool != pool {
            env.storage()
                .persistent()
                .set(&DataKey::TokenConfig(local_token.clone()), &config);

            PoolSetEvent {
                token: local_token,
                previous_pool,
                new_pool: pool,
            }
            .publish(&env);
        }

        Ok(())
    }

    /// Transfers the administrator role for a token to a new address (two-step).
    /// The new admin must call `accept_admin_role` to complete the transfer.
    /// Pass None to cancel a pending transfer.
    pub fn transfer_admin_role(
        env: Env,
        local_token: Address,
        new_admin: Option<Address>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let mut config = Self::get_token_config_internal(&env, &local_token);
        let admin = config
            .administrator
            .clone()
            .ok_or(CCIPError::OnlyAdministrator)?;
        admin.require_auth();

        config.pending_administrator = new_admin.clone();
        env.storage()
            .persistent()
            .set(&DataKey::TokenConfig(local_token.clone()), &config);

        AdminTransferRequestedEvent {
            token: local_token,
            current_admin: Some(admin),
            new_admin,
        }
        .publish(&env);

        Ok(())
    }

    /// Accepts the administrator role for a token.
    /// Can only be called by the pending administrator.
    pub fn accept_admin_role(env: Env, local_token: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let mut config = Self::get_token_config_internal(&env, &local_token);
        let pending = config
            .pending_administrator
            .clone()
            .ok_or(CCIPError::OnlyPendingAdministrator)?;
        pending.require_auth();

        config.administrator = Some(pending.clone());
        config.pending_administrator = None;
        env.storage()
            .persistent()
            .set(&DataKey::TokenConfig(local_token.clone()), &config);

        AdministratorTransferredEvent {
            token: local_token,
            new_admin: pending,
        }
        .publish(&env);

        Ok(())
    }

    // ========================================
    // Administrator Configuration
    // ========================================

    /// Proposes an administrator for a token. Can only be called by a
    /// registered registry module or the contract owner.
    /// The proposed admin must call `accept_admin_role` to finalize.
    pub fn propose_administrator(
        env: Env,
        caller: Address,
        local_token: Address,
        administrator: Address,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        // Check that the caller is either a registry module or the contract owner
        let is_module = Self::is_registry_module_internal(&env, &caller);
        let is_owner = <Self as Ownable>::owner(&env)
            .map(|o| o == caller)
            .unwrap_or(false);

        if !is_module && !is_owner {
            return Err(CCIPError::OnlyRegistryModuleOrOwner);
        }
        caller.require_auth();

        let config = Self::get_token_config_internal(&env, &local_token);
        if config.administrator.is_some() {
            return Err(CCIPError::TokenAlreadyRegistered);
        }

        let new_config = TokenConfig {
            administrator: None,
            pending_administrator: Some(administrator.clone()),
            token_pool: None,
        };
        env.storage()
            .persistent()
            .set(&DataKey::TokenConfig(local_token.clone()), &new_config);

        Self::add_token_to_index(&env, &local_token);

        AdminTransferRequestedEvent {
            token: local_token,
            current_admin: None,
            new_admin: Some(administrator),
        }
        .publish(&env);

        Ok(())
    }

    // ========================================
    // Registry Module Management (Owner Only)
    // ========================================

    /// Adds a registry module. Only callable by the contract owner.
    pub fn add_registry_module(env: Env, module: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let mut modules: Vec<Address> = env
            .storage()
            .instance()
            .get(&REGISTRY_MODULES)
            .unwrap_or(Vec::new(&env));

        if !Self::is_registry_module_internal(&env, &module) {
            modules.push_back(module.clone());
            env.storage().instance().set(&REGISTRY_MODULES, &modules);

            RegistryModuleAddedEvent { module }.publish(&env);
        }

        Ok(())
    }

    /// Removes a registry module. Only callable by the contract owner.
    pub fn remove_registry_module(env: Env, module: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env)?;

        let modules: Vec<Address> = env
            .storage()
            .instance()
            .get(&REGISTRY_MODULES)
            .unwrap_or(Vec::new(&env));

        let mut new_modules = Vec::new(&env);
        let mut found = false;

        for m in modules.iter() {
            if m == module {
                found = true;
            } else {
                new_modules.push_back(m);
            }
        }

        if found {
            env.storage()
                .instance()
                .set(&REGISTRY_MODULES, &new_modules);
            RegistryModuleRemovedEvent { module }.publish(&env);
        }

        Ok(())
    }

    // ========================================
    // Internal Helpers
    // ========================================

    fn get_token_config_internal(env: &Env, token: &Address) -> TokenConfig {
        // TODO: should we introduce an extension for the TTL here?

        env.storage()
            .persistent()
            .get(&DataKey::TokenConfig(token.clone()))
            .unwrap_or(TokenConfig {
                administrator: None,
                pending_administrator: None,
                token_pool: None,
            })
    }

    fn is_registry_module_internal(env: &Env, module: &Address) -> bool {
        let modules: Vec<Address> = env
            .storage()
            .instance()
            .get(&REGISTRY_MODULES)
            .unwrap_or(Vec::new(env));

        for m in modules.iter() {
            if m == *module {
                return true;
            }
        }
        false
    }

    fn add_token_to_index(env: &Env, token: &Address) {
        let count: u32 = env.storage().instance().get(&TOKEN_COUNT).unwrap_or(0u32);

        // Only add if not already indexed (idempotent, same as EVM's EnumerableSet.add)
        for i in 0..count {
            let existing: Address = env
                .storage()
                .persistent()
                .get(&DataKey::TokenIndex(i))
                .unwrap();
            if existing == *token {
                return;
            }
        }

        // TODO: should we introduce an extension for the TTL here?
        env.storage()
            .persistent()
            .set(&DataKey::TokenIndex(count), token);
        env.storage().instance().set(&TOKEN_COUNT, &(count + 1));
    }
}

mod test;
