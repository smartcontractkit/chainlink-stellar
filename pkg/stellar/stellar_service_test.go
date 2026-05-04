package stellar

import (
	"context"
	"errors"
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/require"

	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"
)

// staticRPCClient is a test stub for RPCClient. Each field holds the func to call for that method.
// A nil field means the method should not be called; an unexpected call panics.
type staticRPCClient struct {
	getLedgerEntries func(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error)
	getLatestLedger  func(ctx context.Context) (protocol.GetLatestLedgerResponse, error)
}

func (s *staticRPCClient) Close() error { return nil }

func (s *staticRPCClient) GetLedgerEntries(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
	if s.getLedgerEntries == nil {
		panic("unexpected call to GetLedgerEntries")
	}
	return s.getLedgerEntries(ctx, req)
}

func (s *staticRPCClient) GetLatestLedger(ctx context.Context) (protocol.GetLatestLedgerResponse, error) {
	if s.getLatestLedger == nil {
		panic("unexpected call to GetLatestLedger")
	}
	return s.getLatestLedger(ctx)
}

func TestStellarService_GetLedgerEntries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("WithLiveUntil", func(t *testing.T) {
		liveUntil := uint32(500)
		stub := &staticRPCClient{
			getLedgerEntries: func(_ context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
				require.Equal(t, []string{"a2V5WERS"}, req.Keys) // base64 "keyXDR"
				return protocol.GetLedgerEntriesResponse{
					LatestLedger: 50,
					Entries: []protocol.LedgerEntryResult{
						{KeyXDR: "a2V5WERS", DataXDR: "ZGF0YXJES", LastModifiedLedger: 30, LiveUntilLedgerSeq: &liveUntil},
					},
				}, nil
			},
		}

		svc := &chain{rpc: stub}
		resp, err := svc.GetLedgerEntries(ctx, stellartypes.GetLedgerEntriesRequest{Keys: []stellartypes.XDR{"a2V5WERS"}})
		require.NoError(t, err)
		require.Equal(t, uint32(50), resp.LatestLedger)
		require.Len(t, resp.Entries, 1)
		require.Equal(t, stellartypes.XDR("a2V5WERS"), resp.Entries[0].KeyXDR)
		require.Equal(t, stellartypes.XDR("ZGF0YXJES"), resp.Entries[0].DataXDR)
		require.Equal(t, uint32(30), resp.Entries[0].LastModifiedLedger)
		require.NotNil(t, resp.Entries[0].LiveUntilLedgerSeq)
		require.Equal(t, liveUntil, *resp.Entries[0].LiveUntilLedgerSeq)
	})

	t.Run("NoLiveUntil", func(t *testing.T) {
		stub := &staticRPCClient{
			getLedgerEntries: func(_ context.Context, _ protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
				return protocol.GetLedgerEntriesResponse{
					LatestLedger: 60,
					Entries: []protocol.LedgerEntryResult{
						{KeyXDR: "a2V5Mg==", DataXDR: "ZGF0YTI=", LastModifiedLedger: 40},
					},
				}, nil
			},
		}

		svc := &chain{rpc: stub}
		resp, err := svc.GetLedgerEntries(ctx, stellartypes.GetLedgerEntriesRequest{Keys: []stellartypes.XDR{"a2V5Mg=="}})
		require.NoError(t, err)
		require.Len(t, resp.Entries, 1)
		require.Nil(t, resp.Entries[0].LiveUntilLedgerSeq)
		require.Equal(t, uint32(60), resp.LatestLedger)
	})

	t.Run("MixedLiveUntil", func(t *testing.T) {
		// Two entries: first has LiveUntilLedgerSeq set, second does not.
		// Guards against the loop carrying the pointer from the first entry into the second.
		liveUntil := uint32(777)
		stub := &staticRPCClient{
			getLedgerEntries: func(_ context.Context, _ protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
				return protocol.GetLedgerEntriesResponse{
					LatestLedger: 70,
					Entries: []protocol.LedgerEntryResult{
						{KeyXDR: "azE=", DataXDR: "ZDE=", LastModifiedLedger: 10, LiveUntilLedgerSeq: &liveUntil},
						{KeyXDR: "azI=", DataXDR: "ZDI=", LastModifiedLedger: 20},
					},
				}, nil
			},
		}

		svc := &chain{rpc: stub}
		resp, err := svc.GetLedgerEntries(ctx, stellartypes.GetLedgerEntriesRequest{Keys: []stellartypes.XDR{"azE=", "azI="}})
		require.NoError(t, err)
		require.Len(t, resp.Entries, 2)
		require.NotNil(t, resp.Entries[0].LiveUntilLedgerSeq)
		require.Equal(t, liveUntil, *resp.Entries[0].LiveUntilLedgerSeq)
		require.Nil(t, resp.Entries[1].LiveUntilLedgerSeq)
	})

	t.Run("RPCError", func(t *testing.T) {
		stub := &staticRPCClient{
			getLedgerEntries: func(_ context.Context, _ protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
				return protocol.GetLedgerEntriesResponse{}, errors.New("ledger gone")
			},
		}

		svc := &chain{rpc: stub}
		_, err := svc.GetLedgerEntries(ctx, stellartypes.GetLedgerEntriesRequest{Keys: []stellartypes.XDR{"a2V5"}})
		require.ErrorContains(t, err, "GetLedgerEntries")
		require.ErrorContains(t, err, "ledger gone")
	})
}

func TestStellarService_GetLatestLedger(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("OK", func(t *testing.T) {
		stub := &staticRPCClient{
			getLatestLedger: func(_ context.Context) (protocol.GetLatestLedgerResponse, error) {
				return protocol.GetLatestLedgerResponse{
					Hash:            "ledgerhash",
					ProtocolVersion: 21,
					Sequence:        1234,
					LedgerCloseTime: 9876543210,
				}, nil
			},
		}

		svc := &chain{rpc: stub}
		resp, err := svc.GetLatestLedger(ctx)
		require.NoError(t, err)
		require.Equal(t, stellartypes.LedgerHash("ledgerhash"), resp.Hash)
		require.Equal(t, uint32(21), resp.ProtocolVersion)
		require.Equal(t, uint32(1234), resp.Sequence)
		require.Equal(t, int64(9876543210), resp.LedgerCloseTime)
	})

	t.Run("RPCError", func(t *testing.T) {
		stub := &staticRPCClient{
			getLatestLedger: func(_ context.Context) (protocol.GetLatestLedgerResponse, error) {
				return protocol.GetLatestLedgerResponse{}, errors.New("connection refused")
			},
		}

		svc := &chain{rpc: stub}
		_, err := svc.GetLatestLedger(ctx)
		require.ErrorContains(t, err, "GetLatestLedger")
		require.ErrorContains(t, err, "connection refused")
	})
}
