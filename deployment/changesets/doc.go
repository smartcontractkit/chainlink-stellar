// Package changesets exports one CLDF ChangeSetV2 per Stellar CCIP component
// (deploy + initialize together), wrapping the per-component sequences in
// deployment/sequences. Each changeset resolves its dependency strkeys from
// the environment datastore, so a chain is built by applying them in
// dependency order:
//
//	tier 0: RMN Remote, TokenAdminRegistry, RampRegistry,
//	        VersionedVerifierResolver, Executor
//	tier 1: RMN Proxy (needs RMN Remote);
//	        FeeQuoter (needs the fee-token strkey in its config)
//	tier 2: OnRamp (TokenAdminRegistry + RMN Proxy + FeeQuoter);
//	        OffRamp (RMN Proxy + TokenAdminRegistry);
//	        Router (RMN Proxy);
//	        CommitteeVerifier (RMN Proxy + StorageLocations in its config)
//	tier 3: ccip_receiver_example (Router)
//
// Peripheral components (one instance per token, identified by the config
// Qualifier where several can share a chain):
//
//	        bnm_token, link_token (no datastore dependencies; the pool
//	        changesets take the token strkey from config);
//	        advanced_pool_hooks (issuer-owned hooks contract);
//	        advanced_pool_hooks_extractor (stateless singleton, deploy-only);
//	        token_lock_box (needs the token strkey in its config)
//	Peripheral pools (need Router + RampRegistry + RMN Proxy from the
//	        datastore, plus the token strkey — and, for the canonical
//	        lock-release pool, the lock box strkey — in their config):
//	        burn_mint_pool, lock_release_pool, siloed_lock_release_pool
//
// VerifyPreconditions returns "deploy <X> first" when a dependency ref is
// missing from the datastore.
//
// Apply builds the deployer from the chain's signer, so no raw keypair is
// required and KMS-backed signers work; the address used to predict contract
// IDs is the signer's own address, so a predicted ID can never diverge from
// the key that signs the deploy. Owner defaults to that signer address (the
// configs accept another owner for custom workflows). These changesets never
// transfer ownership or emit MCMS proposals; ownership moves later through
// the transfer-ownership changesets.
//
// Rerun safety comes from the component sequences' three-layer skip
// (datastore ref, predicted contract ID + WASM hash, owner()): re-applying an
// already-deployed component sends no transactions and records no new refs.
//
// Report size: each component changeset appends one sequence report plus at
// most one deploy and one initialize op report (two op reports on a fresh
// deploy, zero on a rerun). Op reports embed the absolute WASM path and are
// deleted from durable pipeline storage after 30 days, so they are a
// crash-resume aid, not the idempotency mechanism — the skip layers are.
package changesets
