package eventsfetcher_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"testing"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	eventsfetcher "github.com/smartcontractkit/chainlink-stellar/ccv/common/events_fetcher"
	"github.com/smartcontractkit/chainlink-stellar/ccv/common/events_fetcher/eventsfetchertest"
)

const testContractID = "CAAQCAIBAEAQCAIBAEAQCAIBAEAQCAIBAEAQCAIBAEAQCAIBAEAQC526"

var testFilters = []protocolrpc.EventFilter{eventsfetcher.ContractTopicFilter(testContractID, "topic")}

func fetch(t *testing.T, f *eventsfetchertest.FakeRPC, from, to uint32, o eventsfetcher.Options) (eventsfetcher.Result, error) {
	t.Helper()
	return eventsfetcher.FetchRange(context.Background(), f, testFilters, from, to, o)
}

func ledgersOf(events []protocolrpc.EventInfo) []int32 {
	out := make([]int32, len(events))
	for i, e := range events {
		out[i] = e.Ledger
	}
	return out
}

func idsOf(events []protocolrpc.EventInfo) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

// Regression: the old readers sent EndLedger=to (exclusive) and stopped on an
// empty first page, so an event in the last ledger of the range was lost.
func TestFetchRange_OnlyEventAtTo(t *testing.T) {
	f := eventsfetchertest.New(10, 2000)
	f.Add(eventsfetchertest.Event(1099, 0, 0, 0, "tx"))

	res, err := fetch(t, f, 1000, 1099, eventsfetcher.Options{})
	require.NoError(t, err)
	assert.Equal(t, []int32{1099}, ledgersOf(res.Events))

	reqs := f.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, uint32(1000), reqs[0].StartLedger)
	assert.Equal(t, uint32(1100), reqs[0].EndLedger, "EndLedger is exclusive, so it must be to+1")
}

func TestFetchRange_BoundsInclusive(t *testing.T) {
	f := eventsfetchertest.New(10, 2000)
	f.Add(
		eventsfetchertest.Event(999, 0, 0, 0, "before"),
		eventsfetchertest.Event(1000, 0, 0, 0, "from"),
		eventsfetchertest.Event(1050, 0, 0, 0, "mid"),
		eventsfetchertest.Event(1099, 3, 1, 2, "to"),
		eventsfetchertest.Event(1100, 0, 0, 0, "after"),
	)

	res, err := fetch(t, f, 1000, 1099, eventsfetcher.Options{})
	require.NoError(t, err)
	assert.Equal(t, []int32{1000, 1050, 1099}, ledgersOf(res.Events))
}

func TestFetchRange_MoreThanLimitInLastLedger(t *testing.T) {
	f := eventsfetchertest.New(10, 2000)
	for i := uint32(0); i < 7; i++ {
		f.Add(eventsfetchertest.Event(1099, i, 0, 0, fmt.Sprint(i)))
	}
	f.Add(eventsfetchertest.Event(1100, 0, 0, 0, "after"))

	res, err := fetch(t, f, 1000, 1099, eventsfetcher.Options{PageLimit: 2})
	require.NoError(t, err)
	assert.Len(t, res.Events, 7)
	for _, e := range res.Events {
		assert.Equal(t, int32(1099), e.Ledger)
	}
}

// A page cut short by the server's response byte budget carries a last-event
// cursor even though it holds fewer events than the limit; it must not be
// mistaken for the end of the range.
func TestFetchRange_SilentPageCap(t *testing.T) {
	f := eventsfetchertest.New(10, 2000)
	f.PageCap = 2
	for i := uint32(0); i < 5; i++ {
		f.Add(eventsfetchertest.Event(1099, i, 0, 0, fmt.Sprint(i)))
	}

	res, err := fetch(t, f, 1000, 1099, eventsfetcher.Options{PageLimit: 100})
	require.NoError(t, err)
	assert.Len(t, res.Events, 5)
}

// The server scans at most 10000 ledgers per request and returns an
// end-of-window cursor; the old readers treated that short page as the end.
func TestFetchRange_ScanCapContinuesWithBoundedRequest(t *testing.T) {
	f := eventsfetchertest.New(10, 30000)
	f.Add(
		eventsfetchertest.Event(15000, 0, 0, 0, "a"),
		eventsfetchertest.Event(24000, 0, 0, 0, "b"),
		eventsfetchertest.Event(26000, 0, 0, 0, "after"),
	)

	res, err := fetch(t, f, 100, 25099, eventsfetcher.Options{})
	require.NoError(t, err)
	assert.Equal(t, []int32{15000, 24000}, ledgersOf(res.Events))

	reqs := f.Requests()
	require.Len(t, reqs, 3)
	for i, start := range []uint32{100, 10100, 20100} {
		assert.Equal(t, start, reqs[i].StartLedger, "request %d", i)
		assert.Equal(t, uint32(25100), reqs[i].EndLedger, "request %d", i)
		assert.Nil(t, reqs[i].Pagination.Cursor, "request %d must be bounded, not a cursor request", i)
	}
}

