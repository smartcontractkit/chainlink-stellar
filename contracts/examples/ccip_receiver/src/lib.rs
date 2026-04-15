#![no_std]

//! Stellar CCIP **application** receiver example — analogue of Solidity
//! [`CCIPReceiver`](https://github.com/smartcontractkit/chainlink-ccip/blob/develop/chains/evm/contracts/applications/CCIPReceiver.sol)
//! plus pieces of [`CCIPClientExample`](https://github.com/smartcontractkit/chainlink-ccip/blob/develop/chains/evm/contracts/applications/CCIPClientExample.sol)
//! / [`CCIPClientExampleWithCCVs`](https://github.com/smartcontractkit/chainlink-ccip/blob/develop/chains/evm/contracts/applications/CCIPClientExampleWithCCVs.sol):
//!
//! - **Router-only `ccip_receive`**: [`Address::require_auth_for_args`](soroban_sdk::Address::require_auth_for_args)
//!   (same role as `onlyRouter` on EVM), plus **source-chain allowlist** matching EVM `validChain(message.sourceChainSelector)`:
//!   the message's `source_chain_selector` must have been enabled via [`ExampleCcipReceiver::enable_remote_chain`]
//!   (non-empty `extra_args`); Router/OffRamp lane checks remain the protocol gate — this is defense-in-depth on the app contract.
//! - **Per-chain `RemoteChainConfig`** (`extra_args` + `allowed_finality_config` as `u32`, EVM `bytes4` / FinalityCodec):
//!   set via [`ExampleCcipReceiver::enable_remote_chain`], read via [`ExampleCcipReceiver::get_remote_chain_config`];
//!   outbound sends use stored `extra_args` only ([`ExampleCcipReceiver::send_data_pay_fee_token`]).
//! - **Per-source CCV lists**: [`ExampleCcipReceiver::apply_ccv_config_updates`] (EVM `applyCCVConfigUpdates`).
//! - **`get_ccvs_and_finality_config`**: EVM-shaped view combining CCV lists + `allowed_finality_config` for a selector.
//!   Stellar OffRamp does **not** invoke this (unlike EVM static-call); for tooling / future protocol integration only.
//! - **`get_remote_chain_selectors`**: Bounded enumeration of selectors configured via `enable_remote_chain` (EVM `getRemoteChainSelectors`).

mod events;

use soroban_sdk::{
    contract, contractimpl, symbol_short, Address, Bytes, BytesN, Env, IntoVal, Symbol, Vec,
};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_interfaces::ccip_receiver::{
    CcvChainConfig, CcvConfigUpdate, CcvsAndFinalityConfig, RemoteChainConfig,
};
use common_interfaces::router::{RouterClient, StellarToAnyMessage};
use common_message::AnyToStellarMessage;
use events::{CcipCcvConfigSetEvent, CcipMessageReceivedEvent, CcipRemoteChainConfiguredEvent};

const ROUTER: Symbol = symbol_short!("ROUTER");
const LAST_MID: Symbol = symbol_short!("LASTMID");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
/// Persistent key for [`RemoteChainConfig`] (EVM `s_chains[selector]`).
const REM_CFG: Symbol = symbol_short!("RMCFG");
/// Instance key: selectors with a non-empty `RemoteChainConfig` (EVM `s_remoteChainSelectors`, insertion order).
const REM_SELS: Symbol = symbol_short!("RMSELS");
/// Cap for [`ExampleCcipReceiver::get_remote_chain_selectors`] (Soroban resource limits; EVM set is unbounded).
const MAX_REMOTE_CHAIN_SELECTORS: u32 = 256;
const CCV_KEY: Symbol = symbol_short!("CCVCG");

#[contract]
pub struct ExampleCcipReceiver;

#[contractimpl]
impl Initializable for ExampleCcipReceiver {
    const INITIALIZED: Symbol = symbol_short!("INIT");
}

