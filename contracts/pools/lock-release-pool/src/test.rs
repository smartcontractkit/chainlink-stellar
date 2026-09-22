#![cfg(test)]

use soroban_sdk::{testutils::Address as _, testutils::Ledger, token, Address, Bytes, Env, Vec};

use crate::{LockReleaseTokenPoolContract, LockReleaseTokenPoolContractClient};
use ccip_ramp_registry::{
    OffRampUpdate, OnRampUpdate, RampRegistryContract, RampRegistryContractClient,
};
use common_error::CCIPError;
use common_interfaces::token_pool::{
    LockOrBurnIn as IfaceLockOrBurnIn, MessageDirection as IfaceMessageDirection,
    PoolRequiredCCVs as IfacePoolRequiredCCVs, ReleaseOrMintIn as IfaceReleaseOrMintIn,
};
use common_pool::{
    encode_local_decimals, ChainUpdate, LockBoxEntry, LockOrBurnIn, MessageDirection,
    RateLimitConfig, ReleaseOrMintIn, TokenTransferFeeConfig, TokenTransferFeeConfigArgs,
};
use pools_token_lock_box::{TokenLockBox, TokenLockBoxClient};
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
            amount: i128,
        ) -> Result<(), CCIPError> {
            let _ = (env, lock_or_burn_in, requested_finality, amount);
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
            amount: i128,
        ) -> Result<(), CCIPError> {
            let _ = (env, lock_or_burn_in, requested_finality, amount);
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
            amount: i128,
        ) -> Result<(), CCIPError> {
            let _ = (env, lock_or_burn_in, requested_finality, amount);
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
}

/// Invokes `TokenPool::release_or_mint` with `caller = self` so `caller.require_auth()` succeeds
/// (mirrors OffRamp calling the pool).
mod inbound_release_stub {
    use soroban_sdk::{contract, contractimpl, Address, Env};

    use crate::LockReleaseTokenPoolContractClient;
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
            let client = LockReleaseTokenPoolContractClient::new(&env, &pool);
            Ok(client.release_or_mint(&me, &input, &requested_finality))
        }
    }
}

const DEFAULT_REMOTE_CHAIN: u64 = 5009297550715157269;

/// Register real Router + RMN proxy + RMN remote contracts and wire them, returning
/// the Router address (to pass to the pool's `initialize`). Mirrors the onramp test
/// wiring; the RMN starts uncursed so happy-path pool operations are unaffected.
/// (M-6: the pool resolves the RMN via `Router.get_config()` at lock_or_burn /
/// release_or_mint time, so tests must stand up a real Router + RMN behind the
/// address passed to `initialize`.)
fn setup_router_with_rmn(env: &Env, owner: &Address) -> (Address, Address) {
    let rmn_remote_id = env.register(RmnRemoteContract, ());
    let rmn_remote_client = RmnRemoteContractClient::new(env, &rmn_remote_id);
    rmn_remote_client.initialize(owner, &Vec::new(env));

    let rmn_proxy_id = env.register(RmnProxyContract, ());
    let rmn_proxy_client = RmnProxyContractClient::new(env, &rmn_proxy_id);
    rmn_proxy_client.initialize(owner, &rmn_remote_id);

    let router_id = env.register(RouterContract, ());
    let router_client = RouterContractClient::new(env, &router_id);
    router_client.initialize(owner, &rmn_proxy_id);

    (router_id, rmn_proxy_id)
}

