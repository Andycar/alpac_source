package tmdbcache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestCircuitBreaker_SkipsSlowUpstream verifies the latency circuit-breaker: a
// healthy-but-chronically-slow upstream (avgLatMs over the threshold) is skipped
// in favor of a faster peer, even though it would still answer 200.
func TestCircuitBreaker_SkipsSlowUpstream(t *testing.T) {
	var slowHits, fastHits atomic.Int32
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowHits.Add(1)
		_, _ = w.Write([]byte("slow"))
	}))
	defer slowSrv.Close()
	fastSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fastHits.Add(1)
		_, _ = w.Write([]byte("fast"))
	}))
	defer fastSrv.Close()

	slow := &Upstream{Name: "slow", APIURL: slowSrv.URL}
	slow.healthy.Store(true)
	slow.avgLatMs.Store(upstreamSlowThresholdMs + 5000) // over the circuit-breaker threshold

	fast := &Upstream{Name: "fast", APIURL: fastSrv.URL}
	fast.healthy.Store(true)
	fast.avgLatMs.Store(100)

	// slow is first in the chain (higher priority) — the breaker must still route past it.
	p := &Pool{
		api:     []*Upstream{slow, fast},
		client:  &http.Client{Timeout: 5 * time.Second},
		timeout: 5 * time.Second,
	}

	res, err := p.FetchAPI(context.Background(), "3/movie/1", "")
	if err != nil {
		t.Fatalf("FetchAPI: %v", err)
	}
	if string(res.Body) != "fast" {
		t.Fatalf("served %q, want fast (slow upstream should be skipped)", res.Body)
	}
	if got := slowHits.Load(); got != 0 {
		t.Errorf("slow upstream hit %d times in first pass, want 0 (circuit open)", got)
	}
	if got := fastHits.Load(); got != 1 {
		t.Errorf("fast upstream hit %d times, want 1", got)
	}
}

// TestCircuitBreaker_FallsBackWhenAllSlow verifies that if every upstream is over
// the threshold, the second pass still tries them (a slow answer beats none).
func TestCircuitBreaker_FallsBackWhenAllSlow(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	u := &Upstream{Name: "only", APIURL: srv.URL}
	u.healthy.Store(true)
	u.avgLatMs.Store(upstreamSlowThresholdMs + 5000) // slow — skipped in first pass

	p := &Pool{api: []*Upstream{u}, client: &http.Client{Timeout: 5 * time.Second}, timeout: 5 * time.Second}
	res, err := p.FetchAPI(context.Background(), "3/movie/1", "")
	if err != nil {
		t.Fatalf("FetchAPI: %v", err)
	}
	if string(res.Body) != "ok" || hits.Load() != 1 {
		t.Fatalf("expected second-pass fallback to hit the sole slow upstream once; body=%q hits=%d", res.Body, hits.Load())
	}
}
