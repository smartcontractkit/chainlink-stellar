package sequences

import (
	"fmt"
	"math/big"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	fqops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
)

// DeployFeeQuoterInput deploys and initializes the FeeQuoter contract. FeeToken
// is the strkey of the SAC used for fee payments, a required input (G9): the
// deploy never derives it. MaxFeeJuelsPerMsg defaults to 1e18 and
// AuthorizedCallers to [owner] (the monolith's constants).
type DeployFeeQuoterInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	FeeToken          string                 `json:"feeToken"`
	MaxFeeJuelsPerMsg *big.Int               `json:"maxFeeJuelsPerMsg,omitempty"`
	AuthorizedCallers []string               `json:"authorizedCallers,omitempty"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployFeeQuoter = cldf_ops.NewSequence(
	"stellar-deploy-fee-quoter",
	SequenceVersion,
	"Deploys and initializes the FeeQuoter Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployFeeQuoterInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "FeeQuoter"); err != nil {
			return ComponentDeployOutput{}, err
		}
		if in.FeeToken == "" {
			return ComponentDeployOutput{}, fmt.Errorf("feeToken is required: pass the fee-token SAC strkey")
		}
		maxFee := in.MaxFeeJuelsPerMsg
		if maxFee == nil {
			maxFee = big.NewInt(1_000_000_000_000_000_000)
		}
		callers := in.AuthorizedCallers
		if len(callers) == 0 {
			callers = []string{owner}
		}
		return deployAndInitialize(b.GetContext(), b, deps, fqops.Deploy,
			stellarccip.FeeQuoterDatastoreRef(), in.ChainSelector, "fee-quoter", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, fqops.Initialize, fqops.InitializeInput{
					ContractID: contractID,
					Owner:      owner,
					StaticConfig: fqbindings.StaticConfig{
						LinkToken:         in.FeeToken,
						MaxFeeJuelsPerMsg: maxFee,
					},
					AuthorizedCallers: callers,
				}, withComponentIdempotencyKey[fqops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)
