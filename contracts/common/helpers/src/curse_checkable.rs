use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_interfaces::rmn_proxy::RmnProxyClient;
use common_interfaces::rmn_remote::RmnRemoteClient;
use soroban_sdk::{contracttrait, symbol_short, Address, BytesN, Env, Symbol};

/// CurseCheckable trait for contracts that can be cursed.
///
/// This trait is used to check curse status via RMN Proxy.
/// It requires the contract to be initialized with the RMN Proxy address.
///
/// # Examples
///
/// ```
/// use soroban_sdk::{Env, Address};
/// use chainlink_stellar::common::helpers::curse_checkable::CurseCheckable;
///
/// const RMN_PROXY: Symbol = symbol_short!("RMN_PROXY");
///
/// #[contractimpl]
/// impl CurseCheckable for MyContract {
///     const RMN_PROXY: Symbol = RMN_PROXY;
/// }
///
/// #[contractimpl]
/// impl MyContract {
///     fn some_function(env: &Env) -> Result<(), CCIPError> {
///         <Self as CurseCheckable>::require_not_cursed(env)?;
///         Ok(())
///     }
/// }
/// ```
#[contracttrait]
pub trait CurseCheckable: Initializable {
    const RMN_PROXY: Symbol = symbol_short!("RMN_PROXY");

    fn init(env: &Env, rmn_proxy: &Address) -> Result<(), CCIPError> {
        // Exported unguarded via `#[contractimpl(contracttrait)]` on the ramps: without
        // this check any account could re-point the RMN proxy post-initialize and
        // defeat every curse check.
        if env.storage().instance().has(&Self::RMN_PROXY) {
            return Err(CCIPError::AlreadyInitialized);
        }
        env.storage().instance().set(&Self::RMN_PROXY, rmn_proxy);
        Ok(())
    }

    fn is_cursed(env: &Env) -> Result<bool, CCIPError> {
        <Self as Initializable>::require_initialized(env)?;

        let rmn_proxy = env
            .storage()
            .instance()
            .get(&Self::RMN_PROXY)
            .ok_or(CCIPError::NotInitialized)?;

        let rmn_proxy_client = RmnProxyClient::new(&env, &rmn_proxy);
        let cursed = rmn_proxy_client.is_cursed();

        Ok(cursed)
    }

    /// Check if a specific subject (chain) is cursed
    fn is_subject_cursed(env: &Env, subject: &BytesN<16>) -> Result<bool, CCIPError> {
        <Self as Initializable>::require_initialized(env)?;

        let rmn_proxy = env
            .storage()
            .instance()
            .get(&Self::RMN_PROXY)
            .ok_or(CCIPError::NotInitialized)?;

        let rmn_proxy_client = RmnProxyClient::new(&env, &rmn_proxy);

        // Get the RMN Remote implementation address from the proxy
        let rmn_address = rmn_proxy_client.get_rmn();

        // Check if this specific subject (chain) is cursed
        let rmn_client = RmnRemoteClient::new(&env, &rmn_address);
        let is_subject_cursed = rmn_client.is_cursed_by_subject(subject);

        Ok(is_subject_cursed)
    }

    fn require_not_cursed(env: &Env) -> Result<(), CCIPError> {
        if Self::is_cursed(env)? {
            return Err(CCIPError::CursedByRMN);
        }

        Ok(())
    }

    /// Require that a specific subject (chain) is not cursed
    fn require_subject_not_cursed(env: &Env, subject: &BytesN<16>) -> Result<(), CCIPError> {
        // First check if globally cursed
        if Self::is_cursed(env)? {
            return Err(CCIPError::CursedByRMN);
        }

        // Then check if this specific subject is cursed
        if Self::is_subject_cursed(env, subject)? {
            return Err(CCIPError::CursedByRMN);
        }

        Ok(())
    }

    /// Require that a chain is not cursed by selector.
    /// Checks both global and chain-specific curse status.
    fn require_chain_not_cursed(env: &Env, chain_selector: u64) -> Result<(), CCIPError> {
        // First check if globally cursed
        if Self::is_cursed(env)? {
            return Err(CCIPError::CursedByRMN);
        }

        // Convert chain selector to 16-byte subject format (last 8 bytes)
        let selector_bytes = chain_selector.to_be_bytes();
        let mut subject_array = [0u8; 16];
        subject_array[8..16].copy_from_slice(&selector_bytes);
        let subject_bytes = BytesN::<16>::from_array(env, &subject_array);

        // Check if this specific chain is cursed
        if Self::is_subject_cursed(env, &subject_bytes)? {
            return Err(CCIPError::CursedByRMN);
        }

        Ok(())
    }
}
