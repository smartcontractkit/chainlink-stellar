#![no_std]

mod constants;
mod crypto;
mod encoding;
mod error;
mod events;
mod types;

pub use error::McmsError;
pub use types::{
    Config, ExpiringRootAndOpCount, MerkleProof, Signature, SignatureVec, Signer, SignerAddresses,
    SignerGroups, StellarOp, StellarRootMetadata, MAX_NUM_SIGNERS, NUM_GROUPS,
};

use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_helpers::soroban_invoke::decode_invoke_args;
use constants::{ENCODING_VERSION, LEDGER_BUMP, LEDGER_THRESHOLD, MAX_ROOT_VALIDITY_SECS};
use crypto::{cmp_bytes32, recover_eth_address_vrs, verify_merkle_proof};
use encoding::{
    contract_id, eth_signed_message_hash_32, hash_root_metadata, hash_set_root_inner,
    hash_stellar_op,
};
use events::{ConfigSetEvent, NewRootEvent, OpExecutedEvent};
use soroban_sdk::{
    contract, contractimpl, symbol_short, Address, BytesN, Env, InvokeError, Map, Symbol, Val, Vec,
};

// --- Storage ---

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
/// Network passphrase hash bytes32 (`chain-selectors` Stellar `ChainID`).
const CHAIN_NETWORK_ID: Symbol = symbol_short!("CHNET");
const CONFIG: Symbol = symbol_short!("MCSCFG");
const CONFIG_VERSION: Symbol = symbol_short!("CFGVER");
/// Immutable deployment-time role label (`PROPOSER`, `CANCELLER`, or `BYPASSER`).
/// Introspection only: never an authorization primitive and never part of leaf hashes.
const INSTANCE_LABEL: Symbol = symbol_short!("INSTLBL");
/// Map padded signer addr -> Signer
const SIGNER_MAP: Symbol = symbol_short!("SIGMAP");
const EXPIRING_ROOT: Symbol = symbol_short!("EXPROOT");
const ROOT_META_STORE: Symbol = symbol_short!("RTMETA");

#[contract]
pub struct McmsContract;

#[contractimpl]
impl Initializable for McmsContract {
    const INITIALIZED: Symbol = INITIALIZED;
}

#[contractimpl(contracttrait)]
impl Ownable for McmsContract {
    const OWNER: Symbol = OWNER;
    const PENDING_OWNER: Symbol = PENDING_OWNER;
}

