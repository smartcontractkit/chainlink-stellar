package changesets

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeploySiloedLockReleasePoolConfig is the input of the siloed lock-release
// pool deploy changeset. The pool takes the token from config; the
// Router/RampRegistry/RMN Proxy strkeys are resolved from the environment
// datastore, not configured here. There is no immutable lock box at
// initialize — the per-remote-chain lock boxes are wired post-deploy
// (configure_lock_boxes), an owner-gated config op, and per-remote-chain pool
// config is applied at lane-configuration time.
type DeploySiloedLockReleasePoolConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Qualifier     string `json:"qualifier" yaml:"qualifier"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	Token         string `json:"token" yaml:"token"`
	TokenDecimals uint32 `json:"tokenDecimals" yaml:"tokenDecimals"`
}

// DeploySiloedLockReleasePool deploys and initializes the siloed lock-release
// token pool on one Stellar chain. Requires Router, RampRegistry and RMN
// Proxy, a non-empty Token strkey with its decimals, and a non-empty Qualifier
// (more than one pool can live on a chain; the qualifier keeps their datastore
// refs, salts and op reports distinct — typically the token symbol).
type DeploySiloedLockReleasePool struct{}

var _ cldf.ChangeSetV2[DeploySiloedLockReleasePoolConfig] = DeploySiloedLockReleasePool{}

func (DeploySiloedLockReleasePool) VerifyPreconditions(e cldf.Environment, cfg DeploySiloedLockReleasePoolConfig) error {
	return verifyComponent(e, cfg.ChainSelector, poolStackDeps, func() error {
		if cfg.Token == "" {
			return fmt.Errorf("token is required: pass the token contract strkey")
		}
		if cfg.TokenDecimals == 0 {
			return fmt.Errorf("tokenDecimals is required")
		}
		if cfg.Qualifier == "" {
			return fmt.Errorf("qualifier is required: one pool per token, identify the instance (e.g. the token symbol)")
		}
		return nil
	})
}

func (DeploySiloedLockReleasePool) Apply(e cldf.Environment, cfg DeploySiloedLockReleasePoolConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, poolStackDeps, cfg.WasmPath, "pools_siloed_lock_release_pool.wasm", sequences.DeploySiloedLockReleasePool,
		func(resolved map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeploySiloedLockReleasePoolInput {
			return sequences.DeploySiloedLockReleasePoolInput{
				ChainSelector:     cfg.ChainSelector,
				Qualifier:         cfg.Qualifier,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				Token:             cfg.Token,
				TokenDecimals:     cfg.TokenDecimals,
				Router:            resolved["Router"],
				RampRegistry:      resolved["RampRegistry"],
				RmnProxy:          resolved["RMN Proxy"],
				ExistingAddresses: existing,
			}
		})
}
