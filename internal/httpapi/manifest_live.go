package httpapi

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/modules"
)

// manifestCandidatePaths returns every location module/manifest.json may live
// in, in lookup order: the runtime dir the admin panel writes to (relToRuntime
// honours LAMPAC_GO_HOME before LAMPAC_GO_REPO_ROOT), the configured repo
// root, and the legacy .NET location. Keeping readers on this list means the
// file the admin form writes is the file the plugin gates actually consult —
// historically they could diverge when LAMPAC_GO_HOME was set.
func manifestCandidatePaths(cfg config.Config) []string {
	rel := filepath.Join("module", "manifest.json")
	paths := []string{relToRuntime(rel)}
	if root := strings.TrimSpace(cfg.Compat.RepoRoot); root != "" {
		if p := filepath.Join(root, rel); p != paths[0] {
			paths = append(paths, p)
		}
	}
	paths = append(paths, "/home/module/manifest.json")
	return paths
}

// liveManifestCache keeps the last successful (or failed) disk read for 60s so
// hot paths (/lampainit.js, /on.js on TV fleets) don't stat the file per hit.
var liveManifestCache = struct {
	sync.Mutex
	key       string
	mods      []modules.RootModule
	expiresAt time.Time
}{}

// liveManifest returns the current module manifest, re-reading it from disk
// with a 60s TTL so admin edits apply without a server restart. When no
// manifest file can be read it returns the boot-time fallback (which is nil
// on manifest-less installs — callers already treat an empty manifest as
// "all modules enabled").
func liveManifest(cfg config.Config, fallback []modules.RootModule) []modules.RootModule {
	candidates := manifestCandidatePaths(cfg)
	key := strings.Join(candidates, "\x00")
	now := time.Now()

	liveManifestCache.Lock()
	defer liveManifestCache.Unlock()

	if liveManifestCache.key != key || !now.Before(liveManifestCache.expiresAt) {
		var mods []modules.RootModule
		for _, p := range candidates {
			if loaded, err := modules.LoadManifest(p); err == nil {
				mods = loaded
				break
			}
		}
		liveManifestCache.key = key
		liveManifestCache.mods = mods
		liveManifestCache.expiresAt = now.Add(60 * time.Second)
	}

	if liveManifestCache.mods == nil {
		return fallback
	}
	return liveManifestCache.mods
}
