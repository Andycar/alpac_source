package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// sumForTarget scans a SHA256SUMS-formatted file and returns the hex digest
// for `target`, or "" if not found.
func sumForTarget(sums []byte, target string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[1], "*") == target {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}

// Config holds runtime-tunable knobs for the updater. Loaded from config.toml
// [updater] section and refreshed on hot-reload.
type Config struct {
	Enabled           bool
	ServerURL         string        // alcopa-site base URL, e.g. "https://lampac.site"
	Channel           string        // "stable" | "beta"
	ServerToken       string        // optional bearer token for closed channels
	CheckInterval     time.Duration // 0 disables the background check
	AutoInstall       bool          // apply downloaded releases automatically
	MaintenanceWindow string        // "HH:MM-HH:MM" local time, "" = any time
	AssetOverride     string        // force a specific asset name
	HistoryPath       string        // JSON file for history + state

	// MinisignPubKey is the raw public key text (either "untrusted comment: ...\nBASE64\n"
	// or just the BASE64 body). If set, Apply() refuses to install a release
	// unless SHA256SUMS + SHA256SUMS.minisig are present and verify.
	MinisignPubKey string
	RequireSignature bool // if true, missing signature is a fatal error

	// PreventDuringStreams defers the auto-install step when ActiveStreams
	// returns > 0. Manual Apply() is never blocked.
	PreventDuringStreams bool
	// ActiveStreams is called by the auto-check loop before applying an
	// update. Return the current count of in-flight proxy streams. Safe
	// to leave nil — the loop treats nil as "0 streams".
	ActiveStreams func() int64

	// PluginsDir / WwwrootDir are the on-disk targets that plugins.tar.gz
	// and wwwroot.tar.gz are extracted into during Apply(). Empty disables
	// the corresponding tarball update (the binary still ships).
	PluginsDir string
	WwwrootDir string
}

