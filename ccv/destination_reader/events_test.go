package destinationreader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/require"
)

// testEvent builds an EventInfo whose ID is a valid event cursor string.
func testEvent(ledger, tx, event uint32) protocolrpc.EventInfo {
	id := protocolrpc.Cursor{Ledger: ledger, Tx: tx, Event: event}.String()
	return protocolrpc.EventInfo{ID: id, Ledger: int32(ledger)}
}

// testEventPage returns n consecutive one-event-per-ledger events starting at
// fromLedger.
func testEventPage(fromLedger uint32, n int) []protocolrpc.EventInfo {
	evs := make([]protocolrpc.EventInfo, n)
	for i := range evs {
		evs[i] = testEvent(fromLedger+uint32(i), 0, 0)
	}
	return evs
}

// eventsTestServer serves canned getEvents responses from handler and records
// every request's params.
func eventsTestServer(t *testing.T, handler func(req protocolrpc.GetEventsRequest) protocolrpc.GetEventsResponse) (*rpcclient.Client, *[]protocolrpc.GetEventsRequest) {
	t.Helper()

	var reqs []protocolrpc.GetEventsRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq struct {
			Method string                       `json:"method"`
			Params protocolrpc.GetEventsRequest `json:"params"`
			ID     any                          `json:"id"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&rpcReq))
		require.Equal(t, protocolrpc.GetEventsMethodName, rpcReq.Method)
		reqs = append(reqs, rpcReq.Params)

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"result":  handler(rpcReq.Params),
			"id":      rpcReq.ID,
		}))
	}))
	t.Cleanup(server.Close)

	client := rpcclient.NewClient(server.URL, nil)
	t.Cleanup(func() { _ = client.Close() })
	return client, &reqs
}

func TestGetEventsInRange_SinglePage(t *testing.T) {
	client, reqs := eventsTestServer(t, func(_ protocolrpc.GetEventsRequest) protocolrpc.GetEventsResponse {
		evs := testEventPage(10, 3)
		return protocolrpc.GetEventsResponse{Events: evs, Cursor: evs[2].ID}
	})

	events, err := GetEventsInRange(context.Background(), client, testStellarContractID, 10, 200, []string{"topic"})
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Len(t, *reqs, 1, "a partial page must not trigger another request")
	require.Equal(t, uint32(10), (*reqs)[0].StartLedger)
	require.Equal(t, uint32(200), (*reqs)[0].EndLedger)
	require.NotNil(t, (*reqs)[0].Pagination)
	require.Equal(t, uint(eventsPageLimit), (*reqs)[0].Pagination.Limit)
	require.Nil(t, (*reqs)[0].Pagination.Cursor)
}

func TestGetEventsInRange_PagesWithCursor(t *testing.T) {
	client, reqs := eventsTestServer(t, func(req protocolrpc.GetEventsRequest) protocolrpc.GetEventsResponse {
		if req.Pagination == nil || req.Pagination.Cursor == nil {
			evs := testEventPage(10, eventsPageLimit)
			return protocolrpc.GetEventsResponse{Events: evs, Cursor: evs[len(evs)-1].ID}
		}
		evs := testEventPage(10+eventsPageLimit, 3)
		return protocolrpc.GetEventsResponse{Events: evs, Cursor: evs[len(evs)-1].ID}
	})

	events, err := GetEventsInRange(context.Background(), client, testStellarContractID, 10, 200, []string{"topic"})
	require.NoError(t, err)
	require.Len(t, events, eventsPageLimit+3)

	require.Len(t, *reqs, 2)
	require.Equal(t, uint32(10), (*reqs)[0].StartLedger)
	require.Equal(t, uint32(200), (*reqs)[0].EndLedger)
	require.Nil(t, (*reqs)[0].Pagination.Cursor)

	// Ledger ranges and a cursor cannot be combined; the cursor request must
	// carry neither.
	require.Zero(t, (*reqs)[1].StartLedger)
	require.Zero(t, (*reqs)[1].EndLedger)
	require.NotNil(t, (*reqs)[1].Pagination.Cursor)
	require.Equal(t, uint32(10+eventsPageLimit-1), (*reqs)[1].Pagination.Cursor.Ledger)
}

func TestGetEventsInRange_DedupesInclusiveCursor(t *testing.T) {
	client, reqs := eventsTestServer(t, func(req protocolrpc.GetEventsRequest) protocolrpc.GetEventsResponse {
		if req.Pagination == nil || req.Pagination.Cursor == nil {
			evs := testEventPage(10, eventsPageLimit)
			return protocolrpc.GetEventsResponse{Events: evs, Cursor: evs[len(evs)-1].ID}
		}
		// A server treating the cursor as inclusive repeats the page boundary
		// event; it must not be collected twice.
		last := testEvent(10+eventsPageLimit-1, 0, 0)
		next := testEvent(10+eventsPageLimit, 0, 0)
		return protocolrpc.GetEventsResponse{Events: []protocolrpc.EventInfo{last, next}, Cursor: next.ID}
	})

	events, err := GetEventsInRange(context.Background(), client, testStellarContractID, 10, 200, []string{"topic"})
	require.NoError(t, err)
	require.Len(t, events, eventsPageLimit+1, "the boundary event must appear exactly once")
	require.Len(t, *reqs, 2)
}

func TestGetEventsInRange_StopsAtEndLedger(t *testing.T) {
	client, reqs := eventsTestServer(t, func(req protocolrpc.GetEventsRequest) protocolrpc.GetEventsResponse {
		if req.Pagination == nil || req.Pagination.Cursor == nil {
			evs := testEventPage(10, eventsPageLimit)
			return protocolrpc.GetEventsResponse{Events: evs, Cursor: evs[len(evs)-1].ID}
		}
		// Cursor queries are open-ended: this page runs past the window.
		evs := []protocolrpc.EventInfo{testEvent(110, 0, 0), testEvent(250, 0, 0), testEvent(260, 0, 0)}
		return protocolrpc.GetEventsResponse{Events: evs, Cursor: evs[len(evs)-1].ID}
	})

	events, err := GetEventsInRange(context.Background(), client, testStellarContractID, 10, 200, []string{"topic"})
	require.NoError(t, err)
	require.Len(t, events, eventsPageLimit+1, "events past endLedger must be dropped")
	require.Equal(t, int32(110), events[len(events)-1].Ledger)
	require.Len(t, *reqs, 2, "reaching endLedger must stop pagination")
}

func TestGetEventsInRange_CursorNotAdvancing(t *testing.T) {
	client, _ := eventsTestServer(t, func(_ protocolrpc.GetEventsRequest) protocolrpc.GetEventsResponse {
		evs := testEventPage(10, eventsPageLimit)
		return protocolrpc.GetEventsResponse{Events: evs, Cursor: evs[len(evs)-1].ID}
	})

	_, err := GetEventsInRange(context.Background(), client, testStellarContractID, 10, 200, []string{"topic"})
	require.ErrorContains(t, err, "did not advance")
}