#[contractimpl]
impl Ownable for ExampleCcipReceiver {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl]
impl ExampleCcipReceiver {
    /// One-time setup: `owner` governs outbound/CCV config; `router` is the only caller for `ccip_receive`.
    pub fn initialize(env: Env, owner: Address, router: Address) -> Result<(), CCIPError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Ownable>::init_owner(&env, &owner)?;
        <Self as Initializable>::init(&env)?;
        env.storage().instance().set(&ROUTER, &router);
        Ok(())
    }

    pub fn type_and_version(_env: Env) -> soroban_sdk::String {
        soroban_sdk::String::from_str(&_env, "ExampleCcipReceiver 1.0.0")
    }

    pub fn get_router(env: Env) -> Result<Address, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&ROUTER)
            .ok_or(CCIPError::NotInitialized)
    }

    /// Entry point invoked by the Router after OffRamp verification (EVM `ccipReceive` analogue).
    pub fn ccip_receive(env: Env, message: AnyToStellarMessage) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;

        let router: Address = env
            .storage()
            .instance()
            .get(&ROUTER)
            .ok_or(CCIPError::NotInitialized)?;

        router.require_auth_for_args(soroban_sdk::vec![&env, message.clone().into_val(&env)]);

        let inbound = Self::get_remote_chain_config(env.clone(), message.source_chain_selector)?;
        if inbound.extra_args.is_empty() {
            return Err(CCIPError::InvalidChainForClient);
        }

        CcipMessageReceivedEvent {
            message_id: message.message_id.clone(),
            source_chain_selector: message.source_chain_selector,
            data_len: message.data.len(),
            sender_len: message.sender.len(),
            dest_token_transfers: message.dest_token_amounts.len(),
        }
        .publish(&env);

        env.storage().instance().set(&LAST_MID, &message.message_id);

        Ok(())
    }

    pub fn last_message_id(env: Env) -> Result<BytesN<32>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&LAST_MID)
            .ok_or(CCIPError::NotInitialized)
    }

    /// Configure a remote chain (EVM `enableChain`): non-empty `extra_args` enables outbound sends;
    /// `allowed_finality_config` is FinalityCodec-style `u32` (EVM `bytes4`) for inbound policy documentation.
    pub fn enable_remote_chain(
        env: Env,
        caller: Address,
        dest_chain_selector: u64,
        extra_args: Bytes,
        allowed_finality_config: u32,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        require_owner_auth(&env, &caller)?;
        if extra_args.is_empty() {
            return Err(CCIPError::ZeroValueNotAllowed);
        }
        let cfg = RemoteChainConfig {
            extra_args: extra_args.clone(),
            allowed_finality_config,
        };
        let key = (REM_CFG, dest_chain_selector);
        env.storage().persistent().set(&key, &cfg);
        track_remote_chain_selector(&env, dest_chain_selector)?;
        CcipRemoteChainConfiguredEvent {
            dest_chain_selector,
            extra_args_len: extra_args.len(),
            allowed_finality_config,
        }
        .publish(&env);
        Ok(())
    }

    pub fn disable_remote_chain(
        env: Env,
        caller: Address,
        dest_chain_selector: u64,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        require_owner_auth(&env, &caller)?;
        let key = (REM_CFG, dest_chain_selector);
        env.storage().persistent().remove(&key);
        untrack_remote_chain_selector(&env, dest_chain_selector);
        CcipRemoteChainConfiguredEvent {
            dest_chain_selector,
            extra_args_len: 0,
            allowed_finality_config: 0,
        }
        .publish(&env);
        Ok(())
    }

    /// EVM `getRemoteChainConfig`: `extra_args` empty and `allowed_finality_config == 0` when unset.
    pub fn get_remote_chain_config(
        env: Env,
        chain_selector: u64,
    ) -> Result<RemoteChainConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let key = (REM_CFG, chain_selector);
        Ok(env
            .storage()
            .persistent()
            .get(&key)
            .unwrap_or(RemoteChainConfig {
                extra_args: Bytes::new(&env),
                allowed_finality_config: 0,
            }))
    }

    pub fn get_remote_chain_extra_args(
        env: Env,
        dest_chain_selector: u64,
    ) -> Result<Bytes, CCIPError> {
        Ok(Self::get_remote_chain_config(env, dest_chain_selector)?.extra_args)
    }

    /// Selectors currently enabled via [`ExampleCcipReceiver::enable_remote_chain`] (non-empty `extra_args`),
    /// in insertion order — EVM `getRemoteChainSelectors`. Capped at [`MAX_REMOTE_CHAIN_SELECTORS`].
    pub fn get_remote_chain_selectors(env: Env) -> Result<Vec<u64>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        Ok(load_remote_chain_selector_list(&env))
    }

    /// EVM `IAny2EVMMessageReceiverV2.getCCVsAndFinalityConfig` shape. `unused` mirrors unused EVM calldata.
    /// **Not called by Stellar OffRamp** today; returns stored CCV row + `allowed_finality_config` from
    /// [`RemoteChainConfig`] for the same `source_chain_selector` (EVM uses one mapping per selector).
    pub fn get_ccvs_and_finality_config(
        env: Env,
        source_chain_selector: u64,
        unused: Bytes,
    ) -> Result<CcvsAndFinalityConfig, CCIPError> {
        let _ = unused;
        <Self as Initializable>::require_initialized(&env)?;
        let ccv = Self::get_ccv_config(env.clone(), source_chain_selector)?;
        let rem = Self::get_remote_chain_config(env, source_chain_selector)?;
        Ok(CcvsAndFinalityConfig {
            required_ccvs: ccv.required_ccvs,
            optional_ccvs: ccv.optional_ccvs,
            optional_threshold: ccv.optional_threshold,
            allowed_finality_config: rem.allowed_finality_config,
        })
    }

    /// Batch-set CCV lists per source chain (mirrors EVM validation rules from `applyCCVConfigUpdates`).
    pub fn apply_ccv_config_updates(
        env: Env,
        caller: Address,
        updates: Vec<CcvConfigUpdate>,
    ) -> Result<(), CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        require_owner_auth(&env, &caller)?;
        for i in 0..updates.len() {
            let u = updates.get(i).ok_or(CCIPError::InvalidConfig)?;
            validate_ccv_config_update(&u)?;
            let cfg = CcvChainConfig {
                required_ccvs: u.required_ccvs.clone(),
                optional_ccvs: u.optional_ccvs.clone(),
                optional_threshold: u.optional_threshold,
            };
            let key = (CCV_KEY, u.source_chain_selector);
            env.storage().persistent().set(&key, &cfg);
            CcipCcvConfigSetEvent {
                source_chain_selector: u.source_chain_selector,
                required_len: u.required_ccvs.len(),
                optional_len: u.optional_ccvs.len(),
                optional_threshold: u.optional_threshold,
            }
            .publish(&env);
        }
        Ok(())
    }

    pub fn get_ccv_config(
        env: Env,
        source_chain_selector: u64,
    ) -> Result<CcvChainConfig, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        let key = (CCV_KEY, source_chain_selector);
        Ok(env
            .storage()
            .persistent()
            .get(&key)
            .unwrap_or(CcvChainConfig {
                required_ccvs: Vec::new(&env),
                optional_ccvs: Vec::new(&env),
                optional_threshold: 0,
            }))
    }

    /// Data-only CCIP send using stored per-destination `extra_args`. `caller` is the Router `sender` and pays fees.
    pub fn send_data_pay_fee_token(
        env: Env,
        caller: Address,
        dest_chain_selector: u64,
        receiver: Bytes,
        data: Bytes,
        fee_token: Address,
        fee_token_amount: i128,
    ) -> Result<BytesN<32>, CCIPError> {
        <Self as Initializable>::require_initialized(&env)?;
        caller.require_auth();

        let extra_args = {
            let key = (REM_CFG, dest_chain_selector);
            match env
                .storage()
                .persistent()
                .get::<(Symbol, u64), RemoteChainConfig>(&key)
            {
                Some(cfg) if !cfg.extra_args.is_empty() => cfg.extra_args,
                _ => return Err(CCIPError::DestinationChainNotEnabled),
            }
        };

        let router: Address = env
            .storage()
            .instance()
            .get(&ROUTER)
            .ok_or(CCIPError::NotInitialized)?;

        let message = StellarToAnyMessage {
            receiver,
            data,
            token_amounts: Vec::new(&env),
            fee_token,
            extra_args,
        };

        let router_client = RouterClient::new(&env, &router);
        Ok(router_client.ccip_send(&caller, &dest_chain_selector, &message, &fee_token_amount))
    }
}

