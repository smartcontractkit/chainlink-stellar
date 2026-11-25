// Package eventsfetchertest provides an in-memory Stellar RPC double whose
// getEvents paging mirrors stellar-rpc's handler, for testing event consumers.
package eventsfetchertest

import (
	"context"
	"fmt"
	"sort"
	"sync"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

const (
	// DefaultScanCap is stellar-rpc's LedgerScanLimit.
	DefaultScanCap uint32 = 10000
	// DefaultLimit is stellar-rpc's default getEvents limit.
	DefaultLimit uint = 100
	// DefaultMaxLimit is stellar-rpc's default maximum getEvents limit.
	DefaultMaxLimit uint = 10000
)

// Event builds an EventInfo positioned at (ledger, tx, op, event), with a
// matching ID and the given transaction hash.
func Event(ledger, tx, op, event uint32, txHash string) protocolrpc.EventInfo {
	return protocolrpc.EventInfo{
		EventType:       protocolrpc.EventTypeContract,
		Ledger:          int32(ledger),
		ID:              protocolrpc.Cursor{Ledger: ledger, Tx: tx, Op: op, Event: event}.String(),
		TransactionHash: txHash,
		TxIndex:         tx,
		OpIndex:         op,
	}
}

// Override lets a test replace the response to the n-th (1-based) getEvents
// call. Returning handled=false falls through to the simulated server.
type Override func(n int, req protocolrpc.GetEventsRequest) (resp protocolrpc.GetEventsResponse, err error, handled bool)

// FakeRPC is an in-memory Stellar RPC serving seeded events. Filters are not
// evaluated: every seeded event matches. The zero value is not usable; use New.
type FakeRPC struct {
	mu sync.Mutex

	events []protocolrpc.EventInfo
	// Latest and Oldest bound the ledgers the node has ingested and retains.
	Latest, Oldest uint32
	// ScanCap is the maximum number of ledgers one request scans.
	ScanCap uint32
	// DefaultLimit and MaxLimit mirror the server's limit flags.
	DefaultLimit, MaxLimit uint
	// PageCap, when non-zero, silently returns at most PageCap events per
	// page with a last-event cursor, like the server's response byte budget.
	PageCap uint
	// InclusiveCursor makes cursor requests repeat the cursor's own event.
	InclusiveCursor bool
	// Override intercepts calls for fault injection.
	Override Override

	requests []protocolrpc.GetEventsRequest
}

// New returns a FakeRPC with stellar-rpc's defaults whose ledger window is
// [oldest, latest].
func New(oldest, latest uint32) *FakeRPC {
	return &FakeRPC{
		Latest:       latest,
		Oldest:       oldest,
		ScanCap:      DefaultScanCap,
		DefaultLimit: DefaultLimit,
		MaxLimit:     DefaultMaxLimit,
	}
}

// Add seeds events. Each must have a valid cursor ID.
func (f *FakeRPC) Add(events ...protocolrpc.EventInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, events...)
	sort.SliceStable(f.events, func(i, j int) bool {
		return mustCursor(f.events[i].ID).Cmp(mustCursor(f.events[j].ID)) < 0
	})
}

// SetLatest moves the node's latest ingested ledger.
func (f *FakeRPC) SetLatest(latest uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Latest = latest
}

// SetOldest moves the node's oldest retained ledger.
func (f *FakeRPC) SetOldest(oldest uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Oldest = oldest
}

// Requests returns a copy of every getEvents request received.
func (f *FakeRPC) Requests() []protocolrpc.GetEventsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocolrpc.GetEventsRequest(nil), f.requests...)
}

// GetLatestLedger returns the node's latest ledger.
func (f *FakeRPC) GetLatestLedger(context.Context) (protocolrpc.GetLatestLedgerResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return protocolrpc.GetLatestLedgerResponse{Sequence: f.Latest}, nil
}

// GetHealth returns the node's ledger window.
func (f *FakeRPC) GetHealth(context.Context) (protocolrpc.GetHealthResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return protocolrpc.GetHealthResponse{Status: "healthy", LatestLedger: f.Latest, OldestLedger: f.Oldest}, nil
}

// GetEvents implements stellar-rpc's getEvents paging
// (cmd/stellar-rpc/internal/methods/get_events.go, v30.0.1).
func (f *FakeRPC) GetEvents(ctx context.Context, req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error) {
	if err := ctx.Err(); err != nil {
		return protocolrpc.GetEventsResponse{}, err
	}
	f.mu.Lock()
	f.requests = append(f.requests, req)
	n := len(f.requests)
	override := f.Override
	f.mu.Unlock()

	if override != nil {
		if resp, err, handled := override(n, req); handled {
			return resp, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if err := req.Valid(f.MaxLimit); err != nil {
		return protocolrpc.GetEventsResponse{}, fmt.Errorf("invalid params: %w", err)
	}

	start := protocolrpc.Cursor{Ledger: req.StartLedger}
	limit := f.DefaultLimit
	if req.Pagination != nil {
		if req.Pagination.Cursor != nil {
			start = *req.Pagination.Cursor
			if !f.InclusiveCursor {
				start.Event++
			}
		}
		if req.Pagination.Limit > 0 {
			limit = req.Pagination.Limit
		}
	}
	endLedger := min(start.Ledger+f.ScanCap, f.Latest+1)
	if req.EndLedger != 0 {
		endLedger = min(req.EndLedger, endLedger)
	}
	if start.Ledger < f.Oldest || start.Ledger > f.Latest {
		return protocolrpc.GetEventsResponse{}, fmt.Errorf("startLedger must be within the ledger range: %d - %d", f.Oldest, f.Latest)
	}
	end := protocolrpc.Cursor{Ledger: endLedger}

	pageCap := limit
	if f.PageCap > 0 && f.PageCap < pageCap {
		pageCap = f.PageCap
	}
	results := []protocolrpc.EventInfo{}
	for _, e := range f.events {
		pos := mustCursor(e.ID)
		if pos.Cmp(start) < 0 || pos.Cmp(end) >= 0 {
			continue
		}
		results = append(results, e)
		if uint(len(results)) == pageCap {
			break
		}
	}

	var cursor string
	if uint(len(results)) == pageCap {
		cursor = results[len(results)-1].ID
	} else {
		maxCursor := protocolrpc.MaxCursor
		maxCursor.Ledger = endLedger - 1
		cursor = maxCursor.String()
	}
	return protocolrpc.GetEventsResponse{
		Events:       results,
		Cursor:       cursor,
		LatestLedger: f.Latest,
		OldestLedger: f.Oldest,
	}, nil
}

func mustCursor(id string) protocolrpc.Cursor {
	c, err := protocolrpc.ParseCursor(id)
	if err != nil {
		panic(fmt.Sprintf("eventsfetchertest: event id %q is not a cursor: %v", id, err))
	}
	return c
}
