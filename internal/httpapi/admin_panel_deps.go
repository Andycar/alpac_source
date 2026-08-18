package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/jacred"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

type depInfo struct {
	Name      string `json:"name"`
	Binary    string `json:"binary"`
	Status    string `json:"status"`           // "ok" | "missing"
	Version   string `json:"version"`          // version string or ""
	Note      string `json:"note"`             // optional hint
	Latest    string `json:"latest,omitempty"` // latest available version
	Updatable bool   `json:"updatable"`        // can be updated/installed from admin panel
	Updating  bool   `json:"updating"`         // update in progress
	Group     string `json:"group"`            // category group
	PipPkg    bool   `json:"pip_pkg"`          // installed via pip (python package)
}

// ---------------------------------------------------------------------------
//  Package manager detection & install recipes
// ---------------------------------------------------------------------------

// detectPkgManager returns the system package manager command.
func detectPkgManager() string {
	for _, pm := range []string{"apt-get", "dnf", "yum", "apk"} {
		if _, err := exec.LookPath(pm); err == nil {
			return pm
		}
	}
	return ""
}

// pkgRecipe maps a dependency binary name to system package names per manager.
// Key format: binary → {apt-get: pkg, dnf: pkg, yum: pkg, apk: pkg}.
var pkgRecipe = map[string]map[string]string{
	"node": {
		"apt-get": "nodejs",
		"dnf":     "nodejs",
		"yum":     "nodejs",
		"apk":     "nodejs",
	},
	"chromium": {
		"apt-get": "chromium-browser",
		"dnf":     "chromium",
		"yum":     "chromium",
		"apk":     "chromium",
	},
	"chromium-browser": {
		"apt-get": "chromium-browser",
		"dnf":     "chromium",
		"yum":     "chromium",
		"apk":     "chromium",
	},
	"proxychains4": {
		"apt-get": "proxychains4",
		"dnf":     "proxychains-ng",
		"yum":     "proxychains-ng",
		"apk":     "proxychains-ng",
	},
	"ffmpeg": {
		"apt-get": "ffmpeg",
		"dnf":     "ffmpeg-free",
		"yum":     "ffmpeg",
		"apk":     "ffmpeg",
	},
	"ffprobe": {
		"apt-get": "ffmpeg",
		"dnf":     "ffmpeg-free",
		"yum":     "ffmpeg",
		"apk":     "ffmpeg",
	},
	"python3": {
		"apt-get": "python3",
		"dnf":     "python3",
		"yum":     "python3",
		"apk":     "python3",
	},
	"pip3": {
		"apt-get": "python3-pip",
		"dnf":     "python3-pip",
		"yum":     "python3-pip",
		"apk":     "py3-pip",
	},
	"curl": {
		"apt-get": "curl",
		"dnf":     "curl",
		"yum":     "curl",
		"apk":     "curl",
	},
	"wget": {
		"apt-get": "wget",
		"dnf":     "wget",
		"yum":     "wget",
		"apk":     "wget",
	},
}

// pipRecipes lists pip package names that can be installed via pip.
var pipRecipes = map[string]bool{
	"yt-dlp-ejs":                true,
	"bgutil-ytdlp-pot-provider": true,
}

// canInstall returns true if we have an install recipe for the binary.
func canInstall(binary string) bool {
	if _, ok := pkgRecipe[binary]; ok {
		return true
	}
	if pipRecipes[binary] {
		return true
	}
	// yt-dlp and TorrServer have dedicated updaters.
	if binary == "yt-dlp" || strings.HasPrefix(binary, "TorrServer") {
		return true
	}
	return false
}

// depGroup defines a category group with icon and label.
type depGroup struct {
	ID    string    `json:"id"`
	Icon  string    `json:"icon"`
	Label string    `json:"label"`
	Deps  []depInfo `json:"deps"`
}

// depUpdateState tracks background update progress per dependency.
var depUpdateState = struct {
	sync.RWMutex
	m map[string]*depUpdateProgress
}{m: make(map[string]*depUpdateProgress)}

type depUpdateProgress struct {
	InProgress bool   `json:"in_progress"`
	Done       bool   `json:"done"`
	Error      string `json:"error,omitempty"`
	OldVersion string `json:"old_version,omitempty"`
	NewVersion string `json:"new_version,omitempty"`
	StartedAt  time.Time
}

