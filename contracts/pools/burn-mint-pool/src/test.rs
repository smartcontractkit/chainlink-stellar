#![cfg(test)]

use soroban_sdk::{
    testutils::{Address as _, Ledger, MockAuth, MockAuthInvoke},
    token, vec, Address, Bytes, BytesN, Env, IntoVal, Val, Vec,
};

use crate::{BurnMintTokenPoolContract, BurnMintTokenPoolContractClient};
use ccip_ramp_registry::{
    OffRampUpdate, OnRampUpdate, RampRegistryContract, RampRegistryContractClient,
};
use common_error::CCIPError;
use common_interfaces::token_pool::{
    LockOrBurnIn as IfaceLockOrBurnIn, MessageDirection as IfaceMessageDirection,
    PoolRequiredCCVs as IfacePoolRequiredCCVs, ReleaseOrMintIn as IfaceReleaseOrMintIn,
};
use common_pool::{
    encode_local_decimals, ChainUpdate, LockOrBurnIn, MessageDirection, RateLimitConfig,
    ReleaseOrMintIn, TokenTransferFeeConfig, TokenTransferFeeConfigArgs,
};
use pools_advanced_pool_hooks::{
    AdvancedPoolHooksContract, AdvancedPoolHooksContractClient, CCVConfigArg,
};
use rmn_proxy::{RmnProxyContract, RmnProxyContractClient};
use rmn_remote::{RmnRemoteContract, RmnRemoteContractClient};
use router::{RouterContract, RouterContractClient};

/// Minimal hook contracts for pool integration tests (must match `PoolHooksInterface` ABI).
mod mock_hooks {
    use soroban_sdk::{contract, contractimpl, symbol_short, Address, Bytes, Env, Symbol, Vec};

    use super::{
        CCIPError, IfaceLockOrBurnIn, IfaceMessageDirection, IfacePoolRequiredCCVs,
        IfaceReleaseOrMintIn,
    };

    #[contract]
    pub struct MockPreflightRejects;

    #[contractimpl]
    impl MockPreflightRejects {
        pub fn preflight_check(
            env: Env,
            lock_or_burn_in: IfaceLockOrBurnIn,
            requested_finality: u32,
            token_args: Bytes,
            amount: i128,
        ) -> Result<(), CCIPError> {
            let _ = (env, lock_or_burn_in, requested_finality, token_args, amount);
            Err(CCIPError::SenderNotAllowed)
        }

        pub fn postflight_check(
            env: Env,
            release_or_mint_in: IfaceReleaseOrMintIn,
            local_amount: i128,
            requested_finality: u32,
        ) -> Result<(), CCIPError> {
            let _ = (env, release_or_mint_in, local_amount, requested_finality);
            Ok(())
        }

        pub fn get_required_ccvs(
            env: Env,
            _local_token: Address,
            _remote_chain_selector: u64,
            _amount: i128,
            _requested_finality: u32,
            _extra_data: Bytes,
            _direction: IfaceMessageDirection,
        ) -> IfacePoolRequiredCCVs {
            IfacePoolRequiredCCVs {
                ccvs: Vec::new(&env),
                include_defaults: true,
            }
        }
    }

    #[contract]
    pub struct MockPostflightRejects;

    #[contractimpl]
    impl MockPostflightRejects {
        pub fn preflight_check(
            env: Env,
            lock_or_burn_in: IfaceLockOrBurnIn,
            requested_finality: u32,
            token_args: Bytes,
            amount: i128,
        ) -> Result<(), CCIPError> {
            let _ = (env, lock_or_burn_in, requested_finality, token_args, amount);
            Ok(())
        }

        pub fn postflight_check(
            env: Env,
            release_or_mint_in: IfaceReleaseOrMintIn,
            local_amount: i128,
            requested_finality: u32,
        ) -> Result<(), CCIPError> {
            let _ = (env, release_or_mint_in, local_amount, requested_finality);
            Err(CCIPError::SenderNotAllowed)
        }

        pub fn get_required_ccvs(
            env: Env,
            _local_token: Address,
            _remote_chain_selector: u64,
            _amount: i128,
            _requested_finality: u32,
            _extra_data: Bytes,
            _direction: IfaceMessageDirection,
        ) -> IfacePoolRequiredCCVs {
            IfacePoolRequiredCCVs {
                ccvs: Vec::new(&env),
                include_defaults: true,
            }
        }
    }

    const RETURNED_CCV_KEY: Symbol = symbol_short!("RCCV");

    #[contract]
    pub struct MockReturnsCcv;

    #[contractimpl]
    impl MockReturnsCcv {
        pub fn set_returned_ccv(env: Env, ccv: Address) {
            env.storage().instance().set(&RETURNED_CCV_KEY, &ccv);
        }

        pub fn preflight_check(
            env: Env,
            lock_or_burn_in: IfaceLockOrBurnIn,
            requested_finality: u32,
            token_args: Bytes,
            amount: i128,
        ) -> Result<(), CCIPError> {
            let _ = (env, lock_or_burn_in, requested_finality, token_args, amount);
            Ok(())
        }

        pub fn postflight_check(
            env: Env,
            release_or_mint_in: IfaceReleaseOrMintIn,
            local_amount: i128,
            requested_finality: u32,
        ) -> Result<(), CCIPError> {
            let _ = (env, release_or_mint_in, local_amount, requested_finality);
            Ok(())
        }

        pub fn get_required_ccvs(
            env: Env,
            _local_token: Address,
            _remote_chain_selector: u64,
            _amount: i128,
            _requested_finality: u32,
            _extra_data: Bytes,
            _direction: IfaceMessageDirection,
        ) -> IfacePoolRequiredCCVs {
            let ccv: Address = env
                .storage()
                .instance()
                .get(&RETURNED_CCV_KEY)
                .expect("set_returned_ccv must be called first");
            IfacePoolRequiredCCVs {
                ccvs: Vec::from_array(&env, [ccv]),
                include_defaults: false,
            }
        }
    }

    const CAPTURED_TOKEN_ARGS_KEY: Symbol = symbol_short!("CTA");

    /// Captures the `token_args` received by `preflight_check` into instance
    /// storage so a test can assert the sender-supplied payload is threaded
    /// end-to-end from `lock_or_burn` to the advanced-pool-hooks contract
    /// (EVM `IAdvancedPoolHooks.preflightCheck` parity).
    #[contract]
    pub struct MockCapturesTokenArgs;

    #[contractimpl]
    impl MockCapturesTokenArgs {
        pub fn get_captured_token_args(env: Env) -> Bytes {
            env.storage()
                .instance()
                .get(&CAPTURED_TOKEN_ARGS_KEY)
                .unwrap_or_else(|| Bytes::new(&env))
        }

        pub fn preflight_check(
            env: Env,
            lock_or_burn_in: IfaceLockOrBurnIn,
            requested_finality: u32,
            token_args: Bytes,
            amount: i128,
        ) -> Result<(), CCIPError> {
            env.storage()
                .instance()
                .set(&CAPTURED_TOKEN_ARGS_KEY, &token_args);
            let _ = (lock_or_burn_in, requested_finality, amount);
            Ok(())
        }

        pub fn postflight_check(
            env: Env,
            release_or_mint_in: IfaceReleaseOrMintIn,
            local_amount: i128,
            requested_finality: u32,
        ) -> Result<(), CCIPError> {
            let _ = (env, release_or_mint_in, local_amount, requested_finality);
            Ok(())
        }

        pub fn get_required_ccvs(
            env: Env,
            _local_token: Address,
            _remote_chain_selector: u64,
            _amount: i128,
            _requested_finality: u32,
            _extra_data: Bytes,
            _direction: IfaceMessageDirection,
        ) -> IfacePoolRequiredCCVs {
            IfacePoolRequiredCCVs {
                ccvs: Vec::new(&env),
                include_defaults: true,
            }
        }
    }
}

mod inbound_release_stub {
    use soroban_sdk::{contract, contractimpl, Address, Env};

    use crate::BurnMintTokenPoolContractClient;
    use common_error::CCIPError;
    use common_pool::{ReleaseOrMintIn, ReleaseOrMintOut};

    #[contract]
    pub struct PoolInboundReleaseStub;

    #[contractimpl]
    impl PoolInboundReleaseStub {
        pub fn release(
            env: Env,
            pool: Address,
            input: ReleaseOrMintIn,
            requested_finality: u32,
        ) -> Result<ReleaseOrMintOut, CCIPError> {
            let me = env.current_contract_address();
            let client = BurnMintTokenPoolContractClient::new(&env, &pool);
            Ok(client.release_or_mint(&me, &input, &requested_finality))
        }
    }
}

const DEFAULT_REMOTE_CHAIN: u64 = 5009297550715157269;

