package httpapi

import (
	"net/http"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxycore"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// installerDefaultsHandler returns the current config as JSON for pre-filling the wizard.
func installerDefaultsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serverReady() {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server not ready"})
			return
		}
		cfg := liveConfig(config.Config{})
		writeJSON(w, http.StatusOK, cfg)
	}
}

// installerApplyHandler receives the full wizard config and saves it.
// Rate-limited to 1 call per minute.
func installerApplyHandler(pwStore **tgauth.PasswordAuthStore) http.HandlerFunc {
	var (
		mu       sync.Mutex
		lastCall time.Time
	)
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if time.Since(lastCall) < time.Minute {
			mu.Unlock()
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited, try again later"})
			return
		}
		lastCall = time.Now()
		mu.Unlock()

		if !serverReady() {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server not ready"})
			return
		}

		// Decode the wizard payload into a generic map for TOML merge.
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}

		// Extract admin password before TOML merge (handled separately via PasswordAuthStore).
		var adminPassword string
		if admin, ok := payload["admin"].(map[string]any); ok {
			if pw, ok := admin["password"].(string); ok && pw != "" {
				adminPassword = pw
			}
		}

		// Force setup_done = true.
		srv, _ := payload["server"].(map[string]any)
		if srv == nil {
			srv = map[string]any{}
			payload["server"] = srv
		}
		srv["setup_done"] = true

		// Save config.toml via read-modify-write.
		if err := saveConfigTOMLFromMap(payload); err != nil {
			log.Error().Err(err).Msg("installer: failed to save config.toml")
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save config: " + err.Error()})
			return
		}

		// Hash and persist admin password if provided.
		if adminPassword != "" {
			ps := tgauth.NewPasswordAuthStore(liveConfig(config.Config{}).Compat.RepoRoot)
			if err := ps.SetPasswordFromPlaintext(adminPassword); err != nil {
				log.Error().Err(err).Msg("installer: failed to hash admin password")
			} else {
				*pwStore = ps
				log.Info().Msg("installer: admin password saved")
			}
		}

		// Return the admin panel path so the UI can redirect.
		adminPath := loadOrGenerateAdminPath()
		log.Info().Msg("installer: setup completed, /web/installer is now blocked")

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"admin_path": "/" + adminPath,
		})
	}
}

// installerValidateHandler performs partial validation (e.g., test TG bot token).
func installerValidateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Check string `json:"check"` // "telegram", "proxy", etc.
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}

		switch req.Check {
		case "telegram":
			ok, info := validateTelegramToken(req.Value)
			writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "info": info})
		case "torrserver":
			ok, info := validateTorrServer(req.Value)
			writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "info": info})
		case "telegram_proxy":
			ok, info := validateTelegramProxy(req.Value)
			writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "info": info})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "info": "no validation for: " + req.Check})
		}
	}
}

// validateTelegramToken checks if a bot token is valid by calling getMe.
func validateTelegramToken(token string) (bool, string) {
	if token == "" {
		return false, "empty token"
	}
	resp, err := http.Get("https://api.telegram.org/bot" + token + "/getMe")
	if err != nil {
		return false, "request failed: " + err.Error()
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, "invalid response"
	}
	if !result.OK {
		return false, "invalid token"
	}
	return true, "@" + result.Result.Username
}

// validateTorrServer pings an external TorrServer by URL.
func validateTorrServer(urlStr string) (bool, string) {
	if urlStr == "" {
		return false, "empty URL"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(urlStr + "/echo")
	if err != nil {
		return false, "connection failed: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true, "TorrServer OK"
	}
	return false, "status " + resp.Status
}

// validateTelegramProxy tests whether api.telegram.org is reachable through
// the given proxy URI. Starts a temporary ProxyCore instance, dials through it,
// and tears it down.
func validateTelegramProxy(uri string) (bool, string) {
	if uri == "" {
		return false, "empty URI"
	}

	// Quick protocol-level test via ProxyCore's TestURI (dials out, returns IP).
	result := proxycore.TestURI(uri)
	if !result.Success {
		return false, "proxy connection failed: " + result.Error
	}

	// Now start a temporary SOCKS5 instance and try to reach Telegram.
	pool, err := proxycore.NewPool([]proxycore.PoolEntry{
		{URI: uri, Label: "_tg_test", Balancers: nil},
	}, 49900, "") // high port to avoid clashing
	if err != nil {
		return false, "proxy start failed: " + err.Error()
	}
	defer pool.StopAll()

	instances := pool.Instances()
	if len(instances) == 0 {
		return false, "no proxy instance started"
	}
	socksAddr := instances[0].SOCKSAddr()

	// Create an HTTP client routed through the SOCKS5 proxy.
	transport := httpclient.NewSOCKS5TransportPublic(socksAddr)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.telegram.org/")
	if err != nil {
		return false, "Telegram unreachable: " + err.Error()
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		// 200 or 404 both mean we reached Telegram's servers.
		return true, "Telegram reachable via proxy (" + result.IP + ")"
	}
	return false, "unexpected status: " + resp.Status
}