fn require_owner_auth(env: &Env, caller: &Address) -> Result<(), CCIPError> {
    let owner = <ExampleCcipReceiver as Ownable>::owner(env).ok_or(CCIPError::NotOwner)?;
    if caller != &owner {
        return Err(CCIPError::Unauthorized);
    }
    caller.require_auth();
    Ok(())
}

fn validate_ccv_config_update(u: &CcvConfigUpdate) -> Result<(), CCIPError> {
    let olen = u.optional_ccvs.len();
    if olen > 0 {
        if u.optional_threshold >= olen {
            return Err(CCIPError::InvalidConfig);
        }
    } else if u.optional_threshold > 0 {
        return Err(CCIPError::InvalidConfig);
    }

    let rlen = u.required_ccvs.len();
    let total = rlen + olen;
    for i in 0..total {
        let ai = addr_at(u, i)?;
        for j in (i + 1)..total {
            let aj = addr_at(u, j)?;
            if ai == aj {
                return Err(CCIPError::InvalidConfig);
            }
        }
    }
    Ok(())
}

fn addr_at(u: &CcvConfigUpdate, idx: u32) -> Result<Address, CCIPError> {
    let rlen = u.required_ccvs.len();
    if idx < rlen {
        u.required_ccvs.get(idx).ok_or(CCIPError::InvalidConfig)
    } else {
        u.optional_ccvs
            .get(idx - rlen)
            .ok_or(CCIPError::InvalidConfig)
    }
}

