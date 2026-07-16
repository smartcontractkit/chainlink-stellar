#![no_std]

mod abi_encoding;
mod constants;
mod crypto;
mod error;
mod events;
mod types;

pub use error::McmsError;
pub use types::{
    Config, ExpiringRootAndOpCount, MerkleProof, Signature, SignatureVec, Signer, SignerAddresses,
    SignerGroups, StellarOp, StellarRootMetadata, MAX_NUM_SIGNERS, NUM_GROUPS,
};

use abi_encoding::{
    eth_signed_message_hash_32, hash_root_metadata, hash_set_root_inner, hash_stellar_op,
};
use common_authorization::Ownable;
use common_error::CCIPError;
use common_guard::initializable::Initializable;
use common_helpers::soroban_invoke::decode_invoke_payload;
use constants::{
    domain_meta, domain_op, LEDGER_BUMP, LEDGER_THRESHOLD, MAX_ROOT_VALIDITY_SECS,
    MIN_SECS_PER_LEDGER_DEFAULT, MIN_SECS_PER_LEDGER_LOWER_BOUND, MIN_SECS_PER_LEDGER_UPPER_BOUND,
    SEEN_TTL_SAFETY_MARGIN_SECS,
};
use crypto::{cmp_bytes32, recover_eth_address_vrs, verify_merkle_proof};
use events::{ConfigSetEvent, MinSecsPerLedgerSetEvent, NewRootEvent, OpExecutedEvent};
use soroban_sdk::{
    address_payload::AddressPayload, contract, contractimpl, symbol_short, Address, BytesN, Env,
    InvokeError, Map, Symbol, Val, Vec,
};
use stellar_strkey::Contract as StrkeyContract;

// --- Storage ---

