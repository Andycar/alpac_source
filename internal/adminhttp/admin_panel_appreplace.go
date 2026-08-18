package adminhttp

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// --- AppReplace API ---

func tgAdminAppReplaceHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			rules := []config.AppReplaceRule{}
			if serverReady() {
				rules = liveConfig(config.Config{}).Web.AppReplace
			}
			if rules == nil {
				rules = []config.AppReplaceRule{}
			}
			writeJSON(w, http.StatusOK, rules)

		case http.MethodPost:
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body: " + err.Error()})
				return
			}

			var rules []config.AppReplaceRule
			if err := stdjson.Unmarshal(body, &rules); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
				return
			}

			// Validate regex patterns.
			for i, rule := range rules {
				if rule.Pattern == "" {
					continue
				}
				if _, err := regexp.Compile(rule.Pattern); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{
						"error": fmt.Sprintf("invalid regex in rule #%d (%s): %s", i+1, rule.Name, err.Error()),
					})
					return
				}
			}

			// Save to config.toml via TOML helpers.
			if err := updateConfigTOMLMap(func(root map[string]any) {
				web, ok := root["web"].(map[string]any)
				if !ok {
					web = map[string]any{}
					root["web"] = web
				}
				// Convert rules to []any for TOML serialization.
				arr := make([]any, len(rules))
				for i, r := range rules {
					arr[i] = map[string]any{
						"name":        r.Name,
						"pattern":     r.Pattern,
						"replacement": r.Replacement,
						"enabled":     r.Enabled,
					}
				}
				web["app_replace"] = arr
			}); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}

			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// --- Custom CSS/JS API ---

type customCodePayload struct {
	CustomCSS string `json:"custom_css"`
	CustomJS  string `json:"custom_js"`
}

func tgAdminCustomCodeHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			var payload customCodePayload
			if serverReady() {
				c := liveConfig(config.Config{})
				payload.CustomCSS = c.Web.CustomCSS
				payload.CustomJS = c.Web.CustomJS
			}
			writeJSON(w, http.StatusOK, payload)

		case http.MethodPost:
			body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20)) // 2 MB limit
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body: " + err.Error()})
				return
			}

			var payload customCodePayload
			if err := stdjson.Unmarshal(body, &payload); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
				return
			}

			if err := updateConfigTOMLMap(func(root map[string]any) {
				web, ok := root["web"].(map[string]any)
				if !ok {
					web = map[string]any{}
					root["web"] = web
				}
				web["custom_css"] = payload.CustomCSS
				web["custom_js"] = payload.CustomJS
			}); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}

			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}