func TestFetchRange_EmptyLargeRange(t *testing.T) {
	f := eventsfetchertest.New(10, 30000)

	res, err := fetch(t, f, 100, 25099, eventsfetcher.Options{})
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Len(t, f.Requests(), 3)
}

// A node that has not ingested through `to` must produce an error, never a
// successful short result the caller would checkpoint past.
func TestFetchRange_LaggingNode(t *testing.T) {
	f := eventsfetchertest.New(10, 1050)
	f.Add(eventsfetchertest.Event(1020, 0, 0, 0, "a"))

	_, err := fetch(t, f, 1000, 1099, eventsfetcher.Options{})
	var inc *eventsfetcher.IncompleteRangeError
	require.ErrorAs(t, err, &inc)
	assert.Equal(t, uint32(1050), inc.ScannedThrough)
	assert.Equal(t, uint32(1050), inc.NodeLatest)
	assert.Equal(t, uint32(1000), inc.From)
	assert.Equal(t, uint32(1099), inc.To)
}

func TestFetchRange_LaggingNodeAfterFullPage(t *testing.T) {
	f := eventsfetchertest.New(10, 1050)
	for i := uint32(0); i < 3; i++ {
		f.Add(eventsfetchertest.Event(1020+i, 0, 0, 0, "a"))
	}

	_, err := fetch(t, f, 1000, 1099, eventsfetcher.Options{PageLimit: 2})
	var inc *eventsfetcher.IncompleteRangeError
	require.ErrorAs(t, err, &inc)
}

func TestFetchRange_NodeAtToIsComplete(t *testing.T) {
	f := eventsfetchertest.New(10, 1099)
	f.Add(eventsfetchertest.Event(1099, 0, 0, 0, "a"))

	res, err := fetch(t, f, 1000, 1099, eventsfetcher.Options{})
	require.NoError(t, err)
	assert.Len(t, res.Events, 1)
}

func TestFetchRange_InclusiveCursorDeduped(t *testing.T) {
	f := eventsfetchertest.New(10, 2000)
	f.InclusiveCursor = true
	for i := uint32(0); i < 5; i++ {
		f.Add(eventsfetchertest.Event(1000+i, 0, 0, 0, fmt.Sprint(i)))
	}

	res, err := fetch(t, f, 1000, 1099, eventsfetcher.Options{PageLimit: 2})
	require.NoError(t, err)
	assert.Equal(t, []int32{1000, 1001, 1002, 1003, 1004}, ledgersOf(res.Events))
}

func TestFetchRange_InclusiveCursorSingleEventPagesStall(t *testing.T) {
	f := eventsfetchertest.New(10, 2000)
	f.InclusiveCursor = true
	f.Add(eventsfetchertest.Event(1000, 0, 0, 0, "a"), eventsfetchertest.Event(1001, 0, 0, 0, "b"))

	_, err := fetch(t, f, 1000, 1099, eventsfetcher.Options{PageLimit: 1})
	require.ErrorIs(t, err, eventsfetcher.ErrCursorStalled)
}

func TestFetchRange_Faults(t *testing.T) {
	full := func(ledger uint32) protocolrpc.GetEventsResponse {
		e := eventsfetchertest.Event(ledger, 0, 0, 0, "x")
		return protocolrpc.GetEventsResponse{Events: []protocolrpc.EventInfo{e}, Cursor: e.ID, LatestLedger: 2000}
	}
	tests := []struct {
		name     string
		override eventsfetchertest.Override
		wantErr  error
		contains string
	}{
		{
			name: "missing cursor",
			override: func(int, protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error, bool) {
				return protocolrpc.GetEventsResponse{LatestLedger: 2000}, nil, true
			},
			wantErr: eventsfetcher.ErrMissingCursor,
		},
		{
			name: "malformed cursor",
			override: func(int, protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error, bool) {
				return protocolrpc.GetEventsResponse{Cursor: "not-a-cursor", LatestLedger: 2000}, nil, true
			},
			contains: "invalid cursor",
		},
		{
			name: "malformed event id",
			override: func(int, protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error, bool) {
				return protocolrpc.GetEventsResponse{Events: []protocolrpc.EventInfo{{ID: "evt-1", Ledger: 1000}}, LatestLedger: 2000}, nil, true
			},
			contains: "invalid event id",
		},
		{
			name: "stalled cursor",
			override: func(int, protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error, bool) {
				return full(1000), nil, true
			},
			wantErr: eventsfetcher.ErrCursorStalled,
		},
		{
			name: "out of order",
			override: func(n int, _ protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error, bool) {
				if n == 1 {
					return full(1050), nil, true
				}
				return full(1020), nil, true
			},
			wantErr: eventsfetcher.ErrOutOfOrder,
		},
		{
			name: "event before from",
			override: func(int, protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error, bool) {
				return full(999), nil, true
			},
			wantErr: eventsfetcher.ErrOutOfOrder,
		},
		{
			name: "error on continuation page",
			override: func(n int, _ protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error, bool) {
				if n == 1 {
					return full(1010), nil, true
				}
				return protocolrpc.GetEventsResponse{}, errors.New("rpc timeout"), true
			},
			contains: "page 2: rpc timeout",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := eventsfetchertest.New(10, 2000)
			f.Override = tt.override
			res, err := fetch(t, f, 1000, 1099, eventsfetcher.Options{})
			require.Error(t, err)
			assert.Nil(t, res.Events, "an error must not return partial events")
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
			if tt.contains != "" {
				assert.ErrorContains(t, err, tt.contains)
			}
		})
	}
}

