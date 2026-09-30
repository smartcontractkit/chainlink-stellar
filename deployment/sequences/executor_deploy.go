package sequences

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	executorbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/executor"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	execops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/executor"
)

// DeployExecutorInput deploys and initializes the source-side Executor
// contract. MaxCCVsPerMsg defaults to 2, AllowedFinalityConfig to 0,
// CcvAllowlistEnabled to false and FeeAggregator to the owner (the monolith's
// constants). The sequence records both the executor and executor-proxy rows:
// Soroban has no delegate proxy, so both point at the same contract.
type DeployExecutorInput struct {
	ChainSelector         uint64                 `json:"chainSelector"`
	Owner                 string                 `json:"owner,omitempty"`
	WasmPath              string                 `json:"wasmPath"`
	MaxCCVsPerMsg         uint32                 `json:"maxCCVsPerMsg,omitempty"`
	AllowedFinalityConfig uint32                 `json:"allowedFinalityConfig,omitempty"`
	CcvAllowlistEnabled   bool                   `json:"ccvAllowlistEnabled,omitempty"`
	FeeAggregator         string                 `json:"feeAggregator,omitempty"`
	ExistingAddresses     []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployExecutor = cldf_ops.NewSequence(
	"stellar-deploy-executor",
	SequenceVersion,
	"Deploys and initializes the Executor Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployExecutorInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		maxCCVs := in.MaxCCVsPerMsg
		if maxCCVs == 0 {
			maxCCVs = 2
		}
		feeAggregator := in.FeeAggregator
		if feeAggregator == "" {
			feeAggregator = owner
		}
		if err := statReleaseWasm(in.WasmPath, "Executor"); err != nil {
			return ComponentDeployOutput{}, err
		}
		out, err := deployAndInitialize(b.GetContext(), b, deps, execops.Deploy,
			stellarccip.DefaultExecutorDatastoreRef(), in.ChainSelector, "executor", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, execops.Initialize, execops.InitializeInput{
					ContractID:    contractID,
					Owner:         owner,
					MaxCCVsPerMsg: maxCCVs,
					DynamicConfig: executorbindings.DynamicConfig{
						AllowedFinalityConfig: in.AllowedFinalityConfig,
						CcvAllowlistEnabled:   in.CcvAllowlistEnabled,
						FeeAggregator:         &feeAggregator,
					},
				}, withComponentIdempotencyKey[execops.InitializeInput](in.ChainSelector))
				return err
			})
		if err != nil {
			return ComponentDeployOutput{}, err
		}
		// Both rows must exist even if only one went missing (e.g. an executor ref
		// recorded by hand): the proxy row is written whenever it is absent.
		proxyRef := stellarccip.ExecutorProxyDatastoreRef(stellarccip.DefaultExecutorQualifier)
		if findExistingComponentRef(in.ExistingAddresses, proxyRef, in.ChainSelector) == nil {
			ref, err := executorProxyRef(in.ChainSelector, out.ContractID)
			if err != nil {
				return ComponentDeployOutput{}, err
			}
			out.Refs = append(out.Refs, ref)
		}
		return out, nil
	},
)

func executorProxyRef(chainSelector uint64, contractID string) (datastore.AddressRef, error) {
	hexAddr, err := stellarutil.StrkeyToHex(contractID)
	if err != nil {
		return datastore.AddressRef{}, err
	}
	return stellarccip.ExecutorProxyDatastoreRef(stellarccip.DefaultExecutorQualifier).FullAddressRef(chainSelector, hexAddr), nil
}
