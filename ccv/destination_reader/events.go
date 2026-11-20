package destinationreader

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

// eventsPageLimit is the per-request page size for event queries; a full page
// means the window may hold more matching events.
const eventsPageLimit = 100

// GetEventsInRange returns every event matching the given topics that the
// contract emitted in [fromLedger, toLedger]. A single getEvents request
// returns at most one server-side page, so this pages through the results
// with cursors; without it a busy window (e.g. a backfill over a shared
// OffRamp) would silently drop every matching event past the first page.
func GetEventsInRange(ctx context.Context, rpcClient *rpcclient.Client, contractID string, fromLedger, toLedger uint32, topics []string) ([]protocolrpc.EventInfo, error) {
	var topicScVals []*xdr.ScVal
	for _, topic := range topics {
		topicScVals = append(topicScVals, scval.SymbolToScValPtr(topic))
	}
	zeroOrMore := protocolrpc.WildCardZeroOrMore
	topicFilter := protocolrpc.TopicFilter{}
	for _, scVal := range topicScVals {
		topicFilter = append(topicFilter, protocolrpc.SegmentFilter{ScVal: scVal})
	}
	topicFilter = append(topicFilter, protocolrpc.SegmentFilter{Wildcard: &zeroOrMore})
	filters := []protocolrpc.EventFilter{
		{
			EventType:   protocolrpc.EventTypeSet{protocolrpc.EventTypeContract: nil},
			ContractIDs: []string{contractID},
			Topics:      []protocolrpc.TopicFilter{topicFilter},
		},
	}

	seen := make(map[string]struct{})
	var out []protocolrpc.EventInfo
	var cursor *protocolrpc.Cursor
	for {
		req := protocolrpc.GetEventsRequest{
			Filters:    filters,
			Pagination: &protocolrpc.PaginationOptions{Limit: eventsPageLimit, Cursor: cursor},
		}
		// A cursor cannot be combined with a ledger range, and a cursor query
		// is open-ended, so the window's end is enforced client-side below.
		if cursor == nil {
			req.StartLedger = fromLedger
			req.EndLedger = toLedger
		}
		resp, err := rpcClient.GetEvents(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("failed to get events: %w", err)
		}
		pastWindow := false
		for _, e := range resp.Events {
			if uint32(e.Ledger) > toLedger {
				pastWindow = true
				break
			}
			// Dedupe by event ID: the cursor convention (exclusive vs.
			// inclusive of the last event) is server-specific.
			if _, dup := seen[e.ID]; dup {
				continue
			}
			seen[e.ID] = struct{}{}
			out = append(out, e)
		}
		if pastWindow || uint(len(resp.Events)) < eventsPageLimit || resp.Cursor == "" {
			break
		}
		next, err := protocolrpc.ParseCursor(resp.Cursor)
		if err != nil {
			return nil, fmt.Errorf("failed to parse events cursor %q: %w", resp.Cursor, err)
		}
		if cursor != nil && next.Cmp(*cursor) <= 0 {
			return nil, fmt.Errorf("events cursor %q did not advance; refusing to loop on the same page", resp.Cursor)
		}
		cursor = &next
	}
	return out, nil
}
