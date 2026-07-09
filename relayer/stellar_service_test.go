package relayer

import (
	"context"
	"errors"
	"math/big"
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	"github.com/smartcontractkit/chainlink-common/pkg/types/core"
	"github.com/smartcontractkit/chainlink-framework/multinode"

	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"

	"github.com/smartcontractkit/chainlink-stellar/internal/mocks"
	"github.com/smartcontractkit/chainlink-stellar/relayer/chain"
	"github.com/smartcontractkit/chainlink-stellar/relayer/txm"
)

type stubChain struct {
	chain.Chain
	rpc      chain.RPCClient
	txMgr    *txm.StellarTxm
	keyStore core.Keystore
}

func (s *stubChain) GetClient(_ context.Context) (chain.RPCClient, error) { return s.rpc, nil }
func (s *stubChain) TxManager() *txm.StellarTxm                           { return s.txMgr }
func (s *stubChain) KeyStore() core.Keystore                              { return s.keyStore }

type stubKeystore struct {
	accounts []string
	err      error
}

func (s *stubKeystore) Accounts(_ context.Context) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.accounts, nil
}

func (s *stubKeystore) Sign(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (s *stubKeystore) Decrypt(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return nil, errors.New("not implemented")
}

// newTestStellarService builds a stellarService backed by the given mock RPC client.
func newTestStellarService(t *testing.T, rpc chain.RPCClient) *stellarService {
	t.Helper()
	svc := newStellarService(&stubChain{rpc: rpc})
	return &svc
}

// newTestStellarServiceWithTxm builds a stellarService backed by the given mock TXM.
func newTestStellarServiceWithTxm(t *testing.T, txMgr StellarTxManager) *stellarService {
	t.Helper()
	return &stellarService{txMgr: txMgr}
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
		require.ErrorIs(t, err, multinode.ErrNodeError)
	})
}

// testContractID returns a valid C… StrKey contract address (all-zero contract).
func testContractID(t *testing.T) string {
	t.Helper()
	id, err := strkey.Encode(strkey.VersionByteContract, make([]byte, 32))
	require.NoError(t, err)
	return id
}

