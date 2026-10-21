package adapters

import (
	"fmt"

	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"

	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	execops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/executor"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/proxy"
)

// StellarCCVProtocolContractsDeployAdapter implements
// ccvdeploymentadapters.ProtocolContractsDeployAdapter for Stellar chains. It
// wraps the existing full-deploy sequence the ccip
// DeployChainContractsAdapter path uses (sequences.StellarDeployChainContracts),
// including the CCV PreDeploy topology stash it takes, so both registration
// paths drive the identical deploy. The full deploy deploys and initializes
// the whole protocol stack — RMN Remote + Proxy, RampRegistry, Router,
// OnRamp, OffRamp, FeeQuoter, and the default fee/policy Executor —
// skip-if-exists throughout.
//
// Field mapping: DeployerContract is a placeholder on stellar (no deployer
// factory) and is ignored; DeployTestRouter has no stellar counterpart (no
// separate TestRouter exists) and is warned-and-ignored; FamilyExtras has no
// stellar interpretation and is warned-and-ignored; Executors may only
// request the default executor — the full deploy deploys exactly one, and
// non-default qualifiers fail fast. BatchOps stays nil: Soroban deploys are
// EOA-only. RefsToTransferOwnership carries the refs this run deployed or
// adopted, populated iff !DeployerKeyOwned, for the follow-up
// TransferOwnership changeset (a re-run where every contract already exists
// records no refs — collect the stack refs from the datastore instead when
// transferring after a no-op re-run).
type StellarCCVProtocolContractsDeployAdapter struct{}

var _ ccvdeploymentadapters.ProtocolContractsDeployAdapter = (*StellarCCVProtocolContractsDeployAdapter)(nil)

var stellarDeployProtocolContracts = cldf_ops.NewSequence(
	"stellar-deploy-protocol-contracts",
	stellarops.ContractDeploymentVersion,
	"Deploys the full Stellar CCIP protocol stack via the shared full-deploy sequence (skip-if-exists)",
	func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input ccvdeploymentadapters.ProtocolContractsDeployInput) (ccvdeploymentadapters.ProtocolContractsDeployOutput, error) {
		if _, ok := chains.StellarChains()[input.ChainSelector]; !ok {
			return ccvdeploymentadapters.ProtocolContractsDeployOutput{},
				fmt.Errorf("stellar chain not found for selector %d", input.ChainSelector)
		}
		if err := validateStellarProtocolDeployInput(input); err != nil {
			return ccvdeploymentadapters.ProtocolContractsDeployOutput{}, err
		}
		if input.DeployTestRouter {
			b.Logger.Warnf("DeployTestRouter ignored on chain %d: no separate TestRouter exists on stellar", input.ChainSelector)
		}
		if len(input.FamilyExtras) > 0 {
			b.Logger.Warnf("FamilyExtras not interpreted by the stellar deploy on chain %d; ignoring %d keys", input.ChainSelector, len(input.FamilyExtras))
		}

		report, err := cldf_ops.ExecuteSequence(b, sequences.StellarDeployChainContracts, chains, ccvadapters.DeployChainContractsInput{
			ChainSelector:     input.ChainSelector,
			DeployerContract:  input.DeployerContract,
			DeployTestRouter:  input.DeployTestRouter,
			ExistingAddresses: input.ExistingAddresses,
			DeployerKeyOwned:  input.DeployerKeyOwned,
		})
		if err != nil {
			return ccvdeploymentadapters.ProtocolContractsDeployOutput{},
				fmt.Errorf("stellar protocol contracts deploy failed: %w", err)
		}
		return stellarProtocolDeployOutput(report.Output.Addresses, input.DeployerKeyOwned), nil
	},
)

func (a *StellarCCVProtocolContractsDeployAdapter) DeployProtocolContracts() *cldf_ops.Sequence[ccvdeploymentadapters.ProtocolContractsDeployInput, ccvdeploymentadapters.ProtocolContractsDeployOutput, cldf_chain.BlockChains] {
	return stellarDeployProtocolContracts
}

// validateStellarProtocolDeployInput rejects the protocol-deploy inputs the
// stellar full deploy cannot honor: non-default executor qualifiers (it
// deploys exactly one, the default executor).
func validateStellarProtocolDeployInput(input ccvdeploymentadapters.ProtocolContractsDeployInput) error {
	for _, e := range input.Executors {
		if e.Qualifier != "" && e.Qualifier != defaultQualifier {
			return fmt.Errorf("executor qualifier %q unsupported on stellar: the full deploy deploys only the default executor", e.Qualifier)
		}
	}
	return nil
}

// stellarProtocolDeployOutput maps the full deploy's recorded refs to the
// chain-agnostic output. RefsToTransferOwnership is populated iff the caller
// did not keep deployer-key ownership, for the follow-up TransferOwnership
// changeset; BatchOps stays nil (EOA-only deploy).
func stellarProtocolDeployOutput(addresses []datastore.AddressRef, deployerKeyOwned bool) ccvdeploymentadapters.ProtocolContractsDeployOutput {
	out := ccvdeploymentadapters.ProtocolContractsDeployOutput{Addresses: addresses}
	if !deployerKeyOwned {
		out.RefsToTransferOwnership = dedupeOwnershipRefs(addresses)
	}
	return out
}

// dedupeOwnershipRefs canonicalizes alias rows to their contract's type and
// drops duplicates, order-independently: a proxy row records the same contract
// as its Executor row, and either row may be the only one recorded (the proxy
// row is written alone when it was missing or stale), so every alias resolves
// to the Executor type before the address dedup — the ownership handoff then
// transfers each contract once under a type its dispatch supports.
func dedupeOwnershipRefs(refs []datastore.AddressRef) []datastore.AddressRef {
	seen := make(map[string]struct{}, len(refs))
	out := make([]datastore.AddressRef, 0, len(refs))
	for _, r := range refs {
		if r.Type == datastore.ContractType(proxy.ContractType) {
			r.Type = datastore.ContractType(execops.ContractType)
		}
		if _, ok := seen[r.Address]; ok {
			continue
		}
		seen[r.Address] = struct{}{}
		out = append(out, r)
	}
	return out
}
