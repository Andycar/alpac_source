package tmdbcache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/httpclient"

	"github.com/rs/zerolog/log"
)

// ErrBulkheadFull is returned by FetchAPI/FetchIMG when the per-kind concurrency
// cap is reached and no slot frees within bulkheadAcquireWait. Callers should
// degrade fast (serve stale/empty/placeholder) rather than block — the whole
// point is to stop a slow upstream from exhausting server resources.
var ErrBulkheadFull = errors.New("tmdbcache: upstream concurrency limit reached")

// bulkheadAcquireWait is how long a fetch will wait for a free slot before
// failing fast. Small enough that a stalled upstream can't build a long queue,
// large enough to smooth brief bursts.
const bulkheadAcquireWait = 500 * time.Millisecond

// upstreamSlowThresholdMs is the latency circuit-breaker: an upstream whose
// moving-average latency exceeds this is skipped in the preferred (first) pass
// even while it still returns 200s, so a chronically-slow-but-not-erroring CDN
// (the "everything got slow" incident) is routed around in favor of a faster
// peer. The periodic healthcheck keeps probing skipped upstreams, so avgLatMs
// recovers and the circuit closes once it speeds up. If ALL upstreams are slow,
// the second pass still tries them (a slow answer beats none).
const upstreamSlowThresholdMs int64 = 4000

// Upstream represents a single TMDB upstream server.
type Upstream struct {
	Name   string // human-readable label ("cub.red", "official", "alcopa")
	APIURL string // e.g. "https://apitmdb.cub.red"
	IMGURL string // e.g. "https://tmdb.cub.watch"

	healthy   atomic.Bool
	avgLatMs  atomic.Int64
	lastCheck atomic.Int64 // unix millis

	// Metrics.
	totalReqs atomic.Int64
	totalErrs atomic.Int64
}

// UpstreamStats holds per-upstream statistics.
type UpstreamStats struct {
	Name      string `json:"name"`
	Healthy   bool   `json:"healthy"`
	AvgLatMs  int64  `json:"avg_lat_ms"`
	TotalReqs int64  `json:"total_reqs"`
	TotalErrs int64  `json:"total_errs"`
}

// Pool manages a list of upstream servers with automatic healthcheck
// and fallback selection.
type Pool struct {
	api          []*Upstream // API upstream chain (ordered by priority)
	img          []*Upstream // IMG upstream chain
	apiKey       string      // injected into API requests
	client       *http.Client
	healthClient *http.Client // separate client with longer timeout for healthchecks
	timeout      time.Duration

	// Bulkheads: bound concurrent upstream fetches per kind so a slow/geo-blocked
	// CDN can't pile up unbounded goroutines/sockets. Buffered channels used as
	// counting semaphores; nil disables (unbounded).
	imgSem chan struct{}
	apiSem chan struct{}

	stopCh chan struct{}
	mu     sync.RWMutex
}

// acquire takes a semaphore slot, honoring ctx and failing fast (ErrBulkheadFull)
// once the brief wait elapses. A nil sem means "unbounded".
func acquire(ctx context.Context, sem chan struct{}) error {
	if sem == nil {
		return nil
	}
	select {
	case sem <- struct{}{}:
		return nil
	default:
	}
	timer := time.NewTimer(bulkheadAcquireWait)
	defer timer.Stop()
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ErrBulkheadFull
	}
}

func release(sem chan struct{}) {
	if sem != nil {
		<-sem
	}
}

// BulkheadStats reports per-kind in-flight vs capacity so the admin TMDB stats
// endpoint can show whether the bulkhead is shedding (in_flight at cap == the
// slow-upstream storm being contained).
func (p *Pool) BulkheadStats() map[string]any {
	return map[string]any{
		"img_in_flight": len(p.imgSem), "img_cap": cap(p.imgSem),
		"api_in_flight": len(p.apiSem), "api_cap": cap(p.apiSem),
	}
}

// SetAPIKeyIfEmpty sets the API key only if one hasn't been configured.
// Used to capture the key from client TMDB proxy requests for server-side features.
func (p *Pool) SetAPIKeyIfEmpty(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.apiKey == "" {
		p.apiKey = key
		log.Info().Msg("tmdb: API key captured from client request")
	}
}

// APIKey returns the current API key (configured or captured from clients).
func (p *Pool) APIKey() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.apiKey
}

// PoolConfig configures the upstream pool.
type PoolConfig struct {
	// CustomAPIHost is the user-configured API host (inserted first if non-empty).
	CustomAPIHost string
	// CustomIMGHost is the user-configured image host (inserted first if non-empty).
	CustomIMGHost string
	// APIKey is the server-side TMDB API key.
	APIKey string
	// TimeoutSec is per-request timeout in seconds (default 8).
	TimeoutSec int
	// MaxConcurrentIMG / MaxConcurrentAPI cap concurrent upstream fetches per kind
	// (bulkhead). <=0 uses the defaults (64 / 48).
	MaxConcurrentIMG int
	MaxConcurrentAPI int
}

