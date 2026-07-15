package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	chain_selectors "github.com/smartcontractkit/chain-selectors"

	clconfig "github.com/smartcontractkit/chainlink-common/pkg/config"
	mncfg "github.com/smartcontractkit/chainlink-framework/multinode/config"
)

// ChainFamilyName is the canonical chain family identifier for Stellar.
const ChainFamilyName = "stellar"

// Node represents a single Soroban RPC endpoint.
type Node struct {
	Name *string       `toml:"Name"`
	URL  *clconfig.URL `toml:"URL"`
	// Order is the node priority used as a tiebreak by the multinode selector (lower wins on
	// equal head). Defaults to 0 when unset.
	Order *int32 `toml:"Order"`
}

// ValidateConfig returns an error for any missing or empty required field.
func (n *Node) ValidateConfig() (err error) {
	if n.Name == nil {
		err = errors.Join(err, clconfig.ErrMissing{Name: "Name", Msg: "required for all nodes"})
	} else if *n.Name == "" {
		err = errors.Join(err, clconfig.ErrEmpty{Name: "Name", Msg: "required for all nodes"})
	}
	if n.URL == nil {
		err = errors.Join(err, clconfig.ErrMissing{Name: "URL", Msg: "required for all nodes"})
	}
	return
}

// Nodes is a slice of Node pointers.
type Nodes []*Node

// TOMLConfig is the parsed configuration for a single Stellar chain as it
// appears inside [[Stellar]] in the Chainlink node TOML.
type TOMLConfig struct {
	// Enabled controls whether this chain is active. Nil means enabled (default).
	Enabled *bool `toml:"Enabled"`

	// ChainID is the unique identifier for this chain configuration
	ChainID string `toml:"ChainID"`

	// Nodes lists the Soroban RPC endpoints for this chain.
	Nodes Nodes `toml:"Nodes"`

	// TxManager holds optional Stellar transaction manager settings. Omitted
	// fields use defaults applied inside txm.New (see DefaultConfigSet).
	TxManager Config `toml:"TxManager"`

	// MultiNode configures RPC node selection, health checking, and failover. Omitted fields
	// are filled by SetDefaults. See chainlink-framework/multinode.
	MultiNode mncfg.MultiNodeConfig `toml:"MultiNode"`

	// RequestTimeout bounds each individual Soroban RPC call (and the underlying HTTP client
	// timeout). Defaults to DefaultRequestTimeout when unset.
	RequestTimeout *clconfig.Duration `toml:"RequestTimeout"`
}

// DefaultRequestTimeout bounds each individual Soroban RPC call when RequestTimeout is unset.
const DefaultRequestTimeout = 30 * time.Second

// SetDefaults fills any unset MultiNode field with a Stellar-appropriate default. The
// framework's config accessors dereference these pointers directly, so every field consumed by
// the node/multinode lifecycle must be non-nil. Tuned to Stellar's ~5-7s ledger close and its
// single-finality model (a closed ledger is final: no finality tag, no reorgs).
func (c *TOMLConfig) SetDefaults() {
	m := &c.MultiNode.MultiNode
	setDefault(&m.Enabled, true)
	setDefault(&m.PollFailureThreshold, uint32(5))
	setDefault(&m.PollInterval, *clconfig.MustNewDuration(10 * time.Second))
	setDefault(&m.SelectionMode, "HighestHead")
	setDefault(&m.SyncThreshold, uint32(5))
	setDefault(&m.NodeIsSyncingEnabled, false)
	setDefault(&m.LeaseDuration, *clconfig.MustNewDuration(0))
	// Poll heads slightly faster than the ~5-7s ledger close so out-of-sync nodes are detected
	setDefault(&m.NewHeadsPollInterval, *clconfig.MustNewDuration(3 * time.Second))
	setDefault(&m.FinalizedBlockPollInterval, *clconfig.MustNewDuration(3 * time.Second))
	setDefault(&m.EnforceRepeatableRead, false)
	setDefault(&m.DeathDeclarationDelay, *clconfig.MustNewDuration(20 * time.Second))
	setDefault(&m.VerifyChainID, true)
	setDefault(&m.NodeNoNewHeadsThreshold, *clconfig.MustNewDuration(30 * time.Second))
	// NoNewFinalizedHeadsThreshold is read unconditionally by the node lifecycle even though
	// the finalized-head subscription is disabled (FinalityTagEnabled=false); keep it non-nil.
	setDefault(&m.NoNewFinalizedHeadsThreshold, *clconfig.MustNewDuration(30 * time.Second))
	// Stellar ledgers are final at close: derive "finalized" as latest (FinalityDepth=0) and
	// never run the finalized-head subscription (FinalityTagEnabled=false).
	setDefault(&m.FinalityDepth, uint32(0))
	setDefault(&m.FinalityTagEnabled, false)
	setDefault(&m.FinalizedBlockOffset, uint32(0))
	setDefault(&c.RequestTimeout, *clconfig.MustNewDuration(DefaultRequestTimeout))
}

func setDefault[T any](p **T, val T) {
	if *p == nil {
		v := val
		*p = &v
	}
}

// IsEnabled returns true when the chain is not explicitly disabled.
func (c *TOMLConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// ValidateConfig returns a combined error for all invalid or missing required fields
func (c *TOMLConfig) ValidateConfig() (err error) {
	if !slices.Contains([]string{
		chain_selectors.STELLAR_MAINNET.ChainID,
		chain_selectors.STELLAR_TESTNET.ChainID,
		chain_selectors.STELLAR_LOCALNET.ChainID},
		c.ChainID) {
		err = errors.Join(err, fmt.Errorf("invalid chain ID %q", c.ChainID))
	}

	if len(c.Nodes) == 0 {
		err = errors.Join(err, clconfig.ErrMissing{Name: "Nodes", Msg: "must have at least one node"})
	} else {
		for _, node := range c.Nodes {
			err = errors.Join(err, node.ValidateConfig())
		}
	}
	return
}

// TOMLString serialises the config back to TOML
func (c *TOMLConfig) TOMLString() (string, error) {
	b, err := toml.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// NewDecodedTOMLConfig decodes rawConfig as Stellar TOML and validates the result
func NewDecodedTOMLConfig(rawConfig string) (*TOMLConfig, error) {
	d := toml.NewDecoder(strings.NewReader(rawConfig))
	d.DisallowUnknownFields()

	var cfg TOMLConfig
	if err := d.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode Stellar config TOML: %w", err)
	}

	if !cfg.IsEnabled() {
		return nil, fmt.Errorf("cannot create chain with ID %s: config is disabled", cfg.ChainID)
	}

	cfg.SetDefaults()

	if err := cfg.ValidateConfig(); err != nil {
		return nil, fmt.Errorf("invalid Stellar config: %w", err)
	}

	return &cfg, nil
}
