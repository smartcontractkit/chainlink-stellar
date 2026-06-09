package relayer

import (
	"context"
	"errors"
	"math/big"
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/require"

	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"

	"github.com/smartcontractkit/chainlink-stellar/internal/mocks"
	"github.com/smartcontractkit/chainlink-stellar/relayer/chain"
	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
	"github.com/smartcontractkit/chainlink-stellar/relayer/txm"
)

const testStellarAccount = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
const testContractID = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4"

// mockTxManager is a minimal test double for stellarTxManager.
type mockTxManager struct {
	enqueueAndWait func(ctx context.Context, req txm.TxRequest) (*txm.TxResult, error)
}

func (m *mockTxManager) EnqueueAndWait(ctx context.Context, req txm.TxRequest) (*txm.TxResult, error) {
	return m.enqueueAndWait(ctx, req)
}

type stubChain struct {
	chain.Chain
	rpc chain.RPCClient
	cfg *config.TOMLConfig
}

func (s *stubChain) GetClient() (chain.RPCClient, error) { return s.rpc, nil }

func (s *stubChain) TxManager() *txm.StellarTxm { return nil }

func (s *stubChain) Config() *config.TOMLConfig {
	if s.cfg != nil {
		return s.cfg
	}
	return &config.TOMLConfig{TxManager: txm.DefaultConfigSet}
}

// newTestStellarServiceWithTxm builds a stellarService backed by the given mock TXM.
// It constructs the service directly to avoid needing a live chain.TxManager().
func newTestStellarServiceWithTxm(t *testing.T, txMgr stellarTxManager) *stellarService {
	t.Helper()
	return &stellarService{txMgr: txMgr}
}

// newTestStellarService builds a stellarService backed by the given mock RPC client.
func newTestStellarService(t *testing.T, rpc chain.RPCClient) *stellarService {
	t.Helper()
	svc := newStellarService(&stubChain{rpc: rpc})
	return &svc
}

func TestStellarService_GetLedgerEntries(t *testing.T) {
	t.Parallel()

	t.Run("WithLiveUntil", func(t *testing.T) {
		ctx := t.Context()
		liveUntil := uint32(500)
		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{
			Keys: []string{"a2V5WERS"},
		}).Return(protocol.GetLedgerEntriesResponse{
			LatestLedger: 50,
			Entries: []protocol.LedgerEntryResult{
				{KeyXDR: "a2V5WERS", DataXDR: "ZGF0YXJES", LastModifiedLedger: 30, LiveUntilLedgerSeq: &liveUntil},
			},
		}, nil)

		svc := newTestStellarService(t, rpc)
		resp, err := svc.GetLedgerEntries(ctx, stellartypes.GetLedgerEntriesRequest{Keys: []string{"a2V5WERS"}})
		require.NoError(t, err)
		require.Equal(t, uint32(50), resp.LatestLedger)
		require.Len(t, resp.Entries, 1)
		require.Equal(t, "a2V5WERS", resp.Entries[0].KeyXDR)
		require.Equal(t, "ZGF0YXJES", resp.Entries[0].DataXDR)
		require.Equal(t, uint32(30), resp.Entries[0].LastModifiedLedger)
		require.NotNil(t, resp.Entries[0].LiveUntilLedgerSeq)
		require.Equal(t, liveUntil, *resp.Entries[0].LiveUntilLedgerSeq)
	})

	t.Run("NoLiveUntil", func(t *testing.T) {
		ctx := t.Context()
		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{
			Keys: []string{"a2V5Mg=="},
		}).Return(protocol.GetLedgerEntriesResponse{
			LatestLedger: 60,
			Entries: []protocol.LedgerEntryResult{
				{KeyXDR: "a2V5Mg==", DataXDR: "ZGF0YTI=", LastModifiedLedger: 40},
			},
		}, nil)

		svc := newTestStellarService(t, rpc)
		resp, err := svc.GetLedgerEntries(ctx, stellartypes.GetLedgerEntriesRequest{Keys: []string{"a2V5Mg=="}})
		require.NoError(t, err)
		require.Len(t, resp.Entries, 1)
		require.Nil(t, resp.Entries[0].LiveUntilLedgerSeq)
		require.Equal(t, uint32(60), resp.LatestLedger)
	})

	t.Run("MixedLiveUntil", func(t *testing.T) {
		ctx := t.Context()
		liveUntil := uint32(777)
		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{
			Keys: []string{"azE=", "azI="},
		}).Return(protocol.GetLedgerEntriesResponse{
			LatestLedger: 70,
			Entries: []protocol.LedgerEntryResult{
				{KeyXDR: "azE=", DataXDR: "ZDE=", LastModifiedLedger: 10, LiveUntilLedgerSeq: &liveUntil},
				{KeyXDR: "azI=", DataXDR: "ZDI=", LastModifiedLedger: 20},
			},
		}, nil)

		svc := newTestStellarService(t, rpc)
		resp, err := svc.GetLedgerEntries(ctx, stellartypes.GetLedgerEntriesRequest{Keys: []string{"azE=", "azI="}})
		require.NoError(t, err)
		require.Len(t, resp.Entries, 2)
		require.NotNil(t, resp.Entries[0].LiveUntilLedgerSeq)
		require.Equal(t, liveUntil, *resp.Entries[0].LiveUntilLedgerSeq)
		require.Nil(t, resp.Entries[1].LiveUntilLedgerSeq)
	})

	t.Run("RPCError", func(t *testing.T) {
		ctx := t.Context()
		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{
			Keys: []string{"a2V5"},
		}).Return(protocol.GetLedgerEntriesResponse{}, errors.New("ledger gone"))

		svc := newTestStellarService(t, rpc)
		_, err := svc.GetLedgerEntries(ctx, stellartypes.GetLedgerEntriesRequest{Keys: []string{"a2V5"}})
		require.ErrorContains(t, err, "GetLedgerEntries")
		require.ErrorContains(t, err, "ledger gone")
	})
}