// latestVersionCache caches GitHub release tag checks.
var latestVersionCache = struct {
	sync.RWMutex
	m  map[string]string
	ts map[string]time.Time
}{m: make(map[string]string), ts: make(map[string]time.Time)}

const latestVersionCacheTTL = 10 * time.Minute

// githubLatestTag fetches the latest release tag from a GitHub repo.
// Uses GitHub API redirect: /repos/{owner}/{repo}/releases/latest → tag_name.
func githubLatestTag(owner, repo string) string {
	cacheKey := owner + "/" + repo

	latestVersionCache.RLock()
	if v, ok := latestVersionCache.m[cacheKey]; ok {
		if time.Since(latestVersionCache.ts[cacheKey]) < latestVersionCacheTTL {
			latestVersionCache.RUnlock()
			return v
		}
	}
	latestVersionCache.RUnlock()

	client := httpclient.New(10 * time.Second)
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "lampac-go")

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(body, &release) != nil {
		return ""
	}

	tag := strings.TrimPrefix(release.TagName, "v")

	latestVersionCache.Lock()
	latestVersionCache.m[cacheKey] = tag
	latestVersionCache.ts[cacheKey] = time.Now()
	latestVersionCache.Unlock()

	return tag
}

func checkDep(name, binary, note string) depInfo {
	d := depInfo{Name: name, Binary: binary, Note: note, Status: "missing"}
	path, err := exec.LookPath(binary)
	if err != nil {
		return d
	}
	d.Status = "ok"

	// Try to get version.
	var out []byte
	switch binary {
	case "ffmpeg", "ffprobe":
		out, _ = exec.Command(path, "-version").Output()
		// first line: "ffmpeg version 6.1.1 ..."
		if lines := strings.SplitN(string(out), "\n", 2); len(lines) > 0 {
			parts := strings.Fields(lines[0])
			if len(parts) >= 3 {
				d.Version = parts[2]
			}
		}
	case "node":
		out, _ = exec.Command(path, "--version").Output()
		d.Version = strings.TrimSpace(string(out))
	case "deno":
		out, _ = exec.Command(path, "--version").Output()
		if lines := strings.SplitN(string(out), "\n", 2); len(lines) > 0 {
			d.Version = strings.TrimSpace(lines[0])
		}
	case "yt-dlp":
		out, _ = exec.Command(path, "--version").Output()
		d.Version = strings.TrimSpace(string(out))
	case "proxychains4":
		// no --version that works reliably
		d.Version = "installed"
	default:
		out, _ = exec.Command(path, "--version").Output()
		s := strings.TrimSpace(string(out))
		if len(s) > 80 {
			s = s[:80]
		}
		d.Version = s
	}

	return d
}

// checkPipPackage checks if a Python pip package is installed.
func checkPipPackage(name, pipPkg, note string) depInfo {
	d := depInfo{Name: name, Binary: pipPkg, Note: note, Status: "missing", PipPkg: true}
	// Try pip show
	out, err := exec.Command("pip", "show", pipPkg).Output()
	if err != nil {
		out, err = exec.Command("pip3", "show", pipPkg).Output()
	}
	if err != nil {
		return d
	}
	d.Status = "ok"
	// Parse Version: line
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Version:") {
			d.Version = strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
			break
		}
	}
	if d.Version == "" {
		d.Version = "installed"
	}
	return d
}

func tgAdminDepsHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		checkLatest := r.URL.Query().Get("check_latest") == "1"

		// Helper: mark updatable if we have an install recipe.
		markUpdatable := func(d *depInfo) {
			if canInstall(d.Binary) {
				d.Updatable = true
			}
		}

		// --- YouTube group ---
		ytdlp := checkDep("yt-dlp", "yt-dlp", "Загрузка и извлечение видео с YouTube")
		if serverReady() {
			localYtdlp := filepath.Join(liveConfig(config.Config{}).Compat.RepoRoot, "bin", "yt-dlp")
			if out, err := exec.Command(localYtdlp, "--version").Output(); err == nil {
				localVer := strings.TrimSpace(string(out))
				if ytdlp.Status == "missing" || localVer > ytdlp.Version {
					ytdlp.Status = "ok"
					ytdlp.Version = localVer
				}
			}
		}
		ytdlp.Updatable = true
		ytdlp.Group = "youtube"

		ejsSolver := checkPipPackage("EJS Challenge Solver", "yt-dlp-ejs",
			"Расшифровка nsig-подписей YouTube (обязательно)")
		ejsSolver.Group = "youtube"
		ejsSolver.Updatable = true

		potProvider := checkPipPackage("PO Token Provider", "bgutil-ytdlp-pot-provider",
			"Генерация PO Token для обхода защиты YouTube")
		potProvider.Group = "youtube"
		potProvider.Updatable = true

		jsNode := checkDep("Node.js", "node", "JS runtime для yt-dlp (v20+)")
		jsNode.Group = "youtube"
		markUpdatable(&jsNode)

		python3 := checkDep("Python 3", "python3", "Нужен для pip-пакетов (yt-dlp-ejs и др.)")
		python3.Group = "youtube"
		markUpdatable(&python3)

		pipDep := depInfo{Name: "pip", Binary: "pip3", Note: "Менеджер пакетов Python", Group: "youtube", Updatable: true}
		if _, err := exec.LookPath("pip3"); err == nil {
			pipDep.Status = "ok"
			if out, e := exec.Command("pip3", "--version").Output(); e == nil {
				pipDep.Version = strings.TrimSpace(string(out))
				if len(pipDep.Version) > 60 {
					pipDep.Version = pipDep.Version[:60]
				}
			} else {
				pipDep.Version = "installed"
			}
		} else if _, err := exec.LookPath("pip"); err == nil {
			pipDep.Status = "ok"
			pipDep.Version = "installed"
		} else {
			pipDep.Status = "missing"
		}

		// --- Balancers group ---
		chromium := checkDep("Chromium", "chromium", "Нужен для Mirage, Kinobase и др.")
		if chromium.Status == "missing" {
			chromium = checkDep("Chromium", "chromium-browser", "Нужен для Mirage, Kinobase и др.")
		}
		chromium.Group = "balancers"
		markUpdatable(&chromium)

		proxychains := checkDep("Proxychains4", "proxychains4", "Проксирование через SOCKS/HTTP для балансеров")
		proxychains.Group = "balancers"
		markUpdatable(&proxychains)

		// --- Torrents group ---
		tsBinary := "TorrServer-linux"
		if runtime.GOOS == "darwin" {
			tsBinary = "TorrServer-darwin"
		}
		ts := depInfo{Name: "TorrServer", Binary: tsBinary, Status: "missing",
			Note: "Стриминг торрентов", Updatable: true, Group: "torrents"}
		if serverReady() {
			cfg := liveConfig(config.Config{})
			homeDir := cfg.TorrServer.HomeDir
			if homeDir == "" {
				homeDir = filepath.Join(cfg.Compat.RepoRoot, "torrserver")
			}
			tsPath := filepath.Join(homeDir, tsBinary)
			if out, err := exec.Command(tsPath, "--version").Output(); err == nil {
				ts.Status = "ok"
				ts.Version = strings.TrimSpace(string(out))
			} else if _, statErr := os.Stat(tsPath); statErr == nil {
				ts.Status = "ok"
				ts.Version = "installed"
			}
		}

		// JacRed — свой (self-hosted) торрент-парсер, управляется сервером.
		jr := depInfo{Name: "JacRed", Binary: "JacRed", Status: "missing",
			Note: "Свой торрент-парсер (jacred-fdb); включается в конфиге: [parser] jacred_local", Updatable: true, Group: "torrents"}
		if serverReady() {
			cfg := liveConfig(config.Config{})
			st := jacredStatusSnapshot(cfg)
			if st.Installed {
				jr.Status = "ok"
				jr.Version = st.Version
				if jr.Version == "" {
					jr.Version = "installed"
				}
				switch {
				case st.Healthy:
					jr.Note = fmt.Sprintf("Свой торрент-парсер: работает, база %d МБ", st.DBSizeMB)
				case cfg.Parser.JacRedLocal:
					stage := st.Stage
					if stage == "" {
						stage = "запускается"
					}
					jr.Note = "Свой торрент-парсер: " + stage
				default:
					jr.Note = "Свой торрент-парсер: установлен, выключен ([parser] jacred_local = false)"
				}
			}
		}

		// --- Media group ---
		ffmpeg := checkDep("FFmpeg", "ffmpeg", "Транскодирование и мультиплексирование видео")
		ffmpeg.Group = "media"
		markUpdatable(&ffmpeg)
		ffprobe := checkDep("FFprobe", "ffprobe", "Анализ медиа-файлов")
		ffprobe.Group = "media"
		markUpdatable(&ffprobe)

		// --- System group ---
		curlDep := checkDep("curl", "curl", "HTTP-запросы")
		curlDep.Group = "system"
		markUpdatable(&curlDep)
		wgetDep := checkDep("wget", "wget", "Загрузка файлов")
		wgetDep.Group = "system"
		markUpdatable(&wgetDep)

		// Build groups.
		groups := []depGroup{
			{ID: "youtube", Icon: "\u25B6", Label: "YouTube", Deps: []depInfo{ytdlp, ejsSolver, potProvider, jsNode, python3, pipDep}},
			{ID: "balancers", Icon: "\u2696", Label: "\u0411\u0430\u043B\u0430\u043D\u0441\u0435\u0440\u044B", Deps: []depInfo{chromium, proxychains}},
			{ID: "torrents", Icon: "\U0001F3AC", Label: "\u0422\u043E\u0440\u0440\u0435\u043D\u0442\u044B", Deps: []depInfo{ts, jr}},
			{ID: "media", Icon: "\U0001F3A5", Label: "\u041C\u0435\u0434\u0438\u0430", Deps: []depInfo{ffmpeg, ffprobe}},
			{ID: "system", Icon: "\U0001F5A5", Label: "\u0421\u0438\u0441\u0442\u0435\u043C\u043D\u044B\u0435", Deps: []depInfo{curlDep, wgetDep}},
		}

		// Flatten for latest version checking.
		var allDeps []*depInfo
		for gi := range groups {
			for di := range groups[gi].Deps {
				allDeps = append(allDeps, &groups[gi].Deps[di])
			}
		}

		// Check latest versions from GitHub if requested.
		if checkLatest {
			type latestResult struct {
				binary string
				latest string
			}
			ch := make(chan latestResult, 2)
			go func() { ch <- latestResult{"yt-dlp", githubLatestTag("yt-dlp", "yt-dlp")} }()
			go func() { ch <- latestResult{"TorrServer", githubLatestTag("YouROK", "TorrServer")} }()
			for range 2 {
				res := <-ch
				if res.latest == "" {
					continue
				}
				for _, d := range allDeps {
					if (res.binary == "yt-dlp" && d.Binary == "yt-dlp") ||
						(res.binary == "TorrServer" && strings.HasPrefix(d.Binary, "TorrServer")) {
						d.Latest = res.latest
					}
				}
			}
		}

		// Fill update-in-progress state.
		depUpdateState.RLock()
		for _, d := range allDeps {
			if p, ok := depUpdateState.m[d.Binary]; ok && p.InProgress {
				d.Updating = true
			}
		}
		depUpdateState.RUnlock()

		writeJSON(w, http.StatusOK, map[string]any{
			"groups": groups,
			"os":     runtime.GOOS,
			"arch":   runtime.GOARCH,
			"go_ver": runtime.Version(),
		})
	}
}