#[contractimpl]
impl McmsContract {
    /// Initialize MCMS atomically with owner, Stellar network id (32-byte passphrase hash per
    /// `chain-selectors`), the initial signer configuration, and an immutable instance label.
    /// Applying the config here means no deployer needs temporary ownership for `set_config`.
    #[allow(clippy::too_many_arguments)]
    pub fn initialize(
        env: Env,
        owner: Address,
        chain_network_id: BytesN<32>,
        signer_addresses: SignerAddresses,
        signer_groups: SignerGroups,
        group_quorums: BytesN<32>,
        group_parents: BytesN<32>,
        instance_label: Symbol,
    ) -> Result<(), McmsError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Ownable>::init_owner(&env, &owner).map_err(McmsError::from)?;
        <Self as Initializable>::init(&env)?;
        env.storage()
            .instance()
            .set(&CHAIN_NETWORK_ID, &chain_network_id);
        env.storage()
            .instance()
            .set(&INSTANCE_LABEL, &instance_label);
        apply_config(
            &env,
            signer_addresses,
            signer_groups,
            group_quorums,
            group_parents,
            false,
        )
    }

    /// Owner-only signer configuration (mirrors Solidity `setConfig`).
    pub fn set_config(
        env: Env,
        signer_addresses: SignerAddresses,
        signer_groups: SignerGroups,
        group_quorums: BytesN<32>,
        group_parents: BytesN<32>,
        clear_root: bool,
    ) -> Result<(), McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env).map_err(McmsError::from)?;
        apply_config(
            &env,
            signer_addresses,
            signer_groups,
            group_quorums,
            group_parents,
            clear_root,
        )
    }

    pub fn set_root(
        env: Env,
        root: BytesN<32>,
        valid_until: u32,
        metadata: StellarRootMetadata,
        metadata_proof: MerkleProof,
        signatures: SignatureVec,
    ) -> Result<(), McmsError> {
        <Self as Initializable>::require_initialized(&env)?;

        if metadata.encoding_version != ENCODING_VERSION {
            return Err(McmsError::UnsupportedEncodingVersion);
        }

        let network_id: BytesN<32> = env.storage().instance().get(&CHAIN_NETWORK_ID).unwrap();
        if metadata.network_id != network_id {
            return Err(McmsError::WrongChainIdMeta);
        }
        contract_id(&metadata.multisig, McmsError::InvalidMultisig)?;
        let self_address = env.current_contract_address();
        if metadata.multisig != self_address {
            return Err(McmsError::WrongMultiSigMeta);
        }

        let config_version: u64 = env
            .storage()
            .persistent()
            .get(&CONFIG_VERSION)
            .ok_or(McmsError::MissingConfig)?;
        if metadata.config_version != config_version {
            return Err(McmsError::ConfigVersionMismatch);
        }
        if metadata.pre_op_count > metadata.post_op_count {
            return Err(McmsError::WrongPostOpCount);
        }

        let inner = hash_set_root_inner(&env, &root, valid_until);
        let signed_hash = eth_signed_message_hash_32(&env, &inner);

        if env.storage().persistent().has(&signed_hash) {
            return Err(McmsError::SignedHashAlreadySeen);
        }

        let cfg: Config = env
            .storage()
            .persistent()
            .get(&CONFIG)
            .ok_or(McmsError::MissingConfig)?;
        let sig_map: Map<BytesN<32>, Signer> = env.storage().persistent().get(&SIGNER_MAP).unwrap();

        verify_signatures(&env, &cfg, &sig_map, &signed_hash, &signatures.inner)?;

        let now = env.ledger().timestamp();
        if u64::from(valid_until) < now {
            return Err(McmsError::ValidUntilHasAlreadyPassed);
        }
        let max_valid = now.saturating_add(MAX_ROOT_VALIDITY_SECS);
        if u64::from(valid_until) > max_valid {
            return Err(McmsError::ValidUntilExceedsMaximum);
        }

        let hashed_leaf = hash_root_metadata(&env, &metadata)?;
        if !verify_merkle_proof(&env, &root, &hashed_leaf, metadata_proof.inner) {
            return Err(McmsError::ProofCannotBeVerified);
        }

        let exp: ExpiringRootAndOpCount =
            env.storage()
                .persistent()
                .get(&EXPIRING_ROOT)
                .unwrap_or(ExpiringRootAndOpCount {
                    root: BytesN::from_array(&env, &[0u8; 32]),
                    valid_until: 0,
                    op_count: 0,
                });

        let stored_meta: StellarRootMetadata = env
            .storage()
            .persistent()
            .get(&ROOT_META_STORE)
            .unwrap_or(StellarRootMetadata {
                network_id: network_id.clone(),
                multisig: self_address,
                pre_op_count: 0,
                post_op_count: 0,
                override_previous_root: false,
                config_version,
                encoding_version: ENCODING_VERSION,
            });

        let op_count = exp.op_count;
        if op_count != stored_meta.post_op_count && !metadata.override_previous_root {
            return Err(McmsError::PendingOps);
        }
        if op_count != metadata.pre_op_count {
            return Err(McmsError::WrongPreOpCount);
        }
        env.storage().persistent().set(&signed_hash, &true);
        env.storage()
            .persistent()
            .extend_ttl(&signed_hash, LEDGER_THRESHOLD, LEDGER_BUMP);

        env.storage().persistent().set(
            &EXPIRING_ROOT,
            &ExpiringRootAndOpCount {
                root: root.clone(),
                valid_until,
                op_count: metadata.pre_op_count,
            },
        );
        env.storage().persistent().set(&ROOT_META_STORE, &metadata);

        NewRootEvent {
            root,
            valid_until,
            metadata,
        }
        .publish(&env);
        bump_ttls(&env);
        Ok(())
    }

    pub fn execute(env: Env, op: StellarOp, proof: MerkleProof) -> Result<(), McmsError> {
        <Self as Initializable>::require_initialized(&env)?;

        if op.encoding_version != ENCODING_VERSION {
            return Err(McmsError::UnsupportedEncodingVersion);
        }

        let meta: StellarRootMetadata = env
            .storage()
            .persistent()
            .get(&ROOT_META_STORE)
            .ok_or(McmsError::MissingRootMetadata)?;
        let mut exp: ExpiringRootAndOpCount = env
            .storage()
            .persistent()
            .get(&EXPIRING_ROOT)
            .unwrap_or(ExpiringRootAndOpCount {
                root: BytesN::from_array(&env, &[0u8; 32]),
                valid_until: 0,
                op_count: 0,
            });

        if meta.post_op_count <= exp.op_count {
            return Err(McmsError::PostOpCountReached);
        }

        let network_id: BytesN<32> = env.storage().instance().get(&CHAIN_NETWORK_ID).unwrap();
        if op.network_id != network_id {
            return Err(McmsError::WrongChainIdOp);
        }

        contract_id(&op.multisig, McmsError::InvalidMultisig)?;
        if op.multisig != env.current_contract_address() {
            return Err(McmsError::WrongMultiSigOp);
        }
        contract_id(&op.target, McmsError::InvalidTarget)?;

        let config_version: u64 = env
            .storage()
            .persistent()
            .get(&CONFIG_VERSION)
            .ok_or(McmsError::MissingConfig)?;
        if meta.config_version != config_version {
            return Err(McmsError::ConfigVersionMismatch);
        }

        let now = env.ledger().timestamp();
        if now > u64::from(exp.valid_until) {
            return Err(McmsError::RootExpired);
        }

        if op.nonce != exp.op_count {
            return Err(McmsError::WrongNonce);
        }

        let args = decode_invoke_args(&env, &op.args_xdr).map_err(|_| McmsError::InvalidArgsXdr)?;
        let leaf = hash_stellar_op(&env, &op)?;

        if !verify_merkle_proof(&env, &exp.root, &leaf, proof.inner) {
            return Err(McmsError::ProofCannotBeVerified);
        }

        exp.op_count = exp
            .op_count
            .checked_add(1)
            .ok_or(McmsError::NonceOverflow)?;

        // Persist before handing control to the target so a reentrant `execute` observes the
        // incremented nonce. If the downstream call fails, returning `Err` from this top-level
        // invocation rolls this write back, so a failed operation still does not consume a nonce.
        env.storage().persistent().set(&EXPIRING_ROOT, &exp);

        // A callee-returned contract error arrives as `Err(Ok(InvokeError::Contract(_)))`;
        // the outer `Ok(Err(_))` arm only covers return-value conversion failures.
        match env.try_invoke_contract::<Val, InvokeError>(&op.target, &op.function, args) {
            Ok(Ok(_)) => {}
            Err(Ok(InvokeError::Contract(_))) => return Err(McmsError::CallReverted),
            Ok(Err(_)) | Err(_) => return Err(McmsError::CallAborted),
        }

        let args_hash: BytesN<32> = env.crypto().keccak256(&op.args_xdr).into();
        OpExecutedEvent {
            nonce: op.nonce,
            target: op.target,
            function: op.function,
            args_hash,
        }
        .publish(&env);
        bump_ttls(&env);
        Ok(())
    }

    // --- getters ---

    pub fn get_config(env: Env) -> Result<Config, McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .persistent()
            .get(&CONFIG)
            .ok_or(McmsError::MissingConfig)
    }

    pub fn get_config_version(env: Env) -> Result<u64, McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .persistent()
            .get(&CONFIG_VERSION)
            .ok_or(McmsError::MissingConfig)
    }

    pub fn get_op_count(env: Env) -> Result<u64, McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        let exp: ExpiringRootAndOpCount =
            env.storage()
                .persistent()
                .get(&EXPIRING_ROOT)
                .unwrap_or(ExpiringRootAndOpCount {
                    root: BytesN::from_array(&env, &[0u8; 32]),
                    valid_until: 0,
                    op_count: 0,
                });
        Ok(exp.op_count)
    }

    pub fn get_root(env: Env) -> Result<(BytesN<32>, u32), McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        let exp: ExpiringRootAndOpCount =
            env.storage()
                .persistent()
                .get(&EXPIRING_ROOT)
                .unwrap_or(ExpiringRootAndOpCount {
                    root: BytesN::from_array(&env, &[0u8; 32]),
                    valid_until: 0,
                    op_count: 0,
                });
        Ok((exp.root, exp.valid_until))
    }

    pub fn get_root_metadata(env: Env) -> Result<StellarRootMetadata, McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .persistent()
            .get(&ROOT_META_STORE)
            .ok_or(McmsError::MissingRootMetadata)
    }

    pub fn chain_network_id(env: Env) -> Result<BytesN<32>, McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&CHAIN_NETWORK_ID)
            .ok_or(McmsError::NotInitialized)
    }

    /// Immutable deployment-time role label (`PROPOSER`, `CANCELLER`, or `BYPASSER`).
    /// Introspection only; never used for authorization.
    pub fn get_instance_label(env: Env) -> Result<Symbol, McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        env.storage()
            .instance()
            .get(&INSTANCE_LABEL)
            .ok_or(McmsError::NotInitialized)
    }

    /// Permissionless TTL extension for fixed persistent keys and instance storage.
    /// Per-hash seen entries are extended at creation. Anyone may call this.
    ///
    /// # Restoring an archived `SeenHash` entry
    ///
    /// `SeenHash(h)` entries are bumped once at creation in [`Self::set_root`] and are
    /// **never** swept by `bump_ttls` (they are not enumerable from inside the contract).
    /// Once their TTL elapses they are archived. Starting with protocol 23, a normal
    /// simulation-prepared invocation includes touched archived entries in its restore list,
    /// so they are automatically restored before guest code runs. A manually constructed
    /// transaction without the required restore list fails before contract execution.
    ///
    /// Replay safety follows directly from restoration: `set_root` observes the original
    /// marker and returns `SignedHashAlreadySeen`. The fixed root-validity horizon is an
    /// independent policy bound, not the replay-protection mechanism.
    ///
    /// There is **no** guest-side "restore seen hash" entrypoint, and one cannot exist:
    /// Soroban does not expose a `restore` host function to contracts. Restoration is a
    /// host-level operation. RPC simulation normally prepares automatic restoration;
    /// `RestoreFootprintOp` remains available for exceptional workflows and fee management.
    pub fn extend_all_ttls(env: Env) -> Result<(), McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        bump_ttls(&env);
        Ok(())
    }
}

