package chain

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The full matrix (declared Content-Length, chunked, transparent gzip at
// decompressed size, exactly-at-cap) is covered by deployment/rpclimit's
// tests; this exercises the relayer's duplicate end-to-end through
// newLimitedHTTPClient, the way multinode_client.go uses it.

func TestNewLimitedHTTPClient_RejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Well over the 32 MiB cap, streamed in chunks so no Content-Length.
		const chunk = 1 << 20
		for i := 0; i < 40; i++ {
			w.Write([]byte(strings.Repeat("a", chunk)))
		}
	}))
	defer srv.Close()

	client := newLimitedHTTPClient(time.Second)
	require.Equal(t, time.Second, client.Timeout)

	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	var mbe *http.MaxBytesError
	require.ErrorAs(t, err, &mbe)
	require.EqualValues(t, maxRPCResponseBytes, mbe.Limit)
}

func TestNewLimitedHTTPClient_PassesNormalResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","result":{"latestLedger":123},"id":1}`))
	}))
	defer srv.Close()

	resp, err := newLimitedHTTPClient(time.Second).Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(data), "latestLedger")
}