func TestFetchRange_InvalidRange(t *testing.T) {
	for _, r := range [][2]uint32{{0, 10}, {11, 10}, {10, math.MaxInt32}} {
		f := eventsfetchertest.New(1, 2000)
		_, err := fetch(t, f, r[0], r[1], eventsfetcher.Options{})
		require.ErrorIs(t, err, eventsfetcher.ErrInvalidRange, "range %v", r)
		assert.Empty(t, f.Requests(), "no RPC call for an invalid range")
	}
}

func TestFetchRange_SingleLedger(t *testing.T) {
	f := eventsfetchertest.New(10, 2000)
	f.Add(eventsfetchertest.Event(1000, 0, 0, 0, "a"), eventsfetchertest.Event(1001, 0, 0, 0, "b"))

	res, err := fetch(t, f, 1000, 1000, eventsfetcher.Options{})
	require.NoError(t, err)
	assert.Equal(t, []int32{1000}, ledgersOf(res.Events))
}

func TestFetchRange_ContextCancelledBetweenPages(t *testing.T) {
	f := eventsfetchertest.New(10, 2000)
	for i := uint32(0); i < 5; i++ {
		f.Add(eventsfetchertest.Event(1000+i, 0, 0, 0, "x"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.Override = func(n int, _ protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error, bool) {
		if n == 1 {
			cancel()
		}
		return protocolrpc.GetEventsResponse{}, nil, false
	}

	res, err := eventsfetcher.FetchRange(ctx, f, testFilters, 1000, 1099, eventsfetcher.Options{PageLimit: 1})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, res.Events)
}

func TestFetchRange_MaxPages(t *testing.T) {
	f := eventsfetchertest.New(10, 2000)
	for i := uint32(0); i < 5; i++ {
		f.Add(eventsfetchertest.Event(1000+i, 0, 0, 0, "x"))
	}

	_, err := fetch(t, f, 1000, 1099, eventsfetcher.Options{PageLimit: 1, MaxPages: 3})
	require.ErrorIs(t, err, eventsfetcher.ErrTooManyPages)
}

// Property: for random event layouts, scan caps and page limits, a range
// returns exactly the seeded events inside it, and splitting the range into
// arbitrary consecutive inclusive chunks (as the verifier does) yields the
// same events with nothing skipped or duplicated at chunk boundaries.
func TestFetchRange_Property(t *testing.T) {
	for seed := int64(1); seed <= 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		f := eventsfetchertest.New(10, 500)
		f.ScanCap = uint32(1 + rng.Intn(40))
		f.InclusiveCursor = rng.Intn(4) == 0
		if rng.Intn(3) == 0 {
			f.PageCap = uint(1 + rng.Intn(3))
			if f.InclusiveCursor {
				// An inclusive cursor with one-event pages cannot make
				// progress; that stall is covered by its own test.
				f.PageCap = max(f.PageCap, 2)
			}
		}
		limit := uint(2 + rng.Intn(4))

		var all []protocolrpc.EventInfo
		seen := map[string]bool{}
		for i := 0; i < rng.Intn(60); i++ {
			e := eventsfetchertest.Event(uint32(10+rng.Intn(491)), uint32(rng.Intn(3)), uint32(rng.Intn(2)), uint32(rng.Intn(2)), "x")
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			all = append(all, e)
		}
		f.Add(all...)

		from := uint32(10 + rng.Intn(100))
		to := from + uint32(rng.Intn(390))
		var want []string
		for _, e := range all {
			if uint32(e.Ledger) >= from && uint32(e.Ledger) <= to {
				want = append(want, e.ID)
			}
		}

		res, err := fetch(t, f, from, to, eventsfetcher.Options{PageLimit: limit})
		require.NoError(t, err, "seed %d", seed)
		assert.ElementsMatch(t, want, idsOf(res.Events), "seed %d: whole range", seed)
		for i := 1; i < len(res.Events); i++ {
			require.Less(t, res.Events[i-1].ID, res.Events[i].ID, "seed %d: ascending, no duplicates", seed)
		}

		chunked := []string{}
		for lo := from; lo <= to; {
			hi := min(to, lo+uint32(rng.Intn(60)))
			r, err := fetch(t, f, lo, hi, eventsfetcher.Options{PageLimit: limit})
			require.NoError(t, err, "seed %d: chunk [%d,%d]", seed, lo, hi)
			chunked = append(chunked, idsOf(r.Events)...)
			lo = hi + 1
		}
		assert.Equal(t, append([]string{}, idsOf(res.Events)...), chunked, "seed %d: chunked union", seed)
	}
}