fn apply_config(
    env: &Env,
    signer_addresses: SignerAddresses,
    signer_groups: SignerGroups,
    group_quorums: BytesN<32>,
    group_parents: BytesN<32>,
    clear_root: bool,
) -> Result<(), McmsError> {
    let signer_addresses = signer_addresses.inner;
    let signer_groups = signer_groups.inner;

    let len = signer_addresses.len();
    if len == 0 || len > MAX_NUM_SIGNERS {
        return Err(McmsError::OutOfBoundsNumOfSigners);
    }
    if len != signer_groups.len() {
        return Err(McmsError::SignerGroupsLengthMismatch);
    }

    validate_group_tree(env, &signer_groups, &group_quorums, &group_parents)?;

    let mut sig_map: Map<BytesN<32>, Signer> = Map::new(env);
    let mut prev: Option<BytesN<32>> = None;
    let mut i = 0u32;
    while i < len {
        let addr = signer_addresses.get(i).unwrap();
        if let Some(ref p) = prev {
            if cmp_bytes32(p, &addr) >= 0 {
                return Err(McmsError::SignersAddressesMustBeStrictlyIncreasing);
            }
        }
        let grp = signer_groups.get(i).unwrap();
        if grp >= NUM_GROUPS {
            return Err(McmsError::OutOfBoundsGroup);
        }
        let signer = Signer {
            addr: addr.clone(),
            index: i,
            group: grp,
        };
        sig_map.set(addr.clone(), signer);
        prev = Some(addr.clone());
        i += 1;
    }

    let cfg = Config {
        signers: collect_signers_vec(env, &signer_addresses, &signer_groups)?,
        group_quorums,
        group_parents,
    };

    let config_version = env
        .storage()
        .persistent()
        .get::<_, u64>(&CONFIG_VERSION)
        .unwrap_or(0)
        .checked_add(1)
        .ok_or(McmsError::ConfigVersionOverflow)?;

    env.storage().persistent().set(&CONFIG, &cfg);
    env.storage().persistent().set(&SIGNER_MAP, &sig_map);
    env.storage()
        .persistent()
        .set(&CONFIG_VERSION, &config_version);

    if clear_root {
        let exp: ExpiringRootAndOpCount =
            env.storage()
                .persistent()
                .get(&EXPIRING_ROOT)
                .unwrap_or(ExpiringRootAndOpCount {
                    root: BytesN::from_array(env, &[0u8; 32]),
                    valid_until: 0,
                    op_count: 0,
                });
        let oc = exp.op_count;
        let meta = StellarRootMetadata {
            network_id: env.storage().instance().get(&CHAIN_NETWORK_ID).unwrap(),
            multisig: env.current_contract_address(),
            pre_op_count: oc,
            post_op_count: oc,
            override_previous_root: true,
            config_version,
            encoding_version: ENCODING_VERSION,
        };
        env.storage().persistent().set(
            &EXPIRING_ROOT,
            &ExpiringRootAndOpCount {
                root: BytesN::from_array(env, &[0u8; 32]),
                valid_until: 0,
                op_count: oc,
            },
        );
        env.storage().persistent().set(&ROOT_META_STORE, &meta);
    }

    ConfigSetEvent {
        config: cfg,
        config_version,
        is_root_cleared: clear_root,
    }
    .publish(env);
    bump_ttls(env);
    Ok(())
}