/// Register real Router + RMN proxy + RMN remote contracts and wire them, returning
/// the Router address (to pass to the pool's `initialize`) and the RMN remote client
/// (so tests can curse a subject). Mirrors the onramp test wiring. The RMN starts
/// uncursed, so happy-path pool operations are unaffected.
fn setup_router_with_rmn(
    env: &Env,
    owner: &Address,
) -> (Address, Address, RmnRemoteContractClient<'static>) {
    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(env, &rmn_remote_id);
    rmn_remote_client.initialize(owner, &Vec::new(env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    let rmn_proxy_client = RmnProxyContractClient::new(env, &rmn_proxy_id);
    rmn_proxy_client.initialize(owner, &rmn_remote_id);

    let router_id = env.register(RouterContract, ());
    let router_client = RouterContractClient::new(env, &router_id);
    router_client.initialize(owner, &rmn_proxy_id);

    (router_id, rmn_proxy_id, rmn_remote_client)
}

fn setup_env() -> (
    Env,
    BurnMintTokenPoolContractClient<'static>,
    Address,
    Address,
    token::Client<'static>,
    token::StellarAssetClient<'static>,
    RampRegistryContractClient<'static>,
    inbound_release_stub::PoolInboundReleaseStubClient<'static>,
    Address,
) {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let registry_id = env.register(RampRegistryContract, ());
    let registry_client = RampRegistryContractClient::new(&env, &registry_id);
    registry_client.initialize(&owner);

    let auth_onramp = Address::generate(&env);
    register_onramp_for_chain(&env, &registry_client, DEFAULT_REMOTE_CHAIN, &auth_onramp);

    let stub_id = env.register(inbound_release_stub::PoolInboundReleaseStub, ());
    let stub_client = inbound_release_stub::PoolInboundReleaseStubClient::new(&env, &stub_id);

    let pool_id = env.register(BurnMintTokenPoolContract, ());
    let pool_client = BurnMintTokenPoolContractClient::new(&env, &pool_id);

    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_client = token::Client::new(&env, &token_address);
    let token_admin_client = token::StellarAssetClient::new(&env, &token_address);

    // Set the pool contract as the token admin so it can mint
    token_admin_client.set_admin(&pool_id);

    let (router, rmn_proxy, _rmn_remote) = setup_router_with_rmn(&env, &owner);
    pool_client.initialize(
        &owner,
        &token_address,
        &7u32,
        &router,
        &registry_client.address,
        &rmn_proxy,
    );

    (
        env,
        pool_client,
        owner,
        token_address,
        token_client,
        token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    )
}

fn register_onramp_for_chain(
    env: &Env,
    registry_client: &RampRegistryContractClient,
    dest_chain_selector: u64,
    onramp: &Address,
) {
    registry_client.apply_onramp_updates(&Vec::from_array(
        env,
        [OnRampUpdate {
            dest_chain_selector,
            onramp: Some(onramp.clone()),
        }],
    ));
}

fn register_offramp_for_chain(
    env: &Env,
    registry_client: &RampRegistryContractClient,
    stub_client: &inbound_release_stub::PoolInboundReleaseStubClient,
    source_chain_selector: u64,
) {
    registry_client.apply_offramp_updates(&Vec::from_array(
        env,
        [OffRampUpdate {
            source_chain_selector,
            offramp: stub_client.address.clone(),
            enabled: true,
        }],
    ));
}

#[test]
fn test_initialize() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    let pool_token = pool_client.get_token();
    assert_eq!(pool_token, token_address);
    assert_eq!(pool_client.get_token_decimals(), 7);

    assert!(pool_client.is_supported_token(&token_address));
    let other_token = Address::generate(&env);
    assert!(!pool_client.is_supported_token(&other_token));
}

#[test]
fn test_burn_and_mint() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    let remote_pool = Bytes::from_slice(&env, &[1u8; 20]);
    let remote_token = Bytes::from_slice(&env, &[2u8; 20]);

    let chain_update = ChainUpdate {
        remote_chain_selector: remote_chain,
        remote_pool_addresses: vec![&env, remote_pool],
        remote_token_address: remote_token.clone(),
        outbound_rate_limiter_config: RateLimitConfig::disabled(),
        inbound_rate_limiter_config: RateLimitConfig::disabled(),
    };
    pool_client.apply_chain_updates(&Vec::from_array(&env, [chain_update]), &Vec::new(&env));

    let sender = Address::generate(&env);
    let burn_amount: i128 = 1_000_000_000;
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    sac_client.mint(&sender, &burn_amount);
    assert_eq!(token_client.balance(&sender), burn_amount);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: burn_amount,
        local_token: token_address.clone(),
    };

    let burn_result = pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(burn_result.dest_token_address, remote_token);
    assert_eq!(token_client.balance(&sender), 0);

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: burn_amount,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };

    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    let mint_result = stub_client.release(&pool_client.address, &release_input, &0u32);
    assert_eq!(mint_result.destination_amount, burn_amount);
    assert_eq!(token_client.balance(&receiver), burn_amount);
}

#[test]
fn test_unsupported_chain_rejected() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    let sender = Address::generate(&env);
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    sac_client.mint(&sender, &1_000_000_000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[1u8; 20]),
        remote_chain_selector: 999,
        original_sender: sender,
        amount: 100,
        local_token: token_address,
    };

    let result = pool_client.try_lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert!(result.is_err());
}

#[test]
fn test_wrong_token_rejected() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    register_onramp_for_chain(&env, &registry_client, 1u64, &auth_onramp);

    let wrong_token = Address::generate(&env);
    let sender = Address::generate(&env);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[1u8; 20]),
        remote_chain_selector: 1,
        original_sender: sender,
        amount: 100,
        local_token: wrong_token,
    };

    let result = pool_client.try_lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert!(result.is_err());
}

fn chain_update(env: &Env, selector: u64, pool_byte: u8, token_byte: u8) -> ChainUpdate {
    ChainUpdate {
        remote_chain_selector: selector,
        remote_pool_addresses: vec![env, Bytes::from_slice(env, &[pool_byte; 20])],
        remote_token_address: Bytes::from_slice(env, &[token_byte; 20]),
        outbound_rate_limiter_config: RateLimitConfig::disabled(),
        inbound_rate_limiter_config: RateLimitConfig::disabled(),
    }
}

fn chain_update_with_limits(
    env: &Env,
    selector: u64,
    pool_byte: u8,
    token_byte: u8,
    outbound: RateLimitConfig,
    inbound: RateLimitConfig,
) -> ChainUpdate {
    ChainUpdate {
        remote_chain_selector: selector,
        remote_pool_addresses: vec![env, Bytes::from_slice(env, &[pool_byte; 20])],
        remote_token_address: Bytes::from_slice(env, &[token_byte; 20]),
        outbound_rate_limiter_config: outbound,
        inbound_rate_limiter_config: inbound,
    }
}

#[test]
#[should_panic(expected = "Error(Contract, #52)")] // InvalidConfig
fn test_apply_chain_updates_rejects_empty_remote_pool_address() {
    // M-14 / INV-POOL-ENC-2/4, INV-PCFG-1: a chain update with an empty remote pool
    // address must be rejected at config time. Mirrors EVM `TokenPool
    // ._validateTokenPoolConfig`, which requires a non-empty `remoteTokenAddress` and a
    // remote pool that is only ever set (never emptied) via `setRemotePool`. An empty
    // pool address would create a degenerate lane whose source-pool validation (C-3) and
    // release/mint destination could never match.
    let (env, pool_client, ..) = setup_env();
    let remote_chain: u64 = 5009297550715157269;

    let update = ChainUpdate {
        remote_chain_selector: remote_chain,
        remote_pool_addresses: Vec::new(&env), // empty ⇒ rejected
        remote_token_address: Bytes::from_slice(&env, &[2u8; 20]),
        outbound_rate_limiter_config: RateLimitConfig::disabled(),
        inbound_rate_limiter_config: RateLimitConfig::disabled(),
    };
    pool_client.apply_chain_updates(&Vec::from_array(&env, [update]), &Vec::new(&env));
}

#[test]
#[should_panic(expected = "Error(Contract, #52)")] // InvalidConfig
fn test_apply_chain_updates_rejects_empty_remote_token_address() {
    // M-14 / INV-POOL-ENC-2/4, INV-PCFG-1: companion to the empty-pool test — an empty
    // remote token address is likewise rejected at config time, before the lane is
    // materialized into storage.
    let (env, pool_client, ..) = setup_env();
    let remote_chain: u64 = 5009297550715157269;

    let update = ChainUpdate {
        remote_chain_selector: remote_chain,
        remote_pool_addresses: vec![&env, Bytes::from_slice(&env, &[1u8; 20])],
        remote_token_address: Bytes::new(&env), // empty ⇒ rejected
        outbound_rate_limiter_config: RateLimitConfig::disabled(),
        inbound_rate_limiter_config: RateLimitConfig::disabled(),
    };
    pool_client.apply_chain_updates(&Vec::from_array(&env, [update]), &Vec::new(&env));
}

/// Like `setup_env` but also returns the RMN remote client (last element), so curse
/// tests can curse the remote chain's subject before invoking the pool. The pool is
/// wired to a real Router + RMN (uncursed initially); happy-path behavior is unchanged.
#[allow(clippy::type_complexity)]
fn setup_env_with_rmn() -> (
    Env,
    BurnMintTokenPoolContractClient<'static>,
    Address,
    Address,
    RampRegistryContractClient<'static>,
    inbound_release_stub::PoolInboundReleaseStubClient<'static>,
    Address,
    RmnRemoteContractClient<'static>,
) {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let registry_id = env.register(RampRegistryContract, ());
    let registry_client = RampRegistryContractClient::new(&env, &registry_id);
    registry_client.initialize(&owner);

    let auth_onramp = Address::generate(&env);
    register_onramp_for_chain(&env, &registry_client, DEFAULT_REMOTE_CHAIN, &auth_onramp);

    let stub_id = env.register(inbound_release_stub::PoolInboundReleaseStub, ());
    let stub_client = inbound_release_stub::PoolInboundReleaseStubClient::new(&env, &stub_id);

    let (router, rmn_proxy, rmn_remote_client) = setup_router_with_rmn(&env, &owner);

    let pool_id = env.register(BurnMintTokenPoolContract, ());
    let pool_client = BurnMintTokenPoolContractClient::new(&env, &pool_id);

    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_admin_client = token::StellarAssetClient::new(&env, &token_address);
    token_admin_client.set_admin(&pool_id);

    pool_client.initialize(
        &owner,
        &token_address,
        &7u32,
        &router,
        &registry_client.address,
        &rmn_proxy,
    );

    (
        env,
        pool_client,
        owner,
        token_address,
        registry_client,
        stub_client,
        auth_onramp,
        rmn_remote_client,
    )
}

/// Build the 16-byte RMN subject for a chain selector (selector in the low 8 bytes),
/// exactly as `BaseTokenPool::require_remote_chain_not_cursed` constructs it (and as
/// `CurseCheckable::require_chain_not_cursed` does for the ramps).
fn rmn_subject(env: &Env, chain_selector: u64) -> BytesN<16> {
    let mut subject = [0u8; 16];
    subject[8..16].copy_from_slice(&chain_selector.to_be_bytes());
    BytesN::from_array(env, &subject)
}

#[test]
#[should_panic(expected = "Error(Contract, #47)")] // CursedByRMN
fn test_lock_or_burn_reverts_when_remote_chain_cursed() {
    // M-6 / INV-POOL-RMN-1: `lock_or_burn` reverts `CursedByRMN` when the RMN has
    // cursed the remote chain's subject, mirroring EVM `TokenPool._validateLockOrBurn`
    // (TokenPool.sol:422). The curse check runs after the chain-support + onramp-auth
    // checks and before any rate-limit consume / token burn.
    let (
        env,
        pool_client,
        owner,
        token_address,
        _registry_client,
        _stub_client,
        auth_onramp,
        rmn_remote_client,
    ) = setup_env_with_rmn();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );

    // Curse the remote chain's subject.
    let _ = rmn_remote_client.curse(
        &owner,
        &Vec::from_array(&env, [rmn_subject(&env, DEFAULT_REMOTE_CHAIN)]),
    );

    let sender = Address::generate(&env);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        original_sender: sender,
        amount: 100,
        local_token: token_address,
    };

    let _ = pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
}

