package httpapi

import (
	"archive/zip"
	"crypto/md5"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  LampaWeb settings (read from init.conf for .NET compat)
// ---------------------------------------------------------------------------

type lampaWebCronSettings struct {
	AutoUpdate     bool   `json:"autoupdate"`
	Git            string `json:"git"`
	Tree           string `json:"tree"`
	IntervalUpdate int    `json:"intervalupdate"`
}

// loadLampaWebCronSettings reads LampaWeb configuration from init.conf.
// Falls back to sensible defaults matching .NET AppInit.conf.LampaWeb.
func loadLampaWebCronSettings() lampaWebCronSettings {
	defaults := lampaWebCronSettings{
		AutoUpdate:     true,
		Git:            "yumata/lampa",
		IntervalUpdate: 90,
	}

	data, ok := readFileAny("init.conf")
	if !ok {
		return defaults
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return defaults
	}

	node, ok := root["LampaWeb"].(map[string]any)
	if !ok {
		return defaults
	}

	result := defaults
	if v, ok := node["autoupdate"].(bool); ok {
		result.AutoUpdate = v
	}
	if v, ok := node["git"].(string); ok && v != "" {
		result.Git = v
	}
	if v, ok := node["tree"].(string); ok {
		result.Tree = v
	}
	if v, ok := node["intervalupdate"].(float64); ok && v > 0 {
		result.IntervalUpdate = int(v)
	}

	return result
}

// ---------------------------------------------------------------------------
//  LampaCron — safe Lampa UI auto-updater
// ---------------------------------------------------------------------------

// LampaCron periodically checks for Lampa UI updates from GitHub and safely
// updates wwwroot/lampa-main/. User-sensitive files (plugins/, personal.lampa,
// plugins_black_list.json) are backed up before extraction and restored after.
//
// Mirrors .NET Lampac.Engine.CRON.LampaCron with added safety for plugins/auth.
type LampaCron struct {
	cfg        config.Config
	client     *http.Client
	currentMD5 string // cached MD5 of current app.min.js
	running    int32
	stop       chan struct{}
}

// NewLampaCron creates a new LampaCron instance.
func NewLampaCron(cfg config.Config) *LampaCron {
	return &LampaCron{
		cfg:    cfg,
		client: httpclient.New(30 * time.Second),
		stop:   make(chan struct{}),
	}
}

// Start begins the background update loop if autoupdate is enabled.
// Initial delay: 20 seconds; interval: max(intervalupdate, 5) minutes.
func (lc *LampaCron) Start() {
	settings := loadLampaWebCronSettings()
	if !settings.AutoUpdate {
		log.Info().Msg("lampa-cron: autoupdate disabled")
		return
	}

	interval := max(settings.IntervalUpdate, 5)

	log.Info().
		Str("git", settings.Git).
		Str("tree", settings.Tree).
		Int("interval_min", interval).
		Msg("lampa-cron: starting")

	go lc.loop(interval)
}

// Stop signals the background loop to exit.
func (lc *LampaCron) Stop() {
	select {
	case lc.stop <- struct{}{}:
	default:
	}
}

func (lc *LampaCron) loop(intervalMin int) {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-lc.stop:
			return
		case <-timer.C:
			lc.update()
			timer.Reset(time.Duration(intervalMin) * time.Minute)
		}
	}
}