func TestStellarService_SimulateTransaction(t *testing.T) {
	t.Parallel()

	t.Run("EmptyContractID", func(t *testing.T) {
		svc := newTestStellarService(t, mocks.NewMockRPCClient(t))

		_, err := svc.SimulateTransaction(t.Context(), stellartypes.SimulateTransactionRequest{
			Function: "get",
		})

		require.ErrorContains(t, err, "contract id is required")
		require.NotErrorIs(t, err, multinode.ErrNodeError)
	})

	t.Run("EmptyFunction", func(t *testing.T) {
		svc := newTestStellarService(t, mocks.NewMockRPCClient(t))

		_, err := svc.SimulateTransaction(t.Context(), stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
		})

		require.ErrorContains(t, err, "function is required")
		require.NotErrorIs(t, err, multinode.ErrNodeError)
	})

	t.Run("InvalidContractID", func(t *testing.T) {
		svc := newTestStellarService(t, mocks.NewMockRPCClient(t))

		_, err := svc.SimulateTransaction(t.Context(), stellartypes.SimulateTransactionRequest{
			ContractID: "not-a-contract",
			Function:   "get",
		})

		require.ErrorContains(t, err, "failed to decode contract id")
		require.NotErrorIs(t, err, multinode.ErrNodeError)
	})

	t.Run("InvalidSourceAccount", func(t *testing.T) {
		svc := newTestStellarService(t, mocks.NewMockRPCClient(t))

		_, err := svc.SimulateTransaction(t.Context(), stellartypes.SimulateTransactionRequest{
			ContractID:    testContractID(t),
			Function:      "get",
			SourceAccount: "not-an-account",
		})

		require.ErrorContains(t, err, "failed to decode source account")
		require.NotErrorIs(t, err, multinode.ErrNodeError)
	})

	t.Run("InvalidAuthMode", func(t *testing.T) {
		svc := newTestStellarService(t, mocks.NewMockRPCClient(t))

		_, err := svc.SimulateTransaction(t.Context(), stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "get",
			AuthMode:   "banana",
		})

		require.Error(t, err)
		require.NotErrorIs(t, err, multinode.ErrNodeError)
	})

	t.Run("ArgConversionFailure", func(t *testing.T) {
		svc := newTestStellarService(t, mocks.NewMockRPCClient(t))

		_, err := svc.SimulateTransaction(t.Context(), stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "get",
			Args: []stellartypes.ScVal{
				{
					Type: stellartypes.ScValTypeBool,
				},
			},
		})

		require.ErrorContains(t, err, "convert args")
		require.NotErrorIs(t, err, multinode.ErrNodeError)
	})

	t.Run("SimulationFailure", func(t *testing.T) {
		ctx := t.Context()

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			SimulateTransaction(mock.Anything, mock.Anything).
			Return(protocol.SimulateTransactionResponse{
				Error:        "HostError: contract trapped",
				LatestLedger: 42,
			}, nil)

		svc := newTestStellarService(t, rpc)

		resp, err := svc.SimulateTransaction(ctx, stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "get",
		})

		require.NoError(t, err)
		require.False(t, resp.Success)
		require.Equal(t, "HostError: contract trapped", resp.Error)
		require.Equal(t, uint32(42), resp.LedgerSequence)
	})

	t.Run("SuccessMapping", func(t *testing.T) {
		ctx := t.Context()

		returnXDR := "AAAAAwAAAAE="
		auth := []string{"auth1", "auth2"}

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			SimulateTransaction(mock.Anything, mock.Anything).
			Return(protocol.SimulateTransactionResponse{
				LatestLedger:       99,
				TransactionDataXDR: "txdata",
				MinResourceFee:     123,
				EventsXDR:          []string{"event1", "event2"},
				Results: []protocol.SimulateHostFunctionResult{
					{
						ReturnValueXDR: &returnXDR,
						AuthXDR:        &auth,
					},
				},
			}, nil)

		svc := newTestStellarService(t, rpc)

		resp, err := svc.SimulateTransaction(ctx, stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "get",
		})

		require.NoError(t, err)
		require.True(t, resp.Success)
		require.Equal(t, uint32(99), resp.LedgerSequence)
		require.Equal(t, returnXDR, resp.ReturnValueXDR)
		require.Equal(t, auth, resp.RequiredAuthXDR)
		require.Equal(t, []string{"event1", "event2"}, resp.EventsXDR)
		require.Equal(t, "txdata", resp.TransactionDataXDR)
		require.Equal(t, int64(123), resp.MinResourceFee)
	})

	t.Run("EmptyReturnValueAllowed", func(t *testing.T) {
		ctx := t.Context()

		empty := ""

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			SimulateTransaction(mock.Anything, mock.Anything).
			Return(protocol.SimulateTransactionResponse{
				LatestLedger: 7,
				Results: []protocol.SimulateHostFunctionResult{
					{
						ReturnValueXDR: &empty,
					},
				},
			}, nil)

		svc := newTestStellarService(t, rpc)

		resp, err := svc.SimulateTransaction(ctx, stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "write",
		})

		require.NoError(t, err)
		require.True(t, resp.Success)
		require.Empty(t, resp.ReturnValueXDR)
	})

	t.Run("CustomSourceAccount", func(t *testing.T) {
		ctx := t.Context()

		raw := make([]byte, 32)
		for i := range raw {
			raw[i] = 0x11
		}

		source, err := strkey.Encode(strkey.VersionByteAccountID, raw)
		require.NoError(t, err)

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			SimulateTransaction(
				mock.Anything,
				mock.MatchedBy(func(req protocol.SimulateTransactionRequest) bool {
					return simulatedTxSource(t, req) == source
				}),
			).
			Return(minimalSimulateSuccess(), nil)

		svc := newTestStellarService(t, rpc)

		_, err = svc.SimulateTransaction(ctx, stellartypes.SimulateTransactionRequest{
			ContractID:    testContractID(t),
			Function:      "get",
			SourceAccount: source,
		})

		require.NoError(t, err)
	})

	t.Run("DefaultPlaceholderSourceAccount", func(t *testing.T) {
		ctx := t.Context()

		placeholder, err := strkey.Encode(strkey.VersionByteAccountID, make([]byte, 32))
		require.NoError(t, err)

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			SimulateTransaction(
				mock.Anything,
				mock.MatchedBy(func(req protocol.SimulateTransactionRequest) bool {
					return simulatedTxSource(t, req) == placeholder
				}),
			).
			Return(minimalSimulateSuccess(), nil)

		svc := newTestStellarService(t, rpc)

		_, err = svc.SimulateTransaction(ctx, stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "get",
		})

		require.NoError(t, err)
	})

	t.Run("RPCError", func(t *testing.T) {
		ctx := t.Context()

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			SimulateTransaction(mock.Anything, mock.Anything).
			Return(protocol.SimulateTransactionResponse{}, errors.New("rpc down"))

		svc := newTestStellarService(t, rpc)

		_, err := svc.SimulateTransaction(ctx, stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "get",
		})

		require.ErrorContains(t, err, "rpc down")
		require.ErrorIs(t, err, multinode.ErrNodeError)
	})

	t.Run("DefaultAuthModeIsRecord", func(t *testing.T) {
		ctx := t.Context()

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			SimulateTransaction(
				mock.Anything,
				mock.MatchedBy(func(req protocol.SimulateTransactionRequest) bool {
					return req.AuthMode == protocol.AuthModeRecord
				}),
			).
			Return(minimalSimulateSuccess(), nil)

		svc := newTestStellarService(t, rpc)

		_, err := svc.SimulateTransaction(ctx, stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "get",
		})

		require.NoError(t, err)
	})

	t.Run("ExplicitAuthMode", func(t *testing.T) {
		ctx := t.Context()

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			SimulateTransaction(
				mock.Anything,
				mock.MatchedBy(func(req protocol.SimulateTransactionRequest) bool {
					return req.AuthMode == protocol.AuthModeEnforce
				}),
			).
			Return(minimalSimulateSuccess(), nil)

		svc := newTestStellarService(t, rpc)

		_, err := svc.SimulateTransaction(ctx, stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "get",
			AuthMode:   stellartypes.SimulateAuthModeEnforce,
		})

		require.NoError(t, err)
	})

	t.Run("ResourceConfig", func(t *testing.T) {
		ctx := t.Context()

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			SimulateTransaction(
				mock.Anything,
				mock.MatchedBy(func(req protocol.SimulateTransactionRequest) bool {
					require.NotNil(t, req.ResourceConfig)
					return req.ResourceConfig.InstructionLeeway == 123
				}),
			).
			Return(minimalSimulateSuccess(), nil)

		svc := newTestStellarService(t, rpc)

		_, err := svc.SimulateTransaction(ctx, stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "get",
			ResourceConfig: &stellartypes.SimulateResourceConfig{
				InstructionLeeway: 123,
			},
		})

		require.NoError(t, err)
	})

	t.Run("ReturnsRequiredAuth", func(t *testing.T) {
		ctx := t.Context()

		auth := []string{"auth1", "auth2"}

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			SimulateTransaction(mock.Anything, mock.Anything).
			Return(protocol.SimulateTransactionResponse{
				Results: []protocol.SimulateHostFunctionResult{
					{
						AuthXDR: &auth,
					},
				},
			}, nil)

		svc := newTestStellarService(t, rpc)

		resp, err := svc.SimulateTransaction(ctx, stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "get",
		})

		require.NoError(t, err)
		require.Equal(t, auth, resp.RequiredAuthXDR)
	})

	t.Run("RestorePreamble", func(t *testing.T) {
		ctx := t.Context()

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			SimulateTransaction(mock.Anything, mock.Anything).
			Return(protocol.SimulateTransactionResponse{
				RestorePreamble: &protocol.RestorePreamble{
					TransactionDataXDR: "restoreData",
					MinResourceFee:     42,
				},
			}, nil)

		svc := newTestStellarService(t, rpc)

		resp, err := svc.SimulateTransaction(ctx, stellartypes.SimulateTransactionRequest{
			ContractID: testContractID(t),
			Function:   "get",
		})

		require.NoError(t, err)
		require.NotNil(t, resp.RestorePreamble)
		require.Equal(t, "restoreData", resp.RestorePreamble.TransactionDataXDR)
		require.Equal(t, int64(42), resp.RestorePreamble.MinResourceFee)
	})
}

