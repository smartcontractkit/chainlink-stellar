package config

import (
	"log"
	"strings"

	clconfig "github.com/smartcontractkit/chainlink-common/pkg/config"
	"github.com/smartcontractkit/chainlink-common/pkg/config/configtest"
)

// defaults is the full default TOMLConfig decoded from docs.toml at init.
var defaults TOMLConfig

func init() {
	if err := configtest.DocDefaultsOnly(strings.NewReader(docsTOML), &defaults, clconfig.DecodeTOML); err != nil {
		log.Fatalf("Failed to initialize Stellar config defaults from docs: %v", err)
	}
}

// Defaults returns a copy of the docs.toml defaults.
func Defaults() (c TOMLConfig) {
	c.SetFrom(&defaults)
	return
}

// SetFrom copies non-nil fields from f onto c (deep-copied, no pointer sharing).
// ChainID is a plain string; a non-empty value in f wins.
func (c *TOMLConfig) SetFrom(f *TOMLConfig) {
	if f.Enabled != nil {
		v := *f.Enabled
		c.Enabled = &v
	}
	if f.ChainID != "" {
		c.ChainID = f.ChainID
	}
	c.TxManager.SetFrom(&f.TxManager)
	c.MultiNode.SetFrom(&f.MultiNode)
	c.Nodes.SetFrom(&f.Nodes)
	if f.RequestTimeout != nil {
		v := *f.RequestTimeout
		c.RequestTimeout = &v
	}
}

// SetFrom copies non-nil fields from f onto c (deep-copied). Simulation hints
// are additive: user hints merge onto c's existing hints, deduped.
func (c *TxManagerConfig) SetFrom(f *TxManagerConfig) {
	if f.BroadcastChanSize != nil {
		v := *f.BroadcastChanSize
		c.BroadcastChanSize = &v
	}
	if f.ConfirmPollInterval != nil {
		v := *f.ConfirmPollInterval
		c.ConfirmPollInterval = &v
	}
	if f.BaseInclusionFee != nil {
		v := *f.BaseInclusionFee
		c.BaseInclusionFee = &v
	}
	if f.MaxInclusionFee != nil {
		v := *f.MaxInclusionFee
		c.MaxInclusionFee = &v
	}
	if f.FeeBumpMultiplier != nil {
		v := *f.FeeBumpMultiplier
		c.FeeBumpMultiplier = &v
	}
	if f.ResourceFeeBuffer != nil {
		v := *f.ResourceFeeBuffer
		c.ResourceFeeBuffer = &v
	}
	if f.RestoreFeeBuffer != nil {
		v := *f.RestoreFeeBuffer
		c.RestoreFeeBuffer = &v
	}
	if f.FeeStatsPollInterval != nil {
		v := *f.FeeStatsPollInterval
		c.FeeStatsPollInterval = &v
	}
	if f.MaxSimulateAttempts != nil {
		v := *f.MaxSimulateAttempts
		c.MaxSimulateAttempts = &v
	}
	if f.MaxSubmitRetryAttempts != nil {
		v := *f.MaxSubmitRetryAttempts
		c.MaxSubmitRetryAttempts = &v
	}
	if f.SubmitRetryDelay != nil {
		v := *f.SubmitRetryDelay
		c.SubmitRetryDelay = &v
	}
	if f.TxTimeoutSecs != nil {
		v := *f.TxTimeoutSecs
		c.TxTimeoutSecs = &v
	}
	if f.LedgerBoundsOffset != nil {
		v := *f.LedgerBoundsOffset
		c.LedgerBoundsOffset = &v
	}
	if f.MaxTxRetryAttempts != nil {
		v := *f.MaxTxRetryAttempts
		c.MaxTxRetryAttempts = &v
	}
	if f.MaxGetClientRetryAttempts != nil {
		v := *f.MaxGetClientRetryAttempts
		c.MaxGetClientRetryAttempts = &v
	}
	if f.MaxRestoreAttempts != nil {
		v := *f.MaxRestoreAttempts
		c.MaxRestoreAttempts = &v
	}
	// Simulation hints are additive: user extras merge onto built-in defaults.
	if len(f.SimulationTerminalHints) > 0 {
		c.SimulationTerminalHints = mergeSimulationHintLists(c.SimulationTerminalHints, f.SimulationTerminalHints)
	}
	if len(f.SimulationRetryableHints) > 0 {
		c.SimulationRetryableHints = mergeSimulationHintLists(c.SimulationRetryableHints, f.SimulationRetryableHints)
	}
	if f.PruneInterval != nil {
		v := *f.PruneInterval
		c.PruneInterval = &v
	}
	if f.PruneTxExpiration != nil {
		v := *f.PruneTxExpiration
		c.PruneTxExpiration = &v
	}
}

// SetFrom merges nodes from f into ns by Name: new names appended, matching names overlaid.
func (ns *Nodes) SetFrom(fs *Nodes) {
	for _, f := range *fs {
		if f.Name == nil {
			cp := *f
			*ns = append(*ns, &cp)
			continue
		}
		idx := -1
		for i, n := range *ns {
			if n.Name != nil && *n.Name == *f.Name {
				idx = i
				break
			}
		}
		if idx == -1 {
			cp := *f
			*ns = append(*ns, &cp)
		} else {
			(*ns)[idx].SetFrom(f)
		}
	}
}

// SetFrom copies non-nil fields from f onto n (deep-copied).
func (n *Node) SetFrom(f *Node) {
	if f.Name != nil {
		v := *f.Name
		n.Name = &v
	}
	if f.URL != nil {
		n.URL = f.URL // clconfig.URL is a value type wrapping url.URL; safe to copy
	}
	if f.Order != nil {
		v := *f.Order
		n.Order = &v
	}
}
