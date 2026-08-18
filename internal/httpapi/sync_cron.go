package httpapi

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"

	"github.com/rs/zerolog/log"
)

// SyncCron runs a background poller that pulls config from the master server
// when running in slave mode. Mirrors the behavior of .NET SyncCron.cs.
type SyncCron struct {
	cfg     config.Config
	client  *http.Client
	running int32
	stop    chan struct{}
}

// NewSyncCron creates a new SyncCron instance.
func NewSyncCron(cfg config.Config) *SyncCron {
	return &SyncCron{
		cfg:    cfg,
		client: httpclient.New(10 * time.Second),
		stop:   make(chan struct{}),
	}
}

// Start begins the background polling loop if sync slave mode is enabled.
// It waits 10 seconds before the first poll, then polls every second
// (matching .NET behavior). The actual HTTP request only fires when the
// inner guard allows it (prevents overlapping requests).
func (sc *SyncCron) Start() {
	sync := sc.cfg.Sync
	if !sync.Enable || sync.Type != "slave" || sync.APIHost == "" || sync.APIPasswd == "" {
		log.Info().
			Bool("enable", sync.Enable).
			Str("type", sync.Type).
			Msg("sync-cron: slave mode not configured, skipping")
		return
	}

	log.Info().
		Str("master", sync.APIHost).
		Bool("sync_full", sync.SyncFull).
		Msg("sync-cron: starting slave poller")

	go sc.loop()
}

// Stop signals the background loop to exit.
func (sc *SyncCron) Stop() {
	select {
	case sc.stop <- struct{}{}:
	default:
	}
}

func (sc *SyncCron) loop() {
	// Initial delay — 10 seconds, same as .NET.
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-sc.stop:
			return
		case <-timer.C:
			sc.poll()
			// Poll interval: 20 minutes for full sync, 60 seconds for users-only.
			// .NET polls every 1s but only fires when the semaphore is free.
			// We use a sane interval instead.
			interval := 60 * time.Second
			if sc.cfg.Sync.SyncFull {
				interval = 20 * time.Minute
			}
			timer.Reset(interval)
		}
	}
}

func (sc *SyncCron) poll() {
	// Prevent overlapping polls.
	if !atomic.CompareAndSwapInt32(&sc.running, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&sc.running, 0)

	sync := sc.cfg.Sync
	target := strings.TrimRight(sync.APIHost, "/") + "/api/sync"

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		log.Debug().Err(err).Msg("sync-cron: failed to create request")
		return
	}
	req.Header.Set("localrequest", sync.APIPasswd)

	resp, err := sc.client.Do(req)
	if err != nil {
		log.Debug().Err(err).Str("target", target).Msg("sync-cron: request failed")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Msg("sync-cron: non-200 response from master")
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB max
	if err != nil {
		log.Debug().Err(err).Msg("sync-cron: failed to read response")
		return
	}

	if len(body) == 0 || string(body) == "error" {
		log.Debug().Msg("sync-cron: master returned error or empty body")
		return
	}

	// Validate that we got valid JSON.
	var parsed map[string]any
	if err := stdjson.Unmarshal(body, &parsed); err != nil {
		log.Debug().Err(err).Msg("sync-cron: invalid JSON from master")
		return
	}

	// Extract and persist accsdb.users to users.json (both full and users-only sync).
	syncUsersToFile(parsed)

	if sync.SyncFull {
		sc.applyFullSync(parsed)
	} else {
		log.Info().Msg("sync-cron: users synced from master")
	}
}

// syncUsersToFile extracts accsdb.users from the master response and writes
// them to users.json. The auth middleware reads users.json on every request,
// so no config reload is needed for user changes to take effect.
func syncUsersToFile(parsed map[string]any) {
	accsdb, _ := parsed["accsdb"].(map[string]any)
	if accsdb == nil {
		return
	}
	users, ok := accsdb["users"]
	if !ok {
		return
	}

	data, err := stdjson.MarshalIndent(users, "", "  ")
	if err != nil {
		log.Debug().Err(err).Msg("sync-cron: failed to marshal users")
		return
	}

	path := relToRuntime("users.json")
	if err := writeFileAtomic(path, data); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("sync-cron: failed to write users.json")
		return
	}
	log.Info().Msg("sync-cron: users.json synced from master")
}

// applyFullSync applies the full config from the master to the slave's
// config.toml (preserving the slave's own [sync] section) and triggers
// a hot-reload so the running server picks up the changes.
func (sc *SyncCron) applyFullSync(parsed map[string]any) {
	sync := sc.cfg.Sync

	// The master response contains both PascalCase keys (from loadMergedConf)
	// and raw TOML keys (from loadConfigTOMLAsMap). Use applyLegacyMapToTOML
	// to map PascalCase → TOML sections, then merge any remaining raw TOML keys.
	if err := updateConfigTOMLMap(func(tomlRoot map[string]any) {
		// Apply PascalCase keys (Rezka, Collaps, etc.) → online.rezka, online.collaps.
		applyLegacyMapToTOML(parsed, tomlRoot)

		// Also merge raw TOML sections that the master includes directly
		// (e.g. "online", "accsdb", "server", "torrserver", "web", etc.).
		// Skip PascalCase keys and "sync" (preserved below).
		for k, v := range parsed {
			if k == "sync" {
				continue
			}
			// Raw TOML keys start with lowercase; PascalCase starts with uppercase.
			if len(k) == 0 || (k[0] >= 'A' && k[0] <= 'Z') {
				continue
			}
			// Only merge map values (TOML sections), not scalars.
			section, ok := v.(map[string]any)
			if !ok {
				// Top-level scalar (e.g. "disable_eng").
				tomlRoot[k] = v
				continue
			}
			existing, _ := tomlRoot[k].(map[string]any)
			if existing == nil {
				existing = make(map[string]any)
				tomlRoot[k] = existing
			}
			for sk, sv := range section {
				existing[sk] = sv
			}
		}

		// Preserve slave's own sync settings so it stays a slave.
		tomlRoot["sync"] = map[string]any{
			"enable":     sync.Enable,
			"type":       sync.Type,
			"api_host":   sync.APIHost,
			"api_passwd": sync.APIPasswd,
			"sync_full":  sync.SyncFull,
		}
	}); err != nil {
		log.Warn().Err(err).Msg("sync-cron: failed to apply full sync to config.toml")
		return
	}

	log.Info().Msg("sync-cron: full config synced from master → config.toml + reload")
}

// writeFileAtomic writes data to a file atomically via a temp file + rename.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
