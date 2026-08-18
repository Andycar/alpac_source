package adminhttp

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/proxycore"
	"lampac-go/internal/sidecar"
	"lampac-go/internal/tgauth"
)

// tgAdminProxycoreHandler handles the /api/proxycore/* endpoints for the
// built-in proxy engine admin panel.
func tgAdminProxycoreHandler(
	store *tgauth.Store,
	adminStore *tgauth.AdminIDStore,
	getPool func() *proxycore.Pool,
	reloadFn func() error,
	binDir string,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		// Route: /api/proxycore/{sub}
		// Determine the sub-path after "/api/proxycore".
		path := r.URL.Path
		sub := ""
		if idx := strings.Index(path, "/api/proxycore"); idx >= 0 {
			sub = strings.TrimPrefix(path[idx:], "/api/proxycore")
			sub = strings.TrimPrefix(sub, "/")
		}

		switch {
		case sub == "" || sub == "status":
			proxycoreStatusHandler(w, r, getPool)
		case sub == "entries":
			proxycoreEntriesHandler(w, r, getPool, reloadFn)
		case sub == "rules":
			proxycoreRulesHandler(w, r, getPool, reloadFn)
		case sub == "test":
			proxycoreTestHandler(w, r)
		case sub == "reload":
			proxycoreReloadHandler(w, r, reloadFn)
		case sub == "logs":
			proxycoreLogsHandler(w, r, getPool)
		case sub == "balancers":
			proxycoreBalancersHandler(w, r)
		case sub == "warp":
			proxycoreWARPHandler(w, r, binDir)
		default:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown endpoint"})
		}
	}
}

// GET /api/proxycore/status — status of all proxy instances.
func proxycoreStatusHandler(w http.ResponseWriter, r *http.Request, getPool func() *proxycore.Pool) {
	pool := getPool()
	if pool == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"entries": []any{},
			"rules":   []any{},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"entries": pool.Status(),
		"rules":   pool.Rules(),
	})
}