fn setup_env() -> (
    Env,
    LockReleaseTokenPoolContractClient<'static>,
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

    let pool_id = env.register(LockReleaseTokenPoolContract, ());
    let pool_client = LockReleaseTokenPoolContractClient::new(&env, &pool_id);

    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_client = token::Client::new(&env, &token_address);
    let token_admin_client = token::StellarAssetClient::new(&env, &token_address);

    let (router, rmn_proxy) = setup_router_with_rmn(&env, &owner);
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

/// Register + initialize a `TokenLockBox` for `token_address`, authorize the
/// pool as an allowed caller, and map it to `chain` via `configure_lock_boxes`.
/// The canonical lock-release pool now escrows in a lockbox (EVM
/// `LockReleaseTokenPool.i_lockBox` parity), so every `lock_or_burn` /
/// `release_or_mint` test must wire one for its remote chain. Returns the
/// lockbox client so tests can fund it (release path) or assert its balance.
/// The lockbox owner is generated internally — only the pool's owner-gated
/// `configure_lock_boxes` matters here, and auth is mocked in tests.
fn wire_lockbox<'a>(
    env: &'a Env,
    pool_client: &LockReleaseTokenPoolContractClient<'_>,
    token_address: &Address,
    chain: u64,
) -> TokenLockBoxClient<'a> {
    let lockbox_owner = Address::generate(env);
    let lockbox_id = env.register(TokenLockBox, ());
    let lockbox_client = TokenLockBoxClient::new(env, &lockbox_id);
    lockbox_client.initialize(&lockbox_owner, token_address);
    lockbox_client.add_allowed_callers(&Vec::from_array(env, [pool_client.address.clone()]));
    pool_client.configure_lock_boxes(&Vec::from_array(
        env,
        [LockBoxEntry {
            remote_chain_selector: chain,
            lock_box: lockbox_client.address.clone(),
        }],
    ));
    lockbox_client
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
fn test_lock_and_release() {
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
    let remote_pool = Bytes::from_slice(&env, &[1u8; 20]);
    let remote_token = Bytes::from_slice(&env, &[2u8; 20]);

    let chain_update = ChainUpdate {
        remote_chain_selector: remote_chain,
        remote_pool_addresses: remote_pool,
        remote_token_address: remote_token.clone(),
        outbound_rate_limiter_config: RateLimitConfig::disabled(),
        inbound_rate_limiter_config: RateLimitConfig::disabled(),
    };
    pool_client.apply_chain_updates(&Vec::from_array(&env, [chain_update]), &Vec::new(&env));

    let lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);

    let sender = Address::generate(&env);
    let lock_amount: i128 = 1_000_000_000;
    token_admin_client.mint(&sender, &lock_amount);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender.clone(),
        amount: lock_amount,
        local_token: token_address.clone(),
    };

    let lock_result = pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(lock_result.dest_token_address, remote_token);

    let pool_address = pool_client.address.clone();
    // The post-fee amount is escrowed in the lockbox; the pool's own balance is
    // fees only (zero here, no fee config). Liquidity lives in the lockbox.
    assert_eq!(token_client.balance(&lockbox_client.address), lock_amount);
    assert_eq!(token_client.balance(&pool_address), 0);
    assert_eq!(token_client.balance(&sender), 0);

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: lock_amount,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };

    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    let release_result = stub_client.release(&pool_client.address, &release_input, &0u32);
    assert_eq!(release_result.destination_amount, lock_amount);
    assert_eq!(token_client.balance(&receiver), lock_amount);
    assert_eq!(token_client.balance(&pool_address), 0);
}

#[test]
fn test_unsupported_chain_rejected() {
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

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1_000_000_000);

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
        remote_pool_addresses: Bytes::from_slice(env, &[pool_byte; 20]),
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
        remote_pool_addresses: Bytes::from_slice(env, &[pool_byte; 20]),
        remote_token_address: Bytes::from_slice(env, &[token_byte; 20]),
        outbound_rate_limiter_config: outbound,
        inbound_rate_limiter_config: inbound,
    }
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

    let _lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);

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
    assert_eq!(token_client.balance(&pool_client.address), 0);
    assert_eq!(token_client.balance(&sender), 0);
}

