#![cfg(test)]

extern crate std;

use soroban_sdk::{testutils::Address as _, token, vec, Address, Bytes, Env, Vec};

use crate::{
    mock_router::{MockRouterContract, MockRouterContractClient},
    LockBoxEntry, SiloedLockReleaseTokenPoolContract, SiloedLockReleaseTokenPoolContractClient,
};
use common_pool::{ChainUpdate, LockOrBurnIn, RateLimitConfig, ReleaseOrMintIn};
use pools_token_lock_box::{TokenLockBox, TokenLockBoxClient};

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
                remote_pool_addresses: Bytes::from_slice(env, &[0xaa; 32]),
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
    on_ramp: Address,
    off_ramp: Address,
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

    let router_id = env.register(MockRouterContract, ());
    let mock_router = MockRouterContractClient::new(&env, &router_id);
    let on_ramp = Address::generate(&env);
    let off_ramp = Address::generate(&env);
    mock_router.set_onramp(&REMOTE_CHAIN, &on_ramp);
    mock_router.add_offramp(&REMOTE_CHAIN, &off_ramp);
    mock_router.set_onramp(&SILOED_CHAIN, &on_ramp);
    mock_router.add_offramp(&SILOED_CHAIN, &off_ramp);

    let pool_id = env.register(SiloedLockReleaseTokenPoolContract, ());
    let pool_client = SiloedLockReleaseTokenPoolContractClient::new(&env, &pool_id);
    pool_client.initialize(&owner, &token_addr, &7, &router_id);

    lockbox_client.add_allowed_callers(&vec![&env, pool_client.address.clone()]);
    add_chain(&env, &pool_client, REMOTE_CHAIN);

    pool_client.configure_lock_boxes(&vec![
        &env,
        LockBoxEntry {
            remote_chain_selector: REMOTE_CHAIN,
            lock_box: lockbox_client.address.clone(),
        },
    ]);

    TestEnv {
        env,
        token_addr,
        pool_client,
        lockbox_client,
        sac,
        tc,
        on_ramp,
        off_ramp,
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
    on_ramp: Address,
    off_ramp: Address,
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

    let router_id = env.register(MockRouterContract, ());
    let mock_router = MockRouterContractClient::new(&env, &router_id);
    let on_ramp = Address::generate(&env);
    let off_ramp = Address::generate(&env);
    mock_router.set_onramp(&REMOTE_CHAIN, &on_ramp);
    mock_router.add_offramp(&REMOTE_CHAIN, &off_ramp);
    mock_router.set_onramp(&SILOED_CHAIN, &on_ramp);
    mock_router.add_offramp(&SILOED_CHAIN, &off_ramp);

    let pool_id = env.register(SiloedLockReleaseTokenPoolContract, ());
    let pool_client = SiloedLockReleaseTokenPoolContractClient::new(&env, &pool_id);
    pool_client.initialize(&owner, &token_addr, &7, &router_id);

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

    MultiLockboxEnv {
        env,
        token_addr,
        pool_client,
        shared_lockbox,
        siloed_lockbox,
        sac,
        tc,
        on_ramp,
        off_ramp,
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
    let out = t.pool_client.lock_or_burn(&t.on_ramp, &input, &0);

    assert_eq!(t.tc.balance(&sender), 500);
    assert_eq!(t.tc.balance(&t.lockbox_client.address), 500);
    assert!(!out.dest_token_address.is_empty());
}

#[test]
fn release_withdraws_from_lockbox() {
    let t = setup();

    let liquidity_provider = Address::generate(&t.env);
    t.sac.mint(&liquidity_provider, &2_000);
    t.lockbox_client
        .add_allowed_callers(&vec![&t.env, liquidity_provider.clone()]);
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
    let out = t.pool_client.release_or_mint(&t.off_ramp, &input, &0);

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

    let router_id = env.register(MockRouterContract, ());
    let mock_router = MockRouterContractClient::new(&env, &router_id);
    let on_ramp = Address::generate(&env);
    mock_router.set_onramp(&REMOTE_CHAIN, &on_ramp);

    let pool_id = env.register(SiloedLockReleaseTokenPoolContract, ());
    let pool_client = SiloedLockReleaseTokenPoolContractClient::new(&env, &pool_id);
    pool_client.initialize(&owner, &token_addr, &7, &router_id);

    let remote_pool = Bytes::from_slice(&env, &[0xaa; 32]);
    let remote_token = Bytes::from_slice(&env, &[0xbb; 32]);
    pool_client.apply_chain_updates(
        &vec![
            &env,
            ChainUpdate {
                remote_chain_selector: REMOTE_CHAIN,
                remote_pool_addresses: remote_pool,
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
    let r = pool_client.try_lock_or_burn(&on_ramp, &input, &0);
    assert!(r.is_err());
}

#[test]
fn many_to_one_lockbox_shared_liquidity() {
    let t = setup();

    add_chain(&t.env, &t.pool_client, SILOED_CHAIN);
    t.pool_client.configure_lock_boxes(&vec![
        &t.env,
        LockBoxEntry {
            remote_chain_selector: SILOED_CHAIN,
            lock_box: t.lockbox_client.address.clone(),
        },
    ]);

    let sender = Address::generate(&t.env);
    t.sac.mint(&sender, &1_000);

    let input_a = LockOrBurnIn {
        receiver: Bytes::from_slice(&t.env, &[0x01; 20]),
        remote_chain_selector: REMOTE_CHAIN,
        original_sender: sender.clone(),
        amount: 300,
        local_token: t.token_addr.clone(),
    };
    t.pool_client.lock_or_burn(&t.on_ramp, &input_a, &0);

    let input_b = LockOrBurnIn {
        receiver: Bytes::from_slice(&t.env, &[0x02; 20]),
        remote_chain_selector: SILOED_CHAIN,
        original_sender: sender.clone(),
        amount: 200,
        local_token: t.token_addr.clone(),
    };
    t.pool_client.lock_or_burn(&t.on_ramp, &input_b, &0);

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
    let expected = soroban_sdk::String::from_str(&t.env, "SiloedLockReleaseTokenPool 1.0.0");
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
    let pool_id = env.register(SiloedLockReleaseTokenPoolContract, ());
    let pool_client = SiloedLockReleaseTokenPoolContractClient::new(&env, &pool_id);
    pool_client.initialize(&owner, &pool_token, &7, &router);
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
    m.pool_client.lock_or_burn(&m.on_ramp, &lock_shared, &0);

    let lock_siloed = LockOrBurnIn {
        receiver: Bytes::from_slice(&m.env, &[0x02; 20]),
        remote_chain_selector: SILOED_CHAIN,
        original_sender: sender.clone(),
        amount: 400,
        local_token: m.token_addr.clone(),
    };
    m.pool_client.lock_or_burn(&m.on_ramp, &lock_siloed, &0);

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
        &m.on_ramp,
        &LockOrBurnIn {
            receiver: Bytes::from_slice(&m.env, &[0x01; 20]),
            remote_chain_selector: SILOED_CHAIN,
            original_sender: sender.clone(),
            amount: 1_000,
            local_token: m.token_addr.clone(),
        },
        &0,
    );
    assert_eq!(m.tc.balance(&m.siloed_lockbox.address), 1_000);
    assert_eq!(m.tc.balance(&m.shared_lockbox.address), 0);

    let receiver = Address::generate(&m.env);
    let out = m.pool_client.release_or_mint(
        &m.off_ramp,
        &ReleaseOrMintIn {
            original_sender: Bytes::from_slice(&m.env, &[0xcd; 20]),
            remote_chain_selector: SILOED_CHAIN,
            receiver: receiver.clone(),
            amount: 1_000,
            local_token: m.token_addr.clone(),
            source_pool_address: Bytes::from_slice(&m.env, &[0xaa; 32]),
            source_pool_data: Bytes::new(&m.env),
        },
        &0,
    );

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
        &m.on_ramp,
        &LockOrBurnIn {
            receiver: Bytes::from_slice(&m.env, &[0x01; 20]),
            remote_chain_selector: REMOTE_CHAIN,
            original_sender: sender.clone(),
            amount: 500,
            local_token: m.token_addr.clone(),
        },
        &0,
    );
    assert_eq!(m.tc.balance(&m.shared_lockbox.address), 500);
    assert_eq!(m.tc.balance(&m.siloed_lockbox.address), 0);

    let receiver = Address::generate(&m.env);
    let out = m.pool_client.release_or_mint(
        &m.off_ramp,
        &ReleaseOrMintIn {
            original_sender: Bytes::from_slice(&m.env, &[0xcd; 20]),
            remote_chain_selector: REMOTE_CHAIN,
            receiver: receiver.clone(),
            amount: 500,
            local_token: m.token_addr.clone(),
            source_pool_address: Bytes::from_slice(&m.env, &[0xaa; 32]),
            source_pool_data: Bytes::new(&m.env),
        },
        &0,
    );

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
        &t.on_ramp,
        &LockOrBurnIn {
            receiver: Bytes::from_slice(&t.env, &[0x01; 20]),
            remote_chain_selector: REMOTE_CHAIN,
            original_sender: sender,
            amount: 100,
            local_token: wrong_token,
        },
        &0,
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
        &t.on_ramp,
        &LockOrBurnIn {
            receiver: Bytes::from_slice(&t.env, &[0x01; 20]),
            remote_chain_selector: unknown_chain,
            original_sender: sender,
            amount: 100,
            local_token: t.token_addr.clone(),
        },
        &0,
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
        &m.on_ramp,
        &LockOrBurnIn {
            receiver: Bytes::from_slice(&m.env, &[0x01; 20]),
            remote_chain_selector: SILOED_CHAIN,
            original_sender: sender,
            amount: 100,
            local_token: m.token_addr.clone(),
        },
        &0,
    );

    let receiver = Address::generate(&m.env);
    let r = m.pool_client.try_release_or_mint(
        &m.off_ramp,
        &ReleaseOrMintIn {
            original_sender: Bytes::from_slice(&m.env, &[0xcd; 20]),
            remote_chain_selector: SILOED_CHAIN,
            receiver: receiver,
            amount: 500,
            local_token: m.token_addr.clone(),
            source_pool_address: Bytes::from_slice(&m.env, &[0xaa; 32]),
            source_pool_data: Bytes::new(&m.env),
        },
        &0,
    );
    assert!(r.is_err());
}

#[test]
fn release_rejects_wrong_token() {
    let t = setup();
    let liquidity_provider = Address::generate(&t.env);
    t.sac.mint(&liquidity_provider, &1_000);
    t.lockbox_client
        .add_allowed_callers(&vec![&t.env, liquidity_provider.clone()]);
    t.lockbox_client.deposit(&liquidity_provider, &1_000);

    let other_admin = Address::generate(&t.env);
    let other_contract = t.env.register_stellar_asset_contract_v2(other_admin);
    let wrong_token = other_contract.address();

    let receiver = Address::generate(&t.env);
    let r = t.pool_client.try_release_or_mint(
        &t.off_ramp,
        &ReleaseOrMintIn {
            original_sender: Bytes::from_slice(&t.env, &[0xcd; 20]),
            remote_chain_selector: REMOTE_CHAIN,
            receiver: receiver,
            amount: 100,
            local_token: wrong_token,
            source_pool_address: Bytes::from_slice(&t.env, &[0xaa; 32]),
            source_pool_data: Bytes::new(&t.env),
        },
        &0,
    );
    assert!(r.is_err());
}

#[test]
fn release_rejects_unsupported_chain() {
    let t = setup();
    let receiver = Address::generate(&t.env);
    let unknown_chain: u64 = 12_345;

    let r = t.pool_client.try_release_or_mint(
        &t.off_ramp,
        &ReleaseOrMintIn {
            original_sender: Bytes::from_slice(&t.env, &[0xcd; 20]),
            remote_chain_selector: unknown_chain,
            receiver: receiver,
            amount: 100,
            local_token: t.token_addr.clone(),
            source_pool_address: Bytes::from_slice(&t.env, &[0xaa; 32]),
            source_pool_data: Bytes::new(&t.env),
        },
        &0,
    );
    assert!(r.is_err());
}
