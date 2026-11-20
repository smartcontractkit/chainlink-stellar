package ccvsourcereader

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/rs/zerolog"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/relayer/mocks"
)

func TestNewSourceReaderWithClient(t *testing.T) {
	lggr := zerolog.New(os.Stdout).With().Timestamp().Logger().Level(zerolog.DebugLevel)

	t.Run("returns error when client is nil", func(t *testing.T) {
		reader, err := NewSourceReaderWithClient(nil, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
		require.Error(t, err)
		require.Nil(t, reader)
		assert.Contains(t, err.Error(), "rpc client is required")
	})

	t.Run("returns error when logger is nil", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)
		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", nil)
		require.Error(t, err)
		require.Nil(t, reader)
		assert.Contains(t, err.Error(), "logger is required")
	})

	t.Run("returns error when ccip onramp address is empty", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)
		reader, err := NewSourceReaderWithClient(mockClient, nil, "", "transfer", "RMNREMOTE", &lggr)
		require.Error(t, err)
		require.Nil(t, reader)
		assert.Contains(t, err.Error(), "ccip onramp address is required")
	})

	t.Run("returns error when ccip message sent topic is empty", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)
		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "", "RMNREMOTE", &lggr)
		require.Error(t, err)
		require.Nil(t, reader)
		assert.Contains(t, err.Error(), "ccip message sent topic is required")
	})

	t.Run("creates reader with mock client", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)

		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
		require.NoError(t, err)
		require.NotNil(t, reader)
		assert.Equal(t, "CADDR", reader.ccipOnrampAddress)
		assert.Equal(t, "transfer", reader.ccipMessageSentTopic)
		assert.Equal(t, "RMNREMOTE", reader.rmnRemoteAddress)
	})
}

func TestLatestAndFinalizedBlock(t *testing.T) {
	lggr := zerolog.New(os.Stdout).With().Timestamp().Logger().Level(zerolog.DebugLevel)

	// Helper to create a valid base64-encoded ledger header
	createLedgerHeaderB64 := func(prevHash xdr.Hash) string {
		hdr := xdr.LedgerHeader{
			LedgerVersion:      22,
			PreviousLedgerHash: prevHash,
			ScpValue:           xdr.StellarValue{},
			TxSetResultHash:    xdr.Hash{},
			BucketListHash:     xdr.Hash{},
			LedgerSeq:          12345,
			TotalCoins:         1000000000,
			FeePool:            100000,
			InflationSeq:       0,
			IdPool:             1000,
			BaseFee:            100,
			BaseReserve:        5000000,
			MaxTxSetSize:       100,
		}
		xdrBytes, _ := hdr.MarshalBinary()
		return base64.StdEncoding.EncodeToString(xdrBytes)
	}

	t.Run("returns latest and finalized block on success", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)

		prevHash := xdr.Hash{0x01, 0x02, 0x03}
		headerB64 := createLedgerHeaderB64(prevHash)

		mockClient.On("GetLatestLedger", mock.Anything).Return(
			protocolrpc.GetLatestLedgerResponse{
				Hash:            "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
				Sequence:        12345,
				LedgerCloseTime: 1704067200, // 2024-01-01 00:00:00 UTC
				LedgerHeader:    headerB64,
			},
			nil,
		).Once()

		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)

		latest, finalized, err := reader.LatestAndFinalizedBlock(context.Background())
		require.NoError(t, err)
		require.NotNil(t, latest)
		require.NotNil(t, finalized)

		// Stellar has instant finality, so latest == finalized
		assert.Equal(t, uint64(12345), latest.Number)
		assert.Equal(t, uint64(12345), finalized.Number)
		assert.Equal(t, latest.Number, finalized.Number)
		assert.Equal(t, latest.Hash, finalized.Hash)
	})

	t.Run("returns error when GetLatestLedger fails", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)

		mockClient.On("GetLatestLedger", mock.Anything).Return(
			protocolrpc.GetLatestLedgerResponse{},
			errors.New("rpc connection failed"),
		).Once()

		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
		require.NoError(t, err)

		latest, finalized, err := reader.LatestAndFinalizedBlock(context.Background())
		require.Error(t, err)
		require.Nil(t, latest)
		require.Nil(t, finalized)
		assert.Contains(t, err.Error(), "failed to get latest ledger")
	})
}