fn load_remote_chain_selector_list(env: &Env) -> Vec<u64> {
    env.storage()
        .instance()
        .get(&REM_SELS)
        .unwrap_or_else(|| Vec::new(env))
}

fn save_remote_chain_selector_list(env: &Env, list: &Vec<u64>) {
    env.storage().instance().set(&REM_SELS, list);
}

fn remote_chain_selector_list_contains(env: &Env, sel: u64) -> bool {
    let v = load_remote_chain_selector_list(env);
    for i in 0..v.len() {
        if v.get(i).unwrap() == sel {
            return true;
        }
    }
    false
}

fn track_remote_chain_selector(env: &Env, sel: u64) -> Result<(), CCIPError> {
    if remote_chain_selector_list_contains(env, sel) {
        return Ok(());
    }
    let mut v = load_remote_chain_selector_list(env);
    if v.len() >= MAX_REMOTE_CHAIN_SELECTORS {
        return Err(CCIPError::InvalidConfig);
    }
    v.push_back(sel);
    save_remote_chain_selector_list(env, &v);
    Ok(())
}

fn untrack_remote_chain_selector(env: &Env, sel: u64) {
    let v = load_remote_chain_selector_list(env);
    let mut out = Vec::new(env);
    let mut found = false;
    for i in 0..v.len() {
        let x = v.get(i).unwrap();
        if x == sel {
            found = true;
        } else {
            out.push_back(x);
        }
    }
    if found {
        save_remote_chain_selector_list(env, &out);
    }
}

mod test;