// tgAdminDepsUpdateHandler handles POST /api/deps/update — updates a dependency.
func tgAdminDepsUpdateHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		var req struct {
			Binary string `json:"binary"` // "yt-dlp" or "TorrServer-linux"
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
			return
		}

		// Normalize TorrServer binary name.
		binary := req.Binary
		if strings.HasPrefix(binary, "TorrServer") {
			if runtime.GOOS == "darwin" {
				binary = "TorrServer-darwin"
			} else {
				binary = "TorrServer-linux"
			}
		}

		// Check if already updating.
		depUpdateState.Lock()
		if p, ok := depUpdateState.m[binary]; ok && p.InProgress {
			depUpdateState.Unlock()
			writeJSON(w, http.StatusConflict, map[string]any{"error": "update already in progress"})
			return
		}
		progress := &depUpdateProgress{InProgress: true, StartedAt: time.Now()}
		depUpdateState.m[binary] = progress
		depUpdateState.Unlock()

		switch {
		case binary == "yt-dlp":
			go updateYtdlp(progress)
		case binary == "JacRed":
			go updateJacRed(progress)
		case strings.HasPrefix(binary, "TorrServer"):
			go updateTorrServer(progress, binary)
		case pipRecipes[binary]:
			go installPipPackage(progress, binary)
		case pkgRecipe[binary] != nil:
			go installSystemPackage(progress, binary)
		default:
			depUpdateState.Lock()
			delete(depUpdateState.m, binary)
			depUpdateState.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "dependency not updatable: " + binary})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "update started"})
	}
}

