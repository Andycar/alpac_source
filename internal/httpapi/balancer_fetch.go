package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"lampac-go/internal/balancerstats"

	"github.com/rs/zerolog/log"
)

// globalBalancerStats is the shared metrics manager. Set once via
// SetGlobalBalancerStats during server boot, read from many goroutines
// (every probe, every admin handler). Wrapped in atomic.Pointer so the
// read-side is lock-free AND has a happens-before guarantee with the
// initialising store — plain `var p *T` would rely on Go's loose memory
// model for cross-goroutine visibility, which is technically undefined.
//
// May be nil in early/test contexts — all readers nil-check.
var globalBalancerStats atomic.Pointer[balancerstats.Manager]

// SetGlobalBalancerStats wires the manager so balancerFetch can record metrics.
func SetGlobalBalancerStats(m *balancerstats.Manager) {
	globalBalancerStats.Store(m)
}

// GetGlobalBalancerStats returns the manager (may be nil before server start).
func GetGlobalBalancerStats() *balancerstats.Manager {
	return globalBalancerStats.Load()
}

// recordBalancerAttempt is a thin wrapper that no-ops when the manager is nil.
func recordBalancerAttempt(balancer string, ok bool, latency time.Duration, status int, errMsg, rawURL string) {
	mgr := globalBalancerStats.Load()
	if mgr == nil || balancer == "" {
		return
	}
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Host
	}
	mgr.Record(balancer, ok, latency.Milliseconds(), status, errMsg, host, rawURL)
}

// recordChecksearchAttempt records a checksearch probe outcome separately from
// stream-fetch attempts. The `ok` flag means "balancer responded with a valid
// HTTP status" — NOT "balancer found content for this film". Empty results
// (show=false from a healthy balancer) are still ok=true.
func recordChecksearchAttempt(balancer string, ok bool, latency time.Duration, status int, errMsg, rawURL string) {
	mgr := globalBalancerStats.Load()
	if mgr == nil || balancer == "" {
		return
	}
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Host
	}
	mgr.RecordChecksearch(balancer, ok, latency.Milliseconds(), status, errMsg, host, rawURL)
}

// balancerFetch performs an HTTP request and returns the response body.
// On any error (network, status, read), it logs a debug message with the
// balancer name and URL, then returns nil. This replaces the silent
// "return nil, false" pattern found in 70+ places across balancers.
func balancerFetch(ctx context.Context, client *http.Client, balancer, method, url string, body io.Reader, headers map[string]string, maxBody int64) ([]byte, bool) {
	if maxBody == 0 {
		maxBody = 4 << 20 // 4 MB default
	}

	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		log.Debug().Err(err).Str("balancer", balancer).Msg("balancerFetch: new request failed")
		recordBalancerAttempt(balancer, false, time.Since(start), 0, err.Error(), url)
		return nil, false
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := client.Do(req)
	if err != nil {
		log.Debug().Err(err).Str("balancer", balancer).Str("url", truncURL(url)).Msg("balancerFetch: request failed")
		recordBalancerAttempt(balancer, false, time.Since(start), 0, err.Error(), url)
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("balancer", balancer).Str("url", truncURL(url)).Msg("balancerFetch: bad status")
		recordBalancerAttempt(balancer, false, time.Since(start), resp.StatusCode, http.StatusText(resp.StatusCode), url)
		return nil, false
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		log.Debug().Err(err).Str("balancer", balancer).Msg("balancerFetch: read body failed")
		recordBalancerAttempt(balancer, false, time.Since(start), resp.StatusCode, err.Error(), url)
		return nil, false
	}

	recordBalancerAttempt(balancer, true, time.Since(start), resp.StatusCode, "", url)
	return data, true
}

// balancerGet is a shorthand for GET requests.
func balancerGet(ctx context.Context, client *http.Client, balancer, url string, headers map[string]string) ([]byte, bool) {
	return balancerFetch(ctx, client, balancer, http.MethodGet, url, nil, headers, 4<<20)
}

// truncURL truncates URL for logging (first 120 chars).
func truncURL(u string) string {
	if len(u) > 120 {
		return u[:120] + "…"
	}
	return u
}