fn collect_signers_vec(
    env: &Env,
    addrs: &Vec<BytesN<32>>,
    groups: &Vec<u32>,
) -> Result<Vec<Signer>, McmsError> {
    let mut out = Vec::new(env);
    let mut i = 0u32;
    while i < addrs.len() {
        out.push_back(Signer {
            addr: addrs.get(i).unwrap(),
            index: i,
            group: groups.get(i).unwrap(),
        });
        i += 1;
    }
    Ok(out)
}

fn validate_group_tree(
    env: &Env,
    signer_groups: &Vec<u32>,
    group_quorums: &BytesN<32>,
    group_parents: &BytesN<32>,
) -> Result<(), McmsError> {
    let mut group_children_counts = [0u32; 32];

    let mut i = 0u32;
    while i < signer_groups.len() {
        let g = signer_groups.get(i).unwrap() as usize;
        if g >= 32 {
            return Err(McmsError::OutOfBoundsGroup);
        }
        group_children_counts[g] += 1;
        i += 1;
    }

    let gq = group_quorums.to_array();
    let gp = group_parents.to_array();

    let mut j = 0usize;
    while j < NUM_GROUPS as usize {
        let idx = NUM_GROUPS as usize - 1 - j;
        if (idx != 0 && gp[idx] as usize >= idx) || (idx == 0 && gp[idx] != 0) {
            return Err(McmsError::GroupTreeNotWellFormed);
        }
        let disabled = gq[idx] == 0;
        if disabled {
            if group_children_counts[idx] > 0 {
                return Err(McmsError::SignerInDisabledGroup);
            }
        } else {
            if group_children_counts[idx] < gq[idx] as u32 {
                return Err(McmsError::OutOfBoundsGroupQuorum);
            }
            let parent = gp[idx] as usize;
            group_children_counts[parent] += 1;
        }
        j += 1;
    }

    let _ = env;
    Ok(())
}