// tgAdminDepsUpdateStatusHandler handles GET /api/deps/update/status.
func tgAdminDepsUpdateStatusHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		binary := r.URL.Query().Get("binary")
		if binary == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing binary param"})
			return
		}

		depUpdateState.RLock()
		p := depUpdateState.m[binary]
		depUpdateState.RUnlock()

		if p == nil {
			writeJSON(w, http.StatusOK, map[string]any{"status": "idle"})
			return
		}

		status := "in_progress"
		if p.Done {
			if p.Error != "" {
				status = "error"
			} else {
				status = "done"
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      status,
			"error":       p.Error,
			"old_version": p.OldVersion,
			"new_version": p.NewVersion,
		})
	}
}

// updateYtdlp downloads the latest yt-dlp binary from GitHub.
func updateYtdlp(progress *depUpdateProgress) {
	defer func() {
		progress.InProgress = false
		progress.Done = true
	}()

	if !serverReady() {
		progress.Error = "server not initialized"
		return
	}
	cfg := liveConfig(config.Config{})
	repoRoot := cfg.Compat.RepoRoot

	// Get current version. Prefer local binary (that's what we'll replace).
	localPath := filepath.Join(repoRoot, "bin", "yt-dlp")
	if out, err := exec.Command(localPath, "--version").Output(); err == nil {
		progress.OldVersion = strings.TrimSpace(string(out))
	} else if p, err := exec.LookPath("yt-dlp"); err == nil {
		if out, e := exec.Command(p, "--version").Output(); e == nil {
			progress.OldVersion = strings.TrimSpace(string(out))
		}
	}

	// Determine download URL.
	var dlURL string
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		dlURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		dlURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux_aarch64"
	case runtime.GOOS == "darwin":
		dlURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_macos"
	default:
		progress.Error = fmt.Sprintf("platform not supported: %s/%s", runtime.GOOS, runtime.GOARCH)
		return
	}

	binDir := filepath.Join(repoRoot, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	tmpPath := localPath + ".tmp"

	log.Info().Str("url", dlURL).Msg("deps: downloading yt-dlp update")

	client := httpclient.New(120 * time.Second)
	resp, err := client.Get(dlURL)
	if err != nil {
		progress.Error = "download failed: " + err.Error()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		progress.Error = fmt.Sprintf("download returned %d", resp.StatusCode)
		return
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		progress.Error = "create file: " + err.Error()
		return
	}
	_, copyErr := io.Copy(f, io.LimitReader(resp.Body, 200<<20)) // max 200MB
	f.Close()
	if copyErr != nil {
		os.Remove(tmpPath)
		progress.Error = "write file: " + copyErr.Error()
		return
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		progress.Error = "chmod: " + err.Error()
		return
	}

	// Verify it works.
	verOut, err := exec.Command(tmpPath, "--version").Output()
	if err != nil {
		os.Remove(tmpPath)
		progress.Error = "verification failed: downloaded binary does not work"
		return
	}
	newVer := strings.TrimSpace(string(verOut))

	// Atomic replace.
	if err := os.Rename(tmpPath, localPath); err != nil {
		os.Remove(tmpPath)
		progress.Error = "replace failed: " + err.Error()
		return
	}

	progress.NewVersion = newVer
	log.Info().Str("old", progress.OldVersion).Str("new", newVer).Msg("deps: yt-dlp updated")

	// Invalidate version cache.
	latestVersionCache.Lock()
	delete(latestVersionCache.m, "yt-dlp/yt-dlp")
	delete(latestVersionCache.ts, "yt-dlp/yt-dlp")
	latestVersionCache.Unlock()
}