const INITIALIZED: Symbol = symbol_short!("INIT");
const OWNER: Symbol = symbol_short!("OWNER");
const PENDING_OWNER: Symbol = symbol_short!("PNDGOWNR");
/// Network passphrase hash bytes32 (`chain-selectors` Stellar `ChainID`).
const CHAIN_NETWORK_ID: Symbol = symbol_short!("CHNET");
const CONFIG: Symbol = symbol_short!("MCSCFG");
/// Map padded signer addr -> Signer
const SIGNER_MAP: Symbol = symbol_short!("SIGMAP");
const EXPIRING_ROOT: Symbol = symbol_short!("EXPROOT");
const ROOT_META_STORE: Symbol = symbol_short!("RTMETA");
/// Operator-configured pessimistic floor on seconds-per-ledger, used to derive the dynamic
/// `valid_until` cap in `set_root`. Persistent so it survives entry archival like every other
/// fixed key, and bumped via [`bump_ttls`] on normal contract activity.
const MIN_SPL: Symbol = symbol_short!("MINSPL");

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
    /// Initialize MCMS with owner and Stellar network id (32-byte passphrase hash per `chain-selectors`).
    pub fn initialize(
        env: Env,
        owner: Address,
        chain_network_id: BytesN<32>,
    ) -> Result<(), McmsError> {
        <Self as Initializable>::require_not_initialized(&env)?;
        <Self as Ownable>::init_owner(&env, &owner).map_err(McmsError::from)?;
        <Self as Initializable>::init(&env)?;
        env.storage()
            .instance()
            .set(&CHAIN_NETWORK_ID, &chain_network_id);
        bump_ttls(&env);
        Ok(())
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

        let signer_addresses = signer_addresses.inner;
        let signer_groups = signer_groups.inner;

        let len = signer_addresses.len();
        if len == 0 || len > MAX_NUM_SIGNERS {
            return Err(McmsError::OutOfBoundsNumOfSigners);
        }
        if len != signer_groups.len() {
            return Err(McmsError::SignerGroupsLengthMismatch);
        }

        validate_group_tree(&env, &signer_groups, &group_quorums, &group_parents)?;

        let mut sig_map: Map<BytesN<32>, Signer> = Map::new(&env);
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
            signers: collect_signers_vec(&env, &signer_addresses, &signer_groups)?,
            group_quorums,
            group_parents,
        };

        env.storage().persistent().set(&CONFIG, &cfg);
        env.storage().persistent().set(&SIGNER_MAP, &sig_map);

        if clear_root {
            let exp: ExpiringRootAndOpCount = env
                .storage()
                .persistent()
                .get(&EXPIRING_ROOT)
                .unwrap_or(ExpiringRootAndOpCount {
                    root: BytesN::from_array(&env, &[0u8; 32]),
                    valid_until: 0,
                    op_count: 0,
                });
            let oc = exp.op_count;
            let self_id = contract_id_of_address(&env.current_contract_address());
            let meta = StellarRootMetadata {
                chain_id: env.storage().instance().get(&CHAIN_NETWORK_ID).unwrap(),
                multisig: self_id,
                pre_op_count: oc,
                post_op_count: oc,
                override_previous_root: true,
            };
            env.storage().persistent().set(
                &EXPIRING_ROOT,
                &ExpiringRootAndOpCount {
                    root: BytesN::from_array(&env, &[0u8; 32]),
                    valid_until: 0,
                    op_count: oc,
                },
            );
            env.storage().persistent().set(&ROOT_META_STORE, &meta);
        }

        ConfigSetEvent {
            config: cfg,
            is_root_cleared: clear_root,
        }
        .publish(&env);
        bump_ttls(&env);
        Ok(())
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
        let effective_max_secs = effective_max_root_validity_secs(&env);
        let max_valid = now.saturating_add(effective_max_secs);
        if u64::from(valid_until) > max_valid {
            return Err(McmsError::ValidUntilExceedsMaximum);
        }

        let dm = domain_meta(&env);
        let hashed_leaf = hash_root_metadata(&env, &dm, &metadata)?;
        if !verify_merkle_proof(&env, &root, &hashed_leaf, metadata_proof.inner) {
            return Err(McmsError::ProofCannotBeVerified);
        }

        let chain_net: BytesN<32> = env.storage().instance().get(&CHAIN_NETWORK_ID).unwrap();
        if metadata.chain_id != chain_net {
            return Err(McmsError::WrongChainIdMeta);
        }

        let self_id = contract_id_of_address(&env.current_contract_address());
        if metadata.multisig != self_id {
            return Err(McmsError::WrongMultiSigMeta);
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
                chain_id: chain_net.clone(),
                multisig: self_id.clone(),
                pre_op_count: 0,
                post_op_count: 0,
                override_previous_root: false,
            });

        let op_count = exp.op_count;
        if op_count != stored_meta.post_op_count && !metadata.override_previous_root {
            return Err(McmsError::PendingOps);
        }
        if op_count != metadata.pre_op_count {
            return Err(McmsError::WrongPreOpCount);
        }
        if metadata.pre_op_count > metadata.post_op_count {
            return Err(McmsError::WrongPostOpCount);
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

        let chain_net: BytesN<32> = env.storage().instance().get(&CHAIN_NETWORK_ID).unwrap();
        if op.chain_id != chain_net {
            return Err(McmsError::WrongChainIdOp);
        }

        let self_id = contract_id_of_address(&env.current_contract_address());
        if op.multisig != self_id {
            return Err(McmsError::WrongMultiSigOp);
        }

        let now = env.ledger().timestamp();
        if now > u64::from(exp.valid_until) {
            return Err(McmsError::RootExpired);
        }

        if op.nonce != exp.op_count {
            return Err(McmsError::WrongNonce);
        }

        if op.value != BytesN::from_array(&env, &[0u8; 32]) {
            return Err(McmsError::NonZeroValue);
        }

        let d = domain_op(&env);
        let leaf = hash_stellar_op(&env, &d, &op)?;

        if !verify_merkle_proof(&env, &exp.root, &leaf, proof.inner) {
            return Err(McmsError::ProofCannotBeVerified);
        }

        exp.op_count = exp
            .op_count
            .checked_add(1)
            .ok_or(McmsError::NonceOverflow)?;

        let target = contract_address_from_contract_id(&env, &op.to);
        let (fn_sym, args) =
            decode_invoke_payload(&env, &op.data).map_err(|_| McmsError::InvalidInvokeData)?;

        // Persist the incremented op_count BEFORE the downstream invoke, mirroring ManyChainMultiSig
        // ("increase the counter *before* execution to prevent reentrancy issues"). A reentrant
        // `execute` for the same nonce then reads the already-incremented op_count from storage and
        // fails the nonce check. `try_invoke_contract` surfaces callee failures as `CallReverted`;
        // as the top-level invocation that fails the transaction and rolls this write back, so a
        // failed call still does not consume the nonce.
        env.storage().persistent().set(&EXPIRING_ROOT, &exp);

        match env.try_invoke_contract::<Val, InvokeError>(&target, &fn_sym, args) {
            Ok(Ok(_)) => {}
            // `Ok(Err(_))` is unreachable for T = Val (T::Error = Infallible); kept for exhaustiveness.
            Ok(Err(_)) => return Err(McmsError::CallReverted),
            // Surface the failure mode instead of collapsing both into CallReverted: a
            // callee-returned contract error vs. a host trap/panic. The callee's specific u32
            // code can't be carried through Soroban's fixed-enum error model, so only the mode
            // is surfaced (CallReverted vs CallAborted).
            Err(e) => {
                let ie = match e {
                    Ok(ie) | Err(ie) => ie,
                };
                return Err(match ie {
                    InvokeError::Abort => McmsError::CallAborted,
                    InvokeError::Contract(_) => McmsError::CallReverted,
                });
            }
        }

        OpExecutedEvent {
            nonce: op.nonce,
            to: op.to,
            data: op.data,
            value: op.value,
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

    /// Owner-only update of the pessimistic floor on seconds-per-ledger used to derive the
    /// dynamic `valid_until` cap in `set_root`. Defaults to
    /// [`crate::constants::MIN_SECS_PER_LEDGER_DEFAULT`] when unset.
    ///
    /// The effective `valid_until` cap is:
    /// `min(MAX_ROOT_VALIDITY_SECS, LEDGER_BUMP * min_secs_per_ledger - SEEN_TTL_SAFETY_MARGIN_SECS)`.
    /// Lowering this value shrinks the cap; raising it has no effect once it exceeds the static
    /// `MAX_ROOT_VALIDITY_SECS` (the static bound still applies). Gated like `set_config`.
    pub fn set_min_secs_per_ledger(env: Env, secs: u64) -> Result<(), McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        <Self as Ownable>::require_owner(&env).map_err(McmsError::from)?;
        if secs < MIN_SECS_PER_LEDGER_LOWER_BOUND || secs > MIN_SECS_PER_LEDGER_UPPER_BOUND {
            return Err(McmsError::InvalidMinSecsPerLedger);
        }
        env.storage().persistent().set(&MIN_SPL, &secs);
        env.storage()
            .persistent()
            .extend_ttl(&MIN_SPL, LEDGER_THRESHOLD, LEDGER_BUMP);
        MinSecsPerLedgerSetEvent {
            min_secs_per_ledger: secs,
        }
        .publish(&env);
        bump_ttls(&env);
        Ok(())
    }

    /// Returns the currently configured `min_secs_per_ledger`, or the default if never set.
    pub fn get_min_secs_per_ledger(env: Env) -> Result<u64, McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        Ok(stored_min_secs_per_ledger(&env))
    }

    /// Permissionless TTL extension for fixed persistent keys and instance storage.
    /// Per-hash seen entries are extended at creation; restore individual archived hashes
    /// via a `RestoreFootprint` transaction if needed. Anyone may call this.
    ///
    /// # Restoring an archived `SeenHash` entry
    ///
    /// `SeenHash(h)` entries are bumped once at creation in [`Self::set_root`] and are
    /// **never** swept by `bump_ttls` (they are not enumerable from inside the contract).
    /// Once their TTL elapses they are archived; reads from inside the contract will return
    /// `false` and the entry's bytes are no longer in live ledger state.
    ///
    /// **Replay safety does NOT depend on archived seen entries being readable.** The
    /// dynamic `valid_until` cap (see [`Self::set_min_secs_per_ledger`]) guarantees that any
    /// `(root, valid_until)` whose `SeenHash` could possibly be archived has already failed
    /// the `valid_until < now` check in `set_root`, so replay is impossible regardless of
    /// archive state.
    ///
    /// There is **no** guest-side "restore seen hash" entrypoint, and one cannot exist:
    /// Soroban does not expose a `restore` host function to contracts. Restoration is a
    /// host-level operation (`RestoreFootprintOp`) initiated by the **transaction submitter**
    /// who must include the archived ledger key in the operation's footprint and pay the
    /// rent fee. This is identical to how the `timelock` contract handles its per-op
    /// timestamp entries — see `contracts/timelock/src/lib.rs` for the same pattern.
    ///
    /// In practice you only need this for forensic/audit reads ("did we ever sign hash X?"):
    /// submit a transaction that (1) includes `RestoreFootprintOp` for the persistent
    /// `BytesN<32>` ledger key holding `X`, and (2) calls a read helper or RPC
    /// `getLedgerEntry` against that key.
    pub fn extend_all_ttls(env: Env) -> Result<(), McmsError> {
        <Self as Initializable>::require_initialized(&env)?;
        bump_ttls(&env);
        Ok(())
    }
}

