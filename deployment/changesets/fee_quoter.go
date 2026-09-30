package changesets

import (
	"fmt"
	"math/big"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployFeeQuoterConfig is the input of the FeeQuoter deploy changeset.
// FeeToken is the strkey of the SAC used for fee payments, a required input:
// the deploy never derives it. MaxFeeJuelsPerMsg defaults to 1e18 and
// AuthorizedCallers to [owner]. No datastore dependencies.
type DeployFeeQuoterConfig struct {
	ChainSelector     uint64   `json:"chainSelector" yaml:"chainSelector"`
	Owner             string   `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath          string   `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	FeeToken          string   `json:"feeToken" yaml:"feeToken"`
	MaxFeeJuelsPerMsg *big.Int `json:"maxFeeJuelsPerMsg,omitempty" yaml:"maxFeeJuelsPerMsg,omitempty"`
	AuthorizedCallers []string `json:"authorizedCallers,omitempty" yaml:"authorizedCallers,omitempty"`
}

// DeployFeeQuoter deploys and initializes the FeeQuoter contract on one
// Stellar chain.
type DeployFeeQuoter struct{}

var _ cldf.ChangeSetV2[DeployFeeQuoterConfig] = DeployFeeQuoter{}

// maxFeeJuelsPerMsgBound is the largest i128 value FeeQuoter's initialize
// accepts (it rejects values <= 0 on chain).
var maxFeeJuelsPerMsgBound = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 127), big.NewInt(1))

func (DeployFeeQuoter) VerifyPreconditions(e cldf.Environment, cfg DeployFeeQuoterConfig) error {
	return verifyComponent(e, cfg.ChainSelector, nil, func() error {
		if cfg.FeeToken == "" {
			return fmt.Errorf("feeToken is required: pass the fee-token SAC strkey")
		}
		if cfg.MaxFeeJuelsPerMsg != nil {
			if cfg.MaxFeeJuelsPerMsg.Sign() <= 0 {
				return fmt.Errorf("maxFeeJuelsPerMsg must be positive")
			}
			if cfg.MaxFeeJuelsPerMsg.Cmp(maxFeeJuelsPerMsgBound) > 0 {
				return fmt.Errorf("maxFeeJuelsPerMsg exceeds the i128 upper bound %s", maxFeeJuelsPerMsgBound)
			}
		}
		return nil
	})
}

func (DeployFeeQuoter) Apply(e cldf.Environment, cfg DeployFeeQuoterConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, nil, cfg.WasmPath, "fee_quoter.wasm", sequences.DeployFeeQuoter,
		func(_ map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployFeeQuoterInput {
			return sequences.DeployFeeQuoterInput{
				ChainSelector:     cfg.ChainSelector,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				FeeToken:          cfg.FeeToken,
				MaxFeeJuelsPerMsg: cfg.MaxFeeJuelsPerMsg,
				AuthorizedCallers: cfg.AuthorizedCallers,
				ExistingAddresses: existing,
			}
		})
}