#[test]
#[should_panic(expected = "Error(Contract, #47)")] // CursedByRMN
fn test_release_or_mint_reverts_when_remote_chain_cursed() {
    // M-6 / INV-POOL-RMN-1: `release_or_mint` reverts `CursedByRMN` when the RMN has
    // cursed the remote chain's subject, mirroring EVM `TokenPool._validateReleaseOrMint`
    // (TokenPool.sol:479). The curse check runs before the source-pool membership check.
    let (
        env,
        pool_client,
        owner,
        token_address,
        registry_client,
        stub_client,
        _auth_onramp,
        rmn_remote_client,
    ) = setup_env_with_rmn();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );

    // Curse the remote chain's subject.
    let _ = rmn_remote_client.curse(
        &owner,
        &Vec::from_array(&env, [rmn_subject(&env, DEFAULT_REMOTE_CHAIN)]),
    );

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        receiver: receiver.clone(),
        amount: 0,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };

    register_offramp_for_chain(&env, &registry_client, &stub_client, DEFAULT_REMOTE_CHAIN);
    let _ = stub_client.release(&pool_client.address, &release_input, &0u32);
}

#[test]
#[should_panic(expected = "Error(Contract, #2)")] // AlreadyInitialized
fn test_initialize_twice_rejected() {
    let (
        _env,
        pool_client,
        owner,
        token_address,
        _token_client,
        _token_admin_client,
        registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();
    let router = Address::generate(&_env);
    let rmn_proxy = Address::generate(&_env);
    pool_client.initialize(
        &owner,
        &token_address,
        &7u32,
        &router,
        &registry_client.address,
        &rmn_proxy,
    );
}

#[test]
fn test_lock_or_burn_zero_amount_succeeds_when_chain_configured() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 0,
        local_token: token_address.clone(),
    };

    let out = pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(out.dest_token_address, Bytes::from_slice(&env, &[2u8; 20]));
    assert_eq!(token_client.balance(&sender), 0);
}

#[test]
fn test_release_or_mint_zero_amount_succeeds() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 0,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };

    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    let out = stub_client.release(&pool_client.address, &release_input, &0u32);
    assert_eq!(out.destination_amount, 0);
    assert_eq!(token_client.balance(&receiver), 0);
}

#[test]
fn test_lock_or_burn_amount_exceeds_sender_balance_fails() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &100);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 101,
        local_token: token_address,
    };

    let result = pool_client.try_lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert!(result.is_err());
}

#[test]
fn test_lock_or_burn_negative_amount_fails() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1_000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: -1,
        local_token: token_address,
    };

    let result = pool_client.try_lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert!(result.is_err());
}

#[test]
fn test_apply_chain_updates_remove_unlists_chain() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    assert!(pool_client.is_supported_chain(&remote_chain));

    pool_client.apply_chain_updates(&Vec::new(&env), &Vec::from_array(&env, [remote_chain]));
    assert!(!pool_client.is_supported_chain(&remote_chain));

    let sender = Address::generate(&env);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 1,
        local_token: token_address.clone(),
    };
    let result = pool_client.try_lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(result, Err(Ok(CCIPError::ChainNotSupported)));

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 9, 8)]),
        &Vec::new(&env),
    );
    assert!(pool_client.is_supported_chain(&remote_chain));
    assert_eq!(
        pool_client.get_remote_token(&remote_chain),
        Bytes::from_slice(&env, &[8u8; 20])
    );
}

#[test]
fn test_apply_chain_updates_duplicate_selector_overwrites_remote_token() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 3, 4)]),
        &Vec::new(&env),
    );
    assert_eq!(
        pool_client.get_remote_token(&remote_chain),
        Bytes::from_slice(&env, &[4u8; 20])
    );

    let sender = Address::generate(&env);
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    sac_client.mint(&sender, &1);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 1,
        local_token: token_address,
    };
    assert!(pool_client
        .try_lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env))
        .is_ok());
}

#[test]
fn test_lock_or_burn_dest_pool_data_encodes_local_decimals() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &100);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 100,
        local_token: token_address,
    };
    let out = pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    let expected = encode_local_decimals(&env, 7).unwrap();
    assert_eq!(out.dest_pool_data, expected);
    assert_eq!(token_client.balance(&sender), 0);
}

#[test]
fn test_release_or_mint_scales_down_remote_more_decimals() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let registry_id = env.register(RampRegistryContract, ());
    let registry_client = RampRegistryContractClient::new(&env, &registry_id);
    registry_client.initialize(&owner);
    let auth_onramp = Address::generate(&env);
    register_onramp_for_chain(&env, &registry_client, DEFAULT_REMOTE_CHAIN, &auth_onramp);

    let stub_id = env.register(inbound_release_stub::PoolInboundReleaseStub, ());
    let stub_client = inbound_release_stub::PoolInboundReleaseStubClient::new(&env, &stub_id);

    let pool_id = env.register(BurnMintTokenPoolContract, ());
    let pool_client = BurnMintTokenPoolContractClient::new(&env, &pool_id);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_client = token::Client::new(&env, &token_address);
    let token_admin_client = token::StellarAssetClient::new(&env, &token_address);
    token_admin_client.set_admin(&pool_id);

    let local_decimals: u32 = 6;
    let (router, rmn_proxy, _rmn_remote) = setup_router_with_rmn(&env, &owner);
    pool_client.initialize(
        &owner,
        &token_address,
        &local_decimals,
        &router,
        &registry_client.address,
        &rmn_proxy,
    );

    let remote_chain: u64 = DEFAULT_REMOTE_CHAIN;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);

    let remote_decimals: u32 = 9;
    let source_amount: i128 = 1_000_000_000; // 1e9 in 9dp
    let expected_local: i128 = 1_000_000; // 1e6 in 6dp

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: source_amount,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: encode_local_decimals(&env, remote_decimals).unwrap(),
    };

    let out = stub_client.release(&pool_client.address, &release_input, &0u32);
    assert_eq!(out.destination_amount, expected_local);
    assert_eq!(token_client.balance(&receiver), expected_local);
}

#[test]
fn test_release_or_mint_scales_up_remote_fewer_decimals() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let registry_id = env.register(RampRegistryContract, ());
    let registry_client = RampRegistryContractClient::new(&env, &registry_id);
    registry_client.initialize(&owner);
    let auth_onramp = Address::generate(&env);
    register_onramp_for_chain(&env, &registry_client, DEFAULT_REMOTE_CHAIN, &auth_onramp);

    let stub_id = env.register(inbound_release_stub::PoolInboundReleaseStub, ());
    let stub_client = inbound_release_stub::PoolInboundReleaseStubClient::new(&env, &stub_id);

    let pool_id = env.register(BurnMintTokenPoolContract, ());
    let pool_client = BurnMintTokenPoolContractClient::new(&env, &pool_id);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_client = token::Client::new(&env, &token_address);
    let token_admin_client = token::StellarAssetClient::new(&env, &token_address);
    token_admin_client.set_admin(&pool_id);

    let local_decimals: u32 = 9;
    let (router, rmn_proxy, _rmn_remote) = setup_router_with_rmn(&env, &owner);
    pool_client.initialize(
        &owner,
        &token_address,
        &local_decimals,
        &router,
        &registry_client.address,
        &rmn_proxy,
    );

    let remote_chain: u64 = DEFAULT_REMOTE_CHAIN;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);

    let remote_decimals: u32 = 6;
    let source_amount: i128 = 1_000_000;
    let expected_local: i128 = 1_000_000_000;

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: source_amount,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: encode_local_decimals(&env, remote_decimals).unwrap(),
    };

    let out = stub_client.release(&pool_client.address, &release_input, &0u32);
    assert_eq!(out.destination_amount, expected_local);
    assert_eq!(token_client.balance(&receiver), expected_local);
}

#[test]
fn test_release_or_mint_invalid_source_pool_data_length() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: Address::generate(&env),
        amount: 100,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::from_slice(&env, &[1u8; 31]),
    };

    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    let e = stub_client
        .try_release(&pool_client.address, &release_input, &0u32)
        .unwrap_err()
        .unwrap();
    assert_eq!(e, CCIPError::InvalidRemoteChainDecimals);
}

#[test]
fn test_release_or_mint_rejects_wrong_source_pool() {
    // Inbound `release_or_mint` must revert `InvalidSourcePoolAddress` when the
    // message's `source_pool_address` is not the configured remote pool for the
    // source chain. Mirrors EVM `TokenPool._validateReleaseOrMint`
    // (`pools/TokenPool.sol:480`), which checks `isRemotePool` before the
    // inbound rate-limit consume.
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        _auth_onramp,
    ) = setup_env();

    // Configure the chain with remote_pool = [1u8;20].
    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    // Claim to originate from a different pool ([9u8;20]).
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: Address::generate(&env),
        amount: 100,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[9u8; 20]),
        source_pool_data: Bytes::new(&env),
    };

    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    let e = stub_client
        .try_release(&pool_client.address, &release_input, &0u32)
        .unwrap_err()
        .unwrap();
    assert_eq!(e, CCIPError::InvalidSourcePoolAddress);
}

#[test]
fn test_add_remote_pool_accepts_inbound_from_new_pool() {
    // H-14: a second remote pool can be added to a chain's configured set
    // without disturbing the first, and inbound `release_or_mint` from the
    // newly-added pool is then accepted (zero-downtime migration). Mirrors EVM
    // `TokenPool.addRemotePool` (`pools/TokenPool.sol:621`).
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        _auth_onramp,
    ) = setup_env();
    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);

    let release_from = |pool_byte: u8| ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: Address::generate(&env),
        amount: 100,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[pool_byte; 20]),
        source_pool_data: Bytes::new(&env),
    };

    // Before adding, inbound from [9;20] is rejected.
    assert_eq!(
        stub_client
            .try_release(&pool_client.address, &release_from(9), &0u32)
            .unwrap_err()
            .unwrap(),
        CCIPError::InvalidSourcePoolAddress
    );

    // Add [9;20] as a second remote pool (owner-gated; auth mocked in setup_env).
    pool_client.add_remote_pool(&remote_chain, &Bytes::from_slice(&env, &[9u8; 20]));

    // Now inbound from [9;20] succeeds, and [1;20] still succeeds.
    assert!(stub_client
        .try_release(&pool_client.address, &release_from(9), &0u32)
        .unwrap()
        .is_ok());
    assert!(stub_client
        .try_release(&pool_client.address, &release_from(1), &0u32)
        .unwrap()
        .is_ok());

    // The configured set now holds both pools.
    let pools = pool_client.get_remote_pools(&remote_chain);
    assert_eq!(pools.len(), 2);
}

