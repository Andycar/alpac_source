package httpapi

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"lampac-go/internal/balancerhealth"
	"lampac-go/internal/browser"
	"lampac-go/internal/config"
	"lampac-go/internal/litesrc"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"
)

var serverStartTime = time.Now()

// globalYTChecker is set by liteSourceHandler when the YouTube checker is
// created, and its mutable fields (ytAPI, tgStore, ytdlpPath) are later
// overwritten from server bootstrap (see server.go after the pool starts).
// Reads come from admin handlers running on chi's request goroutines, so
// the assignment and later field writes need atomic publication.
//
// Wrap the pointer in atomic.Pointer; the inner struct is treated as
// effectively immutable once published — fields are set ONCE during boot,
// before any admin request can race with them. If you need to mutate after
// publication, replace the whole struct, don't poke fields in place.
func tgAdminStatsHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, pl *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// X-Cluster-Key вместо входа админа: тот же внутренний доступ, что уже
		// открывает /lite/* для запросов внутри кластера. Нужен, чтобы снимать
		// показания по источникам скриптом, не заводя человеку сессию в панели.
		if !isTrustedClusterRequest(r) {
			if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
				return
			}
		}

		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		reqSnap := runtimeRequestStats.snapshot()

		proxyLinkEntries := 0
		if pl != nil {
			proxyLinkEntries = pl.Len()
		}

		uptime := time.Since(serverStartTime).Seconds()

		// --- Processes section ---
		processes := map[string]any{}

		// Proxy sidecars (xray / mihomo)
		if pp := liveProxyPool(); pp != nil {
			processes["proxy"] = pp.Status()
			if f := pp.Failures(); len(f) > 0 {
				processes["proxy_failed"] = f
			}
		} else {
			processes["proxy"] = []any{}
		}

		// TorrServer (check if alive — in-process, local or external)
		tsInfo := map[string]any{"configured": false}
		if torrsIsInProcess() {
			// In-process torrent server (built with -tags torrs)
			tsInfo["configured"] = true
			tsInfo["alive"] = true
			tsInfo["inprocess"] = true
			srv := getTorrsServer()
			if srv != nil {
				st := srv.GetSettings()
				list := srv.List()
				tsInfo["cache_size"] = st.CacheSize
				tsInfo["preload_size"] = st.PreloadSize
				tsInfo["active_torrents"] = len(list)
			}
		} else if serverReady() {
			tsCfg := liveConfig(config.Config{}).TorrServer
			if tsCfg.URL != "" {
				// External TorrServer — HTTP health check via /echo
				tsInfo["configured"] = true
				tsInfo["url"] = tsCfg.URL
				tsInfo["alive"] = checkTSHealth(tsCfg.URL)
				tsInfo["external"] = true
			} else if tsCfg.Port > 0 {
				// Local TorrServer — TCP port check
				tsInfo["configured"] = true
				tsInfo["port"] = tsCfg.Port
				tsInfo["alive"] = checkTCPPort("127.0.0.1", tsCfg.Port)
			}
		}
		processes["torrserver"] = tsInfo

		// YouTube / yt-dlp / ffmpeg
		ytInfo := map[string]any{"available": false}
		if yt := litesrc.GetGlobalYTChecker(); yt != nil {
			if info := yt.StatsInfo(); info != nil {
				ytInfo = info
			}
		}
		processes["ytdlp"] = ytInfo

		// --- Transcoding ---
		transcodingInfo := map[string]any{"enabled": false}
		if ts := liveTransSvc(); ts != nil {
			transcodingInfo = ts.StatsSnapshot()
		}

		// --- Custom balancers ---
		var customBalancers []any
		if cbp := liveCustBalPool(); cbp != nil {
			for _, st := range cbp.List() {
				customBalancers = append(customBalancers, st)
			}
		}

		// --- Chrome (chromedp) ---
		chromeInfo := chromeDPStats()

		// --- OS info ---
		hostname, _ := os.Hostname()
		osInfo := map[string]any{
			"hostname": hostname,
			"goos":     runtime.GOOS,
			"goarch":   runtime.GOARCH,
			"pid":      os.Getpid(),
		}

		// --- Per-process OS stats (Linux /proc) ---
		processStats := collectProcessOSStats()

		writeJSON(w, http.StatusOK, map[string]any{
			"mem": map[string]any{
				"alloc_mb":         round2(float64(m.Alloc) / 1048576),
				"total_alloc_mb":   round2(float64(m.TotalAlloc) / 1048576),
				"sys_mb":           round2(float64(m.Sys) / 1048576),
				"heap_alloc_mb":    round2(float64(m.HeapAlloc) / 1048576),
				"heap_inuse_mb":    round2(float64(m.HeapInuse) / 1048576),
				"heap_idle_mb":     round2(float64(m.HeapIdle) / 1048576),
				"heap_released_mb": round2(float64(m.HeapReleased) / 1048576),
				"stack_inuse_mb":   round2(float64(m.StackInuse) / 1048576),
				"heap_objects":     m.HeapObjects,
				"gc_count":         m.NumGC,
				"gc_pause_ms":      round2(float64(m.PauseTotalNs) / 1e6),
			},
			"goroutines":        runtime.NumGoroutine(),
			"uptime_sec":        round2(uptime),
			"proxylink_entries": proxyLinkEntries,
			"requests": map[string]any{
				"req_min":           reqSnap.ReqMin,
				"req_hour":          reqSnap.ReqHour,
				"active":            reqSnap.Active,
				"latency_avg":       reqSnap.LatencyAvgMs,
				"percentiles":       reqSnap.Percentiles,
				"req_history":       reqSnap.History,
				"top_routes":        reqSnap.TopRoutes,
				"top_proxy_plugins": reqSnap.TopProxyPlugins,
			},
			"app_version":      liveVersion(),
			"go_version":       runtime.Version(),
			"num_cpu":          runtime.NumCPU(),
			"processes":        processes,
			"transcoding":      transcodingInfo,
			"custom_balancers": customBalancers,
			"chrome":           chromeInfo,
			"os":               osInfo,
			"process_stats":    processStats,
		})
	}
}

func tgAdminDashboardHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, pl *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// См. tgAdminStatsHandler: ключ кластера — тот же внутренний доступ.
		if !isTrustedClusterRequest(r) {
			if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
				return
			}
		}

		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		reqSnap := runtimeRequestStats.snapshot()
		trafficSnap := runtimeTrafficStats.Snapshot()

		var healthSnap map[string]balancerhealth.HealthStatus
		if hc := balancerhealth.GetGlobalHealthChecker(); hc != nil {
			healthSnap = hc.Snapshot()
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"uptime_sec": round2(time.Since(serverStartTime).Seconds()),
			"mem_mb":     round2(float64(m.Alloc) / 1048576),
			"goroutines": runtime.NumGoroutine(),
			"num_cpu":    runtime.NumCPU(),
			"version":    publicVersionText(),
			"requests": map[string]any{
				"req_min":           reqSnap.ReqMin,
				"req_hour":          reqSnap.ReqHour,
				"active":            reqSnap.Active,
				"latency_avg":       reqSnap.LatencyAvgMs,
				"percentiles":       reqSnap.Percentiles,
				"top_routes":        reqSnap.TopRoutes,
				"top_proxy_plugins": reqSnap.TopProxyPlugins,
			},
			"traffic": trafficSnap,
			"health":  healthSnap,
		})
	}
}