func TestLatestSafeBlock(t *testing.T) {
	lggr := zerolog.New(os.Stdout).With().Timestamp().Logger().Level(zerolog.DebugLevel)

	t.Run("returns nil for Stellar (no safe head concept)", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)

		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
		require.NoError(t, err)

		safe, err := reader.LatestSafeBlock(context.Background())
		require.NoError(t, err)
		require.Nil(t, safe, "Stellar has no safe head — LatestSafeBlock should return nil")
	})
}

func TestGetBlocksHeaders(t *testing.T) {
	lggr := zerolog.New(os.Stdout).With().Timestamp().Logger().Level(zerolog.DebugLevel)

	// Helper to create a valid base64-encoded ledger metadata (LedgerCloseMeta)
	// Using V0 which has simpler structure requirements
	createLedgerMetaB64 := func(seq uint32, prevHash xdr.Hash) string {
		hdr := xdr.LedgerHeader{
			LedgerVersion:      22,
			PreviousLedgerHash: prevHash,
			LedgerSeq:          xdr.Uint32(seq),
		}
		lcm := xdr.LedgerCloseMeta{
			V: 0,
			V0: &xdr.LedgerCloseMetaV0{
				LedgerHeader: xdr.LedgerHeaderHistoryEntry{
					Header: hdr,
				},
				TxSet: xdr.TransactionSet{},
			},
		}
		xdrBytes, _ := lcm.MarshalBinary()
		return base64.StdEncoding.EncodeToString(xdrBytes)
	}

	t.Run("returns headers for single ledger", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)

		prevHash := xdr.Hash{0xaa, 0xbb, 0xcc}
		metaB64 := createLedgerMetaB64(100, prevHash)

		mockClient.On("GetLedgers", mock.Anything, mock.MatchedBy(func(req protocolrpc.GetLedgersRequest) bool {
			return req.StartLedger == 100 && req.Pagination.Limit == 1
		})).Return(
			protocolrpc.GetLedgersResponse{
				Ledgers: []protocolrpc.LedgerInfo{
					{
						Hash:            "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
						Sequence:        100,
						LedgerCloseTime: 1704067200,
						LedgerMetadata:  metaB64,
					},
				},
			},
			nil,
		).Once()

		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
		require.NoError(t, err)

		blockNumbers := []uint64{100}
		headers, err := reader.GetBlocksHeaders(context.Background(), blockNumbers)
		require.NoError(t, err)
		require.Len(t, headers, 1)

		header, exists := headers[uint64(100)]
		require.True(t, exists)
		assert.Equal(t, uint64(100), header.Number)
		assert.Equal(t, "RMNREMOTE", reader.rmnRemoteAddress)
	})

	t.Run("returns headers for multiple ledgers", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)

		// Set up expectations for both ledgers
		mockClient.On("GetLedgers", mock.Anything, mock.MatchedBy(func(req protocolrpc.GetLedgersRequest) bool {
			return req.StartLedger == 100
		})).Return(
			protocolrpc.GetLedgersResponse{
				Ledgers: []protocolrpc.LedgerInfo{
					{
						Hash:            "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
						Sequence:        100,
						LedgerCloseTime: 1704067200,
						LedgerMetadata:  createLedgerMetaB64(100, xdr.Hash{0x01}),
					},
				},
			},
			nil,
		).Once()

		mockClient.On("GetLedgers", mock.Anything, mock.MatchedBy(func(req protocolrpc.GetLedgersRequest) bool {
			return req.StartLedger == 200
		})).Return(
			protocolrpc.GetLedgersResponse{
				Ledgers: []protocolrpc.LedgerInfo{
					{
						Hash:            "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
						Sequence:        200,
						LedgerCloseTime: 1704153600,
						LedgerMetadata:  createLedgerMetaB64(200, xdr.Hash{0x02}),
					},
				},
			},
			nil,
		).Once()

		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
		require.NoError(t, err)

		blockNumbers := []uint64{100, 200}
		headers, err := reader.GetBlocksHeaders(context.Background(), blockNumbers)
		require.NoError(t, err)
		require.Len(t, headers, 2)
		assert.Equal(t, "RMNREMOTE", reader.rmnRemoteAddress)
	})

	t.Run("omits header when ledger not found", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)

		mockClient.On("GetLedgers", mock.Anything, mock.Anything).Return(
			protocolrpc.GetLedgersResponse{
				Ledgers: []protocolrpc.LedgerInfo{}, // Empty response
			},
			nil,
		).Once()

		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
		require.NoError(t, err)

		blockNumbers := []uint64{999999999}
		headers, err := reader.GetBlocksHeaders(context.Background(), blockNumbers)
		require.NoError(t, err)
		require.Empty(t, headers, "not-found ledger must be omitted, not errored")
	})

	t.Run("omits header when GetLedgers fails", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)

		mockClient.On("GetLedgers", mock.Anything, mock.Anything).Return(
			protocolrpc.GetLedgersResponse{},
			errors.New("rpc timeout"),
		).Once()

		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
		require.NoError(t, err)

		blockNumbers := []uint64{100}
		headers, err := reader.GetBlocksHeaders(context.Background(), blockNumbers)
		require.NoError(t, err)
		require.Empty(t, headers, "failed ledger fetch must be omitted, not errored")
	})

	t.Run("omits header when ledger sequence mismatch", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)

		// Return a different sequence than requested
		mockClient.On("GetLedgers", mock.Anything, mock.Anything).Return(
			protocolrpc.GetLedgersResponse{
				Ledgers: []protocolrpc.LedgerInfo{
					{
						Hash:            "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
						Sequence:        999, // Different from requested 100
						LedgerCloseTime: 1704067200,
						LedgerMetadata:  createLedgerMetaB64(999, xdr.Hash{0x01}),
					},
				},
			},
			nil,
		).Once()

		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
		require.NoError(t, err)

		blockNumbers := []uint64{100}
		headers, err := reader.GetBlocksHeaders(context.Background(), blockNumbers)
		require.NoError(t, err)
		require.Empty(t, headers, "seq-mismatch ledger must be omitted, not errored")
	})

	t.Run("omits header when block number exceeds uint32 range", func(t *testing.T) {
		mockClient := mocks.NewMockRPCClient(t)

		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
		require.NoError(t, err)

		// A number larger than uint32 max (ledger seq) is out of range on
		// Stellar; it is omitted per the chainaccess contract.
		blockNumbers := []uint64{1 << 33}
		headers, err := reader.GetBlocksHeaders(context.Background(), blockNumbers)
		require.NoError(t, err)
		require.Empty(t, headers, "out-of-range ledger must be omitted, not errored")
	})
}

