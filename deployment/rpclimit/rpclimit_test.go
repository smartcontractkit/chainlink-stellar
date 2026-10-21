package rpclimit

import (
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testCap is untyped so it flows into both int (strings.Repeat) and int64
// (NewClientWithCap) positions.
const testCap = 4 << 10 // 4 KiB: small enough to exercise cheaply

// requireMaxBytesError asserts err is the overflow error, not a silent
// truncation or a transport failure.
func requireMaxBytesError(t *testing.T, err error) {
	t.Helper()
	var mbe *http.MaxBytesError
	require.ErrorAs(t, err, &mbe)
	require.EqualValues(t, testCap, mbe.Limit)
}

func TestNewClientWiring(t *testing.T) {
	c := NewClient(60 * time.Second)
	require.Equal(t, 60*time.Second, c.Timeout)
	require.NotNil(t, c.Transport, "response cap must be installed as the transport")

	c = NewClientWithCap(time.Second, 1<<20)
	require.Equal(t, time.Second, c.Timeout)
}

func TestTransport_RejectsDeclaredContentLengthOverCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1048576") // over testCap
		w.Write([]byte("x"))
	}))
	defer srv.Close()

	_, err := NewClientWithCap(time.Second, testCap).Get(srv.URL)
	requireMaxBytesError(t, err)
}

// TestTransport_RejectsOversizedLeadingWhitespace is the audit scenario in
// miniature: leading whitespace before a valid JSON body is buffered by
// io.ReadAll just like payload bytes, so it must trip the cap.
func TestTransport_RejectsOversizedLeadingWhitespace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 8 KiB of spaces then a valid JSON-RPC-looking body; no explicit
		// Content-Length so the response is chunked.
		w.Write([]byte(strings.Repeat(" ", 2*testCap)))
		w.Write([]byte(`{"jsonrpc":"2.0","result":{},"id":1}`))
	}))
	defer srv.Close()

	resp, err := NewClientWithCap(time.Second, testCap).Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	requireMaxBytesError(t, err)
}

func TestTransport_RejectsOversizedChunkedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Flushing keeps the response chunked with no Content-Length.
		for i := 0; i < 4; i++ {
			w.Write([]byte(strings.Repeat("a", testCap)))
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	resp, err := NewClientWithCap(time.Second, testCap).Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	requireMaxBytesError(t, err)
}

// TestTransport_CapsDecompressedSize proves the transparent-gzip path is
// bounded at the decompressed size: a body well under the cap compressed
// still trips it once decompressed, because that is what io.ReadAll
// allocates.
func TestTransport_CapsDecompressedSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			t.Error("expected the transport to advertise gzip for a cap on the decompressed size to be exercised")
		}
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		defer zw.Close()
		// 64 KiB of one byte compresses to ~100 bytes: far under testCap
		// compressed, well over it decompressed.
		_, err := zw.Write([]byte(strings.Repeat("a", 16*testCap)))
		require.NoError(t, err)
	}))
	defer srv.Close()

	resp, err := NewClientWithCap(time.Second, testCap).Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	requireMaxBytesError(t, err)
}

func TestTransport_PassesAtAndUnderCap(t *testing.T) {
	const bodyAtCap = testCap
	small := `{"jsonrpc":"2.0","result":{"latestLedger":123},"id":1}`

	for name, body := range map[string]string{
		"exactly at cap": strings.Repeat("a", bodyAtCap),
		"small response": small,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte(body))
			}))
			defer srv.Close()

			resp, err := NewClientWithCap(time.Second, testCap).Get(srv.URL)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			data, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Len(t, data, len(body))
		})
	}
}

func TestTransport_PreservesTransportErrors(t *testing.T) {
	// A closed server surfaces as a transport error, not an overflow error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := NewClientWithCap(time.Second, testCap).Get(url)
	require.Error(t, err)
	var mbe *http.MaxBytesError
	require.False(t, errors.As(err, &mbe))
}
