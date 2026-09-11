package ccvchain

import (
	"strconv"
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	sourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStellarChainConfigLoader(t *testing.T) {
	stellarChainID := chainsel.STELLAR_LOCALNET.ChainID
	wantSelector := strconv.FormatUint(chainsel.STELLAR_LOCALNET.Selector, 10)

	t.Run("skips non-Stellar outputs", func(t *testing.T) {
		out, err := StellarChainConfigLoader([]*blockchain.Output{
			{Family: chainsel.FamilyEVM, ChainID: "1"},
		})
		require.NoError(t, err)
		assert.Empty(t, out)
	})

	t.Run("builds ReaderConfig per Stellar output", func(t *testing.T) {
		out, err := StellarChainConfigLoader([]*blockchain.Output{
			{
				Family:  chainsel.FamilyStellar,
				ChainID: stellarChainID,
				Nodes: []*blockchain.Node{
					{InternalHTTPUrl: "http://stellar-rpc:8080"},
				},
				NetworkSpecificData: &blockchain.NetworkSpecificData{
					StellarNetwork: &blockchain.StellarNetworkInfo{
						NetworkPassphrase: "Standalone Network ; February 2017",
					},
				},
			},
		})
		require.NoError(t, err)
		require.Contains(t, out, wantSelector)
		cfg, ok := out[wantSelector].(sourcereader.ReaderConfig)
		require.True(t, ok, "expected sourcereader.ReaderConfig value")
		assert.Equal(t, "http://stellar-rpc:8080", cfg.SorobanRPCURL)
		assert.Equal(t, "Standalone Network ; February 2017", cfg.NetworkPassphrase)
		assert.Empty(t, cfg.OnRampContractID)
		assert.Empty(t, cfg.RMNRemoteContractID)
	})

	t.Run("error on unknown Stellar chain id", func(t *testing.T) {
		_, err := StellarChainConfigLoader([]*blockchain.Output{
			{
				Family:  chainsel.FamilyStellar,
				ChainID: "unknown-stellar-chain-id",
				Nodes:   []*blockchain.Node{{InternalHTTPUrl: "http://x"}},
				NetworkSpecificData: &blockchain.NetworkSpecificData{
					StellarNetwork: &blockchain.StellarNetworkInfo{NetworkPassphrase: "p"},
				},
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get chain details")
	})
}