#[test]
fn test_remove_remote_pool_rejects_inbound() {
    // H-14: removing a remote pool from a chain's configured set causes inbound
    // `release_or_mint` from it to revert `InvalidSourcePoolAddress`. Removing an
    // absent pool reverts `InvalidRemotePoolAddress` (305). Mirrors EVM
    // `TokenPool.removeRemotePool` (`pools/TokenPool.sol:635`).
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        _auth_onramp,
    ) = setup_env();
    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);

    let release_from_one = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: Address::generate(&env),
        amount: 100,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    // [1;20] accepted before removal.
    assert!(stub_client
        .try_release(&pool_client.address, &release_from_one, &0u32)
        .unwrap()
        .is_ok());

    // Remove [1;20]; inbound from it now reverts.
    pool_client.remove_remote_pool(&remote_chain, &Bytes::from_slice(&env, &[1u8; 20]));
    assert_eq!(
        stub_client
            .try_release(&pool_client.address, &release_from_one, &0u32)
            .unwrap_err()
            .unwrap(),
        CCIPError::InvalidSourcePoolAddress
    );

    // Removing an absent pool reverts InvalidRemotePoolAddress (305).
    assert_eq!(
        pool_client
            .try_remove_remote_pool(&remote_chain, &Bytes::from_slice(&env, &[1u8; 20]))
            .unwrap_err()
            .unwrap(),
        CCIPError::InvalidRemotePoolAddress
    );
}

#[test]
fn test_add_remote_pool_idempotent() {
    // H-14: re-adding an already-configured remote pool is a no-op (EVM
    // `EnumerableSet.add` parity). The set size and contents are unchanged.
    let (env, pool_client, ..) = setup_env();
    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    pool_client.add_remote_pool(&remote_chain, &Bytes::from_slice(&env, &[1u8; 20]));
    let pools = pool_client.get_remote_pools(&remote_chain);
    assert_eq!(pools.len(), 1);
    assert_eq!(pools.get(0).unwrap(), Bytes::from_slice(&env, &[1u8; 20]));
}

#[test]
#[should_panic(expected = "Error(Contract, #52)")] // InvalidConfig
fn test_apply_chain_updates_rejects_empty_remote_pool_element() {
    // H-14 / C-2 gap: now that `remote_pool_addresses` is a `Vec<Bytes>`, the
    // len()==0 guard only catches an empty vec — a vec containing an empty
    // `Bytes` element would slip through and create a degenerate lane whose
    // source-pool validation (C-3) can never match. Reject any empty pool-address
    // element (EVM `TokenPool._validateTokenPoolConfig`).
    let (env, pool_client, ..) = setup_env();
    let remote_chain: u64 = 5009297550715157269;
    let update = ChainUpdate {
        remote_chain_selector: remote_chain,
        remote_pool_addresses: vec![&env, Bytes::new(&env)], // single empty element
        remote_token_address: Bytes::from_slice(&env, &[2u8; 20]),
        outbound_rate_limiter_config: RateLimitConfig::disabled(),
        inbound_rate_limiter_config: RateLimitConfig::disabled(),
    };
    pool_client.apply_chain_updates(&Vec::from_array(&env, [update]), &Vec::new(&env));
}

#[test]
fn test_initialize_rejects_decimals_above_uint8() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let pool_id = env.register(BurnMintTokenPoolContract, ());
    let pool_client = BurnMintTokenPoolContractClient::new(&env, &pool_id);
    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin);
    let token_address = token_contract.address();

    let router = Address::generate(&env);
    let ramp_registry = Address::generate(&env);
    let rmn_proxy = Address::generate(&env);

    let r = pool_client.try_initialize(
        &owner,
        &token_address,
        &256u32,
        &router,
        &ramp_registry,
        &rmn_proxy,
    );
    assert_eq!(r, Err(Ok(CCIPError::InvalidPoolTokenDecimals)));
}

// ================================================================
//  Rate Limit Tests
// ================================================================

#[test]
fn test_lock_or_burn_disabled_rate_limit_passes() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1_000_000);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 1_000_000,
        local_token: token_address,
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(token_client.balance(&sender), 0);
}

#[test]
fn test_lock_or_burn_within_outbound_rate_limit() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                RateLimitConfig::disabled(),
            )],
        ),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &2000);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 500,
        local_token: token_address,
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(token_client.balance(&sender), 1500);
}

#[test]
fn test_lock_or_burn_exceeds_outbound_capacity_rejected() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 500,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                RateLimitConfig::disabled(),
            )],
        ),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1000);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 501,
        local_token: token_address,
    };
    let r = pool_client.try_lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenMaxCapacityExceeded);
}

#[test]
fn test_lock_or_burn_exceeds_available_tokens_rejected() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                RateLimitConfig::disabled(),
            )],
        ),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &2000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 800,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));

    // 200 tokens left, try to burn 201
    let lock_input2 = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 201,
        local_token: token_address,
    };
    let r = pool_client.try_lock_or_burn(&auth_onramp, &lock_input2, &0u32, &Bytes::new(&env));
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);
}

#[test]
fn test_lock_or_burn_refills_over_time() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                RateLimitConfig::disabled(),
            )],
        ),
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &5000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 1000,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));

    // Advance 50 seconds => refill 500
    env.ledger().with_mut(|li| li.timestamp = 150);
    let lock_input2 = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 500,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input2, &0u32, &Bytes::new(&env));

    // Try to burn 1 more — should fail (0 tokens remaining)
    let lock_input3 = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 1,
        local_token: token_address,
    };
    let r = pool_client.try_lock_or_burn(&auth_onramp, &lock_input3, &0u32, &Bytes::new(&env));
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);
}

#[test]
fn test_release_or_mint_within_inbound_rate_limit() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                RateLimitConfig::disabled(),
                inbound,
            )],
        ),
        &Vec::new(&env),
    );

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 500,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    stub_client.release(&pool_client.address, &release_input, &0u32);
    assert_eq!(token_client.balance(&receiver), 500);
}

#[test]
fn test_release_or_mint_exceeds_inbound_capacity_rejected() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 500,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                RateLimitConfig::disabled(),
                inbound,
            )],
        ),
        &Vec::new(&env),
    );

    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: Address::generate(&env),
        amount: 501,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    let r = stub_client.try_release(&pool_client.address, &release_input, &0u32);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenMaxCapacityExceeded);
}

#[test]
fn test_release_or_mint_inbound_refills_over_time() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                RateLimitConfig::disabled(),
                inbound,
            )],
        ),
        &Vec::new(&env),
    );

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 1000,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    stub_client.release(&pool_client.address, &release_input, &0u32);

    // Advance 30s => refill 300
    env.ledger().with_mut(|li| li.timestamp = 130);
    let release_input2 = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 300,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    stub_client.release(&pool_client.address, &release_input2, &0u32);
    assert_eq!(token_client.balance(&receiver), 1300);
}

#[test]
fn test_get_current_rate_limiter_state() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 2000,
        rate: 20,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                inbound,
            )],
        ),
        &Vec::new(&env),
    );

    let state = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(state.outbound.is_enabled);
    assert_eq!(state.outbound.capacity, 1000);
    assert_eq!(state.outbound.tokens, 1000);
    assert!(state.inbound.is_enabled);
    assert_eq!(state.inbound.capacity, 2000);
    assert_eq!(state.inbound.tokens, 2000);

    // Consume some outbound
    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &500);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 500,
        local_token: token_address,
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));

    let state2 = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert_eq!(state2.outbound.tokens, 500);
    assert_eq!(state2.inbound.tokens, 2000);
}

#[test]
fn test_set_rate_limit_config_updates_limits() {
    let (
        env,
        pool_client,
        owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    // Initially disabled
    let state = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(!state.outbound.is_enabled);

    // Enable via set_rate_limit_config
    let new_outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 500,
        rate: 5,
    };
    pool_client.set_rate_limit_config(
        &owner,
        &remote_chain,
        &new_outbound,
        &RateLimitConfig::disabled(),
        &false,
    );

    let state2 = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(state2.outbound.is_enabled);
    assert_eq!(state2.outbound.capacity, 500);
    assert_eq!(state2.outbound.tokens, 500);

    // Verify it enforces the limit
    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1000);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 501,
        local_token: token_address,
    };
    let r = pool_client.try_lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenMaxCapacityExceeded);
}

#[test]
fn test_set_rate_limit_admin_and_admin_can_set_config() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    assert!(pool_client.get_rate_limit_admin().is_none());

    let admin = Address::generate(&env);
    pool_client.set_rate_limit_admin(&admin);
    assert_eq!(pool_client.get_rate_limit_admin().unwrap(), admin);

    let cfg = RateLimitConfig {
        is_enabled: true,
        capacity: 100,
        rate: 1,
    };
    pool_client.set_rate_limit_config(
        &admin,
        &remote_chain,
        &cfg,
        &RateLimitConfig::disabled(),
        &false,
    );

    let state = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(state.outbound.is_enabled);
    assert_eq!(state.outbound.capacity, 100);
}

#[test]
fn test_chain_remove_clears_rate_limits() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                RateLimitConfig::disabled(),
            )],
        ),
        &Vec::new(&env),
    );

    let state = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(state.outbound.is_enabled);

    // Remove the chain
    pool_client.apply_chain_updates(&Vec::new(&env), &Vec::from_array(&env, [remote_chain]));

    // Rate limit state should be gone (defaults to disabled)
    let state2 = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(!state2.outbound.is_enabled);
    assert_eq!(state2.outbound.tokens, 0);
}

#[test]
fn test_both_outbound_and_inbound_limits_enforced() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 500,
        rate: 5,
    };
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 300,
        rate: 3,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                outbound,
                inbound,
            )],
        ),
        &Vec::new(&env),
    );

    // Outbound: burn 400 (within 500 capacity)
    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1000);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 400,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));

    // Inbound: mint 300 (exactly capacity)
    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 300,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    stub_client.release(&pool_client.address, &release_input, &0u32);

    // Inbound: 1 more should fail (0 tokens remaining, no time elapsed)
    let release_input2 = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver,
        amount: 1,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    let r = stub_client.try_release(&pool_client.address, &release_input2, &0u32);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);
}

// ================================================================
//  Fast-Finality (FTF) Rate Limit Tests
// ================================================================

const WAIT_FOR_SAFE: u32 = 1 << 16; // 0x00010000