#[test]
fn test_release_or_mint_zero_amount_succeeds_without_pool_balance() {
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

    let _lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);

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
fn test_release_or_mint_insufficient_pool_liquidity() {
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

    let remote_chain: u64 = 5009297550715157269;
    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 1, 2)]),
        &Vec::new(&env),
    );

    let _lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);

    let sender = Address::generate(&env);
    let locked: i128 = 50;
    token_admin_client.mint(&sender, &locked);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: locked,
        local_token: token_address.clone(),
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));

    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver,
        amount: locked + 1,
        local_token: token_address,
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: Bytes::new(&env),
    };

    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    let result = stub_client.try_release(&pool_client.address, &release_input, &0u32);
    assert_eq!(result, Err(Ok(CCIPError::InsufficientPoolLiquidity)));
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
    let result = stub_client.try_release(&pool_client.address, &release_input, &0u32);
    assert_eq!(result, Err(Ok(CCIPError::InvalidSourcePoolAddress)));
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

    // Owner can re-add the same selector with fresh config
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
    assert_eq!(
        pool_client.get_remote_token(&remote_chain),
        Bytes::from_slice(&env, &[2u8; 20])
    );

    pool_client.apply_chain_updates(
        &Vec::from_array(&env, [chain_update(&env, remote_chain, 3, 4)]),
        &Vec::new(&env),
    );
    assert_eq!(
        pool_client.get_remote_token(&remote_chain),
        Bytes::from_slice(&env, &[4u8; 20])
    );
    assert_eq!(
        pool_client.get_remote_pool(&remote_chain),
        Bytes::from_slice(&env, &[3u8; 20])
    );

    let _lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &1);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 1,
        local_token: token_address,
    };
    let out = pool_client.try_lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert!(out.is_ok());
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

    let lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);

    let sender = Address::generate(&env);
    token_admin_client.mint(&sender, &100);
    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 100,
        local_token: token_address,
    };
    let out = pool_client.lock_or_burn(&auth_onramp, &lock_input, &0u32, &Bytes::new(&env));
    assert_eq!(out.dest_pool_data, encode_local_decimals(&env, 7).unwrap());
    // The 100 is escrowed in the lockbox; the pool balance is fees only (0).
    assert_eq!(token_client.balance(&lockbox_client.address), 100);
    assert_eq!(token_client.balance(&pool_client.address), 0);
}

#[test]
fn test_release_or_mint_scales_down_remote_more_decimals() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let pool_id = env.register(LockReleaseTokenPoolContract, ());
    let pool_client = LockReleaseTokenPoolContractClient::new(&env, &pool_id);
    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_address = token_contract.address();
    let token_client = token::Client::new(&env, &token_address);
    let token_admin_client = token::StellarAssetClient::new(&env, &token_address);

    let registry_id = env.register(RampRegistryContract, ());
    let registry_client = RampRegistryContractClient::new(&env, &registry_id);
    registry_client.initialize(&owner);
    let auth_onramp = Address::generate(&env);
    register_onramp_for_chain(&env, &registry_client, DEFAULT_REMOTE_CHAIN, &auth_onramp);

    let stub_id = env.register(inbound_release_stub::PoolInboundReleaseStub, ());
    let stub_client = inbound_release_stub::PoolInboundReleaseStubClient::new(&env, &stub_id);

    let local_decimals: u32 = 6;
    let (router, rmn_proxy) = setup_router_with_rmn(&env, &owner);
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

    let lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);

    let expected_local: i128 = 1_000_000;
    // Release pulls from the lockbox, so fund it (not the pool address).
    token_admin_client.mint(&lockbox_client.address, &expected_local);

    let remote_decimals: u32 = 9;
    let receiver = Address::generate(&env);
    let release_input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&env, &[4u8; 20]),
        remote_chain_selector: remote_chain,
        receiver: receiver.clone(),
        amount: 1_000_000_000,
        local_token: token_address.clone(),
        source_pool_address: Bytes::from_slice(&env, &[1u8; 20]),
        source_pool_data: encode_local_decimals(&env, remote_decimals).unwrap(),
    };

    register_offramp_for_chain(&env, &registry_client, &stub_client, remote_chain);
    let out = stub_client.release(&pool_client.address, &release_input, &0u32);
    assert_eq!(out.destination_amount, expected_local);
    assert_eq!(token_client.balance(&receiver), expected_local);
    assert_eq!(token_client.balance(&pool_id), 0);
}

