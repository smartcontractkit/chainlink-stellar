package main

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-plugin"
	chainsel "github.com/smartcontractkit/chain-selectors"

	"github.com/smartcontractkit/chainlink-common/pkg/beholder"
	"github.com/smartcontractkit/chainlink-common/pkg/loop"
	"github.com/smartcontractkit/chainlink-common/pkg/types/core"

	"github.com/smartcontractkit/chainlink-stellar/relayer"
	"github.com/smartcontractkit/chainlink-stellar/relayer/chain"
	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
)

const loggerName = "PluginStellar"

func main() {
	s := loop.MustNewStartedServer(loggerName)
	defer s.Stop()

	p := &pluginRelayer{Plugin: loop.Plugin{Logger: s.Logger}}
	defer s.Logger.ErrorIfFn(p.Close, "Failed to close")

	s.MustRegister(p)

	stopCh := make(chan struct{})
	defer close(stopCh)

	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: loop.PluginRelayerHandshakeConfig(),
		Plugins: map[string]plugin.Plugin{
			loop.PluginRelayerName: &loop.GRPCPluginRelayer{
				PluginServer: p,
				BrokerConfig: loop.BrokerConfig{
					StopCh:   stopCh,
					Logger:   s.Logger,
					GRPCOpts: s.GRPCOpts,
				},
			},
		},
		GRPCServer: s.GRPCOpts.NewServer,
	})
}

type pluginRelayer struct {
	loop.Plugin
}

// NewRelayer is the LOOP factory method invoked by the Chainlink node
func (p *pluginRelayer) NewRelayer(
	ctx context.Context,
	rawConfig string,
	keystore core.Keystore,
	_ core.Keystore,
	_ core.CapabilitiesRegistry,
) (loop.Relayer, error) {
	cfg, err := config.NewDecodedTOMLConfig(rawConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to read Stellar config: %w", err)
	}

	rawNodes := make([]map[string]string, 0, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		if n == nil || n.URL == nil {
			continue
		}
		rawNodes = append(rawNodes, map[string]string{"URL": n.URL.String()})
	}
	emitter := loop.NewPluginRelayerConfigEmitter(
		p.Logger,
		beholder.GetClient().Config.AuthPublicKeyHex,
		cfg.ChainID,
		rawNodes,
	)
	if err = emitter.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start config emitter: %w", err)
	}
	p.SubService(emitter)

	var stellarChain chainsel.StellarChain
	for _, sc := range chainsel.StellarALL {
		if sc.ChainID == cfg.ChainID {
			stellarChain = sc
		}
	}

	chainService, err := chain.NewChain(cfg, chain.Opts{Logger: p.Logger, KeyStore: keystore}, stellarChain)
	if err != nil {
		return nil, fmt.Errorf("failed to create Stellar chain: %w", err)
	}

	relay := relayer.NewRelayer(p.Logger, chainService)
	p.SubService(relay)

	return relay, nil
}
