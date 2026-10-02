package changesets

import (
	"fmt"
	"math/big"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployAdvancedPoolHooksConfig is the input of the Advanced Pool Hooks deploy
// changeset. The hooks contract is per-token and issuer-owned: the Qualifier
// identifies the instance (typically the token symbol) and Owner defaults to
// the deployer — for the issuer-driven CCV flow pass the token issuer (who is
// typically also the pool owner). ThresholdAmount defaults to 0 (the hook
// applies to every amount), PolicyEngine to nil (policy checks dormant), and
// AuthorizedCallers to empty — the pools wired to these hooks via
// token_pool.SetAdvancedPoolHooks should be added before they invoke the
// hooks. No datastore dependencies; wiring the hooks to a pool and applying
// CCV configs are owner-gated config ops, not deploy steps.
type DeployAdvancedPoolHooksConfig struct {
	ChainSelector     uint64   `json:"chainSelector" yaml:"chainSelector"`
	Qualifier         string   `json:"qualifier" yaml:"qualifier"`
	Owner             string   `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath          string   `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	Allowlist         []string `json:"allowlist,omitempty" yaml:"allowlist,omitempty"`
	ThresholdAmount   *big.Int `json:"thresholdAmount,omitempty" yaml:"thresholdAmount,omitempty"`
	AuthorizedCallers []string `json:"authorizedCallers,omitempty" yaml:"authorizedCallers,omitempty"`
	PolicyEngine      *string  `json:"policyEngine,omitempty" yaml:"policyEngine,omitempty"`
}

// DeployAdvancedPoolHooks deploys and initializes an Advanced Pool Hooks
// contract on one Stellar chain. Requires a non-empty Qualifier: more than one
// hooks contract can live on a chain (one per token), and the qualifier keeps
// their datastore refs, salts and op reports distinct.
type DeployAdvancedPoolHooks struct{}

var _ cldf.ChangeSetV2[DeployAdvancedPoolHooksConfig] = DeployAdvancedPoolHooks{}

func (DeployAdvancedPoolHooks) VerifyPreconditions(e cldf.Environment, cfg DeployAdvancedPoolHooksConfig) error {
	return verifyComponent(e, cfg.ChainSelector, nil, func() error {
		if cfg.Qualifier == "" {
			return fmt.Errorf("qualifier is required: one hooks contract per token, identify the instance (e.g. the token symbol)")
		}
		return nil
	})
}

func (DeployAdvancedPoolHooks) Apply(e cldf.Environment, cfg DeployAdvancedPoolHooksConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, nil, cfg.WasmPath, "pools_advanced_pool_hooks.wasm", sequences.DeployAdvancedPoolHooks,
		func(_ map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployAdvancedPoolHooksInput {
			return sequences.DeployAdvancedPoolHooksInput{
				ChainSelector:     cfg.ChainSelector,
				Qualifier:         cfg.Qualifier,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				Allowlist:         cfg.Allowlist,
				ThresholdAmount:   cfg.ThresholdAmount,
				AuthorizedCallers: cfg.AuthorizedCallers,
				PolicyEngine:      cfg.PolicyEngine,
				ExistingAddresses: existing,
			}
		})
}
