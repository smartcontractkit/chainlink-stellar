package eventsfetcher

import (
	"context"
	"errors"
	"fmt"
	"math"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/toid"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

const (
	// DefaultPageLimit is the per-request event limit used when
	// Options.PageLimit is zero. It matches stellar-rpc's default limit.
	DefaultPageLimit uint = 100
	// DefaultMaxPages bounds the number of getEvents requests a single
	// FetchRange call may issue when Options.MaxPages is zero.
	DefaultMaxPages = 1000
)

var (
	// ErrInvalidRange is returned, without any RPC call, for a range that
	// cannot be queried: from == 0, from > to, or to beyond the ledger range
	// representable in an event cursor.
	ErrInvalidRange = errors.New("invalid ledger range")
	// ErrMissingCursor is returned when a response that does not complete the
	// range carries no continuation cursor.
	ErrMissingCursor = errors.New("getEvents response has no cursor")
	// ErrCursorStalled is returned when a continuation cursor does not advance
	// past the previous one, which would otherwise loop forever.
	ErrCursorStalled = errors.New("getEvents cursor did not advance")
	// ErrOutOfOrder is returned when an event is positioned before a
	// previously returned event.
	ErrOutOfOrder = errors.New("getEvents returned events out of order")
	// ErrTooManyPages is returned when a range needs more than Options.MaxPages
	// requests.
	ErrTooManyPages = errors.New("getEvents page limit exceeded")
)

// IncompleteRangeError reports that the serving RPC node has not ingested the
// whole requested range yet: it scanned through ScannedThrough, which is its
// latest ledger, but the range extends to To. Callers should retry the same
// range later rather than treat it as complete.
type IncompleteRangeError struct {
	From, To       uint32
	ScannedThrough uint32
	NodeLatest     uint32
}

func (e *IncompleteRangeError) Error() string {
	return fmt.Sprintf("rpc node has only ingested through ledger %d (latest %d), range [%d, %d] is incomplete",
		e.ScannedThrough, e.NodeLatest, e.From, e.To)
}

// Client is the subset of the Stellar RPC client used to fetch events.
// *rpcclient.Client satisfies it.
type Client interface {
	GetEvents(ctx context.Context, req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error)
}

// Options tunes FetchRange. The zero value uses the defaults.
type Options struct {
	// PageLimit is the per-request event limit (default DefaultPageLimit).
	PageLimit uint
	// MaxPages caps the number of requests (default DefaultMaxPages).
	MaxPages int
}

// Result holds every event in the requested range, in ascending position
// order, plus the serving node's ledger window from the last response.
type Result struct {
	Events     []protocolrpc.EventInfo
	NodeLatest uint32
	NodeOldest uint32
	Pages      int
}

// ContractTopicFilter builds a contract-event filter for contractID whose
// leading topics equal the given symbols, followed by any number of further
// topics.
func ContractTopicFilter(contractID string, symbols ...string) protocolrpc.EventFilter {
	zeroOrMore := protocolrpc.WildCardZeroOrMore
	topic := make(protocolrpc.TopicFilter, 0, len(symbols)+1)
	for _, s := range symbols {
		topic = append(topic, protocolrpc.SegmentFilter{ScVal: scval.SymbolToScValPtr(s)})
	}
	topic = append(topic, protocolrpc.SegmentFilter{Wildcard: &zeroOrMore})
	return protocolrpc.EventFilter{
		EventType:   protocolrpc.EventTypeSet{protocolrpc.EventTypeContract: nil},
		ContractIDs: []string{contractID},
		Topics:      []protocolrpc.TopicFilter{topic},
	}
}

// EndOfLedger returns the greatest cursor position within ledger l; every
// event emitted in ledger l is positioned at or before it.
func EndOfLedger(l uint32) protocolrpc.Cursor {
	return protocolrpc.Cursor{Ledger: l, Tx: toid.TransactionMask, Op: toid.OperationMask, Event: math.MaxUint32}
}

// isEndOfWindow reports whether c is the end-of-window cursor stellar-rpc
// returns for a page that did not fill up (MaxCursor's tx/op/event).
func isEndOfWindow(c protocolrpc.Cursor) bool {
	return c.Tx == toid.TransactionMask && c.Op == toid.OperationMask && c.Event == math.MaxUint32
}

// FetchRange returns every event matching filters that was emitted in the
// inclusive ledger range [from, to], in ascending position order.
//
// The result is complete or the call fails: page size is never used to infer
// that the range is exhausted. Completion is decided only by an event past
// `to` or by a cursor at or beyond the end of ledger `to`. When the server's
// scan window ends short of `to` (10000-ledger scan cap) a new bounded request
// continues from the next ledger; when it ends at the node's latest ledger
// (the node lags `to`) an *IncompleteRangeError is returned. Events repeated
// across pages are dropped. Any error returns no events.
func FetchRange(ctx context.Context, c Client, filters []protocolrpc.EventFilter, from, to uint32, o Options) (Result, error) {
	if from == 0 || from > to || to >= math.MaxInt32 {
		return Result{}, fmt.Errorf("%w: [%d, %d]", ErrInvalidRange, from, to)
	}
	limit := o.PageLimit
	if limit == 0 {
		limit = DefaultPageLimit
	}
	maxPages := o.MaxPages
	if maxPages <= 0 {
		maxPages = DefaultMaxPages
	}

	end := EndOfLedger(to)
	wrap := func(page int, err error) error {
		return fmt.Errorf("fetching events in ledgers [%d, %d], page %d: %w", from, to, page, err)
	}

	var (
		res        Result
		last       *protocolrpc.Cursor // position of the last collected event
		prevCursor *protocolrpc.Cursor // cursor of the previous response
		start      = from              // StartLedger of the next bounded request
		cursor     *protocolrpc.Cursor // continuation cursor; nil means a bounded request
	)
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, wrap(res.Pages+1, err)
		}
		if res.Pages >= maxPages {
			return Result{}, wrap(res.Pages+1, fmt.Errorf("%w (%d)", ErrTooManyPages, maxPages))
		}

		req := protocolrpc.GetEventsRequest{
			Filters:    filters,
			Pagination: &protocolrpc.PaginationOptions{Limit: limit, Cursor: cursor},
		}
		if cursor == nil {
			// EndLedger is exclusive.
			req.StartLedger = start
			req.EndLedger = to + 1
		}
		resp, err := c.GetEvents(ctx, req)
		res.Pages++
		if err != nil {
			return Result{}, wrap(res.Pages, err)
		}
		res.NodeLatest, res.NodeOldest = resp.LatestLedger, resp.OldestLedger

		for _, e := range resp.Events {
			pos, err := protocolrpc.ParseCursor(e.ID)
			if err != nil {
				return Result{}, wrap(res.Pages, fmt.Errorf("invalid event id %q: %w", e.ID, err))
			}
			if pos.Ledger > to {
				// Cursor requests are open-ended; the first event past the
				// range ends it.
				return res, nil
			}
			if pos.Ledger < from {
				return Result{}, wrap(res.Pages, fmt.Errorf("%w: event %s precedes ledger %d", ErrOutOfOrder, e.ID, from))
			}
			if last != nil {
				switch pos.Cmp(*last) {
				case 0:
					continue // repeated across a page boundary
				case -1:
					return Result{}, wrap(res.Pages, fmt.Errorf("%w: event %s after %s", ErrOutOfOrder, e.ID, last.String()))
				}
			}
			res.Events = append(res.Events, e)
			last = &pos
		}

		if resp.Cursor == "" {
			return Result{}, wrap(res.Pages, ErrMissingCursor)
		}
		next, err := protocolrpc.ParseCursor(resp.Cursor)
		if err != nil {
			return Result{}, wrap(res.Pages, fmt.Errorf("invalid cursor %q: %w", resp.Cursor, err))
		}
		if next.Cmp(end) >= 0 {
			return res, nil
		}
		if prevCursor != nil && next.Cmp(*prevCursor) <= 0 {
			return Result{}, wrap(res.Pages, fmt.Errorf("%w: %s after %s", ErrCursorStalled, resp.Cursor, prevCursor.String()))
		}
		prevCursor = &next

		if isEndOfWindow(next) {
			if next.Ledger >= resp.LatestLedger {
				return Result{}, &IncompleteRangeError{From: from, To: to, ScannedThrough: next.Ledger, NodeLatest: resp.LatestLedger}
			}
			// The scan cap ended the window before `to`: continue with a new
			// bounded request rather than an open-ended cursor request.
			start = next.Ledger + 1
			cursor = nil
			continue
		}
		// A full page: continue right after its last event.
		cont := next
		cursor = &cont
	}
}
