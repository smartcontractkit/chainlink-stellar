// Package rpclimit bounds the size of Soroban RPC HTTP responses.
//
// The Stellar SDK's rpcclient delegates to creachadair/jrpc2/jhttp, whose
// Recv buffers the whole response body with io.ReadAll before any JSON
// parsing starts, and jrpc2 v1.2.0 exposes no body-size option. A
// misconfigured or compromised RPC endpoint can therefore drive allocations
// bounded only by what it can deliver within the HTTP client's timeout. The
// RoundTripper here caps response bytes instead: responses that declare an
// over-cap Content-Length are rejected before a byte is read, and every body
// is wrapped in http.MaxBytesReader so chunked and transparently decompressed
// (gzip) responses are capped at the decompressed size — the size that
// actually gets allocated. Overflow surfaces as an *http.MaxBytesError from
// the body read, never as a silent truncation.
package rpclimit

import (
	"net/http"
	"time"
)

// DefaultMaxResponseBytes caps one Soroban RPC response. 32 MiB sits well
// above every legitimate response through this client (getEvents and
// getLedgerEntries pages, simulateTransaction with restore data are all far
// smaller) while bounding a hostile endpoint's per-request allocation.
const DefaultMaxResponseBytes int64 = 32 << 20

// NewClient returns an HTTP client for Soroban RPC with the given timeout
// (which still bounds the total request duration, body read included) and
// responses capped at DefaultMaxResponseBytes.
func NewClient(timeout time.Duration) *http.Client {
	return NewClientWithCap(timeout, DefaultMaxResponseBytes)
}

// NewClientWithCap is NewClient with an explicit response cap, which must be
// positive: a non-positive cap rejects every non-empty body (the stdlib
// treats a negative limit as zero).
func NewClientWithCap(timeout time.Duration, maxResponseBytes int64) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: NewTransport(nil, maxResponseBytes),
	}
}

// NewTransport returns an http.RoundTripper wrapping base (http.DefaultTransport
// when nil) that rejects responses larger than maxResponseBytes, which must be
// positive. Requests are not limited: request bodies are generated locally and
// bounded by construction.
func NewTransport(base http.RoundTripper, maxResponseBytes int64) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &limitedTransport{base: base, max: maxResponseBytes}
}

type limitedTransport struct {
	base http.RoundTripper
	max  int64
}

func (t *limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	// Identity responses that declare an over-cap Content-Length are rejected
	// up front, without reading the body. Chunked and transparently
	// decompressed responses carry ContentLength -1 and are bounded by the
	// wrapped body reader below.
	if resp.ContentLength > t.max {
		resp.Body.Close()
		return nil, &http.MaxBytesError{Limit: t.max}
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, t.max)
	return resp, nil
}