// jacredHomeDir resolves the local jacred install directory from config.
func jacredHomeDir(cfg config.Config) string {
	home := strings.TrimSpace(cfg.Parser.JacRedHome)
	if home == "" {
		home = filepath.Join(cfg.Compat.RepoRoot, "jacred")
	}
	return home
}

// jacredStatusSnapshot prefers the live manager (running state, stage) and
// falls back to on-disk inspection when the local parser is disabled.
func jacredStatusSnapshot(cfg config.Config) jacred.Status {
	if mgr := liveJacredMgr(); mgr != nil {
		return mgr.Snapshot()
	}
	return jacred.Inspect(jacredHomeDir(cfg))
}

// updateJacRed installs/updates the local jacred-fdb binary and restarts the
// supervised child so the new release is picked up.
func updateJacRed(progress *depUpdateProgress) {
	defer func() {
		progress.InProgress = false
		progress.Done = true
	}()

	if !serverReady() {
		progress.Error = "server not initialized"
		return
	}
	cfg := liveConfig(config.Config{})
	home := jacredHomeDir(cfg)
	progress.OldVersion = jacred.Inspect(home).Version

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := jacred.Install(ctx, home); err != nil {
		progress.Error = err.Error()
		return
	}
	progress.NewVersion = jacred.Inspect(home).Version
	if mgr := liveJacredMgr(); mgr != nil {
		mgr.RestartChild()
	}
}

// updateTorrServer downloads the latest TorrServer binary from GitHub.
func updateTorrServer(progress *depUpdateProgress, binary string) {
	defer func() {
		progress.InProgress = false
		progress.Done = true
	}()

	if !serverReady() {
		progress.Error = "server not initialized"
		return
	}
	cfg := liveConfig(config.Config{})
	homeDir := cfg.TorrServer.HomeDir
	if homeDir == "" {
		homeDir = filepath.Join(cfg.Compat.RepoRoot, "torrserver")
	}
	_ = os.MkdirAll(homeDir, 0o755)

	binPath := filepath.Join(homeDir, binary)

	// Get current version.
	if out, err := exec.Command(binPath, "--version").Output(); err == nil {
		progress.OldVersion = strings.TrimSpace(string(out))
	}

	// Determine download URL from latest release.
	latest := githubLatestTag("YouROK", "TorrServer")
	if latest == "" {
		progress.Error = "failed to fetch latest TorrServer version"
		return
	}

	// TorrServer release assets follow pattern: TorrServer-linux-amd64, TorrServer-darwin-amd64, etc.
	var assetName string
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		assetName = "TorrServer-linux-amd64"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		assetName = "TorrServer-linux-arm64"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		assetName = "TorrServer-darwin-amd64"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		assetName = "TorrServer-darwin-arm64"
	default:
		progress.Error = fmt.Sprintf("platform not supported: %s/%s", runtime.GOOS, runtime.GOARCH)
		return
	}

	// TorrServer releases use "vX.Y.Z" or "MatriX.XXX" tag format.
	tag := latest
	if !strings.HasPrefix(tag, "v") && !strings.HasPrefix(tag, "MatriX") {
		tag = "v" + tag
	}
	dlURL := fmt.Sprintf("https://github.com/YouROK/TorrServer/releases/download/%s/%s", tag, assetName)

	log.Info().Str("url", dlURL).Str("tag", tag).Msg("deps: downloading TorrServer update")

	tmpPath := binPath + ".tmp"
	client := httpclient.New(120 * time.Second)
	resp, err := client.Get(dlURL)
	if err != nil {
		progress.Error = "download failed: " + err.Error()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		progress.Error = fmt.Sprintf("download returned %d (asset: %s)", resp.StatusCode, assetName)
		return
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		progress.Error = "create file: " + err.Error()
		return
	}
	_, copyErr := io.Copy(f, io.LimitReader(resp.Body, 200<<20))
	f.Close()
	if copyErr != nil {
		os.Remove(tmpPath)
		progress.Error = "write file: " + copyErr.Error()
		return
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		progress.Error = "chmod: " + err.Error()
		return
	}

	// Stop TorrServer process before replacing binary.
	if tsManager != nil {
		log.Info().Msg("deps: stopping TorrServer for update")
		stopTorrServerProcess()
	}

	// Atomic replace.
	if err := os.Rename(tmpPath, binPath); err != nil {
		os.Remove(tmpPath)
		progress.Error = "replace failed: " + err.Error()
		return
	}

	// Get new version.
	if out, err := exec.Command(binPath, "--version").Output(); err == nil {
		progress.NewVersion = strings.TrimSpace(string(out))
	} else {
		progress.NewVersion = tag
	}

	log.Info().Str("old", progress.OldVersion).Str("new", progress.NewVersion).Msg("deps: TorrServer updated")

	// Restart TorrServer process.
	if tsManager != nil {
		log.Info().Msg("deps: restarting TorrServer after update")
		initTorrServerProcess(cfg)
	}

	// Invalidate version cache.
	latestVersionCache.Lock()
	delete(latestVersionCache.m, "YouROK/TorrServer")
	delete(latestVersionCache.ts, "YouROK/TorrServer")
	latestVersionCache.Unlock()
}