// GET/POST /api/proxycore/entries — CRUD for proxy entries.
func proxycoreEntriesHandler(
	w http.ResponseWriter,
	r *http.Request,
	getPool func() *proxycore.Pool,
	reloadFn func() error,
) {
	switch r.Method {
	case http.MethodGet:
		pool := getPool()
		if pool == nil {
			writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": pool.Entries()})

	case http.MethodPost:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 131072))
		var req struct {
			Action string `json:"action"` // "add", "update", "delete"
			proxycore.PoolEntry
			Index int `json:"index"` // for update/delete
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}

		// Atomic config update.
		updateConfigTOMLMap(func(root map[string]any) {
			pc, _ := root["proxycore"].(map[string]any)
			if pc == nil {
				pc = map[string]any{}
				root["proxycore"] = pc
			}
			entriesRaw, _ := pc["entries"].([]any)
			var entries []map[string]any
			for _, e := range entriesRaw {
				if m, ok := e.(map[string]any); ok {
					entries = append(entries, m)
				}
			}

			switch req.Action {
			case "add":
				entries = append(entries, map[string]any{
					"uri":       req.URI,
					"label":     req.Label,
					"balancers": toAnySlice(req.Balancers),
					"engine":    req.Engine,
				})
			case "update":
				if req.Index >= 0 && req.Index < len(entries) {
					// "_keep_" sentinel from the edit modal means "leave the
					// URI untouched, only update metadata". Without this
					// check the literal "_keep_" overwrites the real URI
					// and the next pool reload fails with
					// `missing scheme in URI: "_keep_"`.
					uri := req.URI
					if uri == "_keep_" {
						if prev, ok := entries[req.Index]["uri"].(string); ok {
							uri = prev
						} else {
							uri = ""
						}
					}
					entries[req.Index] = map[string]any{
						"uri":       uri,
						"label":     req.Label,
						"balancers": toAnySlice(req.Balancers),
						"engine":    req.Engine,
					}
				}
			case "delete":
				if req.Index >= 0 && req.Index < len(entries) {
					entries = append(entries[:req.Index], entries[req.Index+1:]...)
				}
			}

			// Convert back.
			result := make([]any, len(entries))
			for i, e := range entries {
				result[i] = e
			}
			pc["entries"] = result
		})

		// Hot-reload proxies.
		if reloadFn != nil {
			if err := reloadFn(); err != nil {
				log.Warn().Err(err).Msg("proxycore: reload after config change failed")
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reload_error": err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// GET/POST /api/proxycore/rules — routing rules CRUD.
func proxycoreRulesHandler(
	w http.ResponseWriter,
	r *http.Request,
	getPool func() *proxycore.Pool,
	reloadFn func() error,
) {
	switch r.Method {
	case http.MethodGet:
		pool := getPool()
		if pool == nil {
			writeJSON(w, http.StatusOK, map[string]any{"rules": []any{}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rules": pool.Rules()})

	case http.MethodPost:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 131072))
		var req struct {
			Action string `json:"action"` // "add", "update", "delete"
			proxycore.RoutingRule
			Index int `json:"index"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}

		updateConfigTOMLMap(func(root map[string]any) {
			pc, _ := root["proxycore"].(map[string]any)
			if pc == nil {
				pc = map[string]any{}
				root["proxycore"] = pc
			}
			rulesRaw, _ := pc["rules"].([]any)
			var rules []map[string]any
			for _, r := range rulesRaw {
				if m, ok := r.(map[string]any); ok {
					rules = append(rules, m)
				}
			}

			switch req.Action {
			case "add":
				rules = append(rules, map[string]any{
					"id":        req.ID,
					"balancers": toAnySlice(req.Balancers),
					"proxy":     req.ProxyID,
					"fallback":  req.Fallback,
					"mode":      string(req.Mode),
				})
			case "update":
				if req.Index >= 0 && req.Index < len(rules) {
					rules[req.Index] = map[string]any{
						"id":        req.ID,
						"balancers": toAnySlice(req.Balancers),
						"proxy":     req.ProxyID,
						"fallback":  req.Fallback,
						"mode":      string(req.Mode),
					}
				}
			case "delete":
				if req.Index >= 0 && req.Index < len(rules) {
					rules = append(rules[:req.Index], rules[req.Index+1:]...)
				}
			}

			result := make([]any, len(rules))
			for i, r := range rules {
				result[i] = r
			}
			pc["rules"] = result
		})

		if reloadFn != nil {
			reloadFn()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// POST /api/proxycore/test — test a proxy URI without adding it.
func proxycoreTestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 131072))
	var req struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.URI == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "uri required"})
		return
	}

	result := proxycore.TestURI(req.URI)

	// Enrich with GeoIP if we have an IP.
	if result.IP != "" {
		country, flag := lookupGeoIP(result.IP)
		result.Country = country
		result.Flag = flag
		runes := []rune(flag)
		if len(runes) == 2 {
			result.CountryCode = string([]byte{byte(runes[0]-0x1F1E6) + 'A', byte(runes[1]-0x1F1E6) + 'A'})
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// POST /api/proxycore/reload — reload all proxy instances.
func proxycoreReloadHandler(w http.ResponseWriter, r *http.Request, reloadFn func() error) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if reloadFn == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "no reload function"})
		return
	}
	if err := reloadFn(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /api/proxycore/logs — connection logs (placeholder).
func proxycoreLogsHandler(w http.ResponseWriter, r *http.Request, getPool func() *proxycore.Pool) {
	// TODO: Implement connection logging in the SOCKS5 server and expose here.
	writeJSON(w, http.StatusOK, map[string]any{"logs": []any{}})
}

// GET /api/proxycore/balancers — list of all available balancer names (including "telegram").
func proxycoreBalancersHandler(w http.ResponseWriter, _ *http.Request) {
	seen := make(map[string]bool)
	type item struct {
		Name    string `json:"name"`
		Display string `json:"display"`
	}
	var list []item

	// Primary source: proxyableBalancers (title-cased display names).
	for _, display := range proxyableBalancers {
		low := strings.ToLower(display)
		if !seen[low] {
			seen[low] = true
			list = append(list, item{Name: low, Display: display})
		}
	}
	// Secondary source: localCorePlugins (lowercase keys).
	for name := range localCorePlugins {
		low := strings.ToLower(name)
		if !seen[low] {
			seen[low] = true
			// Title-case the first letter for display.
			display := strings.ToUpper(name[:1]) + name[1:]
			list = append(list, item{Name: low, Display: display})
		}
	}
	// Pseudo-balancer for Telegram bots.
	if !seen["telegram"] {
		list = append(list, item{Name: "telegram", Display: "Telegram"})
	}

	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"balancers": list})
}

// toAnySlice converts []string to []any for TOML map storage.
func toAnySlice(ss []string) []any {
	result := make([]any, len(ss))
	for i, s := range ss {
		result[i] = s
	}
	return result
}

// POST /api/proxycore/warp — register (or load cached) Cloudflare WARP config.
// Returns a wg:// URI ready to paste into the ProxyCore entry form.
func proxycoreWARPHandler(w http.ResponseWriter, r *http.Request, binDir string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if binDir == "" {
		binDir = "."
	}

	cfg, err := sidecar.RegisterOrLoadWARP(binDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("WARP registration failed: %v", err),
		})
		return
	}

	uri := warpConfigToURI(cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"uri":   uri,
		"label": "🌐 Cloudflare WARP",
	})
}

// warpConfigToURI converts a WARPConfig into a wg:// URI string. The URI is
// the canonical "fully-resolved" form (privkey + actual host:port + peer
// public key + addresses + reserved bytes) so callers don't need access to
// the WARP registration logic at parse time.
func warpConfigToURI(cfg *sidecar.WARPConfig) string {
	q := url.Values{}
	q.Set("publickey", cfg.PeerPublicKey)

	var addrs []string
	if cfg.Addresses.V4 != "" {
		addrs = append(addrs, cfg.Addresses.V4+"/32")
	}
	if cfg.Addresses.V6 != "" {
		addrs = append(addrs, cfg.Addresses.V6+"/128")
	}
	if len(addrs) > 0 {
		q.Set("address", strings.Join(addrs, ","))
	}
	q.Set("mtu", "1280")

	// Encode reserved bytes as "r0,r1,r2".
	if len(cfg.Reserved) >= 3 {
		q.Set("reserved", fmt.Sprintf("%d,%d,%d", cfg.Reserved[0], cfg.Reserved[1], cfg.Reserved[2]))
	}

	// Resolve endpoint via the same normalizer the runtime uses, then split
	// host:port. An empty/invalid endpoint falls back to the well-known WARP
	// IP so we never produce a malformed URI like `wg://key@:2408`.
	out := sidecar.WARPToOutbound(cfg)
	host := out.Server
	port := fmt.Sprintf("%d", out.Port)

	return fmt.Sprintf("wg://%s@%s:%s?%s",
		url.PathEscape(cfg.PrivateKey),
		host, port,
		q.Encode(),
	)
}

// proxycoreWARPBinDir resolves the bin directory for WARP config storage.
// Exported so server_routes_admin.go can pass it to the handler.
func proxycoreWARPBinDir(repoRoot string) string {
	if repoRoot == "" {
		return "bin"
	}
	return filepath.Join(repoRoot, "bin")
}
