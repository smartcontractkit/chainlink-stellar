package sequences

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
)

// DeployBurnMintPoolInput deploys and initializes a burn-mint token pool
// against the already-deployed core stack. The Qualifier identifies the pool
// instance on chains with more than one pool (typically the token symbol).
// Token/TokenDecimals come from config (the pool takes any custom token — BnM,
// LINK, or an issuer token); the Router/RampRegistry/RMN Proxy strkeys are
// resolved from the environment datastore by the changeset. The set_admin
// mint-authority handoff and the TokenAdminRegistry registration are operator
// steps (see RunDeployBnmToken for the one-shot onboarding); per-remote-chain
// pool config (apply_chain_updates) is applied at lane-configuration time.
type DeployBurnMintPoolInput struct {
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

var DeployBurnMintPool = cldf_ops.NewSequence(
	"stellar-deploy-burn-mint-pool",
	SequenceVersion,
	"Deploys and initializes the burn-mint token pool with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployBurnMintPoolInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "Burn-mint pool"); err != nil {
			return ComponentDeployOutput{}, err
		}
		return deployAndInitialize(b.GetContext(), b, deps, bmpops.Deploy,
			stellarccip.BurnMintPoolDatastoreRef(in.Qualifier), in.ChainSelector,
			componentSaltLabel("burn-mint-pool", in.Qualifier), in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, bmpops.Initialize, bmpops.InitializeInput{
					ContractID:    contractID,
					Owner:         owner,
					Token:         in.Token,
					TokenDecimals: in.TokenDecimals,
					Router:        in.Router,
					RampRegistry:  in.RampRegistry,
					RmnProxy:      in.RmnProxy,
				}, withComponentInstanceIdempotencyKey[bmpops.InitializeInput](in.ChainSelector, in.Qualifier))
				return err
			})
	},
)
