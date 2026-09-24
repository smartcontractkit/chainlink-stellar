#![cfg(test)]

extern crate std;

use soroban_sdk::{testutils::Address as _, token, vec, Address, Bytes, Env, Vec};

use crate::{
    LockBoxEntry, SiloedLockReleaseTokenPoolContract, SiloedLockReleaseTokenPoolContractClient,
};
use ccip_ramp_registry::{
    OffRampUpdate, OnRampUpdate, RampRegistryContract, RampRegistryContractClient,
};
use common_error::CCIPError;
use common_interfaces::token_pool::{
    LockOrBurnIn as IfaceLockOrBurnIn, MessageDirection as IfaceMessageDirection,
    PoolRequiredCCVs as IfacePoolRequiredCCVs, ReleaseOrMintIn as IfaceReleaseOrMintIn,
};
use common_pool::{
    ChainUpdate, LockOrBurnIn, MessageDirection, RateLimitConfig, ReleaseOrMintIn,
    TokenTransferFeeConfig, TokenTransferFeeConfigArgs,
};
use pools_token_lock_box::{TokenLockBox, TokenLockBoxClient};
use rmn_proxy::{RmnProxyContract, RmnProxyContractClient};
use rmn_remote::{RmnRemoteContract, RmnRemoteContractClient};
use router::{RouterContract, RouterContractClient};

/// Invokes `release_or_mint` with `caller = self` so `caller.require_auth()` succeeds.
mod inbound_release_stub {
    use soroban_sdk::{contract, contractimpl, Address, Env};

    use crate::SiloedLockReleaseTokenPoolContractClient;
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
            let client = SiloedLockReleaseTokenPoolContractClient::new(&env, &pool);
            Ok(client.release_or_mint(&me, &input, &requested_finality))
        }
    }
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

/// Minimal hook contracts for pool integration tests (must match `PoolHooksInterface` ABI).
mod mock_hooks {
    use soroban_sdk::{contract, contractimpl, symbol_short, Address, Bytes, Env, Symbol, Vec};

    use super::{
        CCIPError, IfaceLockOrBurnIn, IfaceMessageDirection, IfacePoolRequiredCCVs,
        IfaceReleaseOrMintIn,
    };

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

const REMOTE_CHAIN: u64 = 99_999;
const SILOED_CHAIN: u64 = 88_888;

fn disabled_rl() -> RateLimitConfig {
    RateLimitConfig {
        is_enabled: false,
        capacity: 0,
        rate: 0,
    }
}

fn add_chain(env: &Env, pool: &SiloedLockReleaseTokenPoolContractClient, selector: u64) {
    pool.apply_chain_updates(
        &vec![
            env,
            ChainUpdate {
                remote_chain_selector: selector,
                remote_pool_addresses: vec![env, Bytes::from_slice(env, &[0xaa; 32])],
                remote_token_address: Bytes::from_slice(env, &[0xbb; 32]),
                outbound_rate_limiter_config: disabled_rl(),
                inbound_rate_limiter_config: disabled_rl(),
            },
        ],
        &Vec::new(env),
    );
}

struct TestEnv<'a> {
    env: Env,
    token_addr: Address,
    pool_client: SiloedLockReleaseTokenPoolContractClient<'a>,
    lockbox_client: TokenLockBoxClient<'a>,
    sac: token::StellarAssetClient<'a>,
    tc: token::Client<'a>,
    registry_client: RampRegistryContractClient<'a>,
    stub_client: inbound_release_stub::PoolInboundReleaseStubClient<'a>,
    auth_onramp: Address,
}

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

fn setup() -> TestEnv<'static> {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_addr = token_contract.address();
    let sac = token::StellarAssetClient::new(&env, &token_addr);
    let tc = token::Client::new(&env, &token_addr);

    let lockbox_id = env.register(TokenLockBox, ());
    let lockbox_client = TokenLockBoxClient::new(&env, &lockbox_id);
    lockbox_client.initialize(&owner, &token_addr);

    let registry_id = env.register(RampRegistryContract, ());
    let registry_client = RampRegistryContractClient::new(&env, &registry_id);
    registry_client.initialize(&owner);
    let auth_onramp = Address::generate(&env);
    register_onramp_for_chain(&env, &registry_client, REMOTE_CHAIN, &auth_onramp);

    let stub_id = env.register(inbound_release_stub::PoolInboundReleaseStub, ());
    let stub_client = inbound_release_stub::PoolInboundReleaseStubClient::new(&env, &stub_id);

    let pool_id = env.register(SiloedLockReleaseTokenPoolContract, ());
    let pool_client = SiloedLockReleaseTokenPoolContractClient::new(&env, &pool_id);
    let (router, rmn_proxy) = setup_router_with_rmn(&env, &owner);
    pool_client.initialize(
        &owner,
        &token_addr,
        &7,
        &router,
        &registry_client.address,
        &rmn_proxy,
    );

    lockbox_client.add_allowed_callers(&vec![&env, pool_client.address.clone()]);
    add_chain(&env, &pool_client, REMOTE_CHAIN);

    pool_client.configure_lock_boxes(&vec![
        &env,
        LockBoxEntry {
            remote_chain_selector: REMOTE_CHAIN,
            lock_box: lockbox_client.address.clone(),
        },
    ]);

    register_offramp_for_chain(&env, &registry_client, &stub_client, REMOTE_CHAIN);

    TestEnv {
        env,
        token_addr,
        pool_client,
        lockbox_client,
        sac,
        tc,
        registry_client,
        stub_client,
        auth_onramp,
    }
}

/// Extended setup with two distinct lockboxes (shared + siloed) and two chains.
struct MultiLockboxEnv<'a> {
    env: Env,
    token_addr: Address,
    pool_client: SiloedLockReleaseTokenPoolContractClient<'a>,
    shared_lockbox: TokenLockBoxClient<'a>,
    siloed_lockbox: TokenLockBoxClient<'a>,
    sac: token::StellarAssetClient<'a>,
    tc: token::Client<'a>,
    stub_client: inbound_release_stub::PoolInboundReleaseStubClient<'a>,
    auth_onramp: Address,
}

