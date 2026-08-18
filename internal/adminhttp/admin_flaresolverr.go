package adminhttp

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// probeFlareSolverr issues a cheap `sessions.list` against the FlareSolverr v1
// endpoint and parses the JSON greeting. Returns ok and a short status message
// suitable for showing in the admin UI.
func probeFlareSolverr(ctx context.Context, baseURL string) (bool, string) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return false, "URL пустой"
	}
	body, _ := stdjson.Marshal(map[string]any{"cmd": "sessions.list"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1", bytes.NewReader(body))
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "не удалось достучаться: " + err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	var out struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Message string `json:"message"`
	}
	if err := stdjson.Unmarshal(raw, &out); err != nil {
		return false, "невалидный ответ: " + strings.TrimSpace(string(raw))[:min(120, len(strings.TrimSpace(string(raw))))]
	}
	if !strings.EqualFold(out.Status, "ok") {
		return false, "FlareSolverr вернул: " + out.Message
	}
	ver := out.Version
	if ver == "" {
		ver = "ok"
	}
	return true, "FlareSolverr отвечает (" + ver + ")"
}

// FlareSolverr admin API: GET/PUT /admin/api/flaresolverr
//
// GET returns the current global URL, the opt-in balancers list and a list of
// hosts that currently have a cached solved session (so admins can debug
// "why is this balancer fast / slow" without grep'ing logs).
//
// PUT accepts {url:"...", balancers:["kinogo","vdbmovies",...]} and applies it
// to the in-memory registry immediately. Persistence to config.toml is the
// caller's job — for now they can also save through the existing config
// editor; this endpoint is the live-tweak path.
func tgAdminFlareSolverrHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			// ?test=URL probes the FlareSolverr instance and returns reachability.
			if probe := strings.TrimSpace(r.URL.Query().Get("test")); probe != "" {
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				defer cancel()
				ok, msg := probeFlareSolverr(ctx, probe)
				writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "message": msg})
				return
			}
			// Annotated form carries the system flag so the UI can render
			// non-removable badges for code-required balancers like turbo.
			// Plain "balancers" stays for back-compat with older UI builds.
			entries := httpclient.ListFlareSolverrBalancersAnnotated()
			plain := make([]string, 0, len(entries))
			for _, e := range entries {
				plain = append(plain, e.Name)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"url":                 httpclient.FlareSolverrURL(),
				"balancers":           plain,
				"balancer_entries":    entries,
				"user_balancers":      currentFlareSolverrUserBalancers(),
				"sessions":            currentFlareSolverrSessions(),
				"available_balancers": proxyableBalancers,
			})

		case http.MethodPut:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			var req struct {
				URL       string   `json:"url"`
				Balancers []string `json:"balancers"`
			}
			if stdjson.Unmarshal(body, &req) != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}

			url := strings.TrimSpace(req.URL)
			// Dedup + drop empties + lowercase so we don't write back garbage
			// the UI sent (e.g. trailing whitespace, duplicate clicks).
			balancers := normalizeBalancerList(req.Balancers)

			// Apply to live registry first — the user shouldn't have to wait
			// for disk I/O to see their change take effect on the next probe.
			httpclient.SetFlareSolverrURL(url)
			httpclient.ApplyUserFlareSolverrBalancers(balancers)

			// Persist to config.toml so the change survives both a process
			// restart AND any other admin action that triggers a Reload (the
			// proxy editor, TOML editor, etc. — those re-read disk and any
			// in-memory-only edit gets reverted).
			if err := persistFlareSolverrToTOML(url, balancers); err != nil {
				log.Warn().Err(err).Msg("admin: failed to persist FlareSolverr settings to config.toml")
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "persist failed: " + err.Error()})
				return
			}

			// Keep the in-memory cfg snapshot in sync — persistFlareSolverr
			// ToTOML did NOT call Reload (Reload would restart proxy pools,
			// etc. — way too heavy for a settings tweak), so do it inline.
			if serverReady() {
				cfg := liveConfig(config.Config{})
				cfg.Online.FlareSolverr = url
				cfg.Online.FlareSolverrBalancers = append([]string(nil), balancers...)
				storeLiveConfig(cfg)
			}

			entries := httpclient.ListFlareSolverrBalancersAnnotated()
			plain := make([]string, 0, len(entries))
			for _, e := range entries {
				plain = append(plain, e.Name)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":               true,
				"url":              httpclient.FlareSolverrURL(),
				"balancers":        plain,
				"balancer_entries": entries,
				"user_balancers":   currentFlareSolverrUserBalancers(),
			})

		case http.MethodDelete:
			// Convenience: drop the cached session for one host (admin can
			// force a fresh solve without restarting). ?host=foo.bar
			host := strings.TrimSpace(r.URL.Query().Get("host"))
			if host == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host query required"})
				return
			}
			httpclient.InvalidateFlareSolverrSession("https://" + host)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "host": host})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// currentFlareSolverrUserBalancers returns the user-managed list straight from
// config (case preserved for UI display). System balancers (turbo, etc.) are
// NOT in this list — they live in the httpclient registry only.
func currentFlareSolverrUserBalancers() []string {
	if serverReady() {
		cfg := liveConfig(config.Config{})
		out := make([]string, 0, len(cfg.Online.FlareSolverrBalancers))
		for _, n := range cfg.Online.FlareSolverrBalancers {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			out = append(out, n)
		}
		return out
	}
	return nil
}

// normalizeBalancerList drops empty strings, lowercases, and dedupes while
// preserving the order the user picked them. Keeps config.toml from filling
// up with whitespace duplicates when the admin double-clicks Save.
func normalizeBalancerList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, n := range in {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// persistFlareSolverrToTOML writes the FlareSolverr URL and user balancer
// list to config.toml in place, without disturbing other settings. Uses the
// existing atomic read-modify-write helper so the operation survives a
// concurrent edit from another admin tab.
//
// We intentionally do NOT trigger Server.Reload here — Reload restarts proxy
// pools and other heavy components, way too much for a settings tweak. The
// caller updates the in-memory cfg snapshot inline instead.
func persistFlareSolverrToTOML(url string, balancers []string) error {
	return updateConfigTOMLMapNoReload(func(root map[string]any) {
		online, _ := root["online"].(map[string]any)
		if online == nil {
			online = map[string]any{}
			root["online"] = online
		}
		online["flaresolverr"] = url
		// Always write as a list — even an empty list is valid TOML and
		// avoids the "nil vs missing" surprise on the next load.
		arr := make([]any, len(balancers))
		for i, b := range balancers {
			arr[i] = b
		}
		online["flaresolverr_balancers"] = arr
	})
}

// currentFlareSolverrSessions surfaces what hosts are currently cached, so the
// admin can see "ok, solving for foo.bar paid off, lasts another 12 min".
func currentFlareSolverrSessions() []map[string]any {
	// We can't enumerate the unexported store directly without leaking
	// internals; expose a public helper instead.
	return httpclient.SnapshotFlareSolverrSessions()
}
