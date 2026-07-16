package config

import (
	"log"
	"strings"

	clconfig "github.com/smartcontractkit/chainlink-common/pkg/config"
	"github.com/smartcontractkit/chainlink-common/pkg/config/configtest"
)

// defaults is the full default TOMLConfig decoded from docs.toml at init.
// Defaults() returns a copy so callers can mutate it without touching this var.
var defaults TOMLConfig

func init() {
	if err := configtest.DocDefaultsOnly(strings.NewReader(docsTOML), &defaults, clconfig.DecodeTOML); err != nil {
		log.Fatalf("Failed to initialize Stellar config defaults from docs: %v", err)
	}
}

// Defaults returns a TOMLConfig with every field set to its docs.toml default.
// The returned value is a copy; mutating it does not affect the package-level defaults.
func Defaults() (c TOMLConfig) {
	c.SetFrom(&defaults)
	return
}

// SetFrom copies every non-nil field from f onto c, leaving c unchanged for
// fields that are nil in f. It is the merge primitive used by SetDefaults:
// start from Defaults(), then SetFrom the user's partial config so only the
// fields they set override the defaults.
//
// ChainID is a plain string (not *string) because it is mandatory and validated
// separately in ValidateConfig; a non-empty value in f wins.
func (c *TOMLConfig) SetFrom(f *TOMLConfig) {
	if f.Enabled != nil {
		c.Enabled = f.Enabled
	}
	if f.ChainID != "" {
		c.ChainID = f.ChainID
	}
	c.TxManager.SetFrom(&f.TxManager)
	c.MultiNode.SetFrom(&f.MultiNode)
	c.Nodes.SetFrom(&f.Nodes)
	if f.RequestTimeout != nil {
		c.RequestTimeout = f.RequestTimeout
	}
}

// SetFrom copies every non-nil field from f onto c, leaving c unchanged for
// fields that are nil in f.
//
// Simulation hints are the one exception to the copy-if-set rule: they are
// additive. User-supplied hints are merged onto whatever c already has
// (typically the built-in defaults), deduped, rather than replacing them.
func (c *Config) SetFrom(f *Config) {
	if f.BroadcastChanSize != nil {
		c.BroadcastChanSize = f.BroadcastChanSize
	}
	if f.ConfirmPollInterval != nil {
		c.ConfirmPollInterval = f.ConfirmPollInterval
	}
	if f.BaseInclusionFee != nil {
		c.BaseInclusionFee = f.BaseInclusionFee
	}
	if f.MaxInclusionFee != nil {
		c.MaxInclusionFee = f.MaxInclusionFee
	}
	if f.FeeBumpMultiplier != nil {
		c.FeeBumpMultiplier = f.FeeBumpMultiplier
	}
	if f.ResourceFeeBuffer != nil {
		c.ResourceFeeBuffer = f.ResourceFeeBuffer
	}
	if f.RestoreFeeBuffer != nil {
		c.RestoreFeeBuffer = f.RestoreFeeBuffer
	}
	if f.FeeStatsPollInterval != nil {
		c.FeeStatsPollInterval = f.FeeStatsPollInterval
	}
	if f.MaxSimulateAttempts != nil {
		c.MaxSimulateAttempts = f.MaxSimulateAttempts
	}
	if f.MaxSubmitRetryAttempts != nil {
		c.MaxSubmitRetryAttempts = f.MaxSubmitRetryAttempts
	}
	if f.SubmitRetryDelay != nil {
		c.SubmitRetryDelay = f.SubmitRetryDelay
	}
	if f.TxTimeoutSecs != nil {
		c.TxTimeoutSecs = f.TxTimeoutSecs
	}
	if f.LedgerBoundsOffset != nil {
		c.LedgerBoundsOffset = f.LedgerBoundsOffset
	}
	if f.MaxTxRetryAttempts != nil {
		c.MaxTxRetryAttempts = f.MaxTxRetryAttempts
	}
	if f.MaxGetClientRetryAttempts != nil {
		c.MaxGetClientRetryAttempts = f.MaxGetClientRetryAttempts
	}
	if f.MaxRestoreAttempts != nil {
		c.MaxRestoreAttempts = f.MaxRestoreAttempts
	}
	// Simulation hints are additive: user extras merge onto built-in defaults.
	if len(f.SimulationTerminalHints) > 0 {
		c.SimulationTerminalHints = mergeSimulationHintLists(c.SimulationTerminalHints, f.SimulationTerminalHints)
	}
	if len(f.SimulationRetryableHints) > 0 {
		c.SimulationRetryableHints = mergeSimulationHintLists(c.SimulationRetryableHints, f.SimulationRetryableHints)
	}
	if f.PruneInterval != nil {
		c.PruneInterval = f.PruneInterval
	}
	if f.PruneTxExpiration != nil {
		c.PruneTxExpiration = f.PruneTxExpiration
	}
}

// SetFrom merges nodes from f into ns by Name. A node in f whose Name is not
// already in ns is appended; a node with a matching Name has its fields overlaid.
func (ns *Nodes) SetFrom(fs *Nodes) {
	for _, f := range *fs {
		if f.Name == nil {
			*ns = append(*ns, f)
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
			*ns = append(*ns, f)
		} else {
			(*ns)[idx].SetFrom(f)
		}
	}
}

// SetFrom copies every non-nil field from f onto n.
func (n *Node) SetFrom(f *Node) {
	if f.Name != nil {
		n.Name = f.Name
	}
	if f.URL != nil {
		n.URL = f.URL
	}
	if f.Order != nil {
		n.Order = f.Order
	}
}