// Info is the public snapshot returned to the admin UI and API clients.
type Info struct {
	Current        string     `json:"current"`
	Commit         string     `json:"commit,omitempty"`
	BuildDate      string     `json:"build_date,omitempty"`
	Mode           string     `json:"mode"`
	CanSelfUpdate  bool       `json:"can_self_update"`
	Enabled        bool       `json:"enabled"`
	ServerURL      string     `json:"server_url"`
	Channel        string     `json:"channel"`
	TokenSet       bool       `json:"token_set"` // channel password configured
	Asset          string     `json:"asset"`
	AutoInstall    bool       `json:"auto_install"`
	CheckInterval  string     `json:"check_interval"`
	Maintenance    string     `json:"maintenance_window,omitempty"`
	Latest         *Release   `json:"latest,omitempty"`
	LatestVersion  string     `json:"latest_version,omitempty"`
	UpdateAvail    bool       `json:"update_available"`
	LastCheck      *time.Time `json:"last_check,omitempty"`
	LastApply      *time.Time `json:"last_apply,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	HasRollback    bool       `json:"has_rollback"`
	History        []History  `json:"history,omitempty"`
	InProgress     bool       `json:"in_progress"`
	ActiveStreams  int64      `json:"active_streams"`
	SignatureReady bool       `json:"signature_ready"` // minisign pubkey is configured
	RequireSig     bool       `json:"require_signature"`
}

// History records a single update attempt (check or apply).
type History struct {
	Time    time.Time `json:"time"`
	Event   string    `json:"event"` // "check", "apply", "rollback", "error"
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
	Ok      bool      `json:"ok"`
	Message string    `json:"message,omitempty"`
}

// Service is the long-lived updater instance.
type Service struct {
	mu        sync.Mutex
	cfg       atomic.Pointer[Config]
	current   string
	commit    string
	buildDate string
	mode      RunMode

	client *Client

	latest     *Release
	latestTime time.Time
	lastCheck  time.Time
	lastApply  time.Time
	lastError  string
	history    []History
	inProgress atomic.Bool

	// restartDrainer, if set, runs immediately before the process re-execs.
	// The HTTP server wires this to flush in-flight requests so a client
	// mid-download of a plugin isn't cut off by execve. Guarded by mu.
	restartDrainer func()
}

// New creates a Service. `current` must be the running version ("0.4" etc).
func New(cfg Config, current, commit, buildDate string) *Service {
	s := &Service{
		current:   current,
		commit:    commit,
		buildDate: buildDate,
		mode:      DetectMode(),
		history:   []History{},
	}
	s.cfg.Store(&cfg)
	s.client = NewClient(cfg.ServerURL, cfg.ServerToken, cfg.Channel)
	s.loadHistory()
	return s
}

// Reload swaps the config in-place. Callers use this on SIGHUP.
func (s *Service) Reload(cfg Config) {
	s.cfg.Store(&cfg)
	s.client = NewClient(cfg.ServerURL, cfg.ServerToken, cfg.Channel)
}

// Config returns the current config snapshot.
func (s *Service) Config() Config {
	if p := s.cfg.Load(); p != nil {
		return *p
	}
	return Config{}
}

// CurrentVersion returns the version the running binary was built from.
func (s *Service) CurrentVersion() string { return s.current }

// Mode returns the detected run mode.
func (s *Service) Mode() RunMode { return s.mode }

// AssetName returns the expected asset filename for this build.
func (s *Service) AssetName() string {
	c := s.Config()
	if c.AssetOverride != "" {
		return c.AssetOverride
	}
	return AssetName()
}

// Info builds a snapshot for UI consumption.
func (s *Service) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.Config()
	info := Info{
		Current:        s.current,
		Commit:         s.commit,
		BuildDate:      s.buildDate,
		Mode:           s.mode.String(),
		CanSelfUpdate:  s.mode.CanSelfUpdate(),
		Enabled:        cfg.Enabled,
		ServerURL:      cfg.ServerURL,
		Channel:        cfg.Channel,
		TokenSet:       cfg.ServerToken != "",
		Asset:          s.AssetName(),
		AutoInstall:    cfg.AutoInstall,
		CheckInterval:  cfg.CheckInterval.String(),
		Maintenance:    cfg.MaintenanceWindow,
		Latest:         s.latest,
		LastError:      s.lastError,
		History:        append([]History(nil), s.history...),
		InProgress:     s.inProgress.Load(),
		SignatureReady: cfg.MinisignPubKey != "",
		RequireSig:     cfg.RequireSignature,
	}
	if !s.lastCheck.IsZero() {
		t := s.lastCheck
		info.LastCheck = &t
	}
	if !s.lastApply.IsZero() {
		t := s.lastApply
		info.LastApply = &t
	}
	if s.latest != nil {
		info.LatestVersion = NormalizeVersion(s.latest.TagName)
		info.UpdateAvail = CompareVersions(info.LatestVersion, s.current) > 0
	}
	if cfg.ActiveStreams != nil {
		info.ActiveStreams = cfg.ActiveStreams()
	}
	if exe, err := os.Executable(); err == nil {
		if rp, err := filepath.EvalSymlinks(exe); err == nil {
			exe = rp
		}
		if _, err := os.Stat(exe + ".old"); err == nil {
			info.HasRollback = true
		}
	}
	return info
}

// Check talks to the update server and refreshes the cached `latest`.
func (s *Service) Check() (*Release, error) {
	cfg := s.Config()
	if cfg.ServerURL == "" {
		return nil, errors.New("updater: server_url not configured")
	}
	rel, err := s.client.Latest()
	s.mu.Lock()
	s.lastCheck = time.Now()
	if err != nil {
		s.lastError = err.Error()
		s.appendHistoryLocked(History{Time: s.lastCheck, Event: "check", Ok: false, Message: err.Error()})
		s.mu.Unlock()
		return nil, err
	}
	s.latest = rel
	s.latestTime = time.Now()
	s.lastError = ""
	newer := CompareVersions(NormalizeVersion(rel.TagName), s.current) > 0
	msg := fmt.Sprintf("latest=%s current=%s", NormalizeVersion(rel.TagName), s.current)
	s.appendHistoryLocked(History{
		Time: s.lastCheck, Event: "check", Ok: true, From: s.current, To: NormalizeVersion(rel.TagName), Message: msg,
	})
	s.mu.Unlock()
	_ = newer
	return rel, nil
}

// Apply downloads the new binary and atomically swaps it in for the running
// one. It does NOT re-exec — call Restart() after Apply() returns nil to
// activate the new code. Splitting the two lets HTTP handlers surface
// download/activate errors back to the caller before the process disappears.
//
// Errors at any step are also recorded in history + lastError so the admin
// UI can show them after the fact.
func (s *Service) Apply() error {
	if !s.mode.CanSelfUpdate() {
		return fmt.Errorf("updater: self-update not allowed in mode=%s", s.mode)
	}
	if !s.inProgress.CompareAndSwap(false, true) {
		return errors.New("updater: another update is already in progress")
	}
	defer s.inProgress.Store(false)

	s.mu.Lock()
	rel := s.latest
	s.mu.Unlock()
	if rel == nil {
		log.Info().Msg("updater: no cached release, refreshing from server")
		var err error
		rel, err = s.Check()
		if err != nil {
			log.Error().Err(err).Msg("updater: check failed")
			return err
		}
	}

	target := s.AssetName()
	log.Info().Str("asset", target).Str("release", rel.TagName).Msg("updater: applying release")

	asset := rel.FindAsset(target)
	if asset == nil {
		available := make([]string, 0, len(rel.Assets))
		for _, a := range rel.Assets {
			available = append(available, a.Name)
		}
		err := fmt.Errorf("updater: asset %q not found in release %s (available: %s)",
			target, rel.TagName, strings.Join(available, ", "))
		log.Error().Err(err).Msg("updater: asset lookup failed")
		s.recordError(err)
		return err
	}

	// If a minisign pubkey is configured, fetch SHA256SUMS + .minisig and
	// verify before trusting any checksum inside. signedSums is the verified
	// content, kept around so we can look up checksums for the tarball
	// assets later through the same signed file.
	cfg := s.Config()
	var signedSums []byte
	if cfg.MinisignPubKey != "" {
		sums, sig, ferr := rel.FetchSignedSums(cfg.ServerToken)
		if ferr != nil {
			err := fmt.Errorf("fetch signed sums: %w", ferr)
			log.Error().Err(err).Msg("updater: failed to fetch SHA256SUMS")
			s.recordError(err)
			return err
		}
		if sums == nil || sig == nil {
			if cfg.RequireSignature {
				err := errors.New("updater: release is missing SHA256SUMS/.minisig and require_signature=true")
				log.Error().Err(err).Msg("updater: signature required but missing")
				s.recordError(err)
				return err
			}
			log.Warn().Msg("updater: signature not present, falling back to inline SHA256")
		} else {
			if err := VerifyMinisign(sums, string(sig), cfg.MinisignPubKey); err != nil {
				wrapped := fmt.Errorf("signature: %w", err)
				log.Error().Err(wrapped).Msg("updater: minisign verification failed")
				s.recordError(wrapped)
				return wrapped
			}
			signedSums = sums
			log.Info().Msg("updater: minisign verified")
		}
	} else if cfg.RequireSignature {
		err := errors.New("updater: require_signature=true but no minisign_pubkey configured")
		log.Error().Err(err).Msg("updater: signature required but no pubkey")
		s.recordError(err)
		return err
	}

	sumOf := func(name string) string {
		if signedSums != nil {
			return sumForTarget(signedSums, name)
		}
		return rel.FindChecksum(name, cfg.ServerToken)
	}

	sum := sumOf(target)
	if sum == "" && signedSums != nil {
		err := fmt.Errorf("updater: SHA256SUMS is signed but does not list %q", target)
		log.Error().Err(err).Msg("updater: signed sums missing target")
		s.recordError(err)
		return err
	}

	log.Info().Str("url", asset.BrowserDownloadURL).Str("sha256", sum).Msg("updater: downloading asset")
	dl, err := DownloadAsset(asset.BrowserDownloadURL, sum, asset.Size, cfg.ServerToken)
	if err != nil {
		wrapped := fmt.Errorf("download: %w", err)
		log.Error().Err(wrapped).Msg("updater: download failed")
		s.recordError(wrapped)
		return wrapped
	}
	log.Info().Int64("size", dl.Size).Str("sha256", dl.SHA256).Str("staged", dl.StagedPath).Msg("updater: download complete")

	// Apply plugins.tar.gz / wwwroot.tar.gz BEFORE swapping the binary, so
	// that if any tarball fails we can bail out without leaving a half-
	// migrated install (new binary + old plugins). Tarballs that succeed
	// before a later failure are rolled back in reverse order.
	var rolledTargets []string
	applyTarball := func(name, target string) error {
		if target == "" {
			return nil
		}
		a := rel.FindAsset(name)
		if a == nil {
			log.Info().Str("asset", name).Msg("updater: tarball not present in release, skipping")
			return nil
		}
		tSum := sumOf(name)
		if tSum == "" && signedSums != nil {
			return fmt.Errorf("updater: SHA256SUMS does not list %q", name)
		}
		log.Info().Str("asset", name).Str("target", target).Str("sha256", tSum).Msg("updater: applying tarball")
		if _, err := ApplyTarballAsset(a.BrowserDownloadURL, tSum, a.Size, target, cfg.ServerToken); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		rolledTargets = append(rolledTargets, target)
		return nil
	}
	rollbackTarballs := func() {
		for i := len(rolledTargets) - 1; i >= 0; i-- {
			if err := RollbackTarballAsset(rolledTargets[i]); err != nil && !errors.Is(err, ErrNoRollback) {
				log.Warn().Err(err).Str("target", rolledTargets[i]).Msg("updater: tarball rollback failed")
			}
		}
	}

	if err := applyTarball(PluginsAssetName, cfg.PluginsDir); err != nil {
		_ = os.Remove(dl.StagedPath)
		rollbackTarballs()
		wrapped := fmt.Errorf("plugins tarball: %w", err)
		log.Error().Err(wrapped).Msg("updater: tarball apply failed")
		s.recordError(wrapped)
		return wrapped
	}
	if err := applyTarball(WwwrootAssetName, cfg.WwwrootDir); err != nil {
		_ = os.Remove(dl.StagedPath)
		rollbackTarballs()
		wrapped := fmt.Errorf("wwwroot tarball: %w", err)
		log.Error().Err(wrapped).Msg("updater: tarball apply failed")
		s.recordError(wrapped)
		return wrapped
	}

	backup, err := Activate(dl.StagedPath)
	if err != nil {
		// Binary swap failed — undo tarballs so the running process keeps
		// matching the on-disk plugins/wwwroot.
		rollbackTarballs()
		_ = os.Remove(dl.StagedPath)
		wrapped := fmt.Errorf("activate: %w", err)
		log.Error().Err(wrapped).Msg("updater: activate failed")
		s.recordError(wrapped)
		return wrapped
	}
	log.Info().Str("backup", backup).Int("tarballs", len(rolledTargets)).Msg("updater: binary swapped — ready to restart")

	s.mu.Lock()
	s.lastApply = time.Now()
	msg := "activated, restart pending"
	if len(rolledTargets) > 0 {
		msg = fmt.Sprintf("activated (+%d tarballs), restart pending", len(rolledTargets))
	}
	s.appendHistoryLocked(History{
		Time: s.lastApply, Event: "apply", Ok: true,
		From: s.current, To: NormalizeVersion(rel.TagName),
		Message: msg,
	})
	s.saveHistoryLocked()
	s.mu.Unlock()

	return nil
}

// SetRestartDrainer registers a callback that runs once, synchronously, just
// before the process re-execs. The HTTP server uses it to drain in-flight
// requests. Set it before the auto-check loop starts so the write
// happens-before any background Restart() (no data race).
func (s *Service) SetRestartDrainer(fn func()) {
	s.mu.Lock()
	s.restartDrainer = fn
	s.mu.Unlock()
}

// reexecProcess replaces the running process image (execve on Unix; see
// restart_unix.go). It is a package-level var purely so tests can stub it —
// the real implementation never returns on success, which would otherwise
// terminate the test runner.
var reexecProcess = Restart

// Restart re-executes the running binary. On Linux/macOS this uses execve so
// the PID is preserved (systemd-friendly). It does NOT return on success —
// the process image is replaced. Callers typically invoke this from a goroutine
// after Apply() succeeds, so the HTTP response can flush before the process
// disappears.
//
// Before re-exec it runs the registered drainer (if any) to let in-flight HTTP
// requests finish. execve does not wait for active connections; without the
// drain, a client mid-download of a plugin JS (e.g. wasm_loader.js) receives a
// truncated body. For gzip/chunked responses the partial stream decompresses
// to garbage and old WebKit (LG webOS) throws "Unexpected token ILLEGAL".
func (s *Service) Restart() error {
	s.mu.Lock()
	drain := s.restartDrainer
	s.mu.Unlock()
	if drain != nil {
		log.Info().Msg("updater: draining in-flight requests before re-exec")
		drain()
	}

	log.Info().Msg("updater: re-executing process")
	err := reexecProcess()
	if err != nil {
		log.Error().Err(err).Msg("updater: restart failed — binary swapped but process did not re-exec")
		s.recordError(fmt.Errorf("restart: %w", err))
	}
	return err
}

// RollbackNow swaps the previous binary back in. Plugin/wwwroot tarball
// backups (target+".old") are also reverted when present, so the rolled
// binary matches the rolled assets.
func (s *Service) RollbackNow() error {
	if !s.mode.CanSelfUpdate() {
		return fmt.Errorf("updater: rollback not allowed in mode=%s", s.mode)
	}
	if err := Rollback(); err != nil {
		s.recordError(fmt.Errorf("rollback: %w", err))
		return err
	}
	cfg := s.Config()
	rolledTarballs := 0
	for _, target := range []string{cfg.PluginsDir, cfg.WwwrootDir} {
		if target == "" {
			continue
		}
		switch err := RollbackTarballAsset(target); {
		case err == nil:
			rolledTarballs++
		case errors.Is(err, ErrNoRollback):
			// No backup → tarball wasn't applied last time, nothing to do.
		default:
			log.Warn().Err(err).Str("target", target).Msg("updater: tarball rollback failed")
		}
	}
	s.mu.Lock()
	msg := "previous binary restored"
	if rolledTarballs > 0 {
		msg = fmt.Sprintf("previous binary restored (+%d tarballs)", rolledTarballs)
	}
	s.appendHistoryLocked(History{Time: time.Now(), Event: "rollback", Ok: true, Message: msg})
	s.saveHistoryLocked()
	s.mu.Unlock()
	return Restart()
}

// ---------------------------------------------------------------------------
// Background loop
// ---------------------------------------------------------------------------

// RunAutoCheck is started in its own goroutine by the host. It ticks on
// CheckInterval, fetches the latest release, and (if AutoInstall is enabled
// and the maintenance window is active) applies the update.
func (s *Service) RunAutoCheck(stop <-chan struct{}) {
	for {
		cfg := s.Config()
		if !cfg.Enabled || cfg.CheckInterval <= 0 {
			// Disabled — poll config every minute for re-enable.
			select {
			case <-stop:
				return
			case <-time.After(time.Minute):
				continue
			}
		}

		// Initial delay so we don't hammer GitHub right at boot.
		select {
		case <-stop:
			return
		case <-time.After(90 * time.Second):
		}

		rel, err := s.Check()
		if err == nil && cfg.AutoInstall && rel != nil {
			latest := NormalizeVersion(rel.TagName)
			if CompareVersions(latest, s.current) > 0 && s.mode.CanSelfUpdate() && inMaintenanceWindow(cfg.MaintenanceWindow) {
				if cfg.PreventDuringStreams && cfg.ActiveStreams != nil && cfg.ActiveStreams() > 0 {
					s.mu.Lock()
					s.appendHistoryLocked(History{
						Time: time.Now(), Event: "check", Ok: true,
						Message: fmt.Sprintf("auto-install deferred: %d active streams", cfg.ActiveStreams()),
					})
					s.mu.Unlock()
				} else {
					if err := s.Apply(); err == nil {
						_ = s.Restart() // process is replaced on success
					}
				}
			}
		}

		// Wait one interval before next check.
		select {
		case <-stop:
			return
		case <-time.After(cfg.CheckInterval):
		}
	}
}

// inMaintenanceWindow returns true if `now` is inside the given HH:MM-HH:MM
// range (local time). An empty window means "always allowed".
func inMaintenanceWindow(window string) bool {
	if window == "" {
		return true
	}
	var h1, m1, h2, m2 int
	if _, err := fmt.Sscanf(window, "%d:%d-%d:%d", &h1, &m1, &h2, &m2); err != nil {
		return true
	}
	now := time.Now()
	nowMin := now.Hour()*60 + now.Minute()
	start := h1*60 + m1
	end := h2*60 + m2
	if start == end {
		return true
	}
	if start < end {
		return nowMin >= start && nowMin <= end
	}
	// Window crosses midnight.
	return nowMin >= start || nowMin <= end
}

// ---------------------------------------------------------------------------
// History persistence
// ---------------------------------------------------------------------------

func (s *Service) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = err.Error()
	s.appendHistoryLocked(History{Time: time.Now(), Event: "error", Ok: false, Message: err.Error()})
	s.saveHistoryLocked()
}

func (s *Service) appendHistoryLocked(h History) {
	s.history = append(s.history, h)
	if len(s.history) > 100 {
		s.history = s.history[len(s.history)-100:]
	}
}

type persistedState struct {
	History []History `json:"history"`
}

func (s *Service) loadHistory() {
	path := s.Config().HistoryPath
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return
	}
	s.mu.Lock()
	s.history = st.History
	s.mu.Unlock()
}

func (s *Service) saveHistoryLocked() {
	path := s.Config().HistoryPath
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	data, err := json.MarshalIndent(persistedState{History: s.history}, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err == nil {
		_ = os.Rename(tmp, path)
	}
}