// installPipPackage installs or upgrades a Python pip package.
func installPipPackage(progress *depUpdateProgress, pkg string) {
	defer func() {
		progress.InProgress = false
		progress.Done = true
	}()

	// Try pip, fallback to pip3. If neither found, auto-install python3-pip.
	pipCmd := "pip"
	if _, err := exec.LookPath(pipCmd); err != nil {
		pipCmd = "pip3"
		if _, err := exec.LookPath(pipCmd); err != nil {
			log.Info().Msg("deps: pip not found, auto-installing python3-pip")
			if err := autoInstallPip(); err != nil {
				progress.Error = "pip not found and auto-install failed: " + err.Error()
				return
			}
			// Re-check after install.
			pipCmd = "pip3"
			if _, err := exec.LookPath(pipCmd); err != nil {
				pipCmd = "pip"
				if _, err := exec.LookPath(pipCmd); err != nil {
					progress.Error = "pip still not found after installing python3-pip"
					return
				}
			}
		}
	}

	log.Info().Str("pkg", pkg).Str("pip", pipCmd).Msg("deps: installing pip package")

	cmd := exec.Command(pipCmd, "install", "--upgrade", "--break-system-packages", pkg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Retry without --break-system-packages for older pip.
		cmd2 := exec.Command(pipCmd, "install", "--upgrade", pkg)
		out2, err2 := cmd2.CombinedOutput()
		if err2 != nil {
			progress.Error = fmt.Sprintf("pip install failed: %v\n%s", err2, lastLines(string(out2), 5))
			return
		}
		out = out2
	}

	// Get new version.
	showOut, _ := exec.Command(pipCmd, "show", pkg).CombinedOutput()
	for _, line := range strings.Split(string(showOut), "\n") {
		if strings.HasPrefix(line, "Version:") {
			progress.NewVersion = strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
			break
		}
	}
	if progress.NewVersion == "" {
		progress.NewVersion = "installed"
	}

	log.Info().Str("pkg", pkg).Str("ver", progress.NewVersion).Str("out", lastLines(string(out), 3)).Msg("deps: pip package installed")
}

