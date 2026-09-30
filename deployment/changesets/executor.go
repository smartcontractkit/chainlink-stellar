package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployExecutorConfig is the input of the Executor deploy changeset.
// MaxCCVsPerMsg defaults to 2, AllowedFinalityConfig to 0,
// CcvAllowlistEnabled to false and FeeAggregator to the owner. The changeset
// records both the executor and executor-proxy rows: Soroban has no delegate
// proxy, so both point at the same contract.
type DeployExecutorConfig struct {
	ChainSelector         uint64 `json:"chainSelector" yaml:"chainSelector"`
	Owner                 string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath              string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	MaxCCVsPerMsg         uint32 `json:"maxCCVsPerMsg,omitempty" yaml:"maxCCVsPerMsg,omitempty"`
	AllowedFinalityConfig uint32 `json:"allowedFinalityConfig,omitempty" yaml:"allowedFinalityConfig,omitempty"`
	CcvAllowlistEnabled   bool   `json:"ccvAllowlistEnabled,omitempty" yaml:"ccvAllowlistEnabled,omitempty"`
	FeeAggregator         string `json:"feeAggregator,omitempty" yaml:"feeAggregator,omitempty"`
}

// DeployExecutor deploys and initializes the source-side Executor contract on
// one Stellar chain. No dependencies.
type DeployExecutor struct{}

var _ cldf.ChangeSetV2[DeployExecutorConfig] = DeployExecutor{}

func (DeployExecutor) VerifyPreconditions(e cldf.Environment, cfg DeployExecutorConfig) error {
	return verifyComponent(e, cfg.ChainSelector, nil, nil)
}

func (DeployExecutor) Apply(e cldf.Environment, cfg DeployExecutorConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, nil, cfg.WasmPath, "executor.wasm", sequences.DeployExecutor,
		func(_ map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployExecutorInput {
			return sequences.DeployExecutorInput{
				ChainSelector:         cfg.ChainSelector,
				Owner:                 cfg.Owner,
				WasmPath:              wasmPath,
				MaxCCVsPerMsg:         cfg.MaxCCVsPerMsg,
				AllowedFinalityConfig: cfg.AllowedFinalityConfig,
				CcvAllowlistEnabled:   cfg.CcvAllowlistEnabled,
				FeeAggregator:         cfg.FeeAggregator,
				ExistingAddresses:     existing,
			}
		})
}