// defaultAPIUpstreams are the built-in API upstream chain.
var defaultAPIUpstreams = []struct {
	name, api, img string
}{
	{"cub.red", "https://apitmdb.cub.red", "https://imagetmdb.com"},
	{"official", "https://api.themoviedb.org", "https://image.tmdb.org"},
}

// NewPool creates an upstream pool with healthcheck.
func NewPool(cfg PoolConfig) *Pool {
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}

	maxIMG := cfg.MaxConcurrentIMG
	if maxIMG <= 0 {
		maxIMG = 64
	}
	maxAPI := cfg.MaxConcurrentAPI
	if maxAPI <= 0 {
		maxAPI = 48
	}

	p := &Pool{
		apiKey: strings.TrimSpace(cfg.APIKey),
		// Dynamic: re-resolves the "tmdb" proxy per request, so assigning a SOCKS5/VLESS to the "tmdb"
		// balancer in the admin Proxy panel routes image (imagetmdb.com) + API fetches through it WITHOUT
		// a restart — the cub image CDN is geo-blocked from some prod egress IPs.
		client:       httpclient.NewForBalancerDynamic("tmdb", timeout),
		healthClient: httpclient.NewForBalancerDynamic("tmdb", 12*time.Second),
		timeout:      timeout,
		imgSem:       make(chan struct{}, maxIMG),
		apiSem:       make(chan struct{}, maxAPI),
		stopCh:       make(chan struct{}),
	}

	// Build API chain.
	customAPIHost := normalizeHost(cfg.CustomAPIHost)
	customIMGHost := normalizeHost(cfg.CustomIMGHost)

	// If custom host is one of the defaults, don't duplicate.
	seen := map[string]bool{}

	if customAPIHost != "" {
		u := &Upstream{
			Name:   "custom",
			APIURL: customAPIHost,
			IMGURL: customIMGHost,
		}
		u.healthy.Store(true) // assume healthy initially
		p.api = append(p.api, u)
		seen[customAPIHost] = true

		if customIMGHost != "" {
			p.img = append(p.img, u)
		}
	}

	for _, d := range defaultAPIUpstreams {
		if seen[d.api] {
			continue
		}
		u := &Upstream{
			Name:   d.name,
			APIURL: d.api,
			IMGURL: d.img,
		}
		u.healthy.Store(true)
		p.api = append(p.api, u)
		p.img = append(p.img, u)
	}

	// If custom IMG host is unique, insert it first in img chain.
	if customIMGHost != "" {
		found := false
		for _, u := range p.img {
			if u.IMGURL == customIMGHost {
				found = true
				break
			}
		}
		if !found {
			u := &Upstream{
				Name:   "custom-img",
				IMGURL: customIMGHost,
			}
			u.healthy.Store(true)
			p.img = append([]*Upstream{u}, p.img...)
		}
	}

	return p
}

func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimRight(h, "/")
	if h == "" {
		return ""
	}
	if !strings.Contains(h, "://") {
		h = "https://" + h
	}
	return h
}

// StartHealthcheck begins periodic health monitoring.
func (p *Pool) StartHealthcheck() {
	go p.healthcheckLoop()
}

// Stop signals the healthcheck goroutine to exit.
func (p *Pool) Stop() {
	select {
	case p.stopCh <- struct{}{}:
	default:
	}
}

// ---------- Fetch with fallback ----------

// FetchResult holds the result of an upstream fetch.
type FetchResult struct {
	Body    []byte
	Headers map[string]string
	Status  int
	// NoCache — ответ нельзя класть в кэш: он неполный по нашей вине и скоро
	// станет лучше (например, трейлер для карточки ещё ищется в фоне).
	// Кэшировать его на 2 часа значило бы закрепить пустоту.
	NoCache bool
}

// FetchAPI tries each API upstream in order until one succeeds. Bounded by the
// API bulkhead — returns ErrBulkheadFull fast when the cap is reached.
func (p *Pool) FetchAPI(ctx context.Context, path string, query string) (*FetchResult, error) {
	if err := acquire(ctx, p.apiSem); err != nil {
		return nil, err
	}
	defer release(p.apiSem)
	return p.fetchFromChain(ctx, p.api, path, query, true)
}

// FetchIMG tries each IMG upstream in order until one succeeds. Bounded by the
// image bulkhead — returns ErrBulkheadFull fast when the cap is reached.
func (p *Pool) FetchIMG(ctx context.Context, path string) (*FetchResult, error) {
	if err := acquire(ctx, p.imgSem); err != nil {
		return nil, err
	}
	defer release(p.imgSem)
	return p.fetchFromChain(ctx, p.img, path, "", false)
}

