// Package sequences defines CLDF deployment sequences for Stellar CCIP.
//
// Deploy chain contracts uses cldf_chain.BlockChains as the dependency type to match
// chainlink-ccip deployment/v2_0_0 adapter contracts. The sequence builds
// deployment/operations/stellardeps.StellarDeps and a CCIP devenv host from the CLDF Stellar chain entry,
// loads stashed offchain topology via [TakeStellarDeployOffchainTopologyForSelector] when CCV
// registered one (in PreDeployContractsForSelector), then runs [RunStellarCCIPFullDeploy].
// A missing stash is tolerated: committee verifier signer quorums are applied at
// lane-configuration time, so the deploy itself does not need NOP topology data.
// [RunStellarCCIPFullDeployForCCV] converts CCV environment topology and is used by ccv/chain;
// it keeps its nil-topology check, and devenv always supplies a CCV topology.
// The sequence return type matches
// chainlink-ccip/deployment/v2_0_0/adapters.DeployChainContractsAdapter for the module version in go.mod.
// DeployStellarCCIPInnerInput carries ExistingAddresses from the CCIP changeset
// input so deploy can seed the in-memory datastore like EVM ExistingAddresses.
// AllSelectors lists every chain selector in the environment (derived from BlockChains in the adapter sequence).
package sequences