func TestStellarService_GetEvents(t *testing.T) {
	t.Parallel()

	t.Run("Success", func(t *testing.T) {
		ctx := t.Context()

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			GetEvents(mock.Anything, mock.Anything).
			Return(protocol.GetEventsResponse{
				LatestLedger:          100,
				OldestLedger:          90,
				LatestLedgerCloseTime: 111,
				OldestLedgerCloseTime: 99,
				Cursor:                "cursor123",
				Events: []protocol.EventInfo{
					{
						EventType:       protocol.EventTypeContract,
						ID:              "event-id",
						ContractID:      testContractID(t),
						TransactionHash: "txhash",
						Ledger:          95,
						OpIndex:         1,
						TxIndex:         2,
						ValueXDR:        voidScValXDR(t),
					},
				},
			}, nil)

		svc := newTestStellarService(t, rpc)

		resp, err := svc.GetEvents(ctx, stellartypes.GetEventsRequest{})

		require.NoError(t, err)
		require.Equal(t, "cursor123", resp.Cursor)
		require.Equal(t, uint32(100), resp.LatestLedger)
		require.Equal(t, uint32(90), resp.OldestLedger)

		require.Len(t, resp.Events, 1)
		require.Equal(t, "event-id", resp.Events[0].ID)
		require.Equal(t, "txhash", resp.Events[0].TransactionHash)
		require.Equal(t, uint32(1), resp.Events[0].OperationIndex)
		require.Equal(t, uint32(2), resp.Events[0].TransactionIndex)
	})

	t.Run("InvalidLedgerRange", func(t *testing.T) {
		svc := newTestStellarService(t, mocks.NewMockRPCClient(t))

		_, err := svc.GetEvents(t.Context(), stellartypes.GetEventsRequest{
			StartLedger: 100,
			EndLedger:   50,
		})

		require.ErrorContains(t, err, "invalid ledger range")
		require.NotErrorIs(t, err, multinode.ErrNodeError)
	})

	t.Run("OpenEndedRange_StartOnly", func(t *testing.T) {
		ctx := t.Context()

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			GetEvents(mock.Anything, mock.Anything).
			Return(protocol.GetEventsResponse{}, nil)

		svc := newTestStellarService(t, rpc)

		_, err := svc.GetEvents(ctx, stellartypes.GetEventsRequest{
			StartLedger: 100,
		})

		require.NoError(t, err)
	})

	t.Run("OpenEndedRange_EndOnly", func(t *testing.T) {
		ctx := t.Context()

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			GetEvents(mock.Anything, mock.Anything).
			Return(protocol.GetEventsResponse{}, nil)

		svc := newTestStellarService(t, rpc)

		_, err := svc.GetEvents(ctx, stellartypes.GetEventsRequest{
			EndLedger: 100,
		})

		require.NoError(t, err)
	})

	t.Run("RPCError", func(t *testing.T) {
		ctx := t.Context()

		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().
			GetEvents(mock.Anything, mock.Anything).
			Return(protocol.GetEventsResponse{}, errors.New("rpc down"))

		svc := newTestStellarService(t, rpc)

		_, err := svc.GetEvents(ctx, stellartypes.GetEventsRequest{})

		require.ErrorContains(t, err, "rpc down")
		require.ErrorIs(t, err, multinode.ErrNodeError)
	})

	t.Run("InvalidRequestConversion", func(t *testing.T) {
		svc := newTestStellarService(t, mocks.NewMockRPCClient(t))

		_, err := svc.GetEvents(t.Context(), stellartypes.GetEventsRequest{
			Filters: []stellartypes.EventFilter{
				{
					Topics: []stellartypes.TopicFilter{
						{
							Segments: []stellartypes.TopicSegment{
								{
									Value: &stellartypes.ScVal{
										Type: stellartypes.ScValTypeBool,
										// nil Bool -> conversion failure
									},
								},
							},
						},
					},
				},
			},
		})

		require.ErrorContains(t, err, "invalid get events request")
		require.NotErrorIs(t, err, multinode.ErrNodeError)
	})
}

