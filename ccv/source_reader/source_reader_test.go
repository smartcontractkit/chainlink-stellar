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
	eventsfetcher "github.com/smartcontractkit/chainlink-stellar/ccv/common/events_fetcher"
	"github.com/smartcontractkit/chainlink-stellar/ccv/common/events_fetcher/eventsfetchertest"
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
// the given ledger and sequence number, positioned at transaction index
// seqNum within the ledger.
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

	e := eventsfetchertest.Event(uint32(ledger), uint32(seqNum), 0, 0, txHash)
	e.ValueXDR = valueB64
	return e
}

// testOnRampID is a syntactically valid contract strkey; the fake RPC rejects
// filters with malformed contract IDs, as stellar-rpc does.
const testOnRampID = "CAAQCAIBAEAQCAIBAEAQCAIBAEAQCAIBAEAQCAIBAEAQCAIBAEAQC526"

// fakeSourceRPC serves getEvents and getLatestLedger from an in-memory
// stellar-rpc double.
type fakeSourceRPC struct {
	*eventsfetchertest.FakeRPC
}

func (fakeSourceRPC) GetLedgers(context.Context, protocolrpc.GetLedgersRequest) (protocolrpc.GetLedgersResponse, error) {
	return protocolrpc.GetLedgersResponse{}, errors.New("not implemented")
}

func newFakeSourceReader(t *testing.T, oldest, latest uint32) (*SourceReader, *eventsfetchertest.FakeRPC) {
	t.Helper()
	lggr := zerolog.New(os.Stdout).With().Timestamp().Logger().Level(zerolog.DebugLevel)
	f := eventsfetchertest.New(oldest, latest)
	reader, err := NewSourceReaderWithClient(fakeSourceRPC{f}, nil, testOnRampID, "transfer", "RMNREMOTE", &lggr)
	require.NoError(t, err)
	return reader, f
}

func seqNums(events []protocol.MessageSentEvent) []protocol.SequenceNumber {
	out := make([]protocol.SequenceNumber, len(events))
	for i, e := range events {
		out[i] = e.Message.SequenceNumber
	}
	return out
}