/// Returns the operator-configured `min_secs_per_ledger`, falling back to
/// [`MIN_SECS_PER_LEDGER_DEFAULT`] when unset.
fn stored_min_secs_per_ledger(env: &Env) -> u64 {
    env.storage()
        .persistent()
        .get(&MIN_SPL)
        .unwrap_or(MIN_SECS_PER_LEDGER_DEFAULT)
}

/// Computes the effective max `valid_until` horizon in seconds:
/// `min(MAX_ROOT_VALIDITY_SECS, LEDGER_BUMP * min_secs_per_ledger - SEEN_TTL_SAFETY_MARGIN_SECS)`.
///
/// The dynamic term is derived from the worst-case seen-entry lifetime
/// (`LEDGER_BUMP` ledgers at the configured pessimistic seconds-per-ledger). Subtracting the
/// safety margin guarantees `valid_until` always expires *strictly* before any freshly bumped
/// `SeenHash` entry can be archived, closing the replay window without per-hash maintenance.
fn effective_max_root_validity_secs(env: &Env) -> u64 {
    let min_spl = stored_min_secs_per_ledger(env);
    let dynamic = (LEDGER_BUMP as u64)
        .saturating_mul(min_spl)
        .saturating_sub(SEEN_TTL_SAFETY_MARGIN_SECS);
    if dynamic < MAX_ROOT_VALIDITY_SECS {
        dynamic
    } else {
        MAX_ROOT_VALIDITY_SECS
    }
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
/// ROOT_META_STORE) and is called at the end of every successful public function so that
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
    if env.storage().persistent().has(&MIN_SPL) {
        env.storage()
            .persistent()
            .extend_ttl(&MIN_SPL, LEDGER_THRESHOLD, LEDGER_BUMP);
    }
}

fn contract_address_from_contract_id(env: &Env, id: &BytesN<32>) -> Address {
    let sk = StrkeyContract(id.to_array());
    let encoded = sk.to_string();
    Address::from_str(env, encoded.as_str())
}

fn contract_id_of_address(addr: &Address) -> BytesN<32> {
    match addr.to_payload() {
        Some(AddressPayload::ContractIdHash(id)) => id,
        _ => BytesN::from_array(addr.env(), &[0u8; 32]),
    }
}

#[cfg(test)]
mod test;