func TestStellarService_SubmitTransaction(t *testing.T) {
	t.Parallel()

	sym := "transfer"
	baseReq := stellartypes.SubmitTransactionRequest{
		IdempotencyKey:     "idem-1",
		FromAddress:        testStellarAccount(t),
		ContractID:         testContractID(t),
		Function:           "transfer",
		Args:               []stellartypes.ScVal{{Type: stellartypes.ScValTypeSymbol, Symbol: &sym}},
		LedgerBoundsOffset: 2,
	}

	t.Run("Success_Finalized", func(t *testing.T) {
		ctx := t.Context()
		txMgr := mocks.NewMockStellarTxManager(t)
		txMgr.EXPECT().EnqueueAndWait(mock.Anything, mock.Anything).
			RunAndReturn(func(_ context.Context, req txm.TxRequest) (*txm.TxResult, error) {
				require.Equal(t, "idem-1", req.ID)
				require.Equal(t, baseReq.FromAddress, req.FromAddress)
				require.Len(t, req.Operations, 1)
				require.Equal(t, uint32(2), req.LedgerBoundsOffset)
				return &txm.TxResult{
					ID:              "idem-1",
					Hash:            "txhash123",
					Status:          commontypes.Finalized,
					Fee:             big.NewInt(42_000),
					LedgerCloseTime: 1_700_000_000,
					ResultXDR:       "resultXDR",
					ResultMetaXDR:   "metaXDR",
				}, nil
			})

		svc := newTestStellarServiceWithTxm(t, txMgr)
		reply, err := svc.SubmitTransaction(ctx, baseReq)
		require.NoError(t, err)
		require.Equal(t, stellartypes.TxSuccess, reply.TxStatus)
		require.Equal(t, "txhash123", reply.TxHash)
		require.Equal(t, "idem-1", reply.TxIdempotencyKey)
		require.Equal(t, "resultXDR", reply.ResultXDR)
		require.Equal(t, "metaXDR", reply.ResultMetaXDR)
		require.NotNil(t, reply.TransactionFee)
		require.Equal(t, uint64(42_000), *reply.TransactionFee)
		require.NotNil(t, reply.BlockTimestamp)
		require.Equal(t, uint64(1_700_000_000_000_000), *reply.BlockTimestamp)
	})

	t.Run("Failed_OnChain", func(t *testing.T) {
		ctx := t.Context()
		txMgr := mocks.NewMockStellarTxManager(t)
		txMgr.EXPECT().EnqueueAndWait(mock.Anything, mock.Anything).Return(&txm.TxResult{
			ID:        "idem-1",
			Hash:      "failhash",
			Status:    commontypes.Failed,
			ResultXDR: "failedResultXDR",
			Error:     errors.New("tx revert: contract error"),
		}, nil)

		svc := newTestStellarServiceWithTxm(t, txMgr)
		reply, err := svc.SubmitTransaction(ctx, baseReq)
		require.Error(t, err)
		require.ErrorContains(t, err, "contract error")
		require.Equal(t, stellartypes.TxFailed, reply.TxStatus)
		require.Equal(t, "failhash", reply.TxHash)
	})

	t.Run("Fatal_TxmError", func(t *testing.T) {
		ctx := t.Context()
		txMgr := mocks.NewMockStellarTxManager(t)
		txMgr.EXPECT().EnqueueAndWait(mock.Anything, mock.Anything).
			Return(nil, errors.New("simulation failed: insufficient fee"))

		svc := newTestStellarServiceWithTxm(t, txMgr)
		_, err := svc.SubmitTransaction(ctx, baseReq)
		require.Error(t, err)
		require.ErrorContains(t, err, "simulation failed")
	})

	t.Run("MissingContractID", func(t *testing.T) {
		ctx := t.Context()
		svc := newTestStellarServiceWithTxm(t, mocks.NewMockStellarTxManager(t))
		_, err := svc.SubmitTransaction(ctx, stellartypes.SubmitTransactionRequest{Function: "fn"})
		require.ErrorContains(t, err, "contractID is required")
	})

	t.Run("MissingFunction", func(t *testing.T) {
		ctx := t.Context()
		svc := newTestStellarServiceWithTxm(t, mocks.NewMockStellarTxManager(t))
		_, err := svc.SubmitTransaction(ctx, stellartypes.SubmitTransactionRequest{ContractID: testContractID(t)})
		require.ErrorContains(t, err, "function is required")
	})

	t.Run("BadArg_NilBool", func(t *testing.T) {
		ctx := t.Context()
		svc := newTestStellarServiceWithTxm(t, mocks.NewMockStellarTxManager(t))
		_, err := svc.SubmitTransaction(ctx, stellartypes.SubmitTransactionRequest{
			ContractID: testContractID(t),
			Function:   "fn",
			Args:       []stellartypes.ScVal{{Type: stellartypes.ScValTypeBool}},
		})
		require.Error(t, err)
		require.ErrorContains(t, err, "convert args")
	})

	t.Run("ContextCancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		txMgr := mocks.NewMockStellarTxManager(t)
		txMgr.EXPECT().EnqueueAndWait(mock.Anything, mock.Anything).Return(nil, context.Canceled)

		svc := newTestStellarServiceWithTxm(t, txMgr)
		_, err := svc.SubmitTransaction(ctx, baseReq)
		require.Error(t, err)
	})
}