// makeMessageSentEventInfo builds a decodable CCIPMessageSent RPC event for
// the given ledger and sequence number.
func makeMessageSentEventInfo(t *testing.T, ledger int32, seqNum uint64, txHash string) protocolrpc.EventInfo {
	t.Helper()

	msg := &protocol.Message{
		Version:             protocol.MessageVersion,
		SourceChainSelector: 1,
		DestChainSelector:   2,
		SequenceNumber:      protocol.SequenceNumber(seqNum),
	}
	encoded, err := msg.Encode()
	require.NoError(t, err)

	var messageID [32]byte
	messageID[0] = byte(seqNum)

	eventVal, err := scval.BuildStructScVal(map[string]xdr.ScVal{
		"dest_chain_selector": scval.Uint64ToScVal(2),
		"sequence_number":     scval.Uint64ToScVal(seqNum),
		"message_id":          scval.BytesToScVal(messageID[:]),
		"encoded_message":     scval.BytesToScVal(encoded),
	})
	require.NoError(t, err)

	valueB64, err := xdr.MarshalBase64(eventVal)
	require.NoError(t, err)

	return protocolrpc.EventInfo{
		EventType:       protocolrpc.EventTypeContract,
		Ledger:          ledger,
		TransactionHash: txHash,
		ValueXDR:        valueB64,
	}
}

