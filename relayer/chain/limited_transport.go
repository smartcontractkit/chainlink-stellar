package chain

import (
	"net/http"
	"time"
)

// maxRPCResponseBytes caps one Soroban RPC response. 32 MiB sits well above
// every legitimate response (getEvents/getLedgerEntries pages,
// simulateTransaction with restore data) while bounding a hostile endpoint's
// per-request allocation. The Stellar SDK's rpcclient buffers whole response
// bodies via jhttp's io.ReadAll, and jrpc2 v1.2.0 has no body-size option, so
// the bound has to come from the HTTP transport.
//
// This duplicates deployment/rpclimit (same cap, same semantics) because the
// relayer deliberately does not depend on the deployment module.
const maxRPCResponseBytes int64 = 32 << 20

// newLimitedHTTPClient returns the Soroban RPC HTTP client: the given timeout
// bounds total request duration (body read included), and responses larger
// than maxRPCResponseBytes are rejected — over-cap Content-Length up front,
// everything else (chunked, transparently decompressed gzip) via
// http.MaxBytesReader while reading, so overflow surfaces as an
// *http.MaxBytesError instead of a silent truncation.
func newLimitedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &limitedTransport{base: http.DefaultTransport, max: maxRPCResponseBytes},
	}
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
	if resp.ContentLength > t.max {
		resp.Body.Close()
		return nil, &http.MaxBytesError{Limit: t.max}
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, t.max)
	return resp, nil
}