fn setup_multi_lockbox() -> MultiLockboxEnv<'static> {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_addr = token_contract.address();
    let sac = token::StellarAssetClient::new(&env, &token_addr);
    let tc = token::Client::new(&env, &token_addr);

    let shared_id = env.register(TokenLockBox, ());
    let shared_lockbox = TokenLockBoxClient::new(&env, &shared_id);
    shared_lockbox.initialize(&owner, &token_addr);

    let siloed_id = env.register(TokenLockBox, ());
    let siloed_lockbox = TokenLockBoxClient::new(&env, &siloed_id);
    siloed_lockbox.initialize(&owner, &token_addr);

    let registry_id = env.register(RampRegistryContract, ());
    let registry_client = RampRegistryContractClient::new(&env, &registry_id);
    registry_client.initialize(&owner);
    let auth_onramp = Address::generate(&env);
    register_onramp_for_chain(&env, &registry_client, REMOTE_CHAIN, &auth_onramp);
    register_onramp_for_chain(&env, &registry_client, SILOED_CHAIN, &auth_onramp);

    let stub_id = env.register(inbound_release_stub::PoolInboundReleaseStub, ());
    let stub_client = inbound_release_stub::PoolInboundReleaseStubClient::new(&env, &stub_id);

    let pool_id = env.register(SiloedLockReleaseTokenPoolContract, ());
    let pool_client = SiloedLockReleaseTokenPoolContractClient::new(&env, &pool_id);
    let (router, rmn_proxy) = setup_router_with_rmn(&env, &owner);
    pool_client.initialize(
        &owner,
        &token_addr,
        &7,
        &router,
        &registry_client.address,
        &rmn_proxy,
    );

    shared_lockbox.add_allowed_callers(&vec![&env, pool_client.address.clone()]);
    siloed_lockbox.add_allowed_callers(&vec![&env, pool_client.address.clone()]);

    add_chain(&env, &pool_client, REMOTE_CHAIN);
    add_chain(&env, &pool_client, SILOED_CHAIN);

    pool_client.configure_lock_boxes(&vec![
        &env,
        LockBoxEntry {
            remote_chain_selector: REMOTE_CHAIN,
            lock_box: shared_lockbox.address.clone(),
        },
        LockBoxEntry {
            remote_chain_selector: SILOED_CHAIN,
            lock_box: siloed_lockbox.address.clone(),
        },
    ]);

    register_offramp_for_chain(&env, &registry_client, &stub_client, REMOTE_CHAIN);
    register_offramp_for_chain(&env, &registry_client, &stub_client, SILOED_CHAIN);

    MultiLockboxEnv {
        env,
        token_addr,
        pool_client,
        shared_lockbox,
        siloed_lockbox,
        sac,
        tc,
        stub_client,
        auth_onramp,
    }
}