#[test]
fn test_ftf_inbound_uses_ftf_bucket_when_configured() {
    let (
        env,
        pool_client,
        owner,
        token_address,
        token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    // Configure FTF inbound bucket with smaller capacity than default
    let ftf_inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 200,
        rate: 2,
    };
    pool_client.set_rate_limit_config(
        &owner,
        &remote_chain,
        &RateLimitConfig::disabled(),
        &ftf_inbound,
        &true,
    );

    let receiver = Address::generate(&env);

    // FTF inbound: 200 should succeed (exactly at FTF capacity)
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 200,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    stub_client.release(&pool_client.address, &release_input, &WAIT_FOR_SAFE);
    assert_eq!(token_client.balance(&receiver), 200);

    // FTF inbound: 1 more should fail (FTF bucket exhausted)
    env.ledger().with_mut(|li| li.timestamp = 101);
    let release_input2 = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 3,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    let r = stub_client.try_release(&pool_client.address, &release_input2, &WAIT_FOR_SAFE);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);

    // Default inbound should still be unaffected (disabled = no limit)
    let release_default = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 500,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    stub_client.release(&pool_client.address, &release_default, &0u32);
    assert_eq!(token_client.balance(&receiver), 700);
}

#[test]
fn test_ftf_inbound_falls_back_to_default_bucket_when_not_configured() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    let inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 500,
        rate: 5,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                RateLimitConfig::disabled(),
                inbound,
            )],
        ),
        &Vec::new(&env),
    );
    // No FTF buckets configured — FTF requests should fall back to the default inbound bucket.

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 500,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    stub_client.release(&pool_client.address, &release_input, &WAIT_FOR_SAFE);
    assert_eq!(token_client.balance(&receiver), 500);

    // Default bucket exhausted; another FTF request should fail
    env.ledger().with_mut(|li| li.timestamp = 101);
    let release_input2 = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 6,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    let r = stub_client.try_release(&pool_client.address, &release_input2, &WAIT_FOR_SAFE);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);
}

#[test]
fn test_ftf_outbound_uses_ftf_bucket_when_configured() {
    let (
        env,
        pool_client,
        owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    // Configure allowed finality to permit WAIT_FOR_SAFE
    pool_client.set_allowed_finality_config(&WAIT_FOR_SAFE);

    // Configure FTF outbound bucket
    let ftf_outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 300,
        rate: 3,
    };
    pool_client.set_rate_limit_config(
        &owner,
        &remote_chain,
        &ftf_outbound,
        &RateLimitConfig::disabled(),
        &true,
    );

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &5000);

    // FTF outbound: 300 should succeed (exactly at FTF capacity)
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 300,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input, &WAIT_FOR_SAFE, &Bytes::new(&env));

    // FTF outbound: 1 more should fail (FTF bucket exhausted)
    env.ledger().with_mut(|li| li.timestamp = 101);
    let lock_input2 = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 4,
        local_token: token_address.clone(),
    };
    let r = pool_client.try_lock_or_burn(
        &auth_onramp,
        &lock_input2,
        &WAIT_FOR_SAFE,
        &Bytes::new(&env),
    );
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::TokenRateLimitReached);

    // Default outbound should still be unaffected (disabled = no limit)
    let lock_default = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 1000,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_default, &0u32, &Bytes::new(&env));
}

#[test]
fn test_ftf_outbound_rejected_when_finality_not_allowed() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    // allowed finality is default (0) — WAIT_FOR_SAFE is not allowed

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 100,
        local_token: token_address,
    };
    let r =
        pool_client.try_lock_or_burn(&auth_onramp, &lock_input, &WAIT_FOR_SAFE, &Bytes::new(&env));
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::InvalidRequestedFinality);
}

// ----------------------------------------------------------------
// Source-side pool finality minimum — block-depth mode (burn-mint).
//
// Mirrors the lock-release tests: the token issuer (pool owner) sets
// `allowed_finality_config` to a block depth, the minimum source-chain
// finality for outbound transfers on every lane from this source. A user
// requesting FASTER finality (fewer confirmations) reverts; a SLOWER one is
// admitted with the user's value honored. EVM `FinalityCodec` parity.
// ----------------------------------------------------------------

#[test]
fn test_outbound_block_depth_faster_than_minimum_reverts() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    // Issuer sets a minimum of 10 source-chain confirmations for all lanes from
    // this source (pool-wide `allowed_finality_config`, EVM parity).
    pool_client.set_allowed_finality_config(&10u32);

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1000);

    // User requests only 5 confirmations — faster than the 10-confirmation
    // minimum ⇒ source revert with InvalidRequestedFinality (#315).
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 100,
        local_token: token_address,
    };
    let r = pool_client.try_lock_or_burn(&auth_onramp, &lock_input, &5u32, &Bytes::new(&env));
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::InvalidRequestedFinality);
}

#[test]
fn test_outbound_block_depth_slower_than_minimum_admitted() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );
    // Issuer sets a minimum of 10 source-chain confirmations.
    pool_client.set_allowed_finality_config(&10u32);

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1000);

    // User requests 20 confirmations — slower than the 10-confirmation minimum
    // ⇒ admitted; the user's (slower) value is honored, no revert.
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 100,
        local_token: token_address,
    };
    let out = pool_client.lock_or_burn(&auth_onramp, &lock_input, &20u32, &Bytes::new(&env));
    // No fee config ⇒ fee 0 ⇒ full amount is burned for the wire.
    assert_eq!(out.dest_token_amount, 100);
}

#[test]
fn test_set_and_get_allowed_finality_config_round_trip() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    // Default before any set is WAIT_FOR_FINALITY (0).
    assert_eq!(pool_client.get_allowed_finality_config(), 0u32);

    // Set + read back a flag-mode config.
    pool_client.set_allowed_finality_config(&WAIT_FOR_SAFE);
    assert_eq!(pool_client.get_allowed_finality_config(), WAIT_FOR_SAFE);

    // Set + read back a block-depth-mode config.
    pool_client.set_allowed_finality_config(&10u32);
    assert_eq!(pool_client.get_allowed_finality_config(), 10u32);

    // Re-setting overwrites the prior value (modify path).
    pool_client.set_allowed_finality_config(&42u32);
    assert_eq!(pool_client.get_allowed_finality_config(), 42u32);
    let _ = &env; // keep env alive
}

#[test]
fn test_set_allowed_finality_config_rejects_without_owner_auth() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    // With no auths mocked, the owner-gated setter must reject a non-owner
    // caller (Ownable::require_owner panics, surfaced as an Err by try_).
    env.mock_auths(&[]);
    let r = pool_client.try_set_allowed_finality_config(&WAIT_FOR_SAFE);
    assert!(r.is_err());
    // The value must NOT have been persisted.
    assert_eq!(pool_client.get_allowed_finality_config(), 0u32);
}

#[test]
fn test_ftf_and_default_buckets_are_independent() {
    let (
        env,
        pool_client,
        owner,
        token_address,
        token_client,
        _token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    // Set up default inbound bucket
    let default_inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 1000,
        rate: 10,
    };
    pool_client.apply_chain_updates(
        &Vec::from_array(
            &env,
            [chain_update_with_limits(
                &env,
                remote_chain,
                1,
                2,
                RateLimitConfig::disabled(),
                default_inbound,
            )],
        ),
        &Vec::new(&env),
    );

    // Set up FTF inbound bucket with different capacity
    let ftf_inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 300,
        rate: 3,
    };
    pool_client.set_rate_limit_config(
        &owner,
        &remote_chain,
        &RateLimitConfig::disabled(),
        &ftf_inbound,
        &true,
    );

    let receiver = Address::generate(&env);

    // Exhaust the FTF bucket
    let release_ftf = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 300,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    stub_client.release(&pool_client.address, &release_ftf, &WAIT_FOR_SAFE);
    assert_eq!(token_client.balance(&receiver), 300);

    // Default bucket should still have its full 1000 capacity
    let release_default = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 1000,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };
    stub_client.release(&pool_client.address, &release_default, &0u32);
    assert_eq!(token_client.balance(&receiver), 1300);
}

// ================================================================
// Advanced pool hooks (EVM `IAdvancedPoolHooks` parity)
// ================================================================

#[test]
fn test_advanced_pool_hooks_admin_roundtrip() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    assert!(pool_client.get_advanced_pool_hooks().is_none());

    let hooks_id = env.register(mock_hooks::MockPreflightRejects, ());
    let hooks = hooks_id.clone();
    pool_client.set_advanced_pool_hooks(&hooks);
    assert_eq!(pool_client.get_advanced_pool_hooks().unwrap(), hooks);

    pool_client.remove_advanced_pool_hooks();
    assert!(pool_client.get_advanced_pool_hooks().is_none());
}

#[test]
fn test_set_advanced_pool_hooks_rejects_without_owner_auth() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    let hooks_id = env.register(mock_hooks::MockPreflightRejects, ());
    let hooks = hooks_id.clone();
    env.mock_auths(&[]);
    let r = pool_client.try_set_advanced_pool_hooks(&hooks);
    assert!(r.is_err());
}

#[test]
fn test_preflight_hook_rejects_lock_or_burn() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let hooks_id = env.register(mock_hooks::MockPreflightRejects, ());
    pool_client.set_advanced_pool_hooks(&hooks_id.clone());

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1_000_000_000);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 1_000_000_000,
        local_token: token_address.clone(),
    };

    let r = pool_client.try_lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::SenderNotAllowed);
    assert_eq!(token_client.balance(&sender), 1_000_000_000);
}

/// The sender-supplied `token_args` (threaded from the CCIP message's
/// `extra_args.token_args` → OnRamp → `lock_or_burn`) must reach the
/// advanced-pool-hooks `preflight_check` byte-for-byte (EVM
/// `IAdvancedPoolHooks.preflightCheck` parity).
#[test]
fn test_preflight_hook_receives_token_args() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let hooks_id = env.register(mock_hooks::MockCapturesTokenArgs, ());
    let hooks_client = mock_hooks::MockCapturesTokenArgsClient::new(&env, &hooks_id);
    pool_client.set_advanced_pool_hooks(&hooks_id.clone());

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1_000_000_000);

    let token_args = Bytes::from_array(&env, &[0xde, 0xad, 0xbe, 0xef]);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 1_000_000_000,
        local_token: token_address.clone(),
    };

    // Empty before the call proves the hook actually ran and captured.
    assert_eq!(hooks_client.get_captured_token_args(), Bytes::new(&env));

    pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &token_args);

    // The sender-supplied `token_args` reached the hooks preflight unchanged.
    assert_eq!(hooks_client.get_captured_token_args(), token_args);
}

#[test]
fn test_postflight_hook_rejects_release_or_mint() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        token_admin_client,
        registry_client,
        stub_client,
        auth_onramp,
    ) = setup_env();

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let hooks_id = env.register(mock_hooks::MockPostflightRejects, ());
    pool_client.set_advanced_pool_hooks(&hooks_id.clone());

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1_000_000_000);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: 1_000_000_000,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(token_client.balance(&sender), 0);

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 1_000_000_000,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };

    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    let r = stub_client.try_release(&pool_client.address, &release_input, &0u32);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::SenderNotAllowed);
    assert_eq!(token_client.balance(&receiver), 0);
}