fn verify_signatures(
    env: &Env,
    cfg: &Config,
    sig_map: &Map<BytesN<32>, Signer>,
    signed_hash: &BytesN<32>,
    signatures: &Vec<Signature>,
) -> Result<(), McmsError> {
    let gq = cfg.group_quorums.to_array();
    let gp = cfg.group_parents.to_array();

    if gq[0] == 0 {
        return Err(McmsError::MissingConfig);
    }

    let mut group_vote_counts: Map<u32, u32> = Map::new(env);
    let mut prev: Option<BytesN<32>> = None;

    let mut i = 0u32;
    while i < signatures.len() {
        let sig = signatures.get(i).unwrap();
        let recovered = recover_eth_address_vrs(env, signed_hash, sig.v, &sig.r, &sig.s)?;

        if let Some(ref p) = prev {
            if cmp_bytes32(p, &recovered) >= 0 {
                return Err(McmsError::SignersAddressesMustBeStrictlyIncreasingSigs);
            }
        }
        prev = Some(recovered.clone());

        let signer = sig_map
            .get(recovered.clone())
            .ok_or(McmsError::InvalidSigner)?;

        let mut group = signer.group;
        loop {
            let cv = group_vote_counts.get(group).unwrap_or(0);
            group_vote_counts.set(group, cv + 1);
            let cur = group_vote_counts.get(group).unwrap();

            if cur != gq[group as usize] as u32 {
                break;
            }
            if group == 0 {
                break;
            }
            group = gp[group as usize] as u32;
        }

        i += 1;
    }

    let root_votes = group_vote_counts.get(0).unwrap_or(0);
    if root_votes < gq[0] as u32 {
        return Err(McmsError::InsufficientSigners);
    }

    Ok(())
}

