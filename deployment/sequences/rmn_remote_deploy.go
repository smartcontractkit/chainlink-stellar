package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	rmnremoteops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// DeployRMNRemoteInput deploys and initializes the RMN Remote contract.
// Owner defaults to the deployer address. CurseAdmins default to none;
// EnableFastCurse prepends the fast-curse timelock resolved from
// ExistingAddresses (qualifier FastCurseQualifier, default the Ultra Fast
// Curse qualifier), mirroring the EVM deploy.
type DeployRMNRemoteInput struct {
	ChainSelector      uint64                 `json:"chainSelector"`
	Owner              string                 `json:"owner,omitempty"`
	WasmPath           string                 `json:"wasmPath"`
	CurseAdmins        []string               `json:"curseAdmins,omitempty"`
	EnableFastCurse    bool                   `json:"enableFastCurse,omitempty"`
	FastCurseQualifier string                 `json:"fastCurseQualifier,omitempty"`
	ExistingAddresses  []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployRMNRemote = cldf_ops.NewSequence(
	"stellar-deploy-rmn-remote",
	SequenceVersion,
	"Deploys and initializes the RMN Remote Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployRMNRemoteInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "RMN Remote"); err != nil {
			return ComponentDeployOutput{}, err
		}
		curseAdmins := in.CurseAdmins
		if in.EnableFastCurse {
			fastQual := in.FastCurseQualifier
			if fastQual == "" {
				fastQual = cciputils.UltraFastCurseMCMSQualifier
			}
			fastTL, ok := mcmsutil.FindExistingStellarTimelock(in.ExistingAddresses, in.ChainSelector, fastQual)
			if !ok {
				return ComponentDeployOutput{}, fmt.Errorf(
					"enable fast curse: no RBACTimelock deployed for qualifier %q on chain %d; deploy the fast-curse MCMS stack first", fastQual, in.ChainSelector)
			}
			curseAdmins = append([]string{fastTL}, curseAdmins...)
		}
		return deployAndInitialize(b.GetContext(), b, deps, rmnremoteops.Deploy,
			stellarccip.RMNRemoteDatastoreRef(), in.ChainSelector, "rmn-remote", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, rmnremoteops.Initialize, rmnremoteops.InitializeInput{
					ContractID:  contractID,
					Owner:       owner,
					CurseAdmins: curseAdmins,
				}, withComponentIdempotencyKey[rmnremoteops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)

// defaultOwner fills the Owner default (P6): the deployer signer address.
func defaultOwner(owner, deployerAddress string) string {
	if owner == "" {
		return deployerAddress
	}
	return owner
}

// withComponentIdempotencyKey scopes a singleton component op's reports to the
// chain. Multi-instance components (per-token pools, per-token hooks) must use
// withComponentInstanceIdempotencyKey instead.
func withComponentIdempotencyKey[IN any](chainSelector uint64) cldf_ops.ExecuteOption[IN, stellardeps.StellarDeps] {
	return cldf_ops.WithIdempotencyKey[IN, stellardeps.StellarDeps](componentIdempotencyKey(chainSelector, ""))
}

// withComponentInstanceIdempotencyKey scopes a multi-instance component op's
// reports to the chain and the instance (its qualifier), so deploying a second
// pool or hooks contract never reuses the first instance's cached op report.
func withComponentInstanceIdempotencyKey[IN any](chainSelector uint64, qualifier string) cldf_ops.ExecuteOption[IN, stellardeps.StellarDeps] {
	return cldf_ops.WithIdempotencyKey[IN, stellardeps.StellarDeps](componentIdempotencyKey(chainSelector, qualifier))
}
