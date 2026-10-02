package changesets

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployLockReleasePoolConfig is the input of the canonical lock-release pool
// deploy changeset. The pool takes the token and its lock box from config; the
// Router/RampRegistry/RMN Proxy strkeys are resolved from the environment
// datastore, not configured here. The LockBox must be an initialized
// TokenLockBox for the same token (the pool validates token support on-chain);
// deploy it first (DeployTokenLockBox). Per-remote-chain pool config is
// applied at lane-configuration time.
type DeployLockReleasePoolConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Qualifier     string `json:"qualifier" yaml:"qualifier"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	Token         string `json:"token" yaml:"token"`
	TokenDecimals uint32 `json:"tokenDecimals" yaml:"tokenDecimals"`
	LockBox       string `json:"lockBox" yaml:"lockBox"`
}

// DeployLockReleasePool deploys and initializes the canonical (non-siloed)
// lock-release token pool on one Stellar chain. Requires Router, RampRegistry
// and RMN Proxy, a non-empty Token strkey with its decimals and its LockBox,
// and a non-empty Qualifier (more than one pool can live on a chain; the
// qualifier keeps their datastore refs, salts and op reports distinct —
// typically the token symbol).
type DeployLockReleasePool struct{}

var _ cldf.ChangeSetV2[DeployLockReleasePoolConfig] = DeployLockReleasePool{}

func (DeployLockReleasePool) VerifyPreconditions(e cldf.Environment, cfg DeployLockReleasePoolConfig) error {
	return verifyComponent(e, cfg.ChainSelector, poolStackDeps, func() error {
		if cfg.Token == "" {
			return fmt.Errorf("token is required: pass the token contract strkey")
		}
		if cfg.TokenDecimals == 0 {
			return fmt.Errorf("tokenDecimals is required")
		}
		if cfg.LockBox == "" {
			return fmt.Errorf("lockBox is required: pass the TokenLockBox strkey (deploy DeployTokenLockBox first)")
		}
		if cfg.Qualifier == "" {
			return fmt.Errorf("qualifier is required: one pool per token, identify the instance (e.g. the token symbol)")
		}
		return nil
	})
}

func (DeployLockReleasePool) Apply(e cldf.Environment, cfg DeployLockReleasePoolConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, poolStackDeps, cfg.WasmPath, "pools_lock_release_pool.wasm", sequences.DeployLockReleasePool,
		func(resolved map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployLockReleasePoolInput {
			return sequences.DeployLockReleasePoolInput{
				ChainSelector:     cfg.ChainSelector,
				Qualifier:         cfg.Qualifier,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				Token:             cfg.Token,
				TokenDecimals:     cfg.TokenDecimals,
				LockBox:           cfg.LockBox,
				Router:            resolved["Router"],
				RampRegistry:      resolved["RampRegistry"],
				RmnProxy:          resolved["RMN Proxy"],
				ExistingAddresses: existing,
			}
		})
}
