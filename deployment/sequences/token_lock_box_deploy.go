package sequences

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	tlbops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_lock_box"
)

// DeployTokenLockBoxInput deploys and initializes a TokenLockBox — the box the
// canonical lock-release pool deposits user tokens into. The Qualifier
// identifies the box instance on chains with more than one (typically the token
// symbol, matching the pool it serves). Token comes from config; a lock-release
// pool takes the box strkey in its own config.
type DeployTokenLockBoxInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Qualifier         string                 `json:"qualifier"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	Token             string                 `json:"token"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployTokenLockBox = cldf_ops.NewSequence(
	"stellar-deploy-token-lock-box",
	SequenceVersion,
	"Deploys and initializes the TokenLockBox Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployTokenLockBoxInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "Token lock box"); err != nil {
			return ComponentDeployOutput{}, err
		}
		return deployAndInitialize(b.GetContext(), b, deps, tlbops.Deploy,
			stellarccip.TokenLockBoxDatastoreRef(in.Qualifier), in.ChainSelector,
			componentSaltLabel("token-lock-box", in.Qualifier), in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, tlbops.Initialize, tlbops.InitializeInput{
					ContractID: contractID,
					Owner:      owner,
					Token:      in.Token,
				}, withComponentInstanceIdempotencyKey[tlbops.InitializeInput](in.ChainSelector, in.Qualifier))
				return err
			})
	},
)