#[test]
fn test_initialize_rejects_decimals_above_uint8() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let pool_id = env.register(LockReleaseTokenPoolContract, ());
    let pool_client = LockReleaseTokenPoolContractClient::new(&env, &pool_id);
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
fn test_lock_or_burn_outbound_refills_over_time() {
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

    let lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);

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
    assert_eq!(token_client.balance(&lockbox_client.address), 1000);

    // Advance 50s => 500 tokens refilled
    env.ledger().with_mut(|li| li.timestamp = 150);
    let lock_input2 = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[3u8; 20]),
        remote_chain_selector: remote_chain,
        original_sender: sender,
        amount: 500,
        local_token: token_address,
    };
    pool_client.lock_or_burn(&auth_onramp, &lock_input2, &0u32, &Bytes::new(&env));
    assert_eq!(token_client.balance(&lockbox_client.address), 1500);
}

#[test]
fn test_release_or_mint_exceeds_inbound_capacity_rejected() {
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

    // Fund pool with enough liquidity
    token_admin_client.mint(&pool_client.address, &1000);

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
        token_admin_client,
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

    let lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);
    // Release pulls from the lockbox, so fund it (not the pool address).
    token_admin_client.mint(&lockbox_client.address, &5000);

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
    assert_eq!(token_client.balance(&receiver), 1000);

    // Advance 30s => 300 refilled
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
}

#[test]
fn test_set_rate_limit_config_updates_limits() {
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

    let state = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(!state.outbound.is_enabled);

    let new_outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 500,
        rate: 5,
    };
    pool_client.set_rate_limit_config(
        &remote_chain,
        &new_outbound,
        &RateLimitConfig::disabled(),
        &false,
    );

    let state2 = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(state2.outbound.is_enabled);
    assert_eq!(state2.outbound.capacity, 500);

    // Verify enforcement
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

    pool_client.apply_chain_updates(&Vec::new(&env), &Vec::from_array(&env, [remote_chain]));

    let state2 = pool_client.get_current_rate_limiter_state(&remote_chain, &false);
    assert!(!state2.outbound.is_enabled);
    assert_eq!(state2.outbound.tokens, 0);
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
        _owner,
        token_address,
        token_client,
        token_admin_client,
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

    // Configure FTF inbound bucket with smaller capacity than what we'll try
    let ftf_inbound = RateLimitConfig {
        is_enabled: true,
        capacity: 200,
        rate: 2,
    };
    pool_client.set_rate_limit_config(
        &remote_chain,
        &RateLimitConfig::disabled(),
        &ftf_inbound,
        &true,
    );

    let lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);
    // Release pulls from the lockbox, so fund it (not the pool address).
    token_admin_client.mint(&lockbox_client.address, &10_000);

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

    // FTF inbound: 1 more should fail (FTF bucket exhausted, only 2 refilled)
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
        token_admin_client,
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

    let lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);
    // Release pulls from the lockbox, so fund it (not the pool address).
    token_admin_client.mint(&lockbox_client.address, &10_000);

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

    // Configure allowed finality to permit WAIT_FOR_SAFE
    pool_client.set_allowed_finality_config(&WAIT_FOR_SAFE);

    // Configure FTF outbound bucket
    let ftf_outbound = RateLimitConfig {
        is_enabled: true,
        capacity: 300,
        rate: 3,
    };
    pool_client.set_rate_limit_config(
        &remote_chain,
        &ftf_outbound,
        &RateLimitConfig::disabled(),
        &true,
    );

    let _lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);

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

    // FTF outbound: exceeding refill should fail (FTF bucket exhausted)
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

#[test]
fn test_ftf_and_default_buckets_are_independent() {
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
        &remote_chain,
        &RateLimitConfig::disabled(),
        &ftf_inbound,
        &true,
    );

    let lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);
    // Release pulls from the lockbox, so fund it (not the pool address).
    token_admin_client.mint(&lockbox_client.address, &10_000);

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

// ============================================================
// Pool Fee Tests
// ============================================================

