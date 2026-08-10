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
	// fields use defaults applied by SetDefaults (from docs.toml).
	TxManager TxManagerConfig `toml:"TxManager"`

	// BalanceMonitor holds optional account balance monitor settings. Omitted
	// fields use defaults applied by SetDefaults (from docs.toml).
	BalanceMonitor BalanceMonitorConfig `toml:"BalanceMonitor"`

	// MultiNode configures RPC node selection, health checking, and failover. Omitted fields
	// are filled by SetDefaults. See chainlink-framework/multinode.
	MultiNode mncfg.MultiNodeConfig `toml:"MultiNode"`

	// RequestTimeout bounds each individual Soroban RPC call (and the underlying HTTP client
	// timeout). Defaults to DefaultRequestTimeout when unset.
	RequestTimeout *clconfig.Duration `toml:"RequestTimeout"`
}

// DefaultRequestTimeout bounds each individual Soroban RPC call when RequestTimeout is unset.
const DefaultRequestTimeout = 30 * time.Second

// BalanceMonitorConfig configures the keystore account balance monitor.
type BalanceMonitorConfig struct {
	// BalancePollPeriod is the rate to poll account balances for the
	// account_balance telemetry gauge.
	BalancePollPeriod *clconfig.Duration `toml:"BalancePollPeriod"`
}

// SetDefaults fills unset fields with docs.toml defaults via SetFrom, then
// resolves TXM simulation hints. Returns a fully-resolved config in one phase.
func (c *TOMLConfig) SetDefaults() {
	d := Defaults()
	d.SetFrom(c)
	// docs.toml has empty hint arrays; Resolve fills the built-in defaults.
	d.TxManager.Resolve()
	*c = d
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