/// Extend the TTL of fixed persistent storage keys and instance storage.
///
/// Per-hash seen entries (persistent key = signed hash as `BytesN<32>`) are NOT enumerable here; each entry
/// receives its TTL at creation time in `set_root` and can be individually restored if
/// archived.  This helper covers the fixed keys (CONFIG, SIGNER_MAP, EXPIRING_ROOT,
/// ROOT_META_STORE, CONFIG_VERSION) and is called at the end of every successful mutating public
/// function so that
/// normal contract activity is sufficient to keep them alive.
fn bump_ttls(env: &Env) {
    env.storage()
        .instance()
        .extend_ttl(LEDGER_THRESHOLD, LEDGER_BUMP);
    if env.storage().persistent().has(&CONFIG) {
        env.storage()
            .persistent()
            .extend_ttl(&CONFIG, LEDGER_THRESHOLD, LEDGER_BUMP);
    }
    if env.storage().persistent().has(&CONFIG_VERSION) {
        env.storage()
            .persistent()
            .extend_ttl(&CONFIG_VERSION, LEDGER_THRESHOLD, LEDGER_BUMP);
    }
    if env.storage().persistent().has(&SIGNER_MAP) {
        env.storage()
            .persistent()
            .extend_ttl(&SIGNER_MAP, LEDGER_THRESHOLD, LEDGER_BUMP);
    }
    if env.storage().persistent().has(&EXPIRING_ROOT) {
        env.storage()
            .persistent()
            .extend_ttl(&EXPIRING_ROOT, LEDGER_THRESHOLD, LEDGER_BUMP);
    }
    if env.storage().persistent().has(&ROOT_META_STORE) {
        env.storage()
            .persistent()
            .extend_ttl(&ROOT_META_STORE, LEDGER_THRESHOLD, LEDGER_BUMP);
    }
}

#[cfg(test)]
mod test;