fn add_remote_chain(env: &Env, pool_client: &LockReleaseTokenPoolContractClient, chain: u64) {
    let remote_pool = Bytes::from_slice(env, &[1u8; 20]);
    let remote_token = Bytes::from_slice(env, &[2u8; 20]);
    pool_client.apply_chain_updates(
        &Vec::from_array(
            env,
            [ChainUpdate {
                remote_chain_selector: chain,
                remote_pool_addresses: remote_pool,
                remote_token_address: remote_token,
                outbound_rate_limiter_config: RateLimitConfig::disabled(),
                inbound_rate_limiter_config: RateLimitConfig::disabled(),
            }],
        ),
        &Vec::new(env),
    );
}

#[test]
fn test_get_fee_returns_zero_when_not_configured() {
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

    let remote_chain: u64 = 42;
    add_remote_chain(&env, &pool_client, remote_chain);

    let result = pool_client.get_fee(&remote_chain, &0i128, &0u32, &Bytes::new(&env));
    assert_eq!(result.fee_usd_cents, 0);
}

#[test]
fn test_set_and_get_pool_fee_config() {
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

    let remote_chain: u64 = 42;
    add_remote_chain(&env, &pool_client, remote_chain);

    let fee_config = TokenTransferFeeConfig {
        dest_gas_overhead: 100,
        dest_bytes_overhead: 32,
        finality_fee_usd_cents: 150,
        fast_finality_fee_usd_cents: 0,
        finality_transfer_fee_bps: 0,
        fast_finality_transfer_fee_bps: 0,
        is_enabled: true,
    };
    let adds = Vec::from_array(
        &env,
        [TokenTransferFeeConfigArgs {
            dest_chain_selector: remote_chain,
            config: fee_config,
        }],
    );
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(&env));

    // Requested finality 0 == WAIT_FOR_FINALITY → resolves finality_fee_usd_cents.
    let result = pool_client.get_fee(&remote_chain, &0i128, &0u32, &Bytes::new(&env));
    assert_eq!(result.fee_usd_cents, 150);
    assert!(result.is_enabled);
}

#[test]
fn test_pool_fee_disabled_returns_zero() {
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

    let remote_chain: u64 = 42;
    add_remote_chain(&env, &pool_client, remote_chain);

    // An enabled config first, so we can observe the disable actually zeroes it.
    let fee_config = TokenTransferFeeConfig {
        dest_gas_overhead: 100,
        dest_bytes_overhead: 32,
        finality_fee_usd_cents: 200,
        fast_finality_fee_usd_cents: 0,
        finality_transfer_fee_bps: 0,
        fast_finality_transfer_fee_bps: 0,
        is_enabled: true,
    };
    let adds = Vec::from_array(
        &env,
        [TokenTransferFeeConfigArgs {
            dest_chain_selector: remote_chain,
            config: fee_config,
        }],
    );
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(&env));

    // Disabling removes the stored entry; `get_fee` then returns a disabled
    // (all-zero) result, signalling the OnRamp to fall back to the FeeQuoter.
    let disables = Vec::from_array(&env, [remote_chain]);
    pool_client.apply_token_fee_config_updates(&Vec::new(&env), &disables);

    let result = pool_client.get_fee(&remote_chain, &0i128, &0u32, &Bytes::new(&env));
    assert_eq!(result.fee_usd_cents, 0);
    assert!(!result.is_enabled);
}

#[test]
#[should_panic(expected = "Error(Contract, #302)")]
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
        auth_onramp,
    ) = setup_env();

    let unsupported_chain: u64 = 99999;
    let fee_config = TokenTransferFeeConfig {
        dest_gas_overhead: 100,
        dest_bytes_overhead: 32,
        finality_fee_usd_cents: 50,
        fast_finality_fee_usd_cents: 0,
        finality_transfer_fee_bps: 0,
        fast_finality_transfer_fee_bps: 0,
        is_enabled: true,
    };
    let adds = Vec::from_array(
        &env,
        [TokenTransferFeeConfigArgs {
            dest_chain_selector: unsupported_chain,
            config: fee_config,
        }],
    );
    // The chain-support check runs before config validation, so an unsupported
    // chain yields ChainNotSupported (#302).
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(&env));
}

