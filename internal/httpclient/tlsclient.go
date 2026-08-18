// Package httpclient — tls-client transport for full Chrome HTTP/2 fingerprinting.
//
// Unlike the uTLS-based transport (utls.go), which only spoofs the TLS ClientHello
// but uses Go's default HTTP/2 framing, tls-client (bogdanfinn/tls-client) also
// spoofs the HTTP/2 fingerprint: pseudo-header order, SETTINGS frame values
// (INITIAL_WINDOW_SIZE, HEADER_TABLE_SIZE), PRIORITY frames, and WINDOW_UPDATE
// patterns. This makes requests indistinguishable from real Chrome to CDNs that
// fingerprint both TLS and HTTP/2 layers (e.g. stream-balancer-allo-1.live).
//
// tls-client uses bogdanfinn/fhttp (a fork of net/http) internally, so we need
// to convert between net/http and fhttp request/response types.
package httpclient

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// tlsClientTransport wraps a tls-client HttpClient as an http.RoundTripper.
// Converts between net/http and fhttp types transparently.
type tlsClientTransport struct {
	client tls_client.HttpClient
}

// RoundTrip implements http.RoundTripper.
func (t *tlsClientTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Convert net/http.Request → fhttp.Request
	fReq, err := fhttp.NewRequest(req.Method, req.URL.String(), req.Body)
	if err != nil {
		return nil, err
	}

	// Copy headers (net/http.Header → fhttp.Header).
	for k, vs := range req.Header {
		for _, v := range vs {
			fReq.Header.Add(k, v)
		}
	}
	// Preserve lowercase header keys (e.g. "pc_hash") that bypass Go's canonicalization.
	for k, vs := range req.Header {
		if k != http.CanonicalHeaderKey(k) {
			fReq.Header[k] = vs
		}
	}
	fReq.Host = req.Host

	// Execute via tls-client (Chrome TLS + HTTP/2 fingerprint).
	fResp, err := t.client.Do(fReq)
	if err != nil {
		return nil, err
	}

	// Convert fhttp.Response → net/http.Response
	resp := &http.Response{
		Status:        fResp.Status,
		StatusCode:    fResp.StatusCode,
		Proto:         fResp.Proto,
		ProtoMajor:    fResp.ProtoMajor,
		ProtoMinor:    fResp.ProtoMinor,
		Body:          fResp.Body,
		ContentLength: fResp.ContentLength,
		Uncompressed:  fResp.Uncompressed,
		Request:       req,
	}

	// Convert headers back.
	resp.Header = make(http.Header, len(fResp.Header))
	for k, vs := range fResp.Header {
		resp.Header[k] = vs
	}

	// Transfer-Encoding
	resp.TransferEncoding = fResp.TransferEncoding

	// Trailer
	if len(fResp.Trailer) > 0 {
		resp.Trailer = make(http.Header, len(fResp.Trailer))
		for k, vs := range fResp.Trailer {
			resp.Trailer[k] = vs
		}
	}

	// Content-Length from header if not set
	if resp.ContentLength <= 0 {
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
				resp.ContentLength = n
			}
		}
	}

	return resp, nil
}

// newTLSClientTransport creates a tls-client based transport with Chrome 133 profile.
// socksAddr may be empty for direct connections.
func newTLSClientTransport(socksAddr string, timeoutSec int) (*tlsClientTransport, error) {
	opts := []tls_client.HttpClientOption{
		tls_client.WithClientProfile(profiles.Chrome_133),
		tls_client.WithInsecureSkipVerify(),
		tls_client.WithTimeoutSeconds(timeoutSec),
		tls_client.WithNotFollowRedirects(),
	}

	if socksAddr != "" {
		opts = append(opts, tls_client.WithProxyUrl(fmt.Sprintf("socks5://%s", socksAddr)))
	}

	client, err := tls_client.NewHttpClient(nil, opts...)
	if err != nil {
		return nil, fmt.Errorf("tls-client: %w", err)
	}

	return &tlsClientTransport{client: client}, nil
}

// NewTLSClientDirect creates an *http.Client with full Chrome HTTP/2 fingerprint
// via tls-client. No proxy. Uses the given timeout.
func NewTLSClientDirect(timeout time.Duration) *http.Client {
	t, err := newTLSClientTransport("", int(timeout.Seconds()))
	if err != nil {
		return NewUTLS(timeout)
	}
	return &http.Client{Transport: t}
}

// NewTLSClientViaSOCKS5 creates an *http.Client with full Chrome HTTP/2
// fingerprint routed through a SOCKS5 proxy.
func NewTLSClientViaSOCKS5(socksAddr string, timeout time.Duration) *http.Client {
	t, err := newTLSClientTransport(socksAddr, int(timeout.Seconds()))
	if err != nil {
		return NewUTLSViaSOCKS5(socksAddr, timeout)
	}
	return &http.Client{Transport: t}
}

// NewTLSClientViaSOCKS5NoTimeout creates an *http.Client with full Chrome
// HTTP/2 fingerprint routed through SOCKS5, with no request timeout.
// Used for streaming large 4K segments (.ts/.m4s).
func NewTLSClientViaSOCKS5NoTimeout(socksAddr string) *http.Client {
	t, err := newTLSClientTransport(socksAddr, 0)
	if err != nil {
		return NewUTLSViaSOCKS5NoTimeout(socksAddr)
	}
	return &http.Client{Transport: t}
}

// NewTLSClientDirectNoTimeout creates an *http.Client with full Chrome HTTP/2
// fingerprint, no proxy, no timeout.
func NewTLSClientDirectNoTimeout() *http.Client {
	t, err := newTLSClientTransport("", 0)
	if err != nil {
		return &http.Client{Transport: UTLSTransport133}
	}
	return &http.Client{Transport: t}
}

// Ensure tlsClientTransport satisfies http.RoundTripper at compile time.
var _ http.RoundTripper = (*tlsClientTransport)(nil)

// Suppress unused import warning for io (used for body types).
var _ = io.EOF
