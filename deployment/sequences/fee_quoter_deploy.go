package sequences

import (
	"fmt"
	"math/big"
	"slices"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	fqops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
)

// DeployFeeQuoterInput creates the FeeQuoter contract. FeeToken
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
	"Creates the FeeQuoter Soroban contract through its constructor with skip-if-exists",
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
		staticConfig := fqbindings.StaticConfig{
			LinkToken:         in.FeeToken,
			MaxFeeJuelsPerMsg: maxFee,
		}
		ctorArgs, err := fqbindings.ConstructorArgs(owner, staticConfig, callers)
		if err != nil {
			return ComponentDeployOutput{}, fmt.Errorf("encode FeeQuoter constructor args: %w", err)
		}
		ctx := b.GetContext()
		return deployConstructedComponent(ctx, b, deps, fqops.Deploy,
			stellarccip.FeeQuoterDatastoreRef(), in.ChainSelector, "fee-quoter", in.WasmPath, ctorArgs, in.ExistingAddresses,
			func(contractID string) error {
				c := fqbindings.NewFeeQuoterClient(deps.Invoker, contractID)
				gotOwner, err := c.Owner(ctx)
				if err != nil {
					return err
				}
				if err := requireOwner(gotOwner, owner); err != nil {
					return err
				}
				static, err := c.GetStaticConfig(ctx)
				if err != nil {
					return err
				}
				if err := requireField("link token", static.LinkToken, staticConfig.LinkToken); err != nil {
					return err
				}
				if static.MaxFeeJuelsPerMsg == nil || static.MaxFeeJuelsPerMsg.Cmp(maxFee) != 0 {
					return fmt.Errorf("max fee juels per message is %v, expected %v", static.MaxFeeJuelsPerMsg, maxFee)
				}
				gotCallers, err := c.GetAuthorizedCallers(ctx)
				if err != nil {
					return err
				}
				if !slices.Equal(gotCallers, callers) {
					return fmt.Errorf("authorized callers are %v, expected %v", gotCallers, callers)
				}
				return nil
			})
	},
)
