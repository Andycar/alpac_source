package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/updater"

	"github.com/rs/zerolog/log"
)

// Cluster build distribution: the primary serves its own binary, nodes pull it.
//
// Nodes used to drift silently. /api/cluster/ping answered {ok, mode} and
// nothing else, so a node running a months-old build looked identical to a
// current one while behaving differently on every source — and the only way to
// notice was a user reporting that one server finds a film and another does not.
//
// Identity is the SHA-256 of the executable, not the version string: every
// build is stamped with the same `-X main.version=0.5`, so comparing versions
// would report "same" for two completely different binaries.

// buildInfo describes the executable this process is running.
type buildInfo struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
}

var (
	selfBuildOnce sync.Once
	selfBuildVal  buildInfo
	selfBuildErr  error
)

// selfBuild hashes the running executable once and caches the result. The file
// behind a running process does not change under it — an update replaces the
// path, and the replacement only matters after the restart.
func selfBuild() (buildInfo, error) {
	selfBuildOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			selfBuildErr = err
			return
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		f, err := os.Open(exe)
		if err != nil {
			selfBuildErr = err
			return
		}
		defer f.Close()
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			selfBuildErr = err
			return
		}
		selfBuildVal = buildInfo{
			SHA256: hex.EncodeToString(h.Sum(nil)),
			Size:   n,
			GOOS:   runtime.GOOS,
			GOARCH: runtime.GOARCH,
		}
	})
	// Version is resolved per call, never cached: selfBuild() is first invoked
	// during start-up wiring, before liveVersion() has a value, and a cached
	// empty string would then be reported forever.
	out := selfBuildVal
	out.Version = liveVersion()
	return out, selfBuildErr
}

// selfBuildShort is the identifier shown in logs and the admin dashboard.
func selfBuildShort() string {
	b, err := selfBuild()
	if err != nil || b.SHA256 == "" {
		return ""
	}
	return b.SHA256[:12]
}

// clusterUpdateAuthorized accepts the cluster key from either header: nodes use
// X-Cluster-Key, while updater.DownloadAsset can only send a Bearer token.
func clusterUpdateAuthorized(r *http.Request, expected string) bool {
	if strings.TrimSpace(expected) == "" {
		return false
	}
	if secretEqual(r.Header.Get("X-Cluster-Key"), expected) {
		return true
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(auth, "Bearer ") {
		return secretEqual(strings.TrimPrefix(auth, "Bearer "), expected)
	}
	return false
}

// secretEqual compares in constant time — these endpoints hand out the binary,
// so the key must not be guessable one byte at a time.
func secretEqual(got, want string) bool {
	g, w := strings.TrimSpace(got), strings.TrimSpace(want)
	if g == "" || w == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(g), []byte(w)) == 1
}

// clusterReleaseHandler — GET /api/cluster/release. Metadata of the binary this
// primary is running, so a node can decide whether it needs to fetch it.
func clusterReleaseHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := liveConfig(config.Config{})
		if !cfg.Cluster.Enable {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "cluster disabled"})
			return
		}
		if !clusterUpdateAuthorized(r, cfg.Cluster.APIKey) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "bad key"})
			return
		}
		b, err := selfBuild()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, b)
	}
}

// clusterBinaryHandler — GET /api/cluster/binary. Streams this primary's own
// executable to an authorized node.
func clusterBinaryHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := liveConfig(config.Config{})
		if !cfg.Cluster.Enable {
			http.Error(w, "cluster disabled", http.StatusNotFound)
			return
		}
		if !clusterUpdateAuthorized(r, cfg.Cluster.APIKey) {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		exe, err := os.Executable()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		f, err := os.Open(exe)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", st.Size()))
		w.Header().Set("X-Build-SHA256", selfBuildShort())
		http.ServeContent(w, r, updater.AssetName(), st.ModTime(), f)
	}
}

// ---------------------------------------------------------------------------
//  Node side — pull the primary's build
// ---------------------------------------------------------------------------

const (
	clusterUpdateInterval = 10 * time.Minute
	// clusterUpdateRetryAfter keeps a build that failed to take from being
	// retried in a loop. Without it a binary that starts, fails to match, and
	// gets fetched again would restart the node every interval forever.
	clusterUpdateRetryAfter = 2 * time.Hour
)

type clusterUpdateState struct {
	SHA256 string    `json:"sha256"`
	At     time.Time `json:"at"`
}

type clusterSelfUpdater struct {
	cfgFn  func() config.Config
	client *http.Client
	stop   chan struct{}
	once   sync.Once
}

func newClusterSelfUpdater(cfgFn func() config.Config) *clusterSelfUpdater {
	return &clusterSelfUpdater{
		cfgFn:  cfgFn,
		client: httpclient.New(30 * time.Second),
		stop:   make(chan struct{}),
	}
}

