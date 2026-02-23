use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_interfaces::rmn_proxy::RmnProxyClient;
use soroban_sdk::{contracttrait, symbol_short, Address, Env, Symbol};

/// Cursable trait for contracts that can be cursed.
///
/// This trait is used to check curse status via RMN Proxy.
///
/// # Examples
///
/// ```
/// use soroban_sdk::{Env, Address};
/// use chainlink_stellar::common::helpers::cursable::Cursable;
///
/// pub trait Cursable {
///     fn is_cursed(env: &Env, address: &Address) -> bool;
/// }
/// ```
#[contracttrait]
pub trait CurseCheckable: Initializable {
    const RMN_PROXY: Symbol = symbol_short!("RMN_PROXY");

    fn init(env: &Env, rmn_proxy: &Address) -> Result<(), CCIPError> {
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

    fn require_not_cursed(env: &Env) -> Result<(), CCIPError> {
        if Self::is_cursed(env)? {
            return Err(CCIPError::CursedByRMN);
        }

        Ok(())
    }
}