#[test]
fn lock_deposits_into_lockbox() {
    let t = setup();
    let sender = Address::generate(&t.env);
    t.sac.mint(&sender, &1_000);

    let input = LockOrBurnIn {
        receiver: Bytes::from_slice(&t.env, &[0x01; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount: 500,
        local_token: t.token_addr.clone(),
    };
    let out = t
        .pool_client
        .lock_or_burn(&t.auth_onramp, &input, &0, &Bytes::new(&t.env));

    assert_eq!(t.tc.balance(&sender), 500);
    assert_eq!(t.tc.balance(&t.lockbox_client.address), 500);
    assert!(!out.dest_token_address.is_empty());
}

/// Integration: `lock_or_burn` must not leave a standing SAC allowance from the pool to the lockbox.
#[test]
fn lock_or_burn_leaves_no_token_allowance_on_lockbox() {
    let t = setup();
    let sender = Address::generate(&t.env);
    t.sac.mint(&sender, &1_000);

    let input = LockOrBurnIn {
        receiver: Bytes::from_slice(&t.env, &[0x01; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount: 500,
        local_token: t.token_addr.clone(),
    };
    t.pool_client
        .lock_or_burn(&t.auth_onramp, &input, &0, &Bytes::new(&t.env));

    let pool_addr = t.pool_client.address.clone();
    let remaining = t.tc.allowance(&pool_addr, &t.lockbox_client.address);
    assert_eq!(remaining, 0);
}

#[test]
fn release_withdraws_from_lockbox() {
    let t = setup();

    let liquidity_provider = Address::generate(&t.env);
    t.sac.mint(&liquidity_provider, &2_000);
    t.lockbox_client
        .add_allowed_callers(&vec![&t.env, liquidity_provider.clone()]);
    let exp = t.env.ledger().sequence().saturating_add(10_000);
    t.tc.approve(&liquidity_provider, &t.lockbox_client.address, &2_000, &exp);
    t.lockbox_client.deposit(&liquidity_provider, &2_000);

    let receiver = Address::generate(&t.env);
    let input = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&t.env, &[0xcd; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        receiver: receiver.clone(),
        amount: 800,
        local_token: t.token_addr.clone(),
        source_pool_address: Bytes::from_slice(&t.env, &[0xaa; 32]),
        source_pool_data: Bytes::new(&t.env),
    };
    let out = t.stub_client.release(&t.pool_client.address, &input, &0);

    assert_eq!(out.destination_amount, 800);
    assert_eq!(t.tc.balance(&receiver), 800);
    assert_eq!(t.tc.balance(&t.lockbox_client.address), 1_200);
}

#[test]
fn get_lock_box_returns_configured_address() {
    let t = setup();
    let addr = t.pool_client.get_lock_box(&REMOTE_CHAIN);
    assert_eq!(addr, t.lockbox_client.address);
}

#[test]
fn get_all_lock_box_configs() {
    let t = setup();
    let cfgs = t.pool_client.get_all_lock_box_configs();
    assert_eq!(cfgs.len(), 1);
    let entry = cfgs.get(0).unwrap();
    assert_eq!(entry.remote_chain_selector, REMOTE_CHAIN);
    assert_eq!(entry.lock_box, t.lockbox_client.address);
}

#[test]
fn unconfigured_lockbox_rejects_lock() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let token_addr = token_contract.address();

    let registry_id = env.register(RampRegistryContract, ());
    let registry_client = RampRegistryContractClient::new(&env, &registry_id);
    registry_client.initialize(&owner);
    let auth_onramp = Address::generate(&env);
    register_onramp_for_chain(&env, &registry_client, REMOTE_CHAIN, &auth_onramp);

    let pool_id = env.register(SiloedLockReleaseTokenPoolContract, ());
    let pool_client = SiloedLockReleaseTokenPoolContractClient::new(&env, &pool_id);
    let (router, rmn_proxy) = setup_router_with_rmn(&env, &owner);
    pool_client.initialize(
        &owner,
        &token_addr,
        &7,
        &router,
        &registry_client.address,
        &rmn_proxy,
    );

    let remote_pool = Bytes::from_slice(&env, &[0xaa; 32]);
    let remote_token = Bytes::from_slice(&env, &[0xbb; 32]);
    pool_client.apply_chain_updates(
        &vec![
            &env,
            ChainUpdate {
                remote_chain_selector: REMOTE_CHAIN,
                remote_pool_addresses: vec![&env, remote_pool],
                remote_token_address: remote_token,
                outbound_rate_limiter_config: disabled_rl(),
                inbound_rate_limiter_config: disabled_rl(),
            },
        ],
        &Vec::new(&env),
    );

    let sender = Address::generate(&env);
    let sac = token::StellarAssetClient::new(&env, &token_addr);
    sac.mint(&sender, &500);

    let input = LockOrBurnIn {
        receiver: Bytes::from_slice(&env, &[0x01; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        original_sender: sender,
        amount: 100,
        local_token: token_addr,
    };
    let r = pool_client.try_lock_or_burn(&auth_onramp, &input, &0, &Bytes::new(&env));
    assert!(r.is_err());
}

#[test]
fn many_to_one_lockbox_shared_liquidity() {
    let t = setup();

    add_chain(&t.env, &t.pool_client, SILOED_CHAIN);
    register_onramp_for_chain(&t.env, &t.registry_client, SILOED_CHAIN, &t.auth_onramp);
    t.pool_client.configure_lock_boxes(&vec![
        &t.env,
        LockBoxEntry {
            remote_chain_selector: SILOED_CHAIN,
            lock_box: t.lockbox_client.address.clone(),
        },
    ]);
    register_offramp_for_chain(&t.env, &t.registry_client, &t.stub_client, SILOED_CHAIN);

    let sender = Address::generate(&t.env);
    t.sac.mint(&sender, &1_000);

    let input_a = LockOrBurnIn {
        receiver: Bytes::from_slice(&t.env, &[0x01; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount: 300,
        local_token: t.token_addr.clone(),
    };
    t.pool_client
        .lock_or_burn(&t.auth_onramp, &input_a, &0, &Bytes::new(&t.env));

    let input_b = LockOrBurnIn {
        receiver: Bytes::from_slice(&t.env, &[0x02; 20]),
        remote_chain_selector: SILOED_CHAIN,
        original_sender: sender.clone(),
        amount: 200,
        local_token: t.token_addr.clone(),
    };
    t.pool_client
        .lock_or_burn(&t.auth_onramp, &input_b, &0, &Bytes::new(&t.env));

    assert_eq!(t.tc.balance(&t.lockbox_client.address), 500);
}

// ================================================================
// Constructor / initialization (EVM test_constructor parity)
// ================================================================

#[test]
fn constructor_sets_token_and_version() {
    let t = setup();
    assert_eq!(t.pool_client.get_token(), t.token_addr);
    assert_eq!(t.pool_client.get_token_decimals(), 7);
    let version = t.pool_client.type_and_version();
    let expected = soroban_sdk::String::from_str(&t.env, "SiloedLockReleaseTokenPool-dev 2.0.0");
    assert_eq!(version, expected);
}

// ================================================================
// configureLockBoxes error paths
// ================================================================

/// EVM: test_configureLockBoxes_RevertWhen_InvalidToken
#[test]
fn configure_lockboxes_rejects_wrong_token() {
    let env = Env::default();
    env.mock_all_auths_allowing_non_root_auth();

    let owner = Address::generate(&env);
    let token_admin = Address::generate(&env);
    let token_contract = env.register_stellar_asset_contract_v2(token_admin.clone());
    let pool_token = token_contract.address();

    let other_admin = Address::generate(&env);
    let other_contract = env.register_stellar_asset_contract_v2(other_admin.clone());
    let other_token = other_contract.address();

    let lockbox_id = env.register(TokenLockBox, ());
    let lockbox_client = TokenLockBoxClient::new(&env, &lockbox_id);
    lockbox_client.initialize(&owner, &other_token);

    let router = Address::generate(&env);
    let ramp_registry = Address::generate(&env);
    let rmn_proxy = Address::generate(&env);
    let pool_id = env.register(SiloedLockReleaseTokenPoolContract, ());
    let pool_client = SiloedLockReleaseTokenPoolContractClient::new(&env, &pool_id);
    pool_client.initialize(&owner, &pool_token, &7, &router, &ramp_registry, &rmn_proxy);
    add_chain(&env, &pool_client, REMOTE_CHAIN);

    let r = pool_client.try_configure_lock_boxes(&vec![
        &env,
        LockBoxEntry {
            remote_chain_selector: REMOTE_CHAIN,
            lock_box: lockbox_client.address.clone(),
        },
    ]);
    assert!(r.is_err());
}

// ================================================================
// getLockBox (EVM test_getLockBox, test_getLockBox_RevertWhen_LockBoxNotConfigured)
// ================================================================

/// EVM: test_getLockBox — distinct lockboxes for different chains
#[test]
fn get_lock_box_returns_distinct_addresses() {
    let m = setup_multi_lockbox();
    assert_eq!(
        m.pool_client.get_lock_box(&REMOTE_CHAIN),
        m.shared_lockbox.address
    );
    assert_eq!(
        m.pool_client.get_lock_box(&SILOED_CHAIN),
        m.siloed_lockbox.address
    );
    assert_ne!(m.shared_lockbox.address, m.siloed_lockbox.address);
}

/// EVM: test_getLockBox_RevertWhen_LockBoxNotConfigured
#[test]
fn get_lock_box_rejects_unconfigured_chain() {
    let t = setup();
    let unknown_chain: u64 = 12_345;
    let r = t.pool_client.try_get_lock_box(&unknown_chain);
    assert!(r.is_err());
}

// ================================================================
// getAllLockBoxConfigs with multiple chains (EVM test_getAllLockBoxConfigs)
// ================================================================

#[test]
fn get_all_lock_box_configs_multiple_chains() {
    let m = setup_multi_lockbox();

    let third_chain: u64 = 77_777;
    add_chain(&m.env, &m.pool_client, third_chain);
    m.pool_client.configure_lock_boxes(&vec![
        &m.env,
        LockBoxEntry {
            remote_chain_selector: third_chain,
            lock_box: m.shared_lockbox.address.clone(),
        },
    ]);

    let cfgs = m.pool_client.get_all_lock_box_configs();
    assert_eq!(cfgs.len(), 3);

    let selectors: std::vec::Vec<u64> = cfgs.iter().map(|c| c.remote_chain_selector).collect();
    assert!(selectors.contains(&REMOTE_CHAIN));
    assert!(selectors.contains(&SILOED_CHAIN));
    assert!(selectors.contains(&third_chain));

    for cfg in cfgs.iter() {
        if cfg.remote_chain_selector == SILOED_CHAIN {
            assert_eq!(cfg.lock_box, m.siloed_lockbox.address);
        } else {
            assert_eq!(cfg.lock_box, m.shared_lockbox.address);
        }
    }
}

// ================================================================
// lockOrBurn — siloed vs shared isolation
// (EVM test_lockOrBurn_SiloedFunds + test_lockOrBurn_UnsiloedFunds)
// ================================================================

#[test]
fn siloed_and_shared_lockbox_isolation() {
    let m = setup_multi_lockbox();
    let sender = Address::generate(&m.env);
    m.sac.mint(&sender, &2_000);

    let lock_shared = LockOrBurnIn {
        receiver: Bytes::from_slice(&m.env, &[0x01; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount: 600,
        local_token: m.token_addr.clone(),
    };
    m.pool_client
        .lock_or_burn(&m.auth_onramp, &lock_shared, &0, &Bytes::new(&m.env));

    let lock_siloed = LockOrBurnIn {
        receiver: Bytes::from_slice(&m.env, &[0x02; 20]),
        remote_chain_selector: SILOED_CHAIN,
        original_sender: sender.clone(),
        amount: 400,
        local_token: m.token_addr.clone(),
    };
    m.pool_client
        .lock_or_burn(&m.auth_onramp, &lock_siloed, &0, &Bytes::new(&m.env));

    assert_eq!(m.tc.balance(&m.shared_lockbox.address), 600);
    assert_eq!(m.tc.balance(&m.siloed_lockbox.address), 400);
    assert_eq!(m.tc.balance(&sender), 1_000);
}

// ================================================================
// releaseOrMint — full lock→release cycle
// (EVM test_ReleaseOrMint_SiloedChain + test_ReleaseOrMint_UnsiloedChain)
// ================================================================

/// Lock to siloed chain, then release back — siloed lockbox should drain to zero.
#[test]
fn release_drains_siloed_lockbox() {
    let m = setup_multi_lockbox();
    let sender = Address::generate(&m.env);
    m.sac.mint(&sender, &1_000);

    m.pool_client.lock_or_burn(
        &m.auth_onramp,
        &LockOrBurnIn {
            receiver: Bytes::from_slice(&m.env, &[0x01; 20]),
            remote_chain_selector: SILOED_CHAIN,
            original_sender: sender.clone(),
            amount: 1_000,
            local_token: m.token_addr.clone(),
        },
        &0,
        &Bytes::new(&m.env),
    );
    assert_eq!(m.tc.balance(&m.siloed_lockbox.address), 1_000);
    assert_eq!(m.tc.balance(&m.shared_lockbox.address), 0);

    let receiver = Address::generate(&m.env);
    let release_in = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&m.env, &[0xcd; 20]),
        remote_chain_selector: SILOED_CHAIN,
        receiver: receiver.clone(),
        amount: 1_000,
        local_token: m.token_addr.clone(),
        source_pool_address: Bytes::from_slice(&m.env, &[0xaa; 32]),
        source_pool_data: Bytes::new(&m.env),
    };
    let out = m
        .stub_client
        .release(&m.pool_client.address, &release_in, &0);

    assert_eq!(out.destination_amount, 1_000);
    assert_eq!(m.tc.balance(&receiver), 1_000);
    assert_eq!(m.tc.balance(&m.siloed_lockbox.address), 0);
    assert_eq!(m.tc.balance(&m.shared_lockbox.address), 0);
}

/// Lock to shared chain, then release back — shared lockbox should drain to zero.
#[test]
fn release_drains_shared_lockbox() {
    let m = setup_multi_lockbox();
    let sender = Address::generate(&m.env);
    m.sac.mint(&sender, &500);

    m.pool_client.lock_or_burn(
        &m.auth_onramp,
        &LockOrBurnIn {
            receiver: Bytes::from_slice(&m.env, &[0x01; 20]),
            remote_chain_selector: REMOTE_CHAIN,
            original_sender: sender.clone(),
            amount: 500,
            local_token: m.token_addr.clone(),
        },
        &0,
        &Bytes::new(&m.env),
    );
    assert_eq!(m.tc.balance(&m.shared_lockbox.address), 500);
    assert_eq!(m.tc.balance(&m.siloed_lockbox.address), 0);

    let receiver = Address::generate(&m.env);
    let release_in = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&m.env, &[0xcd; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        receiver: receiver.clone(),
        amount: 500,
        local_token: m.token_addr.clone(),
        source_pool_address: Bytes::from_slice(&m.env, &[0xaa; 32]),
        source_pool_data: Bytes::new(&m.env),
    };
    let out = m
        .stub_client
        .release(&m.pool_client.address, &release_in, &0);

    assert_eq!(out.destination_amount, 500);
    assert_eq!(m.tc.balance(&receiver), 500);
    assert_eq!(m.tc.balance(&m.shared_lockbox.address), 0);
}

// ================================================================
// lockOrBurn error paths
// ================================================================

#[test]
fn lock_rejects_wrong_token() {
    let t = setup();

    let other_admin = Address::generate(&t.env);
    let other_contract = t.env.register_stellar_asset_contract_v2(other_admin);
    let wrong_token = other_contract.address();

    let sender = Address::generate(&t.env);
    let r = t.pool_client.try_lock_or_burn(
        &t.auth_onramp,
        &LockOrBurnIn {
            receiver: Bytes::from_slice(&t.env, &[0x01; 20]),
            remote_chain_selector: REMOTE_CHAIN,
            original_sender: sender,
            amount: 100,
            local_token: wrong_token,
        },
        &0,
        &Bytes::new(&t.env),
    );
    assert!(r.is_err());
}

#[test]
fn lock_rejects_unsupported_chain() {
    let t = setup();
    let sender = Address::generate(&t.env);
    t.sac.mint(&sender, &500);

    let unknown_chain: u64 = 12_345;
    let r = t.pool_client.try_lock_or_burn(
        &t.auth_onramp,
        &LockOrBurnIn {
            receiver: Bytes::from_slice(&t.env, &[0x01; 20]),
            remote_chain_selector: unknown_chain,
            original_sender: sender,
            amount: 100,
            local_token: t.token_addr.clone(),
        },
        &0,
        &Bytes::new(&t.env),
    );
    assert!(r.is_err());
}

// ================================================================
// releaseOrMint error paths
// ================================================================

#[test]
fn release_rejects_insufficient_liquidity() {
    let m = setup_multi_lockbox();
    let sender = Address::generate(&m.env);
    m.sac.mint(&sender, &100);

    m.pool_client.lock_or_burn(
        &m.auth_onramp,
        &LockOrBurnIn {
            receiver: Bytes::from_slice(&m.env, &[0x01; 20]),
            remote_chain_selector: SILOED_CHAIN,
            original_sender: sender,
            amount: 100,
            local_token: m.token_addr.clone(),
        },
        &0,
        &Bytes::new(&m.env),
    );

    let receiver = Address::generate(&m.env);
    let release_in = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&m.env, &[0xcd; 20]),
        remote_chain_selector: SILOED_CHAIN,
        receiver: receiver,
        amount: 500,
        local_token: m.token_addr.clone(),
        source_pool_address: Bytes::from_slice(&m.env, &[0xaa; 32]),
        source_pool_data: Bytes::new(&m.env),
    };
    let r = m
        .stub_client
        .try_release(&m.pool_client.address, &release_in, &0);
    assert!(r.is_err());
}

#[test]
fn release_rejects_wrong_token() {
    let t = setup();
    let liquidity_provider = Address::generate(&t.env);
    t.sac.mint(&liquidity_provider, &1_000);
    t.lockbox_client
        .add_allowed_callers(&vec![&t.env, liquidity_provider.clone()]);
    let exp = t.env.ledger().sequence().saturating_add(10_000);
    t.tc.approve(&liquidity_provider, &t.lockbox_client.address, &1_000, &exp);
    t.lockbox_client.deposit(&liquidity_provider, &1_000);

    let other_admin = Address::generate(&t.env);
    let other_contract = t.env.register_stellar_asset_contract_v2(other_admin);
    let wrong_token = other_contract.address();

    let receiver = Address::generate(&t.env);
    let release_in = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&t.env, &[0xcd; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        receiver: receiver,
        amount: 100,
        local_token: wrong_token,
        source_pool_address: Bytes::from_slice(&t.env, &[0xaa; 32]),
        source_pool_data: Bytes::new(&t.env),
    };
    let r = t
        .stub_client
        .try_release(&t.pool_client.address, &release_in, &0);
    assert!(r.is_err());
}

#[test]
fn release_rejects_unsupported_chain() {
    let t = setup();
    let receiver = Address::generate(&t.env);
    let unknown_chain: u64 = 12_345;

    let release_in = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&t.env, &[0xcd; 20]),
        remote_chain_selector: unknown_chain,
        receiver: receiver,
        amount: 100,
        local_token: t.token_addr.clone(),
        source_pool_address: Bytes::from_slice(&t.env, &[0xaa; 32]),
        source_pool_data: Bytes::new(&t.env),
    };
    let r = t
        .stub_client
        .try_release(&t.pool_client.address, &release_in, &0);
    assert!(r.is_err());
}

#[test]
fn release_rejects_wrong_source_pool() {
    // Inbound `release_or_mint` must revert `InvalidSourcePoolAddress` when the
    // message's `source_pool_address` is not the configured remote pool for the
    // source chain. Mirrors EVM `TokenPool._validateReleaseOrMint`
    // (`pools/TokenPool.sol:480`), which checks `isRemotePool` before the
    // inbound rate-limit consume. `setup()` configures REMOTE_CHAIN with
    // remote_pool = [0xaa;32] (see `add_chain`); here we claim [0x99;32].
    let t = setup();
    let receiver = Address::generate(&t.env);

    let release_in = ReleaseOrMintIn {
        original_sender: Bytes::from_slice(&t.env, &[0xcd; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        receiver: receiver,
        amount: 100,
        local_token: t.token_addr.clone(),
        source_pool_address: Bytes::from_slice(&t.env, &[0x99; 32]),
        source_pool_data: Bytes::new(&t.env),
    };
    let r = t
        .stub_client
        .try_release(&t.pool_client.address, &release_in, &0);
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::InvalidSourcePoolAddress);
}

#[test]
fn add_remote_pool_accepts_inbound_from_new_pool() {
    // H-14: a second remote pool can be added to REMOTE_CHAIN's set and inbound
    // `release_or_mint` from it then passes the source-pool membership check
    // (zero-downtime migration). Mirrors EVM `TokenPool.addRemotePool`
    // (`pools/TokenPool.sol:621`). `setup()` configures REMOTE_CHAIN with
    // [0xaa;32]; siloed lock-release needs lockbox liquidity to fully release,
    // so we assert the membership check passes (the error is no longer
    // `InvalidSourcePoolAddress`), which is what H-14 changes.
    let t = setup();
    let release_from = |pool_bytes: [u8; 32]| {
        t.stub_client.try_release(
            &t.pool_client.address,
            &ReleaseOrMintIn {
                original_sender: Bytes::from_slice(&t.env, &[0xcd; 20]),
                remote_chain_selector: REMOTE_CHAIN,
                receiver: Address::generate(&t.env),
                amount: 100,
                local_token: t.token_addr.clone(),
                source_pool_address: Bytes::from_slice(&t.env, &pool_bytes),
                source_pool_data: Bytes::new(&t.env),
            },
            &0,
        )
    };

    // Before adding, inbound from [0x99;32] fails the membership check.
    assert_eq!(
        release_from([0x99; 32]).unwrap_err().unwrap(),
        CCIPError::InvalidSourcePoolAddress
    );

    // Add [0x99;32] as a second remote pool.
    t.pool_client
        .add_remote_pool(&REMOTE_CHAIN, &Bytes::from_slice(&t.env, &[0x99; 32]));

    // After adding, the membership check passes for [0x99;32] (the error is no
    // longer InvalidSourcePoolAddress) and [0xaa;32] still passes.
    assert!(!matches!(
        release_from([0x99; 32]),
        Err(Ok(CCIPError::InvalidSourcePoolAddress))
    ));
    assert!(!matches!(
        release_from([0xaa; 32]),
        Err(Ok(CCIPError::InvalidSourcePoolAddress))
    ));

    let pools = t.pool_client.get_remote_pools(&REMOTE_CHAIN);
    assert_eq!(pools.len(), 2);
}

#[test]
fn remove_remote_pool_rejects_inbound() {
    // H-14: removing a remote pool causes inbound from it to fail the membership
    // check again; removing an absent pool returns `InvalidRemotePoolAddress`
    // (305). Mirrors EVM `TokenPool.removeRemotePool` (`pools/TokenPool.sol:635`).
    let t = setup();
    let release_from_aa = || {
        t.stub_client.try_release(
            &t.pool_client.address,
            &ReleaseOrMintIn {
                original_sender: Bytes::from_slice(&t.env, &[0xcd; 20]),
                remote_chain_selector: REMOTE_CHAIN,
                receiver: Address::generate(&t.env),
                amount: 100,
                local_token: t.token_addr.clone(),
                source_pool_address: Bytes::from_slice(&t.env, &[0xaa; 32]),
                source_pool_data: Bytes::new(&t.env),
            },
            &0,
        )
    };

    // [0xaa;32] passes the membership check before removal.
    assert!(!matches!(
        release_from_aa(),
        Err(Ok(CCIPError::InvalidSourcePoolAddress))
    ));

    // Remove [0xaa;32]; inbound from it now fails the membership check.
    t.pool_client
        .remove_remote_pool(&REMOTE_CHAIN, &Bytes::from_slice(&t.env, &[0xaa; 32]));
    assert_eq!(
        release_from_aa().unwrap_err().unwrap(),
        CCIPError::InvalidSourcePoolAddress
    );

    // Removing an absent pool returns InvalidRemotePoolAddress (305).
    assert_eq!(
        t.pool_client
            .try_remove_remote_pool(&REMOTE_CHAIN, &Bytes::from_slice(&t.env, &[0xaa; 32]))
            .unwrap_err()
            .unwrap(),
        CCIPError::InvalidRemotePoolAddress
    );
}

#[test]
fn add_remote_pool_idempotent() {
    // H-14: re-adding an already-configured remote pool is a no-op (EVM
    // `EnumerableSet.add` parity).
    let t = setup();
    t.pool_client
        .add_remote_pool(&REMOTE_CHAIN, &Bytes::from_slice(&t.env, &[0xaa; 32]));
    let pools = t.pool_client.get_remote_pools(&REMOTE_CHAIN);
    assert_eq!(pools.len(), 1);
    assert_eq!(
        pools.get(0).unwrap(),
        Bytes::from_slice(&t.env, &[0xaa; 32])
    );
}

#[test]
#[should_panic(expected = "Error(Contract, #52)")] // InvalidConfig
fn apply_chain_updates_rejects_empty_remote_pool_element() {
    // H-14 / C-2 gap: a `Vec<Bytes>` containing an empty `Bytes` element must be
    // rejected (the len()==0 guard only catches an empty vec).
    let t = setup();
    t.pool_client.apply_chain_updates(
        &vec![
            &t.env,
            ChainUpdate {
                remote_chain_selector: REMOTE_CHAIN,
                remote_pool_addresses: vec![&t.env, Bytes::new(&t.env)], // single empty element
                remote_token_address: Bytes::from_slice(&t.env, &[0xbb; 32]),
                outbound_rate_limiter_config: disabled_rl(),
                inbound_rate_limiter_config: disabled_rl(),
            },
        ],
        &Vec::new(&t.env),
    );
}

// ============================================================
// Pool fee (BaseTokenPool re-exports)
// ============================================================

#[test]
fn get_fee_returns_zero_when_not_configured() {
    let t = setup();
    let result = t
        .pool_client
        .get_fee(&REMOTE_CHAIN, &0i128, &0u32, &Bytes::new(&t.env));
    assert_eq!(result.fee_usd_cents, 0);
}

#[test]
fn set_and_get_pool_fee_config() {
    let t = setup();
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
        &t.env,
        [TokenTransferFeeConfigArgs {
            dest_chain_selector: REMOTE_CHAIN,
            config: fee_config,
        }],
    );
    t.pool_client
        .apply_token_fee_config_updates(&adds, &Vec::new(&t.env));
    // Requested finality 0 == WAIT_FOR_FINALITY → resolves finality_fee_usd_cents.
    let result = t
        .pool_client
        .get_fee(&REMOTE_CHAIN, &0i128, &0u32, &Bytes::new(&t.env));
    assert_eq!(result.fee_usd_cents, 150);
}

#[test]
fn pool_fee_disabled_returns_zero() {
    let t = setup();
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
        &t.env,
        [TokenTransferFeeConfigArgs {
            dest_chain_selector: REMOTE_CHAIN,
            config: fee_config,
        }],
    );
    t.pool_client
        .apply_token_fee_config_updates(&adds, &Vec::new(&t.env));
    // Disabling removes the stored entry; `get_fee` then returns a disabled
    // (all-zero) result. Adds reject `is_enabled == false`, so the disable list
    // is the only way to disable (EVM `TokenPool.applyTokenTransferFeeConfigUpdates`).
    let disables = Vec::from_array(&t.env, [REMOTE_CHAIN]);
    t.pool_client
        .apply_token_fee_config_updates(&Vec::new(&t.env), &disables);
    let result = t
        .pool_client
        .get_fee(&REMOTE_CHAIN, &0i128, &0u32, &Bytes::new(&t.env));
    assert_eq!(result.fee_usd_cents, 0);
}

#[test]
#[should_panic(expected = "Error(Contract, #302)")]
fn set_pool_fee_unsupported_chain_rejected() {
    let t = setup();
    // Must differ from `REMOTE_CHAIN` (99_999 == 99999 in Rust).
    let unsupported_chain: u64 = 12_345;
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
        &t.env,
        [TokenTransferFeeConfigArgs {
            dest_chain_selector: unsupported_chain,
            config: fee_config,
        }],
    );
    // The chain-support check runs before config validation, so an unsupported
    // chain yields ChainNotSupported (#302).
    t.pool_client
        .apply_token_fee_config_updates(&adds, &Vec::new(&t.env));
}

// ----------------------------------------------------------------
// Non-zero bps fee coverage (H-13, EVM `TokenPool._getFee` / `applyFee`
// parity). The fee tests above all set `*_transfer_fee_bps: 0` and assert
// only `fee_usd_cents`; these exercise the in-token bps deduction path.
// `dest_gas_overhead` must be non-zero (config validation rejects 0 → #321).
// ----------------------------------------------------------------
const SILOED_E18: i128 = 1_000_000_000_000_000_000;
const SILOED_WAIT_FOR_SAFE: u32 = 1 << 16; // 0x00010000

fn apply_siloed_bps_fee_config(t: &TestEnv, finality_bps: u32, fast_bps: u32) {
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
        &t.env,
        [TokenTransferFeeConfigArgs {
            dest_chain_selector: REMOTE_CHAIN,
            config,
        }],
    );
    t.pool_client
        .apply_token_fee_config_updates(&adds, &Vec::new(&t.env));
}

