package httpapi

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"strings"

	"lampac-go/internal/cluster"
	"lampac-go/internal/config"
	"lampac-go/internal/userdata"

	"github.com/rs/zerolog/log"
)

// clusterPingHandler responds to health probes from the primary.
// Auth: X-Cluster-Key must match cluster.api_key.
func clusterPingHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serverReady() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
			return
		}
		cfg := liveConfig(config.Config{})
		if !cfg.Cluster.Enable {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "cluster disabled"})
			return
		}
		if !validateClusterKey(r, cfg.Cluster.APIKey) {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "bad key"})
			return
		}

		// version lets the primary see, in one place, whether the fleet is
		// running the same build. Divergence used to be invisible: the probe
		// answered {ok, mode} and nothing else, so a node stuck on an old
		// binary looked identical to an up-to-date one while behaving
		// differently on every source.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"mode":    cfg.Cluster.Mode,
			"version": liveVersion(),
			// build is the real identity: every build carries the same version
			// string, so only the binary hash tells two of them apart.
			"build": selfBuildShort(),
		})
	}
}

// clusterUserdataDeleteHandler removes a user's server-side storage blobs on THIS
// node at another node's request — the admin panel's «удалить бэкапы на всех
// зеркалах» fan-out. Owners/profiles are computed by the calling server (this
// node may have no tgauth record for the user); the storage layout is identical
// everywhere, so explicit lists are enough.
// Auth: X-Cluster-Key must match cluster.api_key.
func clusterUserdataDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serverReady() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
			return
		}
		cfg := liveConfig(config.Config{})
		if !cfg.Cluster.Enable {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "cluster disabled"})
			return
		}
		if !validateClusterKey(r, cfg.Cluster.APIKey) {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "bad key"})
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var req struct {
			OwnerUIDs  []string `json:"owner_uids"`
			ProfileIDs []string `json:"profile_ids"`
			Paths      []string `json:"paths"`
		}
		if stdjson.Unmarshal(body, &req) != nil || len(req.OwnerUIDs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "owner_uids required"})
			return
		}
		deleted, freed := userdata.AdminDeleteUserStorage(req.OwnerUIDs, req.ProfileIDs, req.Paths)
		log.Info().Int("deleted", deleted).Int64("freed", freed).Msg("cluster: userdata delete request served")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted, "freed": freed})
	}
}

// clusterStatusHandler returns the status of all cluster nodes.
// Only available on primary.
func clusterStatusHandler(pool *cluster.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serverReady() || pool == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "not available"})
			return
		}
		cfg := liveConfig(config.Config{})
		if !isLocalRequest(r, cfg.Sync.APIPasswd) && !validateClusterKey(r, cfg.Cluster.APIKey) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "unauthorized"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"nodes": pool.Snapshot(),
		})
	}
}

// validateClusterKey checks the X-Cluster-Key header against the expected key.
func validateClusterKey(r *http.Request, expected string) bool {
	if expected == "" {
		return false
	}
	key := strings.TrimSpace(r.Header.Get("X-Cluster-Key"))
	return key != "" && key == expected
}

// validateClusterConfig logs warnings for misconfigurations that will silently
// break cluster behaviour at runtime. Called at startup (and on reload) when
// cluster.enable is true. Non-fatal — the server keeps running, but the
// problem is flagged in admin logs and the /servers dashboard banner.
//
// Checks:
//   - cluster.api_key empty            → primary↔node handshake will 403
//   - cluster.mode invalid             → mode must be "primary" or "node"
//   - primary with 0 nodes (info)      → just running locally, not a problem
//   - proxy_link.shared_secret empty   → /proxy/<enc> URLs minted by node A
//     won't decrypt on node B (broken streams
//     when forwarder picks a different node
//     than the one that minted the link)
func validateClusterConfig(cfg config.Config) {
	cl := cfg.Cluster
	if cl.Mode != "primary" && cl.Mode != "node" {
		log.Warn().Str("mode", cl.Mode).Msg("cluster: invalid mode — must be \"primary\" or \"node\"; cluster behaviour disabled")
		return
	}
	if strings.TrimSpace(cl.APIKey) == "" {
		log.Warn().Msg("cluster: api_key is empty — primary↔node ping will fail with 403. Set [cluster] api_key in config.toml")
	}
	if strings.TrimSpace(cfg.ProxyLink.SharedSecret) == "" {
		log.Warn().Msg("cluster: proxy_link.shared_secret is empty — /proxy/<enc> URLs will not decode across nodes. Cross-node stream forwarding will fail with 500. Set [proxy_link] shared_secret in config.toml (same value on primary and all nodes)")
	} else {
		// Check if a cached random key exists from a previous run. The
		// proxylink Manager auto-rotates on Reload(), but if the user
		// edited config.toml manually and forgot to reload — flag it.
		cacheDir := strings.TrimSpace(cfg.ProxyLink.CacheDir)
		if cacheDir == "" {
			cacheDir = "cache"
		}
		// cache file path inside cacheDir; existence is just a hint.
		// (We don't read or compare contents — that's proxylink's concern.)
		_ = cacheDir
	}
	log.Info().Str("mode", cl.Mode).Bool("api_key", cl.APIKey != "").Bool("shared_secret", cfg.ProxyLink.SharedSecret != "").Msg("cluster: configuration validated")
}

// isTrustedClusterRequest reports whether the request carries a valid
// X-Cluster-Key for the currently-configured cluster API key. Used by
// auth / WAF middlewares to skip per-user checks when the request was
// already validated by the primary and forwarded over the cluster.
//
// Always reads the live config so hot-reload of cluster.api_key takes
// effect without restarting middleware.
func isTrustedClusterRequest(r *http.Request) bool {
	if !serverReady() {
		return false
	}
	cfg := liveConfig(config.Config{})
	if !cfg.Cluster.Enable {
		return false
	}
	return validateClusterKey(r, cfg.Cluster.APIKey)
}