// update checks for new Lampa UI version and applies it if found.
func (lc *LampaCron) update() {
	if !atomic.CompareAndSwapInt32(&lc.running, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&lc.running, 0)

	settings := loadLampaWebCronSettings()
	if !settings.AutoUpdate {
		return
	}

	git := settings.Git
	if git == "" {
		git = "yumata/lampa"
	}
	tree := strings.TrimSpace(settings.Tree)
	isTree := tree != ""
	branch := "main"
	if isTree {
		branch = tree
	}

	wwwroot := lc.resolveWWWRoot()
	lampaDir := filepath.Join(wwwroot, "lampa-main")
	appJSPath := filepath.Join(lampaDir, "app.min.js")

	// ------ 1. Check if update is needed ------

	needsUpdate, err := lc.checkUpdate(git, branch, isTree, tree, lampaDir, appJSPath)
	if err != nil {
		log.Debug().Err(err).Msg("lampa-cron: update check failed")
		return
	}
	if !needsUpdate {
		return
	}

	log.Info().Str("git", git).Str("branch", branch).Msg("lampa-cron: update available, downloading")

	// ------ 2. Download ZIP ------

	var zipURL string
	if isTree {
		zipURL = fmt.Sprintf("https://github.com/%s/archive/%s.zip", git, tree)
	} else {
		zipURL = fmt.Sprintf("https://github.com/%s/archive/refs/heads/main.zip", git)
	}

	zipPath := filepath.Join(wwwroot, "lampa.zip")
	if err := lc.downloadFile(zipURL, zipPath); err != nil {
		log.Warn().Err(err).Msg("lampa-cron: failed to download ZIP")
		return
	}
	defer os.Remove(zipPath)

	// Reset cached MD5 since we're updating.
	lc.currentMD5 = ""

	// ------ 3. Safe update with backup/restore ------

	backups := lc.backupUserFiles(lampaDir)

	if err := extractZipSafe(zipPath, wwwroot); err != nil {
		log.Warn().Err(err).Msg("lampa-cron: failed to extract ZIP")
		lc.restoreUserFiles(lampaDir, backups)
		return
	}

	// Handle tree branch: copy extracted folder contents into lampa-main.
	if isTree {
		treeDir := filepath.Join(wwwroot, "lampa-"+tree)
		if err := copyDirContents(treeDir, lampaDir); err != nil {
			log.Warn().Err(err).Msg("lampa-cron: failed to copy tree dir")
		}
		_ = os.WriteFile(filepath.Join(lampaDir, "tree"), []byte(tree), 0o644)
		_ = os.RemoveAll(treeDir)
	}

	// ------ 4. Restore user-sensitive files ------

	lc.restoreUserFiles(lampaDir, backups)

	// ------ 5. Post-process ------

	lampaPostProcess(lampaDir)

	log.Info().Msg("lampa-cron: update complete")
}

// ---------------------------------------------------------------------------
//  Update check
// ---------------------------------------------------------------------------

// checkUpdate returns true if Lampa UI needs updating.
func (lc *LampaCron) checkUpdate(git, branch string, isTree bool, tree, lampaDir, appJSPath string) (bool, error) {
	// If app.min.js doesn't exist, definitely need to install.
	if _, err := os.Stat(appJSPath); os.IsNotExist(err) {
		return true, nil
	}

	// If using a specific tree and it matches current marker, skip.
	if isTree {
		treePath := filepath.Join(lampaDir, "tree")
		if data, err := os.ReadFile(treePath); err == nil {
			if strings.TrimSpace(string(data)) == tree {
				return false, nil
			}
		}
	}

	// Fetch remote app.min.js and compare MD5.
	remoteURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/app.min.js", git, branch)
	req, err := http.NewRequest(http.MethodGet, remoteURL, nil)
	if err != nil {
		return false, err
	}

	resp, err := lc.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false, fmt.Errorf("HTTP %d from GitHub", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB limit
	if err != nil {
		return false, err
	}

	// Verify it's a real Lampa app file.
	if !strings.Contains(string(body), "author: 'Yumata'") {
		return false, nil
	}

	remoteMD5 := md5Hex(body)

	// Compute local MD5 (cached across checks).
	if lc.currentMD5 == "" {
		lc.currentMD5 = md5FileHex(appJSPath)
	}

	if lc.currentMD5 != "" && remoteMD5 != lc.currentMD5 {
		return true, nil
	}

	// Tree mode: write marker even when MD5 matches (first run).
	if isTree {
		_ = os.WriteFile(filepath.Join(lampaDir, "tree"), []byte(tree), 0o644)
	}

	return false, nil
}

// ---------------------------------------------------------------------------
//  Backup / restore of user-sensitive files
// ---------------------------------------------------------------------------

// userFileBackup holds the content of one user file.
type userFileBackup struct {
	relPath string
	data    []byte
	isDir   bool
}

// backupUserFiles saves user-sensitive files before ZIP extraction.
// Backed-up items: plugins/, vender/hls/, personal.lampa,
// plugins_black_list.json, img/welcome.jpg.
func (lc *LampaCron) backupUserFiles(lampaDir string) []userFileBackup {
	var backups []userFileBackup

	// Individual files.
	for _, rel := range []string{"personal.lampa", "plugins_black_list.json", "img/welcome.jpg"} {
		path := filepath.Join(lampaDir, rel)
		if data, err := os.ReadFile(path); err == nil {
			backups = append(backups, userFileBackup{relPath: rel, data: data})
		}
	}

	// Directories to backup recursively: plugins/, vender/hls/.
	backupDirs := []string{"plugins", filepath.Join("vender", "hls")}
	for _, dirRel := range backupDirs {
		dir := filepath.Join(lampaDir, dirRel)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				rel, _ := filepath.Rel(lampaDir, path)
				if info.IsDir() {
					backups = append(backups, userFileBackup{relPath: rel, isDir: true})
				} else {
					if data, rerr := os.ReadFile(path); rerr == nil {
						backups = append(backups, userFileBackup{relPath: rel, data: data})
					}
				}
				return nil
			})
		}
	}

	if len(backups) > 0 {
		log.Debug().Int("files", len(backups)).Msg("lampa-cron: backed up user files")
	}
	return backups
}