func (u *clusterSelfUpdater) Start() { go u.loop() }

func (u *clusterSelfUpdater) Stop() {
	u.once.Do(func() { close(u.stop) })
}

func (u *clusterSelfUpdater) loop() {
	// A short initial delay lets the server finish coming up; restarting a node
	// two seconds into its boot would look like a crash loop to systemd.
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-u.stop:
			return
		case <-timer.C:
		}
		u.tick()
		timer.Reset(clusterUpdateInterval)
	}
}

func (u *clusterSelfUpdater) tick() {
	cfg := u.cfgFn()
	cl := cfg.Cluster
	if !cl.Enable || strings.EqualFold(strings.TrimSpace(cl.Mode), "primary") {
		return // the primary is the source, it never pulls
	}
	if !cl.AutoUpdate {
		return
	}
	host := strings.TrimRight(strings.TrimSpace(cl.PrimaryHost), "/")
	if host == "" || strings.TrimSpace(cl.APIKey) == "" {
		return
	}
	mode := updater.DetectMode()
	if !mode.CanSelfUpdate() {
		log.Debug().Str("mode", mode.String()).Msg("cluster update: run mode cannot self-update")
		return
	}

	remote, err := u.fetchRelease(host, cl.APIKey)
	if err != nil {
		log.Debug().Err(err).Str("primary", host).Msg("cluster update: release check failed")
		return
	}
	local, err := selfBuild()
	if err != nil {
		log.Debug().Err(err).Msg("cluster update: cannot hash own binary")
		return
	}
	if remote.SHA256 == "" || remote.SHA256 == local.SHA256 {
		return // already identical
	}
	// A binary built for another platform would install and refuse to start.
	if remote.GOOS != local.GOOS || remote.GOARCH != local.GOARCH {
		log.Warn().
			Str("primary", remote.GOOS+"/"+remote.GOARCH).
			Str("local", local.GOOS+"/"+local.GOARCH).
			Msg("cluster update: platform mismatch, skipping")
		return
	}
	if u.recentlyTried(cfg, remote.SHA256) {
		return
	}

	log.Info().
		Str("from", short(local.SHA256)).Str("to", short(remote.SHA256)).
		Str("primary", host).Msg("cluster update: pulling primary build")

	res, err := updater.DownloadAsset(host+"/api/cluster/binary", remote.SHA256, remote.Size, cl.APIKey)
	if err != nil {
		log.Warn().Err(err).Msg("cluster update: download failed")
		return
	}
	u.markTried(cfg, remote.SHA256)
	backup, err := updater.Activate(res.StagedPath)
	if err != nil {
		log.Error().Err(err).Msg("cluster update: activate failed")
		return
	}
	log.Info().Str("backup", backup).Str("build", short(remote.SHA256)).
		Msg("cluster update: installed, restarting")
	if err := updater.Restart(); err != nil {
		log.Error().Err(err).Msg("cluster update: restart failed — rolling back")
		if rbErr := updater.Rollback(); rbErr != nil {
			log.Error().Err(rbErr).Msg("cluster update: rollback failed")
		}
	}
}

func (u *clusterSelfUpdater) fetchRelease(host, key string) (buildInfo, error) {
	var out buildInfo
	req, err := http.NewRequest(http.MethodGet, host+"/api/cluster/release", nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("X-Cluster-Key", key)
	resp, err := u.client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return out, err
	}
	return out, stdjson.Unmarshal(body, &out)
}

func clusterUpdateStatePath(cfg config.Config) string {
	root := strings.TrimSpace(cfg.Compat.RepoRoot)
	if root == "" {
		root = "."
	}
	return filepath.Join(root, "database", "cluster", "last_update.json")
}

// recentlyTried reports whether this exact build was already attempted a short
// while ago — the guard against restart loops when an update does not stick.
func (u *clusterSelfUpdater) recentlyTried(cfg config.Config, sha string) bool {
	data, err := os.ReadFile(clusterUpdateStatePath(cfg))
	if err != nil {
		return false
	}
	var st clusterUpdateState
	if stdjson.Unmarshal(data, &st) != nil {
		return false
	}
	if st.SHA256 != sha {
		return false
	}
	if time.Since(st.At) > clusterUpdateRetryAfter {
		return false
	}
	log.Warn().Str("build", short(sha)).Time("tried", st.At).
		Msg("cluster update: this build was already attempted recently — not retrying")
	return true
}

func (u *clusterSelfUpdater) markTried(cfg config.Config, sha string) {
	p := clusterUpdateStatePath(cfg)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	data, err := stdjson.Marshal(clusterUpdateState{SHA256: sha, At: time.Now()})
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