func (p *Pool) fetchFromChain(ctx context.Context, chain []*Upstream, path, query string, isAPI bool) (*FetchResult, error) {
	// First pass: try healthy AND not-chronically-slow upstreams only. The latency
	// circuit-breaker skips a healthy-but-slow upstream so we prefer a faster peer;
	// avgLatMs == 0 (never fetched) is never "slow", so new upstreams get a chance.
	for _, u := range chain {
		if !u.healthy.Load() {
			continue
		}
		if lat := u.avgLatMs.Load(); lat > upstreamSlowThresholdMs {
			continue
		}
		result, err := p.tryUpstream(ctx, u, path, query, isAPI)
		if err == nil {
			return result, nil
		}
		u.totalErrs.Add(1)
		u.healthy.Store(false)
		log.Debug().Err(err).Str("upstream", u.Name).Str("path", path).
			Msg("tmdb: upstream failed, trying next")
	}

	// Second pass: try ALL upstreams (recovery attempt).
	for _, u := range chain {
		result, err := p.tryUpstream(ctx, u, path, query, isAPI)
		if err == nil {
			u.healthy.Store(true)
			return result, nil
		}
		u.totalErrs.Add(1)
	}

	return nil, fmt.Errorf("all %d upstreams failed for %s", len(chain), path)
}

func (p *Pool) tryUpstream(ctx context.Context, u *Upstream, path, query string, isAPI bool) (*FetchResult, error) {
	var target string
	if isAPI {
		target = u.APIURL + "/" + path
	} else {
		if u.IMGURL == "" {
			return nil, fmt.Errorf("upstream %s has no IMG URL", u.Name)
		}
		target = u.IMGURL + "/" + path
	}

	if query != "" {
		target += "?" + query
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	u.totalReqs.Add(1)
	start := time.Now()

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	latMs := time.Since(start).Milliseconds()
	// Exponential moving average for latency.
	old := u.avgLatMs.Load()
	if old == 0 {
		u.avgLatMs.Store(latMs)
	} else {
		u.avgLatMs.Store((old*7 + latMs*3) / 10) // weight: 70% old, 30% new
	}

	if resp.StatusCode >= 400 {
		// Read body to drain connection but ignore.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("upstream %s returned HTTP %d", u.Name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body from %s: %w", u.Name, err)
	}

	headers := make(map[string]string, 5)
	for _, key := range []string{"Content-Type", "Cache-Control", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(key); v != "" {
			headers[key] = v
		}
	}

	return &FetchResult{
		Body:    body,
		Headers: headers,
		Status:  resp.StatusCode,
	}, nil
}

// ---------- Healthcheck ----------

func (p *Pool) healthcheckLoop() {
	// Initial check after 5s.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-timer.C:
			p.runHealthcheck()
			timer.Reset(30 * time.Second)
		}
	}
}

func (p *Pool) runHealthcheck() {
	// Check all unique upstreams.
	checked := map[string]bool{}

	all := make([]*Upstream, 0, len(p.api)+len(p.img))
	all = append(all, p.api...)
	all = append(all, p.img...)

	for _, u := range all {
		if checked[u.Name] {
			continue
		}
		checked[u.Name] = true

		wasHealthy := u.healthy.Load()

		// Check API if available.
		if u.APIURL != "" {
			ok := p.checkAPIHealth(u)
			u.healthy.Store(ok)
			if ok != wasHealthy {
				log.Info().Str("upstream", u.Name).Bool("healthy", ok).
					Msg("tmdb: upstream health changed")
			}
		} else if u.IMGURL != "" {
			// IMG-only upstream.
			ok := p.checkIMGHealth(u)
			u.healthy.Store(ok)
			if ok != wasHealthy {
				log.Info().Str("upstream", u.Name).Bool("healthy", ok).
					Msg("tmdb: img upstream health changed")
			}
		}

		u.lastCheck.Store(time.Now().UnixMilli())
	}
}

func (p *Pool) checkAPIHealth(u *Upstream) bool {
	target := u.APIURL + "/3/configuration"
	if p.apiKey != "" {
		target += "?api_key=" + p.apiKey
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false
	}

	start := time.Now()
	resp, err := p.healthClient.Do(req)
	if err != nil {
		log.Debug().Err(err).Str("upstream", u.Name).Msg("tmdb: healthcheck failed")
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	latMs := time.Since(start).Milliseconds()
	old := u.avgLatMs.Load()
	if old == 0 {
		u.avgLatMs.Store(latMs)
	} else {
		u.avgLatMs.Store((old*7 + latMs*3) / 10)
	}

	return resp.StatusCode == http.StatusOK
}

func (p *Pool) checkIMGHealth(u *Upstream) bool {
	// HEAD request for a well-known small image.
	target := u.IMGURL + "/t/p/w92/wwemzKWzjKYJFfCeiB57q3r4Bcm.png"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return false
	}

	resp, err := p.healthClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	// 200, 301, 302 are all acceptable for images.
	return resp.StatusCode < 400
}

// ---------- Metrics ----------

// UpstreamStatsList returns stats for all upstreams.
func (p *Pool) UpstreamStatsList() []UpstreamStats {
	seen := map[string]bool{}
	var result []UpstreamStats

	all := make([]*Upstream, 0, len(p.api)+len(p.img))
	all = append(all, p.api...)
	all = append(all, p.img...)

	for _, u := range all {
		if seen[u.Name] {
			continue
		}
		seen[u.Name] = true
		result = append(result, UpstreamStats{
			Name:      u.Name,
			Healthy:   u.healthy.Load(),
			AvgLatMs:  u.avgLatMs.Load(),
			TotalReqs: u.totalReqs.Load(),
			TotalErrs: u.totalErrs.Load(),
		})
	}
	return result
}