func TestFetchMessageSentEvents(t *testing.T) {
	t.Run("event in the last ledger of the range is returned", func(t *testing.T) {
		// Regression: EndLedger is exclusive and an empty first page used to
		// end pagination, so this event was lost permanently.
		reader, f := newFakeSourceReader(t, 10, 2000)
		f.Add(makeMessageSentEventInfo(t, 1099, 1, "tx-a"))

		events, err := reader.FetchMessageSentEvents(context.Background(), 1000, 1099)
		require.NoError(t, err)
		assert.Equal(t, []protocol.SequenceNumber{1}, seqNums(events))
		assert.Equal(t, uint64(1099), events[0].BlockNumber)
	})

	t.Run("consecutive inclusive chunks lose and duplicate nothing", func(t *testing.T) {
		reader, f := newFakeSourceReader(t, 10, 2000)
		for i, ledger := range []int32{1000, 1100, 1101, 1201, 1202, 1300} {
			f.Add(makeMessageSentEventInfo(t, ledger, uint64(i+1), "tx"))
		}

		var got []protocol.SequenceNumber
		for from := uint64(1000); from <= 1300; from += 101 {
			events, err := reader.FetchMessageSentEvents(context.Background(), from, min(from+100, 1300))
			require.NoError(t, err)
			got = append(got, seqNums(events)...)
		}
		assert.Equal(t, []protocol.SequenceNumber{1, 2, 3, 4, 5, 6}, got)
	})

	t.Run("pages through more events than one page holds", func(t *testing.T) {
		reader, f := newFakeSourceReader(t, 10, 2000)
		for i := 0; i < 250; i++ {
			f.Add(makeMessageSentEventInfo(t, int32(1000+i/3), uint64(i), "tx"))
		}

		events, err := reader.FetchMessageSentEvents(context.Background(), 1000, 1099)
		require.NoError(t, err)
		assert.Len(t, events, 250)
	})

	t.Run("range wider than the server scan cap is fully covered", func(t *testing.T) {
		reader, f := newFakeSourceReader(t, 10, 40000)
		f.Add(makeMessageSentEventInfo(t, 25000, 1, "tx"))

		events, err := reader.FetchMessageSentEvents(context.Background(), 100, 30000)
		require.NoError(t, err)
		assert.Equal(t, []protocol.SequenceNumber{1}, seqNums(events))
	})

	t.Run("toBlock 0 reads through the latest ledger", func(t *testing.T) {
		reader, f := newFakeSourceReader(t, 10, 1500)
		f.Add(makeMessageSentEventInfo(t, 1500, 1, "tx"))

		events, err := reader.FetchMessageSentEvents(context.Background(), 1000, 0)
		require.NoError(t, err)
		assert.Equal(t, []protocol.SequenceNumber{1}, seqNums(events))
		assert.Equal(t, uint32(1501), f.Requests()[0].EndLedger)
	})

	t.Run("toBlock 0 errors when the events node lags an already-reported latest", func(t *testing.T) {
		// The verifier saw ledger 1500 via LatestAndFinalizedBlock and will
		// checkpoint there, but the node now serving lags at 1400.
		reader, _ := newFakeSourceReader(t, 10, 1400)
		reader.observeLatest(1500)

		events, err := reader.FetchMessageSentEvents(context.Background(), 1000, 0)
		var inc *eventsfetcher.IncompleteRangeError
		require.ErrorAs(t, err, &inc, "a short result would let the caller skip ledgers 1401-1500")
		assert.Nil(t, events)
	})

	t.Run("explicit toBlock past the node's latest errors", func(t *testing.T) {
		reader, _ := newFakeSourceReader(t, 10, 1050)

		_, err := reader.FetchMessageSentEvents(context.Background(), 1000, 1099)
		var inc *eventsfetcher.IncompleteRangeError
		require.ErrorAs(t, err, &inc)
	})

	t.Run("range below the minimum ledger is empty without an RPC call", func(t *testing.T) {
		reader, f := newFakeSourceReader(t, 1, 2000)

		events, err := reader.FetchMessageSentEvents(context.Background(), 0, 5)
		require.NoError(t, err)
		assert.Empty(t, events)
		assert.Empty(t, f.Requests())
	})

	t.Run("from is clamped to the minimum ledger", func(t *testing.T) {
		reader, f := newFakeSourceReader(t, 1, 2000)

		_, err := reader.FetchMessageSentEvents(context.Background(), 0, 100)
		require.NoError(t, err)
		assert.Equal(t, uint32(7), f.Requests()[0].StartLedger)
	})

	t.Run("undecodable event is skipped and the rest returned", func(t *testing.T) {
		reader, f := newFakeSourceReader(t, 10, 2000)
		bad := eventsfetchertest.Event(1050, 0, 0, 0, "tx-bad")
		bad.ValueXDR = "not-xdr"
		f.Add(makeMessageSentEventInfo(t, 1010, 1, "tx-a"), bad, makeMessageSentEventInfo(t, 1090, 2, "tx-b"))

		events, err := reader.FetchMessageSentEvents(context.Background(), 1000, 1099)
		require.NoError(t, err)
		assert.Equal(t, []protocol.SequenceNumber{1, 2}, seqNums(events))
	})

	t.Run("rpc error mid-pagination returns an error, not a partial result", func(t *testing.T) {
		reader, f := newFakeSourceReader(t, 10, 2000)
		for i := 0; i < 150; i++ {
			f.Add(makeMessageSentEventInfo(t, int32(1000+i/2), uint64(i), "tx"))
		}
		f.Override = func(n int, _ protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error, bool) {
			if n == 2 {
				return protocolrpc.GetEventsResponse{}, errors.New("rpc timeout"), true
			}
			return protocolrpc.GetEventsResponse{}, nil, false
		}

		events, err := reader.FetchMessageSentEvents(context.Background(), 1000, 1099)
		require.Error(t, err)
		assert.Nil(t, events)
		assert.Contains(t, err.Error(), "page 2")
	})

	t.Run("filters on the onramp contract and message sent topic", func(t *testing.T) {
		reader, f := newFakeSourceReader(t, 10, 2000)

		_, err := reader.FetchMessageSentEvents(context.Background(), 1000, 1099)
		require.NoError(t, err)
		req := f.Requests()[0]
		require.Len(t, req.Filters, 1)
		assert.Equal(t, []string{testOnRampID}, req.Filters[0].ContractIDs)
		assert.Equal(t, scval.SymbolToScValPtr("transfer"), req.Filters[0].Topics[0][0].ScVal)
	})

	t.Run("rejects ledger numbers beyond uint32", func(t *testing.T) {
		reader, _ := newFakeSourceReader(t, 10, 2000)

		_, err := reader.FetchMessageSentEvents(context.Background(), 1<<33, 0)
		require.Error(t, err)
		_, err = reader.FetchMessageSentEvents(context.Background(), 1, 1<<33)
		require.Error(t, err)
	})
}

func TestLatestAndFinalizedBlockRaisesHighWaterMark(t *testing.T) {
	lggr := zerolog.Nop()
	mockClient := mocks.NewMockRPCClient(t)
	header := xdr.LedgerHeader{LedgerSeq: 1500}
	headerB64, err := xdr.MarshalBase64(header)
	require.NoError(t, err)
	mockClient.On("GetLatestLedger", mock.Anything).Return(protocolrpc.GetLatestLedgerResponse{
		Hash:         strings.Repeat("ab", 32),
		Sequence:     1500,
		LedgerHeader: headerB64,
	}, nil)
	reader, err := NewSourceReaderWithClient(mockClient, nil, "CADDR", "transfer", "RMNREMOTE", &lggr)
	require.NoError(t, err)

	_, _, err = reader.LatestAndFinalizedBlock(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint32(1500), reader.latestSeen.Load())
	assert.Equal(t, uint32(1500), reader.observeLatest(1400), "the mark never moves backwards")
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
