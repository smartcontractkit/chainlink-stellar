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
// VerifyPreconditions returns "deploy <X> first" when a dependency ref is
// missing from the datastore.
//
// Apply builds the deployer from the chain's signer, so no raw keypair is
// required and KMS-backed signers work; the address used to predict contract
// IDs is the signer's own address, so a predicted ID can never diverge from
// the key that signs the deploy. Deploys leave the deployer key as owner;
// ownership moves later through the transfer-ownership changesets.
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