func TestFetchMessageSentEventsPagination(t *testing.T) {
	lggr := zerolog.New(os.Stdout).With().Timestamp().Logger().Level(zerolog.DebugLevel)

	newReader := func(t *testing.T) (*SourceReader, *mocks.MockRPCClient) {
		t.Helper()
		mockClient := mocks.NewMockRPCClient(t)
		reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
		require.NoError(t, err)
		return reader, mockClient
	}

	t.Run("consumes all pages and returns distinct events", func(t *testing.T) {
		reader, mockClient := newReader(t)

		cursor1 := protocolrpc.Cursor{Ledger: 150, Tx: 1, Op: 0, Event: 0}.String()
		evtA := makeMessageSentEventInfo(t, 150, 1, "tx-a")
		evtB := makeMessageSentEventInfo(t, 180, 2, "tx-b")

		mockClient.On("GetEvents", mock.Anything, mock.MatchedBy(func(req protocolrpc.GetEventsRequest) bool {
			return req.Pagination == nil && req.StartLedger == 100 && req.EndLedger == 200
		})).Return(
			protocolrpc.GetEventsResponse{Events: []protocolrpc.EventInfo{evtA}, Cursor: cursor1},
			nil,
		).Once()

		// Continuation requests must carry only the cursor — ledger ranges and
		// cursors are mutually exclusive in GetEventsRequest.
		mockClient.On("GetEvents", mock.Anything, mock.MatchedBy(func(req protocolrpc.GetEventsRequest) bool {
			return req.Pagination != nil && req.Pagination.Cursor != nil &&
				req.Pagination.Cursor.Ledger == 150 &&
				req.StartLedger == 0 && req.EndLedger == 0
		})).Return(
			protocolrpc.GetEventsResponse{Events: []protocolrpc.EventInfo{evtB}, Cursor: ""},
			nil,
		).Once()

		events, err := reader.FetchMessageSentEvents(context.Background(), 100, 200)
		require.NoError(t, err)
		require.Len(t, events, 2, "second page event must not be dropped")
		assert.Equal(t, protocol.SequenceNumber(1), events[0].Message.SequenceNumber)
		assert.Equal(t, protocol.SequenceNumber(2), events[1].Message.SequenceNumber)
	})

	t.Run("single page without cursor issues one request", func(t *testing.T) {
		reader, mockClient := newReader(t)

		evtA := makeMessageSentEventInfo(t, 150, 1, "tx-a")

		mockClient.On("GetEvents", mock.Anything, mock.MatchedBy(func(req protocolrpc.GetEventsRequest) bool {
			return req.Pagination == nil && req.StartLedger == 100 && req.EndLedger == 200
		})).Return(
			protocolrpc.GetEventsResponse{Events: []protocolrpc.EventInfo{evtA}, Cursor: ""},
			nil,
		).Once()

		events, err := reader.FetchMessageSentEvents(context.Background(), 100, 200)
		require.NoError(t, err)
		require.Len(t, events, 1)
	})

	t.Run("returns error when a continuation page fails", func(t *testing.T) {
		reader, mockClient := newReader(t)

		cursor1 := protocolrpc.Cursor{Ledger: 150, Tx: 1, Op: 0, Event: 0}.String()
		evtA := makeMessageSentEventInfo(t, 150, 1, "tx-a")

		mockClient.On("GetEvents", mock.Anything, mock.MatchedBy(func(req protocolrpc.GetEventsRequest) bool {
			return req.Pagination == nil
		})).Return(
			protocolrpc.GetEventsResponse{Events: []protocolrpc.EventInfo{evtA}, Cursor: cursor1},
			nil,
		).Once()

		mockClient.On("GetEvents", mock.Anything, mock.MatchedBy(func(req protocolrpc.GetEventsRequest) bool {
			return req.Pagination != nil
		})).Return(
			protocolrpc.GetEventsResponse{},
			errors.New("rpc timeout"),
		).Once()

		events, err := reader.FetchMessageSentEvents(context.Background(), 100, 200)
		require.Error(t, err, "incomplete retrieval must error, not return a partial result")
		assert.Nil(t, events)
		assert.Contains(t, err.Error(), "page 2")
	})

	t.Run("stops at events beyond the fixed ledger range", func(t *testing.T) {
		reader, mockClient := newReader(t)

		cursor1 := protocolrpc.Cursor{Ledger: 150, Tx: 1, Op: 0, Event: 0}.String()
		evtA := makeMessageSentEventInfo(t, 150, 1, "tx-a")
		evtOutOfRange := makeMessageSentEventInfo(t, 250, 2, "tx-b")

		mockClient.On("GetEvents", mock.Anything, mock.MatchedBy(func(req protocolrpc.GetEventsRequest) bool {
			return req.Pagination == nil
		})).Return(
			protocolrpc.GetEventsResponse{Events: []protocolrpc.EventInfo{evtA}, Cursor: cursor1},
			nil,
		).Once()

		mockClient.On("GetEvents", mock.Anything, mock.MatchedBy(func(req protocolrpc.GetEventsRequest) bool {
			return req.Pagination != nil
		})).Return(
			protocolrpc.GetEventsResponse{Events: []protocolrpc.EventInfo{evtOutOfRange}, Cursor: ""},
			nil,
		).Once()

		events, err := reader.FetchMessageSentEvents(context.Background(), 100, 200)
		require.NoError(t, err)
		require.Len(t, events, 1, "events past toBlock on a continuation page must be excluded")
		assert.Equal(t, protocol.SequenceNumber(1), events[0].Message.SequenceNumber)
	})

	t.Run("returns error on malformed continuation cursor", func(t *testing.T) {
		reader, mockClient := newReader(t)

		evtA := makeMessageSentEventInfo(t, 150, 1, "tx-a")

		mockClient.On("GetEvents", mock.Anything, mock.Anything).Return(
			protocolrpc.GetEventsResponse{Events: []protocolrpc.EventInfo{evtA}, Cursor: "not-a-cursor"},
			nil,
		).Once()

		events, err := reader.FetchMessageSentEvents(context.Background(), 100, 200)
		require.Error(t, err)
		assert.Nil(t, events)
		assert.Contains(t, err.Error(), "continuation cursor")
	})
}

func TestToBytes32(t *testing.T) {
	// Exactly 64 hex characters (32 bytes), with and without 0x prefix, decodes.
	for _, in := range []string{
		"0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
	} {
		out, err := toBytes32(in)
		require.NoError(t, err)
		assert.Equal(t, []byte{0x12, 0x34, 0x56, 0x78, 0x90, 0xab, 0xcd, 0xef}, out[:8])
	}

	// Anything other than exactly 64 hex characters must be rejected, not
	// silently padded into a different 32-byte hash (report L-6): odd-length
	// truncated (63), even-length truncated (32), and over-length (66).
	for _, in := range []string{
		"0x" + strings.Repeat("ab", 31) + "a", // 63: odd-length truncation
		"0x" + strings.Repeat("ab", 16),       // 32: even-length truncation
		"0x" + strings.Repeat("ab", 33),       // 66: over-length
	} {
		_, err := toBytes32(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be exactly 64 characters")
	}
}