// restoreUserFiles restores previously backed-up user files.
func (lc *LampaCron) restoreUserFiles(lampaDir string, backups []userFileBackup) {
	if len(backups) == 0 {
		return
	}

	for _, b := range backups {
		path := filepath.Join(lampaDir, b.relPath)
		if b.isDir {
			_ = os.MkdirAll(path, 0o755)
		} else {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, b.data, 0o644)
		}
	}

	log.Debug().Int("files", len(backups)).Msg("lampa-cron: restored user files")
}

// ---------------------------------------------------------------------------
//  Post-processing
// ---------------------------------------------------------------------------

// lampaPostProcess injects lampainit.js and creates marker files.
// Called after ZIP extraction and user-file restoration.
func lampaPostProcess(lampaDir string) {
	// Inject lampainit.js into index.html.
	indexPath := filepath.Join(lampaDir, "index.html")
	if html, err := os.ReadFile(indexPath); err == nil {
		content := string(html)
		if !strings.Contains(content, `"/lampainit.js"`) {
			content = strings.Replace(content, "</body>",
				`<script src="/lampainit.js"></script></body>`, 1)
			_ = os.WriteFile(indexPath, []byte(content), 0o644)
		}
	}

	// Create personal.lampa if missing.
	personalPath := filepath.Join(lampaDir, "personal.lampa")
	if _, err := os.Stat(personalPath); os.IsNotExist(err) {
		_ = os.WriteFile(personalPath, []byte{}, 0o644)
	}

	// Create plugins_black_list.json if missing.
	blacklistPath := filepath.Join(lampaDir, "plugins_black_list.json")
	if _, err := os.Stat(blacklistPath); os.IsNotExist(err) {
		_ = os.WriteFile(blacklistPath, []byte("[]"), 0o644)
	}

	// Create plugins/modification.js if missing.
	modJSPath := filepath.Join(lampaDir, "plugins", "modification.js")
	if _, err := os.Stat(modJSPath); os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Join(lampaDir, "plugins"), 0o755)
		_ = os.WriteFile(modJSPath, []byte{}, 0o644)
	}
}

// ---------------------------------------------------------------------------
//  Helpers
// ---------------------------------------------------------------------------

// resolveWWWRoot returns the absolute path to the wwwroot directory.
func (lc *LampaCron) resolveWWWRoot() string {
	candidates := []string{
		filepath.Join(lc.cfg.Compat.RepoRoot, "wwwroot"),
		"wwwroot",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	// Default: create in repo root.
	dir := filepath.Join(lc.cfg.Compat.RepoRoot, "wwwroot")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// downloadFile downloads a URL to a local path.
func (lc *LampaCron) downloadFile(url, dest string) error {
	resp, err := lc.client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, io.LimitReader(resp.Body, 100<<20)) // 100 MB max
	return err
}

// extractZipSafe extracts a ZIP archive to the destination directory.
// Existing files are overwritten; non-ZIP files are preserved (no deletion).
// Path traversal attempts are silently skipped.
func extractZipSafe(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		name := filepath.Clean(f.Name)
		if strings.Contains(name, "..") {
			continue
		}

		outPath := filepath.Join(destDir, name)

		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(outPath, 0o755)
			continue
		}

		_ = os.MkdirAll(filepath.Dir(outPath), 0o755)

		rc, err := f.Open()
		if err != nil {
			continue
		}

		out, err := os.Create(outPath)
		if err != nil {
			rc.Close()
			continue
		}

		_, _ = io.Copy(out, io.LimitReader(rc, 50<<20)) // 50 MB per file limit
		out.Close()
		rc.Close()
	}
	return nil
}

// copyDirContents copies all files from src to dst recursively.
func copyDirContents(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(src, path)
		outPath := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(outPath, 0o755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		_ = os.MkdirAll(filepath.Dir(outPath), 0o755)
		return os.WriteFile(outPath, data, 0o644)
	})
}

// md5Hex returns hex-encoded MD5 of data.
func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

// md5FileHex returns hex-encoded MD5 of file contents.
func md5FileHex(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
