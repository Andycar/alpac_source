package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// tgAdminWAFHandler serves the WAF admin API:
//
//	GET    /api/admin/waf/state         — current config + stats + manual bans
//	POST   /api/admin/waf/config        — replace config; persist + hot-reload
//	POST   /api/admin/waf/reload        — re-read init.conf + apply
//	POST   /api/admin/waf/ban           — add a manual ban
//	DELETE /api/admin/waf/ban/{ip}      — remove a manual ban
//
// Auth: super admin only — WAF controls block traffic for everyone, so this
// must not be downgraded to plain admin.
func tgAdminWAFHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		path := r.URL.Path

		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/waf/state"):
			writeWAFState(w)

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/waf/config"):
			handleWAFConfigSave(w, r)

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/waf/reload"):
			reloadWAF()
			writeWAFState(w)

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/waf/ban/remove"):
			// Body-based remove. Preferred over DELETE for CIDR IPs (with '/')
			// since URL-encoded slashes (%2F) don't round-trip through chi.
			handleWAFBanRemoveBody(w, r)

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/waf/ban"):
			handleWAFBanAdd(w, r)

		case r.Method == http.MethodDelete && strings.Contains(path, "/waf/ban/"):
			ip := strings.TrimSpace(chi.URLParam(r, "ip"))
			if ip == "" {
				// Fallback for routes not bound through chi.URLParam (e.g. legacy
				// mounts) — extract the trailing segment manually.
				if idx := strings.LastIndex(path, "/waf/ban/"); idx >= 0 {
					ip = path[idx+len("/waf/ban/"):]
				}
			}
			// URL-decode the IP. CIDR values contain '/' which the client must
			// encode as %2F; chi.URLParam does not unescape these for us.
			if dec, err := url.PathUnescape(ip); err == nil {
				ip = strings.TrimSpace(dec)
			}
			handleWAFBanRemove(w, ip)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func writeWAFState(w http.ResponseWriter) {
	state := currentWafState()
	if state == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"config":     wafConfigDefault,
			"stats":      map[string]any{},
			"manualBans": []any{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"config":     state.CfgAny(),
		"stats":      state.Stats(),
		"manualBans": state.CollectManualBansAny(),
	})
}

func handleWAFConfigSave(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	// Validate JSON generically (the host owns the wafConfig type + unmarshal).
	var probe map[string]any
	if err := stdjson.Unmarshal(body, &probe); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json: " + err.Error()})
		return
	}
	if err := saveWafConfigJSON(body); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	reloadWAF()
	writeWAFState(w)
}

func handleWAFBanAdd(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	var req struct {
		IP     string `json:"ip"`
		TTLSec int64  `json:"ttlSec"`
		Reason string `json:"reason"`
	}
	if err := stdjson.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	state := currentWafState()
	if state == nil {
		// WAF not yet initialised — initialise from an empty config so we can
		// still record the ban (it will be picked up on the next ReloadWAF).
		state = reloadWAF()
	}
	var ttl time.Duration
	if req.TTLSec > 0 {
		ttl = time.Duration(req.TTLSec) * time.Second
	}
	if err := state.AddManualBan(req.IP, req.Reason, ttl); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"manualBans": state.CollectManualBansAny(),
	})
}

func handleWAFBanRemove(w http.ResponseWriter, ip string) {
	state := currentWafState()
	if state == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "manualBans": []any{}})
		return
	}
	if err := state.RemoveManualBan(ip); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"manualBans": state.CollectManualBansAny(),
	})
}

func handleWAFBanRemoveBody(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	var req struct {
		IP string `json:"ip"`
	}
	if err := stdjson.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	handleWAFBanRemove(w, strings.TrimSpace(req.IP))
}
