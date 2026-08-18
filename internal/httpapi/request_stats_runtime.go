package httpapi

import (
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxLatencySamples = 2000
const maxRouteKeysPerMinute = 200
const maxRouteLatencySamples = 400
const maxProxyPluginKeysPerMinute = 128
const maxProxyPluginLatencySamples = 400

type requestRouteSnapshot struct {
	Route     string  `json:"route"`
	Count     int64   `json:"count"`
	Errors5xx int64   `json:"errors_5xx"`
	AvgMs     float64 `json:"avg_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
	MaxMs     float64 `json:"max_ms"`
}

type requestStatsSnapshot struct {
	ReqMin          int64
	ReqHour         int64
	Active          int64
	LatencyAvgMs    float64
	Percentiles     map[string]float64
	History         [10]int64 // last 10 minutes of req counts (idx 0 = -9min, idx 9 = now)
	TopRoutes       []requestRouteSnapshot
	TopProxyPlugins []requestProxyPluginSnapshot
}

type routeMinuteStat struct {
	Count   int64
	Err5xx  int64
	SumMs   float64
	MaxMs   float64
	Samples []float64
}

type requestProxyPluginSnapshot struct {
	Plugin    string  `json:"plugin"`
	Count     int64   `json:"count"`
	Errors5xx int64   `json:"errors_5xx"`
	AvgMs     float64 `json:"avg_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
	MaxMs     float64 `json:"max_ms"`
}

type requestStatsTracker struct {
	mu        sync.Mutex
	counts    map[int64]int64
	latencies map[int64][]float64
	routes    map[int64]map[string]*routeMinuteStat
	proxyPL   map[int64]map[string]*routeMinuteStat
	active    atomic.Int64
}

func newRequestStatsTracker() *requestStatsTracker {
	return &requestStatsTracker{
		counts:    make(map[int64]int64),
		latencies: make(map[int64][]float64),
		routes:    make(map[int64]map[string]*routeMinuteStat),
		proxyPL:   make(map[int64]map[string]*routeMinuteStat),
	}
}

func (t *requestStatsTracker) begin() func(time.Duration) {
	now := time.Now().UTC()
	minuteKey := now.Truncate(time.Minute).Unix()

	t.active.Add(1)
	t.mu.Lock()
	t.counts[minuteKey]++
	t.cleanupLocked(minuteKey)
	t.mu.Unlock()

	return func(elapsed time.Duration) {
		defer t.active.Add(-1)

		ms := float64(elapsed.Microseconds()) / 1000.0
		if ms < 0 || math.IsNaN(ms) || math.IsInf(ms, 0) {
			return
		}

		nowKey := time.Now().UTC().Truncate(time.Minute).Unix()
		t.mu.Lock()
		samples := t.latencies[nowKey]
		if len(samples) < maxLatencySamples {
			t.latencies[nowKey] = append(samples, ms)
		} else {
			samples[rand.Intn(len(samples))] = ms // reservoir sampling
		}
		t.cleanupLocked(nowKey)
		t.mu.Unlock()
	}
}

// longLivedRoutes are endpoints whose duration is intentionally long
// (WebSocket / SSE / pprof traces). Including them in the slow-routes
// leaderboard drowns out real upstream slow-paths — a /nws connection that
// stays open for the user's whole session reports 10-minute "latency" by
// design, not by bug. They're still tracked elsewhere (active count,
// goroutine dump); they just don't get a row in the top-slow table.
var longLivedRoutes = map[string]bool{
	"/nws":               true,
	"/ws":                true,
	"/debug/pprof/trace": true, // CPU profile capture, intentionally slow
}

func (t *requestStatsTracker) observeRoute(route string, status int, elapsed time.Duration) {
	route = strings.TrimSpace(route)
	if route == "" {
		route = "/other"
	}
	if longLivedRoutes[route] {
		return
	}
	ms := float64(elapsed.Microseconds()) / 1000.0
	if ms < 0 || math.IsNaN(ms) || math.IsInf(ms, 0) {
		return
	}

	nowKey := time.Now().UTC().Truncate(time.Minute).Unix()
	t.mu.Lock()
	routeMap := t.routes[nowKey]
	if routeMap == nil {
		routeMap = make(map[string]*routeMinuteStat, 32)
		t.routes[nowKey] = routeMap
	}

	if len(route) > 120 {
		route = route[:120]
	}

	stat, ok := routeMap[route]
	if !ok {
		if len(routeMap) >= maxRouteKeysPerMinute {
			route = "/other"
			stat = routeMap[route]
		}
		if stat == nil {
			stat = &routeMinuteStat{}
			routeMap[route] = stat
		}
	}

	stat.Count++
	observeMinuteStat(stat, status, ms, maxRouteLatencySamples)

	t.cleanupLocked(nowKey)
	t.mu.Unlock()
}

func (t *requestStatsTracker) observeProxyPL(plugin string, status int, elapsed time.Duration) {
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin == "" {
		plugin = "unknown"
	}
	ms := float64(elapsed.Microseconds()) / 1000.0
	if ms < 0 || math.IsNaN(ms) || math.IsInf(ms, 0) {
		return
	}

	nowKey := time.Now().UTC().Truncate(time.Minute).Unix()
	t.mu.Lock()
	pluginMap := t.proxyPL[nowKey]
	if pluginMap == nil {
		pluginMap = make(map[string]*routeMinuteStat, 32)
		t.proxyPL[nowKey] = pluginMap
	}

	if len(plugin) > 64 {
		plugin = plugin[:64]
	}

	stat, ok := pluginMap[plugin]
	if !ok {
		if len(pluginMap) >= maxProxyPluginKeysPerMinute {
			plugin = "other"
			stat = pluginMap[plugin]
		}
		if stat == nil {
			stat = &routeMinuteStat{}
			pluginMap[plugin] = stat
		}
	}

	stat.Count++
	observeMinuteStat(stat, status, ms, maxProxyPluginLatencySamples)
	t.cleanupLocked(nowKey)
	t.mu.Unlock()
}

func (t *requestStatsTracker) snapshot() requestStatsSnapshot {
	now := time.Now().UTC().Truncate(time.Minute)
	key := now.Unix()

	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupLocked(key)

	reqMin := t.counts[key]
	reqHour := int64(0)
	for i := range 60 {
		minKey := now.Add(-time.Duration(i) * time.Minute).Unix()
		reqHour += t.counts[minKey]
	}

	// Collect per-minute counts for last 10 minutes (sparkline data).
	var history [10]int64
	for i := range 10 {
		mk := now.Add(-time.Duration(9-i) * time.Minute).Unix()
		history[i] = t.counts[mk]
	}

	samples := append([]float64(nil), t.latencies[key]...)
	sort.Float64s(samples)

	avg := 0.0
	if len(samples) > 0 {
		sum := 0.0
		for _, v := range samples {
			sum += v
		}
		avg = sum / float64(len(samples))
	}

	topRoutes := summarizeMinuteStats(t.routes[key], 12)
	topProxyPlugins := make([]requestProxyPluginSnapshot, 0, 12)
	for _, row := range summarizeMinuteStats(t.proxyPL[key], 12) {
		topProxyPlugins = append(topProxyPlugins, requestProxyPluginSnapshot{
			Plugin:    row.Route,
			Count:     row.Count,
			Errors5xx: row.Errors5xx,
			AvgMs:     row.AvgMs,
			P95Ms:     row.P95Ms,
			P99Ms:     row.P99Ms,
			MaxMs:     row.MaxMs,
		})
	}

	return requestStatsSnapshot{
		ReqMin:       reqMin,
		ReqHour:      reqHour,
		Active:       t.active.Load(),
		LatencyAvgMs: round2(avg),
		Percentiles: map[string]float64{
			"50": round2(percentile(samples, 50)),
			"75": round2(percentile(samples, 75)),
			"90": round2(percentile(samples, 90)),
			"95": round2(percentile(samples, 95)),
			"99": round2(percentile(samples, 99)),
		},
		History:         history,
		TopRoutes:       topRoutes,
		TopProxyPlugins: topProxyPlugins,
	}
}

func (t *requestStatsTracker) cleanupLocked(currentMinute int64) {
	keepFrom := currentMinute - int64((2*time.Hour)/time.Minute)
	for key := range t.counts {
		if key < keepFrom {
			delete(t.counts, key)
		}
	}
	for key := range t.latencies {
		if key < keepFrom {
			delete(t.latencies, key)
		}
	}
	for key := range t.routes {
		if key < keepFrom {
			delete(t.routes, key)
		}
	}
	for key := range t.proxyPL {
		if key < keepFrom {
			delete(t.proxyPL, key)
		}
	}
}

func observeMinuteStat(stat *routeMinuteStat, status int, ms float64, maxSamples int) {
	stat.SumMs += ms
	if ms > stat.MaxMs {
		stat.MaxMs = ms
	}
	if status >= http.StatusInternalServerError {
		stat.Err5xx++
	}
	if len(stat.Samples) < maxSamples {
		stat.Samples = append(stat.Samples, ms)
	} else {
		stat.Samples[rand.Intn(len(stat.Samples))] = ms
	}
}

func summarizeMinuteStats(items map[string]*routeMinuteStat, limit int) []requestRouteSnapshot {
	if len(items) == 0 {
		return nil
	}

	out := make([]requestRouteSnapshot, 0, len(items))
	for key, stat := range items {
		if stat == nil || stat.Count <= 0 {
			continue
		}

		rs := append([]float64(nil), stat.Samples...)
		sort.Float64s(rs)

		out = append(out, requestRouteSnapshot{
			Route:     key,
			Count:     stat.Count,
			Errors5xx: stat.Err5xx,
			AvgMs:     round2(stat.SumMs / float64(stat.Count)),
			P95Ms:     round2(percentile(rs, 95)),
			P99Ms:     round2(percentile(rs, 99)),
			MaxMs:     round2(stat.MaxMs),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].P95Ms != out[j].P95Ms {
			return out[i].P95Ms > out[j].P95Ms
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].AvgMs > out[j].AvgMs
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	if p <= 0 {
		return samples[0]
	}
	if p >= 100 {
		return samples[len(samples)-1]
	}
	idx := max(int(math.Ceil((p/100.0)*float64(len(samples))))-1, 0)
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

var runtimeRequestStats = newRequestStatsTracker()
