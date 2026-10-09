package sequences

import (
	"encoding/binary"

	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	executorbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/executor"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	execops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/executor"
)

// FamilyDefaultAllowedFinality is the raw allowed-finality value the stellar
// chain family declares (adapters.StellarChainFamilyAdapter.GetDefaultFinalityConfig:
// wait-for-finality | safe-flag | depth 1; keep in sync with it). EVM deploy
// defaults apply the same kind of family default to executors (ccv
// deploy_defaults.go defaultFinalityConfig), so the Executor deploys
// permissive of every requested-finality mode the lane flows send (0, block
// depth, safe flag). The default AllowedFinalityConfig of 0
// (wait-for-finality only) makes the OnRamp's get_fee path — which
// cross-calls Executor::get_fee — reject depth/safe requests with
// InvalidRequestedFinality (#315).
var FamilyDefaultAllowedFinality = func() uint32 {
	raw := finality.Config{WaitForFinality: true, WaitForSafe: true, BlockDepth: 1}.Raw()
	return binary.BigEndian.Uint32(raw[:])
}()

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
		// The proxy row must exist and point at this Executor: it is written
		// whenever it is missing or stale (e.g. hand-recorded, or left over from
		// an earlier deploy with a different deployer). The OnRamp resolves its
		// default executor through the proxy row.
		refs, err := componentRefs(stellarccip.ExecutorProxyDatastoreRef(stellarccip.DefaultExecutorQualifier), in.ChainSelector, out.ContractID)
		if err != nil {
			return ComponentDeployOutput{}, err
		}
		proxyRow := refs[0]
		if existing := findExistingComponentRef(in.ExistingAddresses, stellarccip.ExecutorProxyDatastoreRef(stellarccip.DefaultExecutorQualifier), in.ChainSelector); existing == nil || existing.Address != proxyRow.Address {
			out.Refs = append(out.Refs, proxyRow)
		}
		return out, nil
	},
)