#[test]
fn get_fee_siloed_fast_finality_bps_selection() {
    let t = setup();
    t.pool_client
        .set_allowed_finality_config(&SILOED_WAIT_FOR_SAFE);
    // Distinct USD-cent AND bps values per finality mode, so both fields'
    // selection can be asserted (`apply_siloed_bps_fee_config` sets USD cents to 0).
    let adds = Vec::from_array(
        &t.env,
        [TokenTransferFeeConfigArgs {
            dest_chain_selector: REMOTE_CHAIN,
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
    t.pool_client
        .apply_token_fee_config_updates(&adds, &Vec::new(&t.env));

    // Default finality → finality_* fields (mirrors test_applyFee_DefaultFinality).
    let default = t.pool_client.get_fee(
        &REMOTE_CHAIN,
        &(1_000 * SILOED_E18),
        &0u32,
        &Bytes::new(&t.env),
    );
    assert_eq!(default.token_fee_bps, 250);
    assert_eq!(default.fee_usd_cents, 40);
    assert!(default.is_enabled);

    // Fast finality → fast_finality_* fields (mirrors test_applyFee_CustomFinality).
    let fast = t.pool_client.get_fee(
        &REMOTE_CHAIN,
        &(1_000 * SILOED_E18),
        &SILOED_WAIT_FOR_SAFE,
        &Bytes::new(&t.env),
    );
    assert_eq!(fast.token_fee_bps, 500);
    assert_eq!(fast.fee_usd_cents, 80);
    assert!(fast.is_enabled);
}

// ----------------------------------------------------------------
// Source-side pool finality minimum — siloed lock-release pool.
//
// The siloed pool enforces the same source-side finality minimum as the
// canonical lock-release pool (`consume_outbound_rate_limit` →
// `ensure_requested_finality_allowed`). This closes the gap where the siloed
// pool had inbound/fee-selection coverage but no outbound reject test: a user
// requesting FASTER finality (fewer confirmations) than the issuer-configured
// minimum must revert on source with InvalidRequestedFinality (#315).
// ----------------------------------------------------------------
#[test]
fn siloed_outbound_block_depth_faster_than_minimum_reverts() {
    let t = setup();

    // Issuer sets a minimum of 10 source-chain confirmations for all lanes
    // from this source (pool-wide `allowed_finality_config`, EVM parity).
    t.pool_client.set_allowed_finality_config(&10u32);

    let sender = Address::generate(&t.env);
    t.sac.mint(&sender, &(1_000 * SILOED_E18));

    // User requests only 5 confirmations — faster than the 10-confirmation
    // minimum ⇒ source revert with InvalidRequestedFinality (#315).
    let r = t.pool_client.try_lock_or_burn(
        &t.auth_onramp,
        &LockOrBurnIn {
            receiver: Bytes::from_slice(&t.env, &[0x01; 20]),
            remote_chain_selector: REMOTE_CHAIN,
            original_sender: sender,
            amount: 100 * SILOED_E18,
            local_token: t.token_addr.clone(),
        },
        &5u32,
        &Bytes::new(&t.env),
    );
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::InvalidRequestedFinality);
}

#[test]
fn lock_or_burn_siloed_nonzero_bps_fee() {
    let t = setup();
    apply_siloed_bps_fee_config(&t, 100, 0);

    let sender = Address::generate(&t.env);
    let amount: i128 = 1_000 * SILOED_E18; // 1000e18 (EVM parity)
    t.sac.mint(&sender, &amount);

    let input = LockOrBurnIn {
        receiver: Bytes::from_slice(&t.env, &[0x01; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount,
        local_token: t.token_addr.clone(),
    };
    let out = t
        .pool_client
        .lock_or_burn(&t.auth_onramp, &input, &0, &Bytes::new(&t.env));

    // fee = 1000e18 * 100 / 10000 = 10e18; dest = 990e18.
    let fee: i128 = 10 * SILOED_E18;
    let dest: i128 = 990 * SILOED_E18;
    assert_eq!(out.dest_token_amount, dest);

    let pool_addr = t.pool_client.address.clone();
    // The full amount is pulled to the pool; dest is escrowed in the siloed
    // lockbox, leaving only the fee on the pool's own balance.
    assert_eq!(t.tc.balance(&t.lockbox_client.address), dest);
    assert_eq!(t.tc.balance(&pool_addr), fee);
    assert_eq!(t.tc.balance(&sender), 0);
}

#[test]
fn lock_or_burn_siloed_dust_amount_zero_fee() {
    let t = setup();
    apply_siloed_bps_fee_config(&t, 250, 0);

    let sender = Address::generate(&t.env);
    let amount: i128 = 39; // 39 * 250 / 10000 = 0 (floor) — mirrors test_applyFee_RoundsFeeDownToZeroOnDustAmounts
    t.sac.mint(&sender, &amount);

    let input = LockOrBurnIn {
        receiver: Bytes::from_slice(&t.env, &[0x01; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount,
        local_token: t.token_addr.clone(),
    };
    let out = t
        .pool_client
        .lock_or_burn(&t.auth_onramp, &input, &0, &Bytes::new(&t.env));
    assert_eq!(out.dest_token_amount, 39);

    let pool_addr = t.pool_client.address.clone();
    // No fee accrues; the full dust amount is escrowed in the lockbox.
    assert_eq!(t.tc.balance(&t.lockbox_client.address), 39);
    assert_eq!(t.tc.balance(&pool_addr), 0);
    assert_eq!(t.tc.balance(&sender), 0);
}

// ================================================================
// `get_required_ccvs` (EVM `TokenPool.getRequiredCCVs`)
// ================================================================

#[test]
fn get_required_ccvs_empty_without_hooks() {
    let t = setup();
    let v = t.pool_client.get_required_ccvs(
        &t.token_addr,
        &REMOTE_CHAIN,
        &100i128,
        &0u32,
        &Bytes::new(&t.env),
        &MessageDirection::Outbound,
    );
    assert_eq!(v.ccvs.len(), 0);
    assert!(
        v.include_defaults,
        "pools without hooks should fall back to lane defaults"
    );
}

#[test]
fn get_required_ccvs_delegates_to_hooks() {
    let t = setup();
    let hooks_id = t.env.register(mock_hooks::MockReturnsCcv, ());
    let hooks_client = mock_hooks::MockReturnsCcvClient::new(&t.env, &hooks_id);
    let expected_ccv = Address::generate(&t.env);
    hooks_client.set_returned_ccv(&expected_ccv);

    t.pool_client.set_advanced_pool_hooks(&hooks_id.clone());

    let v = t.pool_client.get_required_ccvs(
        &t.token_addr,
        &REMOTE_CHAIN,
        &100i128,
        &0u32,
        &Bytes::new(&t.env),
        &MessageDirection::Inbound,
    );
    assert_eq!(v.ccvs.len(), 1);
    assert_eq!(v.ccvs.get(0).unwrap(), expected_ccv);
    assert!(!v.include_defaults);
}

/// Proves the sender-supplied `token_args` (extra args → OnRamp →
/// `lock_or_burn`) reaches the advanced-pool-hooks `preflight_check`
/// byte-for-byte (EVM `IAdvancedPoolHooks.preflightCheck` parity). `setup()`
/// already wires a lockbox for `REMOTE_CHAIN`, so a successful preflight lets
/// `lock_or_burn` escrow the tokens there.
#[test]
fn test_preflight_hook_receives_token_args() {
    let t = setup();

    let hooks_id = t.env.register(mock_hooks::MockCapturesTokenArgs, ());
    let hooks_client = mock_hooks::MockCapturesTokenArgsClient::new(&t.env, &hooks_id);
    t.pool_client.set_advanced_pool_hooks(&hooks_id.clone());

    let sender = Address::generate(&t.env);
    t.sac.mint(&sender, &1_000_000_000);

    let token_args = Bytes::from_array(&t.env, &[0xde, 0xad, 0xbe, 0xef]);

    let lock_input = LockOrBurnIn {
        receiver: Bytes::from_slice(&t.env, &[3u8; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount: 1_000_000_000,
        local_token: t.token_addr.clone(),
    };

    // Empty before the call proves the hook actually ran and captured.
    assert_eq!(hooks_client.get_captured_token_args(), Bytes::new(&t.env));

    t.pool_client
        .lock_or_burn(&t.auth_onramp, &lock_input, &0u32, &token_args);

    // The sender-supplied `token_args` reached the hooks preflight unchanged,
    // and the tokens were escrowed in the lockbox (siloed lock-release parity).
    assert_eq!(hooks_client.get_captured_token_args(), token_args);
    assert_eq!(t.tc.balance(&t.lockbox_client.address), 1_000_000_000);
    assert_eq!(t.tc.balance(&sender), 0);
}

/// H-13 / auth-fix: a caller that is neither the owner nor the fee admin must be
/// rejected with `Unauthorized` (typed error, not a host auth trap) — the
/// identity check runs before `require_auth`. Proves the
/// `require_owner_or_fee_admin` gate is caller-identity-bound.
#[test]
fn test_withdraw_fee_tokens_rejects_unauthorized_caller() {
    let t = setup();

    let stranger = Address::generate(&t.env);
    let recipient = Address::generate(&t.env);
    let r = t.pool_client.try_withdraw_fee_tokens(
        &stranger,
        &Vec::from_array(&t.env, [t.token_addr.clone()]),
        &recipient,
    );
    assert!(r.is_err(), "unauthorized caller must be rejected");
    assert_eq!(r.unwrap_err().unwrap(), CCIPError::Unauthorized);
}

#[test]
fn advanced_pool_hooks_admin_roundtrip() {
    let t = setup();
    assert!(t.pool_client.get_advanced_pool_hooks().is_none());

    let hooks_id = t.env.register(mock_hooks::MockReturnsCcv, ());
    let hooks = hooks_id.clone();
    t.pool_client.set_advanced_pool_hooks(&hooks);
    assert_eq!(t.pool_client.get_advanced_pool_hooks().unwrap(), hooks);

    t.pool_client.remove_advanced_pool_hooks();
    assert!(t.pool_client.get_advanced_pool_hooks().is_none());
}
