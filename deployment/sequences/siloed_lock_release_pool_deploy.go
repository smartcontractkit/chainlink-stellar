package sequences

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	slrrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/siloed_lock_release_pool"
)

// DeploySiloedLockReleasePoolInput deploys and initializes the siloed
// lock-release token pool — the lock-release variant whose per-remote-chain
// lock boxes are configured post-deploy (there is no immutable lock box at
// initialize). The Qualifier identifies the pool instance on chains with more
// than one pool (typically the token symbol). Token/TokenDecimals come from
// config; the Router/RampRegistry/RMN Proxy strkeys are resolved from the
// environment datastore by the changeset. Lock-box wiring
// (configure_lock_boxes) and per-remote-chain pool config are
// lane-configuration-time operator steps.
type DeploySiloedLockReleasePoolInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Qualifier         string                 `json:"qualifier"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	Token             string                 `json:"token"`
	TokenDecimals     uint32                 `json:"tokenDecimals"`
	Router            string                 `json:"router"`
	RampRegistry      string                 `json:"rampRegistry"`
	RmnProxy          string                 `json:"rmnProxy"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeploySiloedLockReleasePool = cldf_ops.NewSequence(
	"stellar-deploy-siloed-lock-release-pool",
	SequenceVersion,
	"Deploys and initializes the siloed lock-release token pool with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeploySiloedLockReleasePoolInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "Siloed lock-release pool"); err != nil {
			return ComponentDeployOutput{}, err
		}
		return deployAndInitialize(b.GetContext(), b, deps, slrrops.Deploy,
			stellarccip.SiloedLockReleasePoolDatastoreRef(in.Qualifier), in.ChainSelector,
			componentSaltLabel("siloed-lock-release-pool", in.Qualifier), in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, slrrops.Initialize, slrrops.InitializeInput{
					ContractID:    contractID,
					Owner:         owner,
					Token:         in.Token,
					TokenDecimals: in.TokenDecimals,
					Router:        in.Router,
					RampRegistry:  in.RampRegistry,
					RmnProxy:      in.RmnProxy,
				}, withComponentInstanceIdempotencyKey[slrrops.InitializeInput](in.ChainSelector, in.Qualifier))
				return err
			})
	},
)