// ----------------------------------------------------------------
// Non-zero bps fee coverage (H-13, EVM `TokenPool._getFee` / `applyFee`
// parity). The fee tests above all set `*_transfer_fee_bps: 0` and assert
// only `fee_usd_cents`; these exercise the in-token bps deduction path.
// `dest_gas_overhead` must be non-zero (config validation rejects 0 → #321).
// ----------------------------------------------------------------
const E18: i128 = 1_000_000_000_000_000_000;

fn apply_bps_fee_config(
    env: &Env,
    pool_client: &LockReleaseTokenPoolContractClient<'_>,
    chain: u64,
    finality_bps: u32,
    fast_bps: u32,
) {
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
fn test_get_fee_fast_vs_default_bps_selection() {
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

    add_remote_chain(&env, &pool_client, DEFAULT_REMOTE_CHAIN);
    pool_client.set_allowed_finality_config(&WAIT_FOR_SAFE);
    // Distinct USD-cent AND bps values per finality mode, so both fields'
    // selection can be asserted (`apply_bps_fee_config` sets USD cents to 0).
    let adds = Vec::from_array(
        &env,
        [TokenTransferFeeConfigArgs {
            dest_chain_selector: DEFAULT_REMOTE_CHAIN,
            config: TokenTransferFeeConfig {
                dest_gas_overhead: 100,
                dest_bytes_overhead: 32,
                finality_fee_usd_cents: 40,
                fast_finality_fee_usd_cents: 80,
                finality_transfer_fee_bps: 250,
                fast_finality_transfer_fee_bps: 500,
                is_enabled: true,
            },
        }],
    );
    pool_client.apply_token_fee_config_updates(&adds, &Vec::new(&env));

    // Default finality → finality_* fields (mirrors test_applyFee_DefaultFinality).
    let default = pool_client.get_fee(
        &DEFAULT_REMOTE_CHAIN,
        &(1_000 * E18),
        &0u32,
        &Bytes::new(&env),
    );
    assert_eq!(default.token_fee_bps, 250);
    assert_eq!(default.fee_usd_cents, 40);
    assert!(default.is_enabled);

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
}

#[test]
fn test_lock_or_burn_with_nonzero_bps_fee() {
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

    add_remote_chain(&env, &pool_client, DEFAULT_REMOTE_CHAIN);
    let lockbox_client = wire_lockbox(&env, &pool_client, &token_address, DEFAULT_REMOTE_CHAIN);
    apply_bps_fee_config(&env, &pool_client, DEFAULT_REMOTE_CHAIN, 100, 0);

    let sender = Address::generate(&env);
    let amount: i128 = 1_000 * E18; // 1000e18 (EVM parity)
    token_admin_client.mint(&sender, &amount);

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
    // The full amount is pulled to the pool; dest is escrowed in the lockbox,
    // leaving only the fee on the pool's own balance (EVM lockOrBurn-with-fee).
    assert_eq!(token_client.balance(&lockbox_client.address), dest);
    assert_eq!(token_client.balance(&pool_address), fee);
    assert_eq!(token_client.balance(&sender), 0);
}

#[test]
fn test_lock_or_burn_dust_amount_zero_fee() {
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

    add_remote_chain(&env, &pool_client, DEFAULT_REMOTE_CHAIN);
    let lockbox_client = wire_lockbox(&env, &pool_client, &token_address, DEFAULT_REMOTE_CHAIN);
    apply_bps_fee_config(&env, &pool_client, DEFAULT_REMOTE_CHAIN, 250, 0);

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
    // No fee accrues; the full dust amount is escrowed in the lockbox.
    assert_eq!(token_client.balance(&lockbox_client.address), 39);
    assert_eq!(token_client.balance(&pool_address), 0);
    assert_eq!(token_client.balance(&sender), 0);
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

    let lockbox_client = wire_lockbox(&env, &pool_client, &token_address, remote_chain);

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

    let pool_address = pool_client.address.clone();
    // The 1e9 is escrowed in the lockbox; the pool balance is fees only (0).
    assert_eq!(token_client.balance(&lockbox_client.address), 1_000_000_000);
    assert_eq!(token_client.balance(&pool_address), 0);

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
