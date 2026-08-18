package adminhttp

import (
	"context"
	stdjson "encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/cluster"
	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// tgAdminClusterHandler powers the admin "🌐 Кластер" tab.
//
// GET  /{adminPath}/api/cluster
//   - returns settings + nodes (with live runtime stats merged in)
//
// POST /{adminPath}/api/cluster
//   - action=add        — add node {name, host, weight, region, notes, enabled?}
//   - action=update     — patch node {id, name?, host?, weight?, enabled?, region?, notes?}
//   - action=delete     — remove node {id}
//   - action=probe      — manual probe {id} (returns refreshed status)
//   - action=settings   — replace settings (whole Settings struct)
//   - action=test       — probe a host that isn't in the pool yet
func tgAdminClusterHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore); !ok {
			return
		}
		if !serverReady() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "not ready"})
			return
		}

		store := liveClusterStore()
		pool := liveClusterPool()
		cfg := liveConfig(config.Config{})

		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, buildClusterSnapshot(store, pool))
			return
		}
		_ = cfg // reload-fresh cfg is read inside buildClusterSnapshot via liveConfig
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Action  string                 `json:"action"`
			Name    string                 `json:"name"`
			Host    string                 `json:"host"`
			Weight  int                    `json:"weight"`
			Enabled *bool                  `json:"enabled"`
			Region  string                 `json:"region"`
			Notes   string                 `json:"notes"`
			ID      string                 `json:"id"`
			Patch   map[string]interface{} `json:"patch"`
			// For settings action.
			Settings *cluster.Settings `json:"settings"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 256<<10))
		_ = r.Body.Close()
		if len(body) > 0 {
			if err := stdjson.Unmarshal(body, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
				return
			}
		}

		// Cluster must be primary mode for write actions.
		if !cfg.Cluster.Enable || cfg.Cluster.Mode != "primary" {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":    false,
				"error": "cluster is not in primary mode — enable [cluster] in config and set mode = \"primary\"",
			})
			return
		}
		if store == nil || pool == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "cluster store not initialised"})
			return
		}

		switch strings.ToLower(strings.TrimSpace(req.Action)) {
		case "add":
			enabled := true
			if req.Enabled != nil {
				enabled = *req.Enabled
			}
			// Auto-generate api_key + shared_secret if either is empty.
			// First-time add bootstraps the cluster transparently.
			secrets, secretsErr := ensureClusterSecrets()
			if secretsErr != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "ensure secrets: " + secretsErr.Error()})
				return
			}
			n, err := store.Add(req.Name, req.Host, req.Weight, enabled, req.Region, req.Notes)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			pool.Reconcile(store.Snapshot())
			resp := map[string]any{"ok": true, "node": n}
			if secrets.APIKeyGenerated || secrets.SharedSecretGenerated {
				resp["secrets_generated"] = map[string]any{
					"api_key":             secrets.APIKey,
					"api_key_was_empty":   secrets.APIKeyGenerated,
					"shared_secret":       secrets.SharedSecret,
					"secret_was_empty":    secrets.SharedSecretGenerated,
					"copy_to_node_config": buildPeerConfigSnippet(secrets.APIKey, secrets.SharedSecret),
				}
			}
			writeJSON(w, http.StatusOK, resp)

		case "update":
			patch := cluster.UpdatePatch{}
			if v, ok := req.Patch["name"].(string); ok {
				patch.Name = &v
			}
			if v, ok := req.Patch["host"].(string); ok {
				patch.Host = &v
			}
			if v, ok := req.Patch["weight"].(float64); ok {
				iv := int(v)
				patch.Weight = &iv
			}
			if v, ok := req.Patch["enabled"].(bool); ok {
				patch.Enabled = &v
			}
			if v, ok := req.Patch["region"].(string); ok {
				patch.Region = &v
			}
			if v, ok := req.Patch["notes"].(string); ok {
				patch.Notes = &v
			}
			id := req.ID
			if id == "" {
				if v, ok := req.Patch["id"].(string); ok {
					id = v
				}
			}
			n, err := store.Update(id, patch)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			pool.Reconcile(store.Snapshot())
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node": n})

		case "delete":
			if err := store.Delete(req.ID); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			pool.Reconcile(store.Snapshot())
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})

		case "probe":
			if req.ID == "" {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "id required"})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			st, ok := pool.ProbeNow(ctx, req.ID)
			if !ok {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "node not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": st})

		case "settings":
			if req.Settings == nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "settings missing"})
				return
			}
			if err := pool.SetSettings(*req.Settings); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": pool.CurrentSettings()})

		case "test":
			// Probe an external host (not yet added). Verifies API key + reachability.
			host := strings.TrimRight(strings.TrimSpace(req.Host), "/")
			if host == "" {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "host required"})
				return
			}
			if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
				host = "http://" + host
			}
			ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
			defer cancel()
			info, err := probeRemoteHost(ctx, host, pool.APIKey())
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "info": info})

		case "ensure-secrets":
			// Generate missing api_key / shared_secret without adding a node.
			// Useful for showing the current values on the very first visit
			// to the cluster tab.
			secrets, err := ensureClusterSecrets()
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":                  true,
				"api_key":             secrets.APIKey,
				"shared_secret":       secrets.SharedSecret,
				"api_key_generated":   secrets.APIKeyGenerated,
				"secret_generated":    secrets.SharedSecretGenerated,
				"copy_to_node_config": buildPeerConfigSnippet(secrets.APIKey, secrets.SharedSecret),
			})

		case "regenerate-secrets":
			// Force regeneration of BOTH secrets. Existing nodes that don't
			// get the new values will fail health checks until reconfigured.
			newAPI := randomHex(32)
			newSecret := randomHex(32)
			if err := forceRewriteClusterSecrets(newAPI, newSecret); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":                  true,
				"api_key":             newAPI,
				"shared_secret":       newSecret,
				"copy_to_node_config": buildPeerConfigSnippet(newAPI, newSecret),
			})

		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action"})
		}
	}
}

// buildClusterSnapshot merges the persistent store entries with live pool
// stats into a single response payload for the admin UI.
func buildClusterSnapshot(store *cluster.Store, pool *cluster.Pool) map[string]any {
	out := map[string]any{
		"enabled": false,
		"mode":    "",
		"nodes":   []any{},
	}
	if serverReady() {
		actual := liveConfig(config.Config{})
		out["enabled"] = actual.Cluster.Enable
		out["mode"] = actual.Cluster.Mode
		out["api_key_set"] = actual.Cluster.APIKey != ""
		out["shared_secret_set"] = strings.TrimSpace(actual.ProxyLink.SharedSecret) != ""
	}
	if pool != nil {
		out["settings"] = pool.CurrentSettings()
		out["nodes"] = pool.Snapshot()
		out["local"] = pool.LocalSnapshot()
		out["recent_events"] = pool.RecentEvents()
	} else if store != nil {
		out["settings"] = store.Settings()
		out["nodes"] = store.Snapshot()
	} else {
		out["settings"] = cluster.DefaultSettings()
	}
	return out
}

// probeRemoteHost tries to GET {host}/api/cluster/ping with the cluster key
// and returns the parsed response or an error.
func probeRemoteHost(ctx context.Context, host, apiKey string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/api/cluster/ping", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("X-Cluster-Key", apiKey)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	out := map[string]any{
		"status":     resp.StatusCode,
		"latency_ms": time.Since(start).Milliseconds(),
	}
	if resp.StatusCode == http.StatusOK {
		var parsed map[string]any
		if err := stdjson.Unmarshal(body, &parsed); err == nil {
			out["response"] = parsed
		}
	} else {
		out["body"] = strings.TrimSpace(string(body))
	}
	return out, nil
}