// installSystemPackage installs a system package via the detected package manager.
func installSystemPackage(progress *depUpdateProgress, binary string) {
	defer func() {
		progress.InProgress = false
		progress.Done = true
	}()

	pm := detectPkgManager()
	if pm == "" {
		progress.Error = "no supported package manager found (apt-get, dnf, yum, apk)"
		return
	}

	recipe, ok := pkgRecipe[binary]
	if !ok {
		progress.Error = "no install recipe for: " + binary
		return
	}
	pkgName, ok := recipe[pm]
	if !ok {
		progress.Error = fmt.Sprintf("no package name for %s on %s", binary, pm)
		return
	}

	log.Info().Str("binary", binary).Str("pkg", pkgName).Str("pm", pm).Msg("deps: installing system package")

	// Build install command. apt needs update first.
	var cmds []*exec.Cmd
	isRoot := os.Getuid() == 0
	makeCmd := func(args ...string) *exec.Cmd {
		if isRoot {
			return exec.Command(args[0], args[1:]...)
		}
		return exec.Command("sudo", append([]string{"-n"}, args...)...)
	}

	switch pm {
	case "apt-get":
		env := append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		update := makeCmd("apt-get", "update", "-qq")
		update.Env = env
		cmds = append(cmds, update)
		install := makeCmd("apt-get", "install", "-y", "-qq", pkgName)
		install.Env = env
		cmds = append(cmds, install)
	case "apk":
		cmds = append(cmds, makeCmd("apk", "add", "--no-cache", pkgName))
	default: // dnf, yum
		cmds = append(cmds, makeCmd(pm, "install", "-y", pkgName))
	}

	var allOut strings.Builder
	for _, cmd := range cmds {
		out, err := cmd.CombinedOutput()
		allOut.Write(out)
		if err != nil {
			progress.Error = fmt.Sprintf("%s failed: %v\n%s", cmd.Path, err, lastLines(allOut.String(), 8))
			return
		}
	}

	// Verify installation.
	if p, err := exec.LookPath(binary); err == nil {
		progress.NewVersion = "installed"
		// Try to get version.
		if vOut, e := exec.Command(p, "--version").Output(); e == nil {
			v := strings.TrimSpace(string(vOut))
			if len(v) > 80 {
				v = v[:80]
			}
			progress.NewVersion = v
		}
	} else {
		progress.NewVersion = "installed (not in PATH yet)"
	}

	log.Info().Str("binary", binary).Str("pkg", pkgName).Str("ver", progress.NewVersion).Msg("deps: system package installed")
}

// tgAdminDepsInstallAllHandler handles POST /api/deps/install-all.
func tgAdminDepsInstallAllHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		var req struct {
			Binaries []string `json:"binaries"` // list of missing dep binaries to install
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil || len(req.Binaries) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "binaries list required"})
			return
		}

		var started []string
		for _, binary := range req.Binaries {
			if !canInstall(binary) {
				continue
			}
			depUpdateState.Lock()
			if p, ok := depUpdateState.m[binary]; ok && p.InProgress {
				depUpdateState.Unlock()
				continue
			}
			progress := &depUpdateProgress{InProgress: true, StartedAt: time.Now()}
			depUpdateState.m[binary] = progress
			depUpdateState.Unlock()

			switch {
			case binary == "yt-dlp":
				go updateYtdlp(progress)
			case strings.HasPrefix(binary, "TorrServer"):
				go updateTorrServer(progress, binary)
			case pipRecipes[binary]:
				go installPipPackage(progress, binary)
			case pkgRecipe[binary] != nil:
				go installSystemPackage(progress, binary)
			}
			started = append(started, binary)
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": started, "count": len(started)})
	}
}

// autoInstallPip tries to install python3-pip via the system package manager.
func autoInstallPip() error {
	pm := detectPkgManager()
	if pm == "" {
		return fmt.Errorf("no package manager found")
	}

	isRoot := os.Getuid() == 0
	makeCmd := func(args ...string) *exec.Cmd {
		if isRoot {
			return exec.Command(args[0], args[1:]...)
		}
		return exec.Command("sudo", append([]string{"-n"}, args...)...)
	}

	var pkgName string
	switch pm {
	case "apt-get":
		pkgName = "python3-pip"
	case "dnf", "yum":
		pkgName = "python3-pip"
	case "apk":
		pkgName = "py3-pip"
	default:
		return fmt.Errorf("unsupported package manager: %s", pm)
	}

	var cmds []*exec.Cmd
	if pm == "apt-get" {
		env := append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		update := makeCmd("apt-get", "update", "-qq")
		update.Env = env
		cmds = append(cmds, update)
		install := makeCmd("apt-get", "install", "-y", "-qq", pkgName)
		install.Env = env
		cmds = append(cmds, install)
	} else if pm == "apk" {
		cmds = append(cmds, makeCmd("apk", "add", "--no-cache", pkgName))
	} else {
		cmds = append(cmds, makeCmd(pm, "install", "-y", pkgName))
	}

	for _, cmd := range cmds {
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %v\n%s", cmd.Path, err, lastLines(string(out), 5))
		}
	}

	log.Info().Str("pm", pm).Str("pkg", pkgName).Msg("deps: python3-pip auto-installed")
	return nil
}

// lastLines returns the last n lines of a string.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.TrimSpace(s)
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