#[test]
fn test_get_required_ccvs_empty_without_hooks() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();
    let v = pool_client.get_required_ccvs(
        &token_address,
        &5009297550715157269u64,
        &100i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Outbound,
    );
    assert_eq!(v.ccvs.len(), 0);
    assert!(
        v.include_defaults,
        "pools without hooks should fall back to lane defaults"
    );
}

#[test]
fn test_get_required_ccvs_delegates_to_hooks() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    let hooks_id = env.register(mock_hooks::MockReturnsCcv, ());
    let hooks_client = mock_hooks::MockReturnsCcvClient::new(&env, &hooks_id);
    let expected_ccv = Address::generate(&env);
    hooks_client.set_returned_ccv(&expected_ccv);

    pool_client.set_advanced_pool_hooks(&hooks_id.clone());

    let v = pool_client.get_required_ccvs(
        &token_address,
        &5009297550715157269u64,
        &100i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Inbound,
    );
    assert_eq!(v.ccvs.len(), 1);
    assert_eq!(v.ccvs.get(0).unwrap(), expected_ccv);
    assert!(!v.include_defaults);
}

#[test]
fn test_get_required_ccvs_real_advanced_pool_hooks() {
    // Closes CCV-7: a real AdvancedPoolHooks contract (not a mock) stores an
    // issuer-configured per-chain CCV choice, and the pool's get_required_ccvs
    // delegates to it end-to-end.
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    let hooks_id = env.register(AdvancedPoolHooksContract, ());
    let hooks_client = AdvancedPoolHooksContractClient::new(&env, &hooks_id);
    let hooks_owner = Address::generate(&env);
    hooks_client.initialize(&hooks_owner, &Vec::new(&env), &0i128);

    pool_client.set_advanced_pool_hooks(&hooks_id);

    let ccv_a = Address::generate(&env);
    let ccv_b = Address::generate(&env);
    let config = CCVConfigArg {
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        outbound_ccvs: vec![&env, ccv_a.clone(), ccv_b.clone()],
        threshold_outbound_ccvs: Vec::new(&env),
        inbound_ccvs: Vec::new(&env),
        threshold_inbound_ccvs: Vec::new(&env),
        outbound_include_defaults: false,
        inbound_include_defaults: true,
    };
    hooks_client.apply_ccv_config_updates(&vec![&env, config]);

    let v = pool_client.get_required_ccvs(
        &token_address,
        &DEFAULT_REMOTE_CHAIN,
        &100i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Outbound,
    );
    assert_eq!(v.ccvs.len(), 2);
    assert_eq!(v.ccvs.get(0).unwrap(), ccv_a);
    assert_eq!(v.ccvs.get(1).unwrap(), ccv_b);
    assert!(
        !v.include_defaults,
        "issuer set include_defaults=false; pool must relay it"
    );
}

#[test]
fn test_get_required_ccvs_threshold_through_pool() {
    // Integration: threshold-amount CCV resolution is exercised through the
    // real pool->hooks delegation, not just on the hooks in isolation.
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    let hooks_id = env.register(AdvancedPoolHooksContract, ());
    let hooks_client = AdvancedPoolHooksContractClient::new(&env, &hooks_id);
    let hooks_owner = Address::generate(&env);
    // threshold_amount = 1_000 configured up front.
    hooks_client.initialize(&hooks_owner, &Vec::new(&env), &1_000i128);
    pool_client.set_advanced_pool_hooks(&hooks_id);

    let base = Address::generate(&env);
    let extra = Address::generate(&env);
    let config = CCVConfigArg {
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        outbound_ccvs: vec![&env, base.clone()],
        threshold_outbound_ccvs: vec![&env, extra.clone()],
        inbound_ccvs: Vec::new(&env),
        threshold_inbound_ccvs: Vec::new(&env),
        outbound_include_defaults: false,
        inbound_include_defaults: true,
    };
    hooks_client.apply_ccv_config_updates(&vec![&env, config]);

    // Below threshold -> base only, through the pool.
    let below = pool_client.get_required_ccvs(
        &token_address,
        &DEFAULT_REMOTE_CHAIN,
        &500i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Outbound,
    );
    assert_eq!(below.ccvs.len(), 1);
    assert_eq!(below.ccvs.get(0).unwrap(), base);

    // At threshold -> base + extra, through the pool.
    let above = pool_client.get_required_ccvs(
        &token_address,
        &DEFAULT_REMOTE_CHAIN,
        &1_000i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Outbound,
    );
    assert_eq!(above.ccvs.len(), 2);
    assert_eq!(above.ccvs.get(0).unwrap(), base);
    assert_eq!(above.ccvs.get(1).unwrap(), extra);
}

#[test]
fn test_get_required_ccvs_inbound_through_pool() {
    // Integration: the inbound-direction CCV list resolves through the real
    // pool->hooks delegation (Outbound would return the empty outbound list).
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    let hooks_id = env.register(AdvancedPoolHooksContract, ());
    let hooks_client = AdvancedPoolHooksContractClient::new(&env, &hooks_id);
    let hooks_owner = Address::generate(&env);
    hooks_client.initialize(&hooks_owner, &Vec::new(&env), &0i128);
    pool_client.set_advanced_pool_hooks(&hooks_id);

    let inc = Address::generate(&env);
    let config = CCVConfigArg {
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        outbound_ccvs: Vec::new(&env),
        threshold_outbound_ccvs: Vec::new(&env),
        inbound_ccvs: vec![&env, inc.clone()],
        threshold_inbound_ccvs: Vec::new(&env),
        outbound_include_defaults: true,
        inbound_include_defaults: false,
    };
    hooks_client.apply_ccv_config_updates(&vec![&env, config]);

    let v = pool_client.get_required_ccvs(
        &token_address,
        &DEFAULT_REMOTE_CHAIN,
        &100i128,
        &0u32,
        &Bytes::new(&env),
        &MessageDirection::Inbound,
    );
    assert_eq!(v.ccvs.len(), 1);
    assert_eq!(v.ccvs.get(0).unwrap(), inc);
    assert!(
        !v.include_defaults,
        "issuer set inbound include_defaults=false; pool must relay it"
    );
}

#[test]
fn test_lock_or_burn_gated_by_real_hooks_allowlist() {
    // Integration (end-to-end behavior): a real AdvancedPoolHooks sender
    // allowlist wired to the pool gates `lock_or_burn`. The hooks
    // `preflight_check` is reached inside `lock_or_burn` via
    // `BaseTokenPool::preflight_check`; a non-allowlisted `original_sender`
    // aborts the host invocation, while an allowlisted one burns.
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );

    // Wire real hooks with a one-sender allowlist.
    let allowed = Address::generate(&env);
    let hooks_id = env.register(AdvancedPoolHooksContract, ());
    let hooks_client = AdvancedPoolHooksContractClient::new(&env, &hooks_id);
    let hooks_owner = Address::generate(&env);
    hooks_client.initialize(&hooks_owner, &vec![&env, allowed.clone()], &0i128);
    pool_client.set_advanced_pool_hooks(&hooks_id);

    let amount: i128 = 1_000_000_000;
    let sac_client = token::StellarAssetClient::new(&env, &token_address);
    sac_client.mint(&allowed, &amount);
    let stranger = Address::generate(&env);
    sac_client.mint(&stranger, &amount);

    // Non-allowlisted original_sender -> preflight aborts -> try_ returns Err.
    let lock_stranger = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        original_sender: stranger.clone(),
        amount,
        local_token: token_address.clone(),
    };
    let r = pool_client.try_lock_or_burn(&auth_onramp, &lock_stranger, &0u32, &Bytes::new(&env));
    assert!(r.is_err(), "non-allowlisted sender must be rejected");
    // The stranger was NOT burned.
    assert_eq!(token_client.balance(&stranger), amount);

    // Allowlisted original_sender -> succeeds; balance burned (fee=0).
    let lock_allowed = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        original_sender: allowed.clone(),
        amount,
        local_token: token_address.clone(),
    };
    let out = pool_client.lock_or_burn(&auth_onramp, &lock_allowed, &0u32, &Bytes::new(&env));
    assert_eq!(out.dest_token_amount, amount);
    assert_eq!(token_client.balance(&allowed), 0);
}

// ================================================================
// Source-side bps fee (H-13, EVM `TokenPool._getFee` / `applyFee` parity)
//
// `fee = amount * bps / BPS_DIVIDER` is deducted inside `lock_or_burn`:
// only `dest_token_amount = amount - fee` is burned / crosses the wire;
// `fee` accrues on the pool balance for `withdraw_fee_tokens` to sweep
// (EVM `TokenPool.lockOrBurn` L288-311). `get_fee` is the view that
// resolves the finality-paired bps (EVM `getFee`); it does NOT return the
// computed fee amount — the amount is realized on-chain by `lock_or_burn`.
// These mirror EVM `TokenPool.applyFee.t.sol`
// (`test_applyFee_CustomFinality`, `test_applyFee_DefaultFinality`,
// `test_applyFee_RoundsFeeDownToZeroOnDustAmounts`) and
// `LockReleaseTokenPool.lockOrBurn.t.sol` (1000e18 @ 100 bps → 10e18 fee).
// ================================================================

const E18: i128 = 1_000_000_000_000_000_000;

/// Apply a `TokenTransferFeeConfig` for `chain` (owner-gated; auth mocked).
fn apply_fee_config(
    env: &Env,
    pool_client: &BurnMintTokenPoolContractClient<'_>,
    chain: u64,
    finality_bps: u32,
    fast_bps: u32,
) {
    // `dest_gas_overhead` must be non-zero — the pool's config validation
    // rejects a zero gas overhead (#321 InvalidTokenTransferFeeConfig).
    let config = TokenTransferFeeConfig {
        dest_gas_overhead: 100,
        dest_bytes_overhead: 32,
        finality_fee_usd_cents: 0,
        fast_finality_fee_usd_cents: 0,
        finality_transfer_fee_bps: finality_bps,
        fast_finality_transfer_fee_bps: fast_bps,
        is_enabled: true,
    };
    let adds = Vec::from_array(
        env,
        [TokenTransferFeeConfigArgs {
            dest_chain_selector: chain,
            config,
        }],
    );
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(env));
}

#[test]
fn test_get_fee_default_finality_nonzero_bps() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    apply_fee_config(&env, &pool_client, DEFAULT_REMOTE_CHAIN, 250, 0);

    // Default finality (0 == WAIT_FOR_FINALITY) resolves finality_transfer_fee_bps.
    let result = pool_client.get_fee(
        &DEFAULT_REMOTE_CHAIN,
        &(1_000 * E18),
        &0u32,
        &Bytes::new(&env),
    );
    assert_eq!(result.token_fee_bps, 250);
    assert!(result.is_enabled);
    assert_eq!(result.fee_usd_cents, 0);
}

