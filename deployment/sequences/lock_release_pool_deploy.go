package sequences

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	lrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/lock_release_pool"
)

// DeployLockReleasePoolInput deploys and initializes the canonical (non-siloed)
// lock-release token pool. The Qualifier identifies the pool instance on
// chains with more than one pool (typically the token symbol). Token /
// TokenDecimals / LockBox come from config; the Router/RampRegistry/RMN Proxy
// strkeys are resolved from the environment datastore by the changeset. The
// LockBox must be an initialized TokenLockBox for the same token (the pool
// validates token support on-chain).
type DeployLockReleasePoolInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Qualifier         string                 `json:"qualifier"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	Token             string                 `json:"token"`
	TokenDecimals     uint32                 `json:"tokenDecimals"`
	LockBox           string                 `json:"lockBox"`
	Router            string                 `json:"router"`
	RampRegistry      string                 `json:"rampRegistry"`
	RmnProxy          string                 `json:"rmnProxy"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployLockReleasePool = cldf_ops.NewSequence(
	"stellar-deploy-lock-release-pool",
	SequenceVersion,
	"Deploys and initializes the canonical lock-release token pool with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployLockReleasePoolInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "Lock-release pool"); err != nil {
			return ComponentDeployOutput{}, err
		}
		return deployAndInitialize(b.GetContext(), b, deps, lrops.Deploy,
			stellarccip.LockReleasePoolDatastoreRef(in.Qualifier), in.ChainSelector,
			componentSaltLabel("lock-release-pool", in.Qualifier), in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, lrops.Initialize, lrops.InitializeInput{
					ContractID:    contractID,
					Owner:         owner,
					Token:         in.Token,
					TokenDecimals: in.TokenDecimals,
					Router:        in.Router,
					RampRegistry:  in.RampRegistry,
					RmnProxy:      in.RmnProxy,
					LockBox:       in.LockBox,
				}, withComponentInstanceIdempotencyKey[lrops.InitializeInput](in.ChainSelector, in.Qualifier))
				return err
			})
	},
)
