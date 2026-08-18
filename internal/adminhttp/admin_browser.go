package adminhttp

import (
	"net/http"
	"strings"

	"lampac-go/internal/browser"
	"lampac-go/internal/tgauth"
)

// admin_browser.go — headless-browser admin handlers split out of the host's
// admin_stats.go grab-bag. tgAdminBrowserInstallHandler is self-contained (only
// the external `browser` package). The browser-pool settings handler stays in
// httpapi for now — it reads the internal browserEngineInfo + Mirage* runtime
// settings.

// tgAdminBrowserPoolHandler handles GET/POST for browser-pool runtime settings.
// The state payload + engine-persist (which touch the internal browserEngineInfo
// + Mirage* runtime settings) are injected; the external `browser` registry
// validation stays inline.
func tgAdminBrowserPoolHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore)
		if !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}

		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, browserPoolPayload())
			return
		}

		// POST: update settings.
		var req struct {
			MaxConcurrent   *int               `json:"max_concurrent"`
			StreamCacheMax  *int               `json:"stream_cache_max"`
			StreamCacheTTLH *int               `json:"stream_cache_ttl_h"`
			Engine          *string            `json:"engine"`
			BalancerEngines *map[string]string `json:"balancer_engines"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
			return
		}

		if req.MaxConcurrent != nil {
			setMirageBrowserLimit(min(max(*req.MaxConcurrent, 1), 64))
		}
		if req.StreamCacheMax != nil {
			setMirageStreamCacheMax(*req.StreamCacheMax)
		}
		if req.StreamCacheTTLH != nil {
			setMirageStreamCacheTTL(*req.StreamCacheTTLH)
		}

		// Engine selection: validate against the registry first.
		var (
			engineToApply string
			overrideMap   map[string]string
			engineTouched bool
		)
		if req.Engine != nil {
			name := strings.TrimSpace(*req.Engine)
			if name != "" {
				if _, err := browser.Get(name); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{
						"error":             "unknown engine: " + name,
						"available_engines": browser.List(),
					})
					return
				}
			}
			engineToApply = name
			engineTouched = true
		}
		if req.BalancerEngines != nil {
			overrideMap = make(map[string]string, len(*req.BalancerEngines))
			for k, v := range *req.BalancerEngines {
				bal := strings.TrimSpace(k)
				eng := strings.TrimSpace(v)
				if bal == "" || eng == "" {
					continue
				}
				if _, err := browser.Get(eng); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{
						"error":             "unknown engine for balancer " + bal + ": " + eng,
						"available_engines": browser.List(),
					})
					return
				}
				overrideMap[bal] = eng
			}
			engineTouched = true
		}

		if engineTouched {
			if req.Engine != nil {
				browser.SetDefault(engineToApply)
			}
			if req.BalancerEngines != nil {
				browser.SetBalancerOverrides(overrideMap)
			}
			if st, msg := browserEnginePersist(); msg != "" {
				writeJSON(w, st, map[string]any{"error": msg})
				return
			}
		}

		p := browserPoolPayload()
		p["ok"] = true
		writeJSON(w, http.StatusOK, p)
	}
}

// tgAdminBrowserInstallHandler triggers a browser-engine install (engines that
// implement browser.Installer — currently playwright, which fetches its Node
// driver + Chromium on first use). Blocks until install completes (by design —
// the admin accepts a long wait when clicking the button).
func tgAdminBrowserInstallHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore)
		if !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}
		var req struct {
			Engine string `json:"engine"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
			return
		}
		name := strings.TrimSpace(req.Engine)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "engine name required"})
			return
		}
		eng, err := browser.Get(name)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"error":             "engine not compiled into this binary",
				"available_engines": browser.List(),
			})
			return
		}
		inst, ok := eng.(browser.Installer)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "engine " + name + " has no install action — install its dependency manually",
			})
			return
		}
		if err := inst.EnsureInstalled(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"engine": name,
			"status": eng.Available(),
		})
	}
}