func TestStellarService_GetTransaction(t *testing.T) {
	t.Parallel()

	t.Run("EmptyTxHash", func(t *testing.T) {
		svc := newTestStellarService(t, mocks.NewMockRPCClient(t))
		_, err := svc.GetTransaction(t.Context(), stellartypes.GetTransactionRequest{})
		require.ErrorContains(t, err, "tx hash is required")
	})

	t.Run("NotFound", func(t *testing.T) {
		ctx := t.Context()
		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().GetTransaction(ctx, protocol.GetTransactionRequest{Hash: "missinghash"}).
			Return(protocol.GetTransactionResponse{
				TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound},
			}, nil)

		svc := newTestStellarService(t, rpc)
		_, err := svc.GetTransaction(ctx, stellartypes.GetTransactionRequest{TxHash: "missinghash"})
		require.ErrorContains(t, err, "transaction not found: missinghash")
	})

	t.Run("Success", func(t *testing.T) {
		ctx := t.Context()
		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().GetTransaction(ctx, protocol.GetTransactionRequest{Hash: "abc123hash"}).
			Return(protocol.GetTransactionResponse{
				TransactionDetails: protocol.TransactionDetails{
					Status: protocol.TransactionStatusSuccess,
					Ledger: 100,
				},
				LedgerCloseTime: 1_700_000_000,
			}, nil)

		svc := newTestStellarService(t, rpc)
		resp, err := svc.GetTransaction(ctx, stellartypes.GetTransactionRequest{TxHash: "abc123hash"})
		require.NoError(t, err)
		require.Equal(t, uint32(100), resp.LedgerSequence)
		require.Equal(t, int64(1_700_000_000), resp.LedgerCloseTime)
	})

	t.Run("RPCError", func(t *testing.T) {
		ctx := t.Context()
		rpc := mocks.NewMockRPCClient(t)
		rpc.EXPECT().GetTransaction(ctx, protocol.GetTransactionRequest{Hash: "abc123hash"}).
			Return(protocol.GetTransactionResponse{}, errors.New("rpc unavailable"))

		svc := newTestStellarService(t, rpc)
		_, err := svc.GetTransaction(ctx, stellartypes.GetTransactionRequest{TxHash: "abc123hash"})
		require.ErrorContains(t, err, "rpc unavailable")
		require.ErrorIs(t, err, multinode.ErrNodeError)
	})
}

