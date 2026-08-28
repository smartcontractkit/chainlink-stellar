package chain

import (
	"context"
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-common/pkg/chains"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"

	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
)

func newTestTOMLConfig(t *testing.T) *config.TOMLConfig {
	t.Helper()
	cfg, err := config.NewDecodedTOMLConfig(`
ChainID = "` + chainsel.STELLAR_TESTNET.ChainID + `"

[[Nodes]]
Name = "primary"
URL = "http://localhost:8000"

[[Nodes]]
Name = "secondary"
URL = "http://localhost:8001"
`)
	require.NoError(t, err)
	return cfg
}

func TestNewMultiNode(t *testing.T) {
	t.Parallel()

	cfg := newTestTOMLConfig(t)
	mn, err := newMultiNode(cfg, logger.Test(t))
	require.NoError(t, err)
	require.NotNil(t, mn)

	// An unstarted pool has no live node, so selection fails fast rather than returning a
	// usable client. This is the path that surfaces multinode.ErrNodeError through GetClient.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = mn.SelectRPC(ctx)
	require.Error(t, err)
}

func TestChainListNodeStatuses(t *testing.T) {
	t.Parallel()

	cfg := newTestTOMLConfig(t)
	mn, err := newMultiNode(cfg, logger.Test(t))
	require.NoError(t, err)

	c := &chain{
		chainInfo: chainsel.STELLAR_TESTNET,
		cfg:       cfg,
		multiNode: mn,
	}

	t.Run("returns all configured nodes", func(t *testing.T) {
		statuses, nextPageToken, total, err := c.ListNodeStatuses(t.Context(), 10, "")
		require.NoError(t, err)

		require.Equal(t, 2, total)
		require.Empty(t, nextPageToken)
		require.Len(t, statuses, 2)

		require.Equal(t, chainsel.STELLAR_TESTNET.ChainID, statuses[0].ChainID)
		require.Equal(t, "primary", statuses[0].Name)
		require.Equal(t, "Undialed", statuses[0].State)
		require.Contains(t, statuses[0].Config, "primary")
		require.Contains(t, statuses[0].Config, "http://localhost:8000")

		require.Equal(t, chainsel.STELLAR_TESTNET.ChainID, statuses[1].ChainID)
		require.Equal(t, "secondary", statuses[1].Name)
		require.Equal(t, "Undialed", statuses[1].State)
		require.Contains(t, statuses[1].Config, "secondary")
		require.Contains(t, statuses[1].Config, "http://localhost:8001")
	})

	t.Run("paginates configured nodes", func(t *testing.T) {
		firstPage, nextPageToken, total, err := c.ListNodeStatuses(t.Context(), 1, "")
		require.NoError(t, err)

		require.Equal(t, 2, total)
		require.Len(t, firstPage, 1)
		require.Equal(t, "primary", firstPage[0].Name)
		require.NotEmpty(t, nextPageToken)

		secondPage, nextPageToken, total, err := c.ListNodeStatuses(t.Context(), 1, nextPageToken)
		require.NoError(t, err)

		require.Equal(t, 2, total)
		require.Len(t, secondPage, 1)
		require.Equal(t, "secondary", secondPage[0].Name)
		require.Empty(t, nextPageToken)
	})

	t.Run("returns out of range", func(t *testing.T) {
		statuses, total, err := c.listNodeStatuses(len(cfg.Nodes), len(cfg.Nodes)+1)

		require.ErrorIs(t, err, chains.ErrOutOfRange)
		require.Nil(t, statuses)
		require.Equal(t, len(cfg.Nodes), total)
	})
}
