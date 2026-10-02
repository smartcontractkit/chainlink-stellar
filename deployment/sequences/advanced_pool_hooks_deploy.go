package sequences

import (
	"math/big"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	aphops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/advanced_pool_hooks"
)

// DeployAdvancedPoolHooksInput deploys and initializes the Advanced Pool Hooks
// contract — the per-token, issuer-owned hooks contract through which a token
// issuer / pool owner requires the CCV set for their token's transfers. The
// Qualifier identifies the instance on chains with more than one hooks
// contract (typically the token symbol); ThresholdAmount defaults to 0 (the
// hook applies to every amount) and PolicyEngine nil (policy checks dormant,
// EVM address(0)).
type DeployAdvancedPoolHooksInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Qualifier         string                 `json:"qualifier"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	Allowlist         []string               `json:"allowlist,omitempty"`
	ThresholdAmount   *big.Int               `json:"thresholdAmount,omitempty"` // nil = 0: the hook applies to every amount
	AuthorizedCallers []string               `json:"authorizedCallers,omitempty"`
	PolicyEngine      *string                `json:"policyEngine,omitempty"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployAdvancedPoolHooks = cldf_ops.NewSequence(
	"stellar-deploy-advanced-pool-hooks",
	SequenceVersion,
	"Deploys and initializes the Advanced Pool Hooks Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployAdvancedPoolHooksInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "Advanced Pool Hooks"); err != nil {
			return ComponentDeployOutput{}, err
		}
		return deployAndInitialize(b.GetContext(), b, deps, aphops.Deploy,
			stellarccip.AdvancedPoolHooksDatastoreRef(in.Qualifier), in.ChainSelector,
			componentSaltLabel("advanced-pool-hooks", in.Qualifier), in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, aphops.Initialize, aphops.InitializeInput{
					ContractID:        contractID,
					Owner:             owner,
					Allowlist:         in.Allowlist,
					ThresholdAmount:   in.ThresholdAmount,
					AuthorizedCallers: in.AuthorizedCallers,
					PolicyEngine:      in.PolicyEngine,
				}, withComponentInstanceIdempotencyKey[aphops.InitializeInput](in.ChainSelector, in.Qualifier))
				return err
			})
	},
)