#[test]
fn test_get_fee_fast_finality_selects_fast_bps() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    // Permit WAIT_FOR_SAFE so the view's `ensure_requested_finality_allowed` passes.
    pool_client.set_allowed_finality_config(&WAIT_FOR_SAFE);
    // Distinct USD-cent AND bps values per finality mode, so both fields'
    // selection can be asserted (the `apply_fee_config` helper sets USD cents
    // to 0, so build the config inline via `fee_config_args`).
    let adds = Vec::from_array(
        &env,
        [fee_config_args(&env, DEFAULT_REMOTE_CHAIN, |c| {
            c.finality_transfer_fee_bps = 100;
            c.fast_finality_transfer_fee_bps = 500;
            c.finality_fee_usd_cents = 40;
            c.fast_finality_fee_usd_cents = 80;
        })],
    );
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(&env));

    // Fast finality → fast_finality_* fields (mirrors test_applyFee_CustomFinality).
    let fast = pool_client.get_fee(
        &DEFAULT_REMOTE_CHAIN,
        &(1_000 * E18),
        &WAIT_FOR_SAFE,
        &Bytes::new(&env),
    );
    assert_eq!(fast.token_fee_bps, 500);
    assert_eq!(fast.fee_usd_cents, 80);
    assert!(fast.is_enabled);

    // Default finality → finality_* fields (mirrors test_applyFee_DefaultFinality).
    let default = pool_client.get_fee(
        &DEFAULT_REMOTE_CHAIN,
        &(1_000 * E18),
        &0u32,
        &Bytes::new(&env),
    );
    assert_eq!(default.token_fee_bps, 100);
    assert_eq!(default.fee_usd_cents, 40);
    assert!(default.is_enabled);
}

#[test]
fn test_lock_or_burn_deducts_bps_fee_and_accrues() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    apply_fee_config(&env, &pool_client, DEFAULT_REMOTE_CHAIN, 100, 0);

    let sender = Address::generate(&env);
    let amount: i128 = 1_000 * E18; // 1000e18 (EVM parity)
    token_admin_client.mint(&sender, &amount);
    assert_eq!(token_client.balance(&sender), amount);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount,
        local_token: token_address.clone(),
    };

    let out = pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));

    // fee = 1000e18 * 100 / 10000 = 10e18; dest = 990e18.
    let fee: i128 = 10 * E18;
    let dest: i128 = 990 * E18;
    assert_eq!(out.dest_token_amount, dest);
    let pool_address = pool_client.address.clone();
    // dest is burned; fee accrues on the pool balance; sender is fully debited.
    assert_eq!(token_client.balance(&pool_address), fee);
    assert_eq!(token_client.balance(&sender), 0);
}

/// TPF-1 (execution path): an FTF (`WAIT_FOR_SAFE`) `lock_or_burn` is charged the
/// `fast_finality_transfer_fee_bps` tier, while a default-finality `lock_or_burn` of
/// the SAME amount is charged the `finality_transfer_fee_bps` tier — so the resulting
/// `dest_token_amount` DIFFERS by requested finality. The `get_fee` view-layer
/// selection (`test_get_fee_fast_finality_selects_fast_bps`) already proves the rate
/// is resolved per finality; this proves the `lock_or_burn` → `_get_fee` execution
/// path actually deducts the selected tier and yields a different destination amount.
/// Distinct bps: finality = 100 (1%), fast_finality = 500 (5%).
#[test]
fn test_lock_or_burn_ftf_fee_differs_from_finality_fee() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    // Permit WAIT_FOR_SAFE for the FTF burn. Default finality (0) is always allowed
    // (`ensure_requested_finality_allowed` short-circuits on WAIT_FOR_FINALITY_FLAG),
    // so the default-finality burn below is unaffected by this setting.
    pool_client.set_allowed_finality_config(&WAIT_FOR_SAFE);
    // Distinct bps per finality tier; USD-cent fees left at 0 so only the bps slice
    // differentiates the two burns: finality = 100 bps, fast_finality = 500 bps.
    apply_fee_config(&env, &pool_client, DEFAULT_REMOTE_CHAIN, 100, 500);

    let amount: i128 = 1_000 * E18;

    // --- Default finality: fee = 1000e18 * 100 / 10000 = 10e18; dest = 990e18. ---
    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &amount);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount,
        local_token: token_address.clone(),
    };
    let out_default = pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(
        out_default.dest_token_amount,
        990 * E18,
        "default-finality lock_or_burn must deduct the finality_transfer_fee_bps (100) tier"
    );

    // --- FTF (WAIT_FOR_SAFE): fee = 1000e18 * 500 / 10000 = 50e18; dest = 950e18. ---
    let sender_ftf = Address::generate(&env);
    token_admin_client.mint(&sender_ftf, &amount);
    let lock_input_ftf = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        original_sender: sender_ftf.clone(),
        amount,
        local_token: token_address.clone(),
    };
    let out_ftf = pool_client.lock_or_burn(
        &auth_onramp,
        &lock_input_ftf,
        &WAIT_FOR_SAFE,
        &Bytes::new(&env),
    );
    assert_eq!(
        out_ftf.dest_token_amount,
        950 * E18,
        "FTF lock_or_burn must deduct the fast_finality_transfer_fee_bps (500) tier"
    );

    // The load-bearing assertion: the destination amount differs by finality.
    assert_ne!(
        out_default.dest_token_amount, out_ftf.dest_token_amount,
        "FTF and default-finality sends of the same amount must produce DIFFERENT dest \
         amounts — proves the pool differentiates fees for faster-than-finality transfers"
    );

    // Fee accrual: default 10e18 + FTF 50e18 = 60e18 on the pool; both senders fully debited.
    let pool_address = pool_client.address.clone();
    assert_eq!(token_client.balance(&pool_address), 60 * E18);
    assert_eq!(token_client.balance(&sender), 0);
    assert_eq!(token_client.balance(&sender_ftf), 0);
}

#[test]
fn test_lock_or_burn_dust_amount_rounds_fee_to_zero() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    apply_fee_config(&env, &pool_client, DEFAULT_REMOTE_CHAIN, 250, 0);

    let sender = Address::generate(&env);
    let amount: i128 = 39; // 39 * 250 / 10000 = 0 (floor) — mirrors test_applyFee_RoundsFeeDownToZeroOnDustAmounts
    token_admin_client.mint(&sender, &amount);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount,
        local_token: token_address.clone(),
    };

    let out = pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(out.dest_token_amount, 39);
    let pool_address = pool_client.address.clone();
    // No fee accrues; the full dust amount is burned.
    assert_eq!(token_client.balance(&pool_address), 0);
    assert_eq!(token_client.balance(&sender), 0);
}

#[test]
fn test_get_fee_disabled_config_returns_zero() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    // No fee config added → `get_token_transfer_fee_config` returns disabled.
    let result = pool_client.get_fee(
        &DEFAULT_REMOTE_CHAIN,
        &(1_000 * E18),
        &0u32,
        &Bytes::new(&env),
    );
    assert_eq!(result.token_fee_bps, 0);
    assert!(!result.is_enabled);
}

#[test]
fn test_lock_or_burn_no_config_burns_full_amount() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    // No fee config → fee 0, full amount burned.
    let sender = Address::generate(&env);
    let amount: i128 = 500 * E18;
    token_admin_client.mint(&sender, &amount);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount,
        local_token: token_address.clone(),
    };

    let out = pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(out.dest_token_amount, amount);
    let pool_address = pool_client.address.clone();
    assert_eq!(token_client.balance(&pool_address), 0);
    assert_eq!(token_client.balance(&sender), 0);
}

#[test]
fn test_withdraw_fee_tokens_sweeps_accrued() {
    let (
        env,
        pool_client,
        owner,
        token_address,
        token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    apply_fee_config(&env, &pool_client, DEFAULT_REMOTE_CHAIN, 100, 0);

    let sender = Address::generate(&env);
    let amount: i128 = 1_000 * E18;
    token_admin_client.mint(&sender, &amount);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));

    let fee: i128 = 10 * E18;
    let pool_address = pool_client.address.clone();
    assert_eq!(token_client.balance(&pool_address), fee);

    // Owner sweeps the accrued fee to a recipient.
    let recipient = Address::generate(&env);
    pool_client.withdraw_fee_tokens(
        &owner,
        &Vec::from_array(&env, [token_address.clone()]),
        &recipient,
    );
    assert_eq!(token_client.balance(&recipient), fee);
    assert_eq!(token_client.balance(&pool_address), 0);

    let _ = owner; // owner-gated call; auth mocked in setup_env
}

/// H-13 / auth-fix: the fee admin (a non-owner party) must be able to call
/// `withdraw_fee_tokens`. Under the old trap-OR gate (`require_owner(env).is_ok()`),
/// `owner.require_auth()` trapped for any non-owner caller before the fee-admin
/// fallback was reachable, so the fee admin could never withdraw. The fix checks
/// identity first (`is_owner` OR fee-admin equality) then calls `require_auth`
/// once on the confirmed party. This test authorizes ONLY the fee admin (precise
/// auth via `mock_auths`, which switches the env off `mock_all_auths`) so that
/// `owner.require_auth()` would trap under the old code — the call succeeding
/// proves the fee-admin path is now reachable (EVM `onlyOwnerOrFeeAdmin` parity).
#[test]
fn test_withdraw_fee_tokens_fee_admin_can_withdraw_precise_auth() {
    let (
        env,
        pool_client,
        owner,
        token_address,
        token_client,
        token_admin_client,
        _registry_client,
        _stub_client,
        auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    apply_fee_config(&env, &pool_client, DEFAULT_REMOTE_CHAIN, 100, 0);

    // Configure a dedicated fee admin distinct from the owner (owner-gated;
    // done while mock_all_auths is still active from setup_env).
    let fee_admin = Address::generate(&env);
    pool_client.set_fee_admin(&fee_admin);
    assert_eq!(pool_client.get_fee_admin().unwrap(), fee_admin);

    // Accrue a fee on the pool.
    let sender = Address::generate(&env);
    let amount: i128 = 1_000 * E18;
    token_admin_client.mint(&sender, &amount);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: DEFAULT_REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    let fee: i128 = 10 * E18;
    let pool_address = pool_client.address.clone();
    assert_eq!(token_client.balance(&pool_address), fee);

    let recipient = Address::generate(&env);
    let fee_tokens = Vec::from_array(&env, [token_address.clone()]);

    // Authorize ONLY the fee admin for this exact invocation. `mock_auths`
    // switches the env to precise-auth mode (displacing `mock_all_auths`), so
    // the owner is NOT authorized — under the old trap-OR gate
    // `owner.require_auth()` would trap here. With the fix, `is_owner(fee_admin)`
    // is false but `is_fee_admin` is true, so `fee_admin.require_auth()` is the
    // single auth call and it succeeds.
    let args: Vec<Val> = soroban_sdk::vec![
        &env,
        fee_admin.clone().into_val(&env),
        fee_tokens.clone().into_val(&env),
        recipient.clone().into_val(&env),
    ];
    let invoke = MockAuthInvoke {
        contract: &pool_client.address,
        fn_name: "withdraw_fee_tokens",
        args,
        sub_invokes: &[],
    };
    env.mock_auths(&[MockAuth {
        address: &fee_admin,
        invoke: &invoke,
    }]);

    pool_client.withdraw_fee_tokens(&fee_admin, &fee_tokens, &recipient);
    assert_eq!(token_client.balance(&recipient), fee);
    assert_eq!(token_client.balance(&pool_address), 0);

    let _ = owner;
}

/// A caller that is neither the owner nor the fee admin must be rejected with
/// `Unauthorized` (a typed error, not a host auth trap): the identity check runs
/// before `require_auth`, so an unrelated caller never reaches an auth call.
/// Auth stays mocked from `setup_env`; the rejection is identity-bound, not
/// auth-bound.
#[test]
fn test_withdraw_fee_tokens_rejects_unauthorized_caller() {
    let (
        env,
        pool_client,
        _owner,
        token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );

    let stranger = Address::generate(&env);
    let recipient = Address::generate(&env);
    let r = pool_client.try_withdraw_fee_tokens(
        &stranger,
        &Vec::from_array(&env, [token_address.clone()]),
        &recipient,
    );
    assert!(r.is_err(), "unauthorized caller must be rejected");
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::Unauthorized);
}