// checkTCPPort checks if a TCP port is accepting connections.
func checkTCPPort(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// checkTSHealth checks if an external TorrServer is alive by calling GET /echo
// with Basic-auth credentials from config. Response should start with "MatriX".
func checkTSHealth(baseURL string) bool {
	proxy := tsProxyPtr.Load()
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/echo", nil)
	if err != nil {
		return false
	}
	if proxy != nil && proxy.authHdr != "" {
		req.Header.Set("Authorization", proxy.authHdr)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	return strings.Contains(string(buf[:n]), "MatriX")
}

// checkTSHealthWithAuth checks TorrServer connectivity with explicit credentials.
// Used by the admin panel "test connection" feature.
func checkTSHealthWithAuth(baseURL, login, password string) (bool, string) {
	target := strings.TrimRight(baseURL, "/") + "/echo"
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return false, "bad url: " + err.Error()
	}
	if password != "" {
		if login == "" {
			login = "ts"
		}
		cred := base64.StdEncoding.EncodeToString([]byte(login + ":" + password))
		req.Header.Set("Authorization", "Basic "+cred)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, "connection failed: " + err.Error()
	}
	defer resp.Body.Close()
	buf := make([]byte, 128)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if resp.StatusCode == http.StatusUnauthorized {
		return false, "auth failed (401) — wrong login/password"
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "MatriX") {
		return false, "unexpected response: " + body
	}
	return true, body
}

// chromeDPStats returns headless Chrome status information.
func chromeDPStats() map[string]any {
	active, limit := litesrc.MirageBrowserStats()
	return map[string]any{
		"allocator_active":   litesrc.MirageAllocatorActive(),
		"max_concurrent":     limit,
		"active_sessions":    active,
		"available_slots":    limit - active,
		"stream_cache_max":   litesrc.GetMirageStreamCacheMax(),
		"stream_cache_ttl_h": litesrc.GetMirageStreamCacheTTLHours(),
	}
}

// collectProcessOSStats gathers /proc stats for all known PIDs.
func collectProcessOSStats() map[string]ProcessOSStats {
	pids := map[int]string{} // pid -> label

	// Self
	pids[os.Getpid()] = "lampac-go"

	// Proxy sidecars
	if pp := liveProxyPool(); pp != nil {
		for _, ps := range pp.Status() {
			if ps.PID > 0 {
				label := ps.Label
				if label == "" {
					label = "proxy"
				}
				pids[ps.PID] = "proxy:" + label
			}
		}
	}

	// Custom balancers
	if cbp := liveCustBalPool(); cbp != nil {
		for _, cb := range cbp.List() {
			if cb.PID > 0 {
				pids[cb.PID] = "custbal:" + cb.Name
			}
		}
	}

	// Transcoding jobs — StatsSnapshot returns map with "jobs" key
	if ts := liveTransSvc(); ts != nil {
		snap := ts.StatsSnapshot()
		if jobs, ok := snap["jobs"].([]map[string]any); ok {
			for _, j := range jobs {
				if pid, ok := j["pid"].(int); ok && pid > 0 {
					sid, _ := j["stream_id"].(string)
					pids[pid] = "ffmpeg:" + sid
				}
			}
		}
	}

	result := make(map[string]ProcessOSStats)
	for pid, label := range pids {
		if stats, ok := readProcessStats(pid); ok {
			stats.PID = pid
			result[label] = stats
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Transcoding admin endpoints
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Browser Pool settings admin endpoint
// ---------------------------------------------------------------------------

// browserCapableBalancers is the list of balancer scrapers that use a
// headless browser and can therefore be retargeted to a non-default
// engine via the admin UI.
//
// "rezka" is intentionally absent: RedHeadSound has been wired to
// chromedp directly since the DDoS-Guard bypass and the team prefers
// to keep it that way — no override knob until a concrete reason
// to expose it appears.
var browserCapableBalancers = []string{
	"mirage", "kinobase", "alloha", "kinogo",
	"turbo", "vibix", "zetflix", "zona",
}

// browserEngineInfo is the snapshot returned in the GET/POST response.
type browserEngineInfo struct {
	engine          string
	balancerEngines map[string]string
	available       []string
	status          map[string]string // engineName → "" if OK or error text
}

func collectBrowserEngineInfo() browserEngineInfo {
	var info browserEngineInfo
	if def := browser.Default(); def != nil {
		info.engine = def.Name()
	}
	info.available = browser.List()
	info.status = make(map[string]string, len(info.available))
	for _, name := range info.available {
		e, err := browser.Get(name)
		if err != nil {
			info.status[name] = err.Error()
			continue
		}
		if err := e.Available(); err != nil {
			info.status[name] = err.Error()
		} else {
			info.status[name] = ""
		}
	}
	// Snapshot per-balancer overrides by asking the registry which
	// engine each balancer currently resolves to, compared to default.
	info.balancerEngines = map[string]string{}
	defName := info.engine
	for _, bal := range browserCapableBalancers {
		if eng := browser.ForBalancer(bal); eng != nil && eng.Name() != defName {
			info.balancerEngines[bal] = eng.Name()
		}
	}
	return info
}
