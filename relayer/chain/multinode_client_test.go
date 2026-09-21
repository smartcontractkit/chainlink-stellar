package chain_test

import (
	"errors"
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-stellar/relayer/chain"
	"github.com/smartcontractkit/chainlink-stellar/relayer/mocks"
)

const testnetPassphrase = "Test SDF Network ; September 2015"

// newTestAdapter wires a MultiNodeClient over a mocked SorobanClient. RPCClientBase is left nil:
// the methods under test (ChainID/Dial/ClientVersion/IsSyncing and promoted domain calls) do not
// touch it.
func newTestAdapter(rpc chain.SorobanClient) *chain.MultiNodeClient {
	return &chain.MultiNodeClient{SorobanClient: rpc}
}

func TestHead(t *testing.T) {
	t.Parallel()

	t.Run("valid head maps sequence to block number", func(t *testing.T) {
		h := &chain.Head{Sequence: 1234}
		require.True(t, h.IsValid())
		require.Equal(t, int64(1234), h.BlockNumber())
		require.Nil(t, h.BlockDifficulty())
		require.Nil(t, h.GetTotalDifficulty())
	})

	t.Run("zero sequence and nil are invalid", func(t *testing.T) {
		require.False(t, (&chain.Head{Sequence: 0}).IsValid())
		require.Equal(t, int64(0), (&chain.Head{Sequence: 0}).BlockNumber())
		var nilHead *chain.Head
		require.False(t, nilHead.IsValid())
		require.Equal(t, int64(0), nilHead.BlockNumber())
	})
}

func TestMultiNodeClient_ChainID(t *testing.T) {
	t.Parallel()

	t.Run("hashes passphrase to the chain-selectors network ID", func(t *testing.T) {
		m := mocks.NewMockSorobanClient(t)
		m.EXPECT().GetNetwork(mock.Anything).
			Return(protocolrpc.GetNetworkResponse{Passphrase: testnetPassphrase}, nil)

		id, err := newTestAdapter(m).ChainID(t.Context())
		require.NoError(t, err)
		// ChainID must equal the chain-selectors ChainID form so VerifyChainID can be enabled.
		require.Equal(t, chainsel.STELLAR_TESTNET.ChainID, id.String())
	})

	t.Run("propagates RPC error", func(t *testing.T) {
		m := mocks.NewMockSorobanClient(t)
		m.EXPECT().GetNetwork(mock.Anything).
			Return(protocolrpc.GetNetworkResponse{}, errors.New("boom"))

		_, err := newTestAdapter(m).ChainID(t.Context())
		require.ErrorContains(t, err, "boom")
	})
}

func TestMultiNodeClient_ClientVersionAndSyncing(t *testing.T) {
	t.Parallel()

	m := mocks.NewMockSorobanClient(t)
	m.EXPECT().GetVersionInfo(mock.Anything).
		Return(protocolrpc.GetVersionInfoResponse{Version: "22.0.0"}, nil)
	c := newTestAdapter(m)

	v, err := c.ClientVersion(t.Context())
	require.NoError(t, err)
	require.Equal(t, "22.0.0", v)

	syncing, err := c.IsSyncing(t.Context())
	require.NoError(t, err)
	require.False(t, syncing)
}

func TestMultiNodeClient_Dial(t *testing.T) {
	t.Parallel()

	t.Run("ok when health succeeds", func(t *testing.T) {
		m := mocks.NewMockSorobanClient(t)
		m.EXPECT().GetHealth(mock.Anything).
			Return(protocolrpc.GetHealthResponse{Status: "healthy"}, nil)
		require.NoError(t, newTestAdapter(m).Dial(t.Context()))
	})

	t.Run("errors when health fails", func(t *testing.T) {
		m := mocks.NewMockSorobanClient(t)
		m.EXPECT().GetHealth(mock.Anything).
			Return(protocolrpc.GetHealthResponse{}, errors.New("unreachable"))
		require.ErrorContains(t, newTestAdapter(m).Dial(t.Context()), "unreachable")
	})
}

func TestMultiNodeClient_ForwardsDomainCall(t *testing.T) {
	t.Parallel()

	// GetFeeStats is a domain RPCClient method promoted from the embedded SorobanClient.
	m := mocks.NewMockSorobanClient(t)
	m.EXPECT().GetFeeStats(mock.Anything).
		Return(protocolrpc.GetFeeStatsResponse{LatestLedger: 42}, nil)

	resp, err := newTestAdapter(m).GetFeeStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, uint32(42), resp.LatestLedger)
}