/// H-13 / auth-fix counterpart for the rate-limit gate: the rate-limit admin (a
/// non-owner party) must be able to call `set_rate_limit_config`. Same trap-OR
/// bug existed in `require_owner_or_rate_limit_admin`. Precise auth authorizes
/// ONLY the rate-limit admin so `owner.require_auth()` would trap under the old
/// code; the config update succeeding proves the admin path is now reachable
/// (EVM `onlyOwnerOrRateLimitAdmin` parity).
#[test]
fn test_set_rate_limit_config_rate_limit_admin_can_set_precise_auth() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    // Configure a dedicated rate-limit admin (owner-gated; while mock_all_auths
    // is still active from setup_env).
    let admin = Address::generate(&env);
    pool_client.set_rate_limit_admin(&admin);
    assert_eq!(pool_client.get_rate_limit_admin().unwrap(), admin);

    let cfg = RateLimitConfig {
        is_enabled: true,
        capacity: 100,
        rate: 1,
    };

    // Authorize ONLY the rate-limit admin for this exact invocation.
    let args: Vec<Val> = soroban_sdk::vec![
        &env,
        admin.clone().into_val(&env),
        remote_chain.into_val(&env),
        cfg.clone().into_val(&env),
        RateLimitConfig::disabled().into_val(&env),
        false.into_val(&env),
    ];
    let invoke = MockAuthInvoke {
        contract: &pool_client.address,
        fn_name: "set_rate_limit_config",
        args,
        sub_invokes: &[],
    };
    env.mock_auths(&[MockAuth {
        address: &admin,
        invoke: &invoke,
    }]);

    pool_client.set_rate_limit_config(
        &admin,
        &remote_chain,
        &cfg,
        &RateLimitConfig::disabled(),
        &false,
    );

    let state = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(state.outbound.is_enabled);
    assert_eq!(state.outbound.capacity, 100);
}

// ================================================================
// H-13 config-validation & read-path coverage.
// `apply_token_fee_config_updates` rejects `is_enabled == false`,
// `*_transfer_fee_bps >= BPS_DIVIDER` (10000), and
// `dest_gas_overhead == 0` (common/pool/src/lib.rs:283-295); the
// dedicated `get_token_transfer_fee_config` read entrypoint had no
// direct test. These close those gaps (EVM `TokenPool.applyFee` /
// `getTokenTransferFeeConfig` parity).
// ================================================================

/// Build a `TokenTransferFeeConfigArgs` for `chain` from an override closure,
/// starting from a valid baseline (mirrors `apply_fee_config`'s config).
fn fee_config_args(
    env: &Env,
    chain: u64,
    override_cfg: impl FnOnce(&mut TokenTransferFeeConfig),
) -> TokenTransferFeeConfigArgs {
    let mut config = TokenTransferFeeConfig {
        dest_gas_overhead: 100,
        dest_bytes_overhead: 32,
        finality_fee_usd_cents: 0,
        fast_finality_fee_usd_cents: 0,
        finality_transfer_fee_bps: 250,
        fast_finality_transfer_fee_bps: 500,
        is_enabled: true,
    };
    override_cfg(&mut config);
    TokenTransferFeeConfigArgs {
        dest_chain_selector: chain,
        config,
    }
}

#[test]
#[should_panic(expected = "Error(Contract, #321)")] // InvalidTokenTransferFeeConfig
fn test_apply_token_fee_config_rejects_disabled() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    // Adds must be enabled — use the disable list to turn a config off.
    let adds = Vec::from_array(
        &env,
        [fee_config_args(&env, DEFAULT_REMOTE_CHAIN, |c| {
            c.is_enabled = false;
        })],
    );
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(&env));
}

#[test]
#[should_panic(expected = "Error(Contract, #322)")] // InvalidTransferFeeBps
fn test_apply_token_fee_config_rejects_finality_bps_at_divider() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    // bps >= BPS_DIVIDER (10000) is rejected.
    let adds = Vec::from_array(
        &env,
        [fee_config_args(&env, DEFAULT_REMOTE_CHAIN, |c| {
            c.finality_transfer_fee_bps = 10_000;
        })],
    );
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(&env));
}

#[test]
#[should_panic(expected = "Error(Contract, #322)")] // InvalidTransferFeeBps
fn test_apply_token_fee_config_rejects_fast_bps_above_divider() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    let adds = Vec::from_array(
        &env,
        [fee_config_args(&env, DEFAULT_REMOTE_CHAIN, |c| {
            c.fast_finality_transfer_fee_bps = 10_001;
        })],
    );
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(&env));
}

#[test]
#[should_panic(expected = "Error(Contract, #321)")] // InvalidTokenTransferFeeConfig
fn test_apply_token_fee_config_rejects_zero_dest_gas_overhead() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );
    let adds = Vec::from_array(
        &env,
        [fee_config_args(&env, DEFAULT_REMOTE_CHAIN, |c| {
            c.dest_gas_overhead = 0;
        })],
    );
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(&env));
}

#[test]
fn test_get_token_transfer_fee_config_roundtrip() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, DEFAULT_REMOTE_CHAIN, 1, 2)]),
        &Vec::new(&env),
    );

    // With no config stored, the entrypoint returns the disabled default.
    let none = pool_client.get_token_transfer_fee_config(&DEFAULT_REMOTE_CHAIN);
    assert_eq!(none, TokenTransferFeeConfig::disabled());

    // Set a known config and read it back via the dedicated entrypoint — every
    // field must round-trip (this is the path `get_fee` consults internally).
    let adds = Vec::from_array(
        &env,
        [fee_config_args(&env, DEFAULT_REMOTE_CHAIN, |c| {
            c.dest_gas_overhead = 777;
            c.dest_bytes_overhead = 64;
            c.finality_fee_usd_cents = 150;
            c.fast_finality_fee_usd_cents = 300;
            c.finality_transfer_fee_bps = 100;
            c.fast_finality_transfer_fee_bps = 200;
        })],
    );
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(&env));

    let cfg = pool_client.get_token_transfer_fee_config(&DEFAULT_REMOTE_CHAIN);
    assert_eq!(cfg.dest_gas_overhead, 777);
    assert_eq!(cfg.dest_bytes_overhead, 64);
    assert_eq!(cfg.finality_fee_usd_cents, 150);
    assert_eq!(cfg.fast_finality_fee_usd_cents, 300);
    assert_eq!(cfg.finality_transfer_fee_bps, 100);
    assert_eq!(cfg.fast_finality_transfer_fee_bps, 200);
    assert!(cfg.is_enabled);
}

#[test]
#[should_panic(expected = "Error(Contract, #302)")] // ChainNotSupported
fn test_set_pool_fee_unsupported_chain_rejected() {
    let (
        env,
        pool_client,
        _owner,
        _token_address,
        _token_client,
        _token_admin_client,
        _registry_client,
        _stub_client,
        _auth_onramp,
    ) = setup_env();

    // 99999 is not a configured chain, so the chain-support check (which runs
    // before config validation) yields ChainNotSupported (#302). Mirrors
    // lock-release test_set_pool_fee_unsupported_chain_rejected and siloed
    // set_pool_fee_unsupported_chain_rejected — closes the burn-mint parity gap.
    let unsupported_chain: u64 = 99999;
    let adds = Vec::from_array(&env, [fee_config_args(&env, unsupported_chain, |_| {})]);
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(&env));
}

// ================================================================
// Claim 1: FTF rate-limit bucket auth gating.
// The FTF (fast-transfer-finality) rate-limit bucket is set via the SAME
// `set_rate_limit_config` entrypoint with `fast_finality = true`, gated by
// `require_owner_or_rate_limit_admin` (lib.rs:367) just like the default
// bucket. The existing positive test (`..._rate_limit_admin_can_set_precise_auth`)
// covers the `fast_finality = false` (default) path with a rate-limit admin;
// this asserts an *unauthorized* caller is rejected on the `fast_finality = true`
// branch — closing the gap that no test gated the FTF branch specifically.
// ================================================================

#[test]
fn test_set_rate_limit_config_rejects_unauthorized_on_fast_finality_branch() {
    let (env, pool_client, _owner, ..) = setup_env();
    env.ledger().with_mut(|li| li.timestamp = 100);

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    // A caller that is neither owner nor the configured rate-limit admin.
    let caller = Address::generate(&env);
    // Turn off mock_all_auths so require_auth() is not satisfied for `caller`.
    env.mock_auths(&[]);
    let r = pool_client.try_set_rate_limit_config(
        &caller,
        &remote_chain,
        &RateLimitConfig {
            is_enabled: true,
            capacity: 100,
            rate: 1,
        },
        &RateLimitConfig::disabled(),
        &true, // fast_finality branch
    );
    assert!(
        r.is_err(),
        "non-owner/non-admin must be rejected from setting the FTF rate-limit bucket"
    );
}
