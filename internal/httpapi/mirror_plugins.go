package httpapi

// mirror_plugins.go — Фаза 3: plugin (and optional wwwroot) sync.
//
// Origin side: serves a gzip tar of ubuntu/plugins/* (or wwwroot/) built from
// the live directory, cached by the directory's max mtime, with a content
// ETag so unchanged pulls are answered 304.
//
// Mirror side: a background syncer pulls the tarball on an interval, sends
// If-None-Match to skip unchanged content, and atomically swaps new content in
// via updater.ApplyTarballFile (same extract/merge/swap the self-updater uses,
// preserving local custom plugins).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/updater"

	"github.com/rs/zerolog/log"
)

// ----------------------------- origin side --------------------------------

// assetTarballCache builds + memoizes a gzip tar of a directory, rebuilding
// only when the directory's max mtime changes. The sha is content-based
// (tar entries carry no mtime) so identical content yields a stable ETag.
type assetTarballCache struct {
	name  string        // top-level dir inside the archive ("plugins"/"wwwroot")
	dirFn func() string // resolves the live directory at call time
	mu    sync.Mutex
	sha   string
	data  []byte
	built int64 // dir max mtime (unixnano) the cache was built for
}

func (a *assetTarballCache) get() ([]byte, string, bool) {
	dir := strings.TrimSpace(a.dirFn())
	if dir == "" {
		return nil, "", false
	}
	mt := dirMaxMtime(dir)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.data != nil && a.built == mt {
		return a.data, a.sha, true
	}
	data, sha, err := buildTarGz(dir, a.name)
	if err != nil {
		log.Warn().Err(err).Str("dir", dir).Msg("mirror: failed to build asset tarball")
		return nil, "", false
	}
	a.data, a.sha, a.built = data, sha, mt
	return data, sha, true
}

// mirrorOriginAssetHandler serves the asset tarball to authenticated mirrors,
// honoring If-None-Match (ETag) so unchanged content returns 304.
func mirrorOriginAssetHandler(cache *assetTarballCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isMirrorOriginRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
			return
		}
		data, sha, ok := cache.get()
		if !ok {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}
		etag := `"` + sha + `"`
		w.Header().Set("ETag", etag)
		if inm := r.Header.Get("If-None-Match"); inm != "" && strings.Contains(inm, sha) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

// dirMaxMtime returns the latest mtime (unixnano) across dir and its entries,
// used purely as a cheap "did anything change" trigger to rebuild the cache.
func dirMaxMtime(dir string) int64 {
	var max int64
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil {
			if t := info.ModTime().UnixNano(); t > max {
				max = t
			}
		}
		return nil
	})
	return max
}

// buildTarGz packs dir into a gzip tar with a single top-level directory named
// topName (matching release.sh's wrapper, so updater.ApplyTarballFile detects
// it). Entries carry no mtime → the sha is content-only and stable.
func buildTarGz(dir, topName string) ([]byte, string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := topName + "/" + filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: name + "/", Mode: 0o755, Typeflag: tar.TypeDir})
		}
		if !info.Mode().IsRegular() {
			return nil // skip symlinks/devices — trees only hold regular files
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(), Typeflag: tar.TypeReg,
		}); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	if err := gz.Close(); err != nil {
		return nil, "", err
	}
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

// ----------------------------- mirror side --------------------------------

// mirrorAssetSyncer pulls an asset tarball from the origin on an interval and
// atomically swaps it into targetDir.
type mirrorAssetSyncer struct {
	name      string // "plugins" | "wwwroot"
	assetPath string // "/api/cluster/plugins.tar.gz"
	targetDir string
	cfgFn     func() config.MirrorConfig
	http      *http.Client
	lastSha   string
	stop      chan struct{}
}

func newMirrorAssetSyncer(name, assetPath, targetDir string, cfgFn func() config.MirrorConfig) *mirrorAssetSyncer {
	return &mirrorAssetSyncer{
		name:      name,
		assetPath: assetPath,
		targetDir: targetDir,
		cfgFn:     cfgFn,
		http:      httpclient.New(2 * time.Minute),
		stop:      make(chan struct{}),
	}
}

func (s *mirrorAssetSyncer) Start() {
	if strings.TrimSpace(s.targetDir) == "" {
		log.Warn().Str("asset", s.name).Msg("mirror: no target dir, asset sync disabled")
		return
	}
	go s.loop()
}

func (s *mirrorAssetSyncer) Stop() {
	select {
	case s.stop <- struct{}{}:
	default:
	}
}

func (s *mirrorAssetSyncer) loop() {
	timer := time.NewTimer(15 * time.Second) // first pull shortly after boot
	defer timer.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-timer.C:
			s.syncOnce()
			min := s.cfgFn().SyncPluginsMin
			if min <= 0 {
				min = 5
			}
			timer.Reset(time.Duration(min) * time.Minute)
		}
	}
}

func (s *mirrorAssetSyncer) syncOnce() {
	cfg := s.cfgFn()
	if cfg.APIHost == "" {
		return
	}
	req, err := http.NewRequest(http.MethodGet, cfg.APIHost+s.assetPath, nil)
	if err != nil {
		return
	}
	req.Header.Set("localrequest", cfg.APIPasswd)
	if s.lastSha != "" {
		req.Header.Set("If-None-Match", `"`+s.lastSha+`"`)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("asset", s.name).Msg("mirror: asset pull failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return // unchanged
	}
	if resp.StatusCode != http.StatusOK {
		log.Warn().Int("status", resp.StatusCode).Str("asset", s.name).Msg("mirror: asset pull non-200")
		return
	}

	tmp, err := os.CreateTemp("", s.name+"-*.tar.gz")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	_, copyErr := io.Copy(tmp, resp.Body)
	_ = tmp.Close()
	if copyErr != nil {
		log.Warn().Err(copyErr).Str("asset", s.name).Msg("mirror: asset download write failed")
		return
	}

	if _, err := updater.ApplyTarballFile(tmpPath, s.targetDir); err != nil {
		log.Warn().Err(err).Str("asset", s.name).Str("target", s.targetDir).Msg("mirror: asset apply failed")
		return
	}
	s.lastSha = strings.Trim(resp.Header.Get("ETag"), `"`)
	log.Info().Str("asset", s.name).Str("target", s.targetDir).Msg("mirror: asset synced from origin")
}