func TestStellarService_GetSigningAccount(t *testing.T) {
	t.Parallel()

	t.Run("NoKeystore", func(t *testing.T) {
		svc := newStellarService(&stubChain{})
		_, err := svc.GetSigningAccount(t.Context())
		require.ErrorContains(t, err, "keystore is not configured")
	})

	t.Run("EmptyAccounts", func(t *testing.T) {
		svc := newStellarService(&stubChain{keyStore: &stubKeystore{accounts: nil}})
		_, err := svc.GetSigningAccount(t.Context())
		require.ErrorContains(t, err, "keystore has no accounts")
	})

	t.Run("KeystoreError", func(t *testing.T) {
		svc := newStellarService(&stubChain{keyStore: &stubKeystore{err: errors.New("keystore down")}})
		_, err := svc.GetSigningAccount(t.Context())
		require.ErrorContains(t, err, "keystore accounts: keystore down")
	})

	t.Run("Success", func(t *testing.T) {
		account := testStellarAccount(t)
		svc := newStellarService(&stubChain{keyStore: &stubKeystore{accounts: []string{account}}})
		resp, err := svc.GetSigningAccount(t.Context())
		require.NoError(t, err)
		require.Equal(t, account, resp.AccountAddress)
	})
}

func testStellarAccount(t *testing.T) string {
	t.Helper()
	accountID := make([]byte, 32)
	addr, err := strkey.Encode(strkey.VersionByteAccountID, accountID)
	require.NoError(t, err)
	return addr
}

func voidScValXDR(t *testing.T) string {
	t.Helper()
	b64, err := xdr.MarshalBase64(xdr.ScVal{Type: xdr.ScValTypeScvVoid})
	require.NoError(t, err)
	return b64
}

func minimalSimulateSuccess() protocol.SimulateTransactionResponse {
	empty := ""
	return protocol.SimulateTransactionResponse{
		Results: []protocol.SimulateHostFunctionResult{
			{ReturnValueXDR: &empty},
		},
	}
}

// simulatedTxSource decodes the simulated transaction XDR and returns its source account address.
func simulatedTxSource(t *testing.T, req protocol.SimulateTransactionRequest) string {
	t.Helper()
	gtx, err := txnbuild.TransactionFromXDR(req.Transaction)
	require.NoError(t, err)
	tx, ok := gtx.Transaction()
	require.True(t, ok)
	return tx.SourceAccount().AccountID
}