func TestStellarService_GetLatestLedger(t *testing.T) {
	t.Parallel()

	t.Run("OK", func(t *testing.T) {
		ctx := t.Context()
		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().GetLatestLedger(ctx).Return(protocol.GetLatestLedgerResponse{
			Hash:            "ledgerhash",
			ProtocolVersion: 21,
			Sequence:        1234,
			LedgerCloseTime: 9876543210,
		}, nil)

		svc := newTestStellarService(t, rpc)
		resp, err := svc.GetLatestLedger(ctx)
		require.NoError(t, err)
		require.Equal(t, "ledgerhash", resp.Hash)
		require.Equal(t, uint32(21), resp.ProtocolVersion)
		require.Equal(t, uint32(1234), resp.Sequence)
		require.Equal(t, int64(9876543210), resp.LedgerCloseTime)
	})

	t.Run("RPCError", func(t *testing.T) {
		ctx := t.Context()
		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().GetLatestLedger(ctx).Return(protocol.GetLatestLedgerResponse{}, errors.New("connection refused"))

		svc := newTestStellarService(t, rpc)
		_, err := svc.GetLatestLedger(ctx)
		require.ErrorContains(t, err, "connection refused")
	})
}

func TestStellarService_SubmitTransaction(t *testing.T) {
	t.Parallel()

	sym := "transfer"
	baseReq := stellartypes.SubmitTransactionRequest{
		IdempotencyKey:     "idem-1",
		FromAddress:        testStellarAccount,
		ContractID:         testContractID,
		Function:           "transfer",
		Args:               []stellartypes.ScVal{{Type: stellartypes.ScValTypeSymbol, Symbol: &sym}},
		LedgerBoundsOffset: 2,
	}

	t.Run("Success_Finalized", func(t *testing.T) {
		ctx := t.Context()
		txMgr := &mockTxManager{
			enqueueAndWait: func(_ context.Context, req txm.TxRequest) (*txm.TxResult, error) {
				require.Equal(t, "idem-1", req.ID)
				require.Equal(t, testStellarAccount, req.FromAddress)
				require.Len(t, req.Operations, 1)
				require.Equal(t, uint32(2), req.LedgerBoundsOffset)
				return &txm.TxResult{
					ID:            "idem-1",
					Hash:          "txhash123",
					Status:        commontypes.Finalized,
					Fee:           big.NewInt(100),
					ResultXDR:     "resultXDR",
					ResultMetaXDR: "metaXDR",
				}, nil
			},
		}

		svc := newTestStellarServiceWithTxm(t, txMgr)
		reply, err := svc.SubmitTransaction(ctx, baseReq)
		require.NoError(t, err)
		require.Equal(t, stellartypes.TxSuccess, reply.TxStatus)
		require.Equal(t, "txhash123", reply.TxHash)
		require.Equal(t, "idem-1", reply.TxIdempotencyKey)
		require.Equal(t, "resultXDR", reply.ResultXDR)
		require.Equal(t, "metaXDR", reply.ResultMetaXDR)
	})

	t.Run("Failed_OnChain", func(t *testing.T) {
		ctx := t.Context()
		txMgr := &mockTxManager{
			enqueueAndWait: func(_ context.Context, _ txm.TxRequest) (*txm.TxResult, error) {
				return &txm.TxResult{
					ID:        "idem-1",
					Hash:      "failhash",
					Status:    commontypes.Failed,
					ResultXDR: "failedResultXDR",
					Error:     errors.New("tx revert: contract error"),
				}, nil
			},
		}

		svc := newTestStellarServiceWithTxm(t, txMgr)
		reply, err := svc.SubmitTransaction(ctx, baseReq)
		require.Error(t, err)
		require.ErrorContains(t, err, "contract error")
		require.Equal(t, stellartypes.TxFailed, reply.TxStatus)
		require.Equal(t, "failhash", reply.TxHash)
	})

	t.Run("Fatal_TxmError", func(t *testing.T) {
		ctx := t.Context()
		txMgr := &mockTxManager{
			enqueueAndWait: func(_ context.Context, _ txm.TxRequest) (*txm.TxResult, error) {
				return nil, errors.New("simulation failed: insufficient fee")
			},
		}

		svc := newTestStellarServiceWithTxm(t, txMgr)
		_, err := svc.SubmitTransaction(ctx, baseReq)
		require.Error(t, err)
		require.ErrorContains(t, err, "simulation failed")
	})

	t.Run("MissingContractID", func(t *testing.T) {
		ctx := t.Context()
		svc := newTestStellarServiceWithTxm(t, &mockTxManager{})
		_, err := svc.SubmitTransaction(ctx, stellartypes.SubmitTransactionRequest{Function: "fn"})
		require.ErrorContains(t, err, "contract_id is required")
	})

	t.Run("MissingFunction", func(t *testing.T) {
		ctx := t.Context()
		svc := newTestStellarServiceWithTxm(t, &mockTxManager{})
		_, err := svc.SubmitTransaction(ctx, stellartypes.SubmitTransactionRequest{ContractID: testContractID})
		require.ErrorContains(t, err, "function is required")
	})

	t.Run("BadArg_NilBool", func(t *testing.T) {
		ctx := t.Context()
		svc := newTestStellarServiceWithTxm(t, &mockTxManager{})
		_, err := svc.SubmitTransaction(ctx, stellartypes.SubmitTransactionRequest{
			ContractID: testContractID,
			Function:   "fn",
			Args:       []stellartypes.ScVal{{Type: stellartypes.ScValTypeBool}}, // Bool is nil
		})
		require.Error(t, err)
		require.ErrorContains(t, err, "convert args")
	})

	t.Run("ContextCancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		txMgr := &mockTxManager{
			enqueueAndWait: func(ctx context.Context, _ txm.TxRequest) (*txm.TxResult, error) {
				return nil, ctx.Err()
			},
		}

		svc := newTestStellarServiceWithTxm(t, txMgr)
		_, err := svc.SubmitTransaction(ctx, baseReq)
		require.Error(t, err)
	})
}
