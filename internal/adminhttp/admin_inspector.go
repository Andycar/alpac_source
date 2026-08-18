package adminhttp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	stdjson "encoding/json"

	"lampac-go/internal/balancerhealth"
	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// DiagIssue represents a single diagnosed problem.
type DiagIssue struct {
	Category string `json:"category"` // system, balancers, torrserver, transcoding, tmdb, youtube, proxy
	Target   string `json:"target"`   // specific object name
	Severity string `json:"severity"` // critical, warning, info
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	FixID    string `json:"fix_id,omitempty"`
	FixLabel string `json:"fix_label,omitempty"`
}

// categoryNames maps internal keys to human-readable labels.
var categoryNames = map[string]string{
	"system":      "Система",
	"balancers":   "Балансеры",
	"torrserver":  "TorrServer",
	"transcoding": "Транскодирование",
	"tmdb":        "TMDB Proxy",
	"youtube":     "YouTube",
	"proxy":       "Прокси",
}

// categoryOrder defines the display order.
var categoryOrder = []string{"system", "balancers", "torrserver", "transcoding", "tmdb", "youtube", "proxy"}

// ---------- API Handlers ----------

func tgAdminInspectorRunHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		cfg := liveConfig(config.Config{})
		category := strings.TrimSpace(r.URL.Query().Get("category"))

		var issues []DiagIssue
		if category != "" {
			issues = runCategoryCheck(cfg, category)
		} else {
			issues = runAllChecks(cfg)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"issues":     issues,
			"categories": categoryNames,
			"order":      categoryOrder,
		})
	}
}

func tgAdminInspectorFixHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		var req struct {
			FixID  string `json:"fix_id"`
			Target string `json:"target"`
		}
		if err := stdjson.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad json"})
			return
		}

		cfg := liveConfig(config.Config{})
		msg, err := applyFix(cfg, req.FixID, req.Target)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
	}
}

// ---------- Runner ----------

func runAllChecks(cfg config.Config) []DiagIssue {
	type result struct {
		issues []DiagIssue
	}

	checkers := map[string]func(config.Config) []DiagIssue{
		"system":      inspectSystem,
		"balancers":   inspectBalancers,
		"torrserver":  inspectTorrServer,
		"transcoding": inspectTranscoding,
		"tmdb":        inspectTMDB,
		"youtube":     inspectYouTube,
		"proxy":       inspectProxy,
	}

	results := make(map[string][]DiagIssue)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for cat, fn := range checkers {
		wg.Add(1)
		go func(cat string, fn func(config.Config) []DiagIssue) {
			defer wg.Done()
			issues := fn(cfg)
			mu.Lock()
			results[cat] = issues
			mu.Unlock()
		}(cat, fn)
	}
	wg.Wait()

	// Collect in order.
	var all []DiagIssue
	for _, cat := range categoryOrder {
		all = append(all, results[cat]...)
	}
	return all
}

func runCategoryCheck(cfg config.Config, category string) []DiagIssue {
	switch category {
	case "system":
		return inspectSystem(cfg)
	case "balancers":
		return inspectBalancers(cfg)
	case "torrserver":
		return inspectTorrServer(cfg)
	case "transcoding":
		return inspectTranscoding(cfg)
	case "tmdb":
		return inspectTMDB(cfg)
	case "youtube":
		return inspectYouTube(cfg)
	case "proxy":
		return inspectProxy(cfg)
	default:
		return nil
	}
}

// ---------- 1. System ----------

func inspectSystem(cfg config.Config) []DiagIssue {
	var issues []DiagIssue

	// Binary dependencies.
	for _, dep := range []struct {
		name, binary, hint string
		severity           string
	}{
		{"FFmpeg", "ffmpeg", "apt install ffmpeg", "critical"},
		{"FFprobe", "ffprobe", "apt install ffmpeg", "warning"},
		{"yt-dlp", "yt-dlp", "pip install yt-dlp", "critical"},
		{"Chromium", "chromium", "apt install chromium", "warning"},
		{"Proxychains4", "proxychains4", "apt install proxychains4", "warning"},
	} {
		if _, err := exec.LookPath(dep.binary); err != nil {
			// Chrome/Chromium: multiple binary names across distros
			if dep.binary == "chromium" {
				found := false
				for _, alt := range []string{"chromium-browser", "google-chrome", "google-chrome-stable"} {
					if _, err2 := exec.LookPath(alt); err2 == nil {
						found = true
						break
					}
				}
				if found {
					continue
				}
			}
			issues = append(issues, DiagIssue{
				Category: "system",
				Target:   dep.name,
				Severity: dep.severity,
				Title:    dep.name + " не найден",
				Detail:   "Установите: " + dep.hint,
			})
		}
	}

	// Node.js / Deno — нужен хотя бы один из них.
	hasJSRuntime := false
	for _, bin := range []string{"node", "deno", "bun"} {
		if _, err := exec.LookPath(bin); err == nil {
			hasJSRuntime = true
			break
		}
	}
	if !hasJSRuntime {
		issues = append(issues, DiagIssue{
			Category: "system",
			Target:   "JS Runtime",
			Severity: "warning",
			Title:    "Node.js / Deno / Bun не найден",
			Detail:   "Нужен хотя бы один JS runtime для yt-dlp. Установите: apt install nodejs",
		})
	}

	// Disk space.
	repoRoot := cfg.Compat.RepoRoot
	if repoRoot == "" {
		repoRoot = "."
	}
	if freeBytes, err := freeDiskSpaceBytes(repoRoot); err == nil {
		freeGB := float64(freeBytes) / (1024 * 1024 * 1024)
		if freeGB < 1.0 {
			issues = append(issues, DiagIssue{
				Category: "system",
				Target:   "disk",
				Severity: "warning",
				Title:    fmt.Sprintf("Мало места на диске: %.1f GB", freeGB),
				Detail:   "Рекомендуется минимум 1 GB свободного места",
				FixID:    "fix_cleanup_cache",
				FixLabel: "Очистить кеш",
			})
		}
	}

	// RAM usage.
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	allocMB := m.Alloc / 1024 / 1024
	sysMB := m.Sys / 1024 / 1024
	if sysMB > 512 {
		issues = append(issues, DiagIssue{
			Category: "system",
			Target:   "memory",
			Severity: "warning",
			Title:    fmt.Sprintf("Высокое потребление памяти: %d MB (alloc %d MB)", sysMB, allocMB),
		})
	}

	// config.toml parse check — loadConfigTOMLAsMap() logs errors internally.
	// If it returns empty map but file is non-empty, TOML has syntax issues.
	if serverReady() {
		tomlPath := cfg.TOMLPath
		if tomlPath == "" {
			tomlPath = config.TOMLFilePath(cfg.Compat.RepoRoot)
		}
		if data, err := os.ReadFile(tomlPath); err == nil && len(data) > 10 {
			root := loadConfigTOMLAsMap()
			if len(root) == 0 {
				issues = append(issues, DiagIssue{
					Category: "system",
					Target:   "config.toml",
					Severity: "critical",
					Title:    "Возможная ошибка в config.toml",
					Detail:   "Файл непустой, но парсинг вернул пустой результат",
				})
			}
		}
	}

	// File permissions.
	for _, f := range []struct {
		relPath string
		name    string
	}{
		{"torrserver/accs.db", "accs.db"},
		{"cache/aeskey", "aeskey"},
		{"database/tgauth/tokens.json", "tokens.json"},
	} {
		path := filepath.Join(repoRoot, f.relPath)
		info, err := os.Stat(path)
		if err != nil {
			continue // file doesn't exist — not an error for inspector
		}
		perm := info.Mode().Perm()
		if perm&0o077 != 0 { // group/other can read
			issues = append(issues, DiagIssue{
				Category: "system",
				Target:   f.name,
				Severity: "warning",
				Title:    fmt.Sprintf("Небезопасные права файла %s: %o", f.name, perm),
				Detail:   "Рекомендуется 0600",
				FixID:    "fix_file_permissions",
				FixLabel: "Исправить права",
			})
		}
	}

	return issues
}

// ---------- 2. Balancers ----------

func inspectBalancers(cfg config.Config) []DiagIssue {
	var issues []DiagIssue

	root := loadMergedConf()

	// Health checker snapshot.
	var healthSnap map[string]balancerhealth.HealthStatus
	if hc := balancerhealth.GetGlobalHealthChecker(); hc != nil {
		healthSnap = hc.Snapshot()
	}

	// Balancers that require tokens.
	tokenFields := map[string]string{
		"Filmix":  "token",
		"Vibix":   "token",
		"Mirage":  "token",
		"Aladdin": "token",
	}

	for _, bName := range knownBalancers() {
		section, ok := root[bName].(map[string]any)
		if !ok {
			continue
		}

		lowerName := strings.ToLower(bName)

		// Check if disabled in config.
		if isDisabledInConf(section) {
			issues = append(issues, DiagIssue{
				Category: "balancers",
				Target:   bName,
				Severity: "info",
				Title:    bName + " отключён в конфиге",
				FixID:    "fix_balancer_enable",
				FixLabel: "Включить",
			})
			continue
		}

		// Check healthcheck status.
		if hs, ok := healthSnap[lowerName]; ok {
			if hs.AutoDisabled {
				issues = append(issues, DiagIssue{
					Category: "balancers",
					Target:   bName,
					Severity: "warning",
					Title:    bName + " автоотключён (healthcheck)",
					Detail:   fmt.Sprintf("Ошибок подряд: %d, последняя: %s", hs.ConsecutiveFails, hs.LastCheckError),
					FixID:    "fix_balancer_reenable",
					FixLabel: "Включить обратно",
				})
			} else if !hs.Healthy {
				detail := hs.LastCheckError
				if hs.LastCheckStatus > 0 {
					detail = fmt.Sprintf("HTTP %d", hs.LastCheckStatus)
					if hs.LastCheckStatus == 403 || hs.LastCheckStatus == 451 {
						detail += " (возможно GeoIP блокировка — нужен прокси)"
					}
				}
				issues = append(issues, DiagIssue{
					Category: "balancers",
					Target:   bName,
					Severity: "critical",
					Title:    bName + " — хост недоступен",
					Detail:   fmt.Sprintf("%s (latency: %dms)", detail, hs.LastCheckLatency),
				})
			}
		}

		// Check required tokens.
		if field, needsToken := tokenFields[bName]; needsToken {
			token := toStringAny(section[field])
			if token == "" {
				issues = append(issues, DiagIssue{
					Category: "balancers",
					Target:   bName,
					Severity: "warning",
					Title:    bName + " — нет токена",
					Detail:   "Поле '" + field + "' пустое. Балансер может не работать без токена.",
				})
			}
		}

		// Mirage/Aladdin/Alloha: check rotate_min and cdn_proxies.
		if bName == "Mirage" || bName == "Aladdin" || bName == "Alloha" {
			if cdnList, ok := section["cdn_proxies"].([]any); ok && len(cdnList) > 0 {
				rotateMin, _ := toFloat64Any(section["rotate_min"])
				if rotateMin <= 0 {
					issues = append(issues, DiagIssue{
						Category: "balancers",
						Target:   bName,
						Severity: "warning",
						Title:    bName + " — rotate_min не задан",
						Detail:   "cdn_proxies настроены, но rotate_min = 0. CDN прокси не будут ротироваться.",
					})
				}
			}
		}

		// PidTor: check redapi.
		if bName == "PidTor" {
			redapi := toStringAny(section["redapi"])
			if redapi == "" {
				issues = append(issues, DiagIssue{
					Category: "balancers",
					Target:   bName,
					Severity: "critical",
					Title:    bName + " — redapi не задан",
					Detail:   "Без RedAPI PidTor не сможет искать торренты.",
				})
			}
		}
	}

	return issues
}

// isDisabledInConf checks if a balancer section has enable:false or enabled:false.
func isDisabledInConf(section map[string]any) bool {
	if v, ok := section["enable"]; ok && !toBoolAny(v) {
		return true
	}
	if v, ok := section["enabled"]; ok && !toBoolAny(v) {
		return true
	}
	return false
}

// ---------- 3. TorrServer ----------

func inspectTorrServer(cfg config.Config) []DiagIssue {
	var issues []DiagIssue

	port := cfg.TorrServer.Port
	homeDir := cfg.TorrServer.HomeDir
	if homeDir == "" {
		homeDir = filepath.Join(cfg.Compat.RepoRoot, "torrserver")
	}
	externalURL := strings.TrimSpace(cfg.TorrServer.URL)

	// Check if configured at all.
	if port <= 0 && externalURL == "" {
		issues = append(issues, DiagIssue{
			Category: "torrserver",
			Target:   "config",
			Severity: "info",
			Title:    "TorrServer не настроен",
			Detail:   "Укажите порт или URL в конфиге",
			FixID:    "fix_ts_config",
			FixLabel: "Настроить (порт 9080)",
		})
		return issues
	}

	// Check binary (local only).
	if externalURL == "" {
		binPath := filepath.Join(homeDir, "TorrServer-linux")
		if runtime.GOOS == "darwin" {
			binPath = filepath.Join(homeDir, "TorrServer-darwin")
		}
		if _, err := os.Stat(binPath); os.IsNotExist(err) {
			issues = append(issues, DiagIssue{
				Category: "torrserver",
				Target:   "binary",
				Severity: "critical",
				Title:    "Бинарник TorrServer не найден",
				Detail:   "Ожидается: " + binPath,
			})
		}

		// Check TCP port.
		if !checkTCPPort("127.0.0.1", port) {
			issues = append(issues, DiagIssue{
				Category: "torrserver",
				Target:   "port",
				Severity: "critical",
				Title:    fmt.Sprintf("TorrServer не отвечает на порту %d", port),
				Detail:   "Процесс TorrServer не запущен или порт занят",
			})
		}
	}

	// Check /echo endpoint.
	var baseURL string
	if externalURL != "" {
		baseURL = externalURL
	} else if port > 0 {
		baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	if baseURL != "" && !checkTSHealth(baseURL) {
		// Only add if TCP port is up (to avoid duplicate).
		if externalURL != "" || checkTCPPort("127.0.0.1", port) {
			issues = append(issues, DiagIssue{
				Category: "torrserver",
				Target:   "health",
				Severity: "critical",
				Title:    "TorrServer /echo не отвечает",
				Detail:   "URL: " + baseURL + "/echo",
			})
		}
	}

	// Check password (accs.db).
	accsPath := filepath.Join(homeDir, "accs.db")
	if _, err := os.Stat(accsPath); os.IsNotExist(err) {
		issues = append(issues, DiagIssue{
			Category: "torrserver",
			Target:   "password",
			Severity: "warning",
			Title:    "Нет пароля TorrServer (accs.db)",
			Detail:   "TorrServer доступен без авторизации при прямом подключении",
			FixID:    "fix_ts_password",
			FixLabel: "Сгенерировать пароль",
		})
	} else if cfg.TorrServer.Password == "" {
		issues = append(issues, DiagIssue{
			Category: "torrserver",
			Target:   "password",
			Severity: "warning",
			Title:    "Пароль TorrServer не прочитан",
			Detail:   "Файл accs.db существует, но пароль не загружен в конфиг",
		})
	}

	return issues
}

// ---------- 4. Transcoding ----------

func inspectTranscoding(cfg config.Config) []DiagIssue {
	var issues []DiagIssue

	if !cfg.Transcoding.Enable {
		issues = append(issues, DiagIssue{
			Category: "transcoding",
			Target:   "config",
			Severity: "info",
			Title:    "Транскодирование отключено",
			FixID:    "fix_transcoding_enable",
			FixLabel: "Включить",
		})
		return issues
	}

	// FFmpeg check.
	ffmpegBin := cfg.Transcoding.FFmpeg
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	if _, err := exec.LookPath(ffmpegBin); err != nil {
		issues = append(issues, DiagIssue{
			Category: "transcoding",
			Target:   "ffmpeg",
			Severity: "critical",
			Title:    "FFmpeg не найден",
			Detail:   fmt.Sprintf("Путь: %s — не найден в PATH", ffmpegBin),
		})
	}

	// Temp directory.
	tempRoot := cfg.Transcoding.TempRoot
	if tempRoot == "" {
		tempRoot = filepath.Join(cfg.Compat.RepoRoot, "cache", "transcoding")
	}
	if _, err := os.Stat(tempRoot); os.IsNotExist(err) {
		issues = append(issues, DiagIssue{
			Category: "transcoding",
			Target:   "tempdir",
			Severity: "warning",
			Title:    "Temp директория не существует",
			Detail:   tempRoot,
			FixID:    "fix_transcoding_tempdir",
			FixLabel: "Создать",
		})
	}

	// Stuck jobs — check via transcoding service.
	if ts := liveTransSvc(); ts != nil {
		snap := ts.StatsSnapshot()
		if jobs, ok := snap["jobs"].([]map[string]any); ok {
			for _, job := range jobs {
				created, _ := job["created_at"].(time.Time)
				if !created.IsZero() && time.Since(created) > 30*time.Minute {
					jobID, _ := job["id"].(string)
					issues = append(issues, DiagIssue{
						Category: "transcoding",
						Target:   jobID,
						Severity: "warning",
						Title:    fmt.Sprintf("Зависший job: %s (%.0f мин)", jobID, time.Since(created).Minutes()),
						FixID:    "fix_transcoding_cleanup",
						FixLabel: "Остановить все",
					})
				}
			}
		}
	}

	return issues
}

// ---------- 5. TMDB Proxy ----------

func inspectTMDB(cfg config.Config) []DiagIssue {
	var issues []DiagIssue

	tp := cfg.TMDBProxy

	if tp.Mode == "disabled" || tp.Mode == "" {
		issues = append(issues, DiagIssue{
			Category: "tmdb",
			Target:   "mode",
			Severity: "info",
			Title:    "TMDB Proxy отключён",
			Detail:   "Режим: " + tp.Mode,
			FixID:    "fix_tmdb_enable",
			FixLabel: "Включить (self)",
		})
		return issues
	}

	// Check upstream connectivity.
	host := tp.Host
	if host == "" {
		host = "tmdb.alcopa.cc"
	}
	if tp.Mode == "self" {
		apiHost := tp.APIHost
		if apiHost == "" {
			apiHost = "api.themoviedb.org"
		}
		// Check API key.
		if tp.APIKey == "" {
			issues = append(issues, DiagIssue{
				Category: "tmdb",
				Target:   "api_key",
				Severity: "warning",
				Title:    "TMDB API ключ не задан",
				Detail:   "Для режима self нужен ключ api.themoviedb.org",
			})
		}
		// Probe API host.
		if !probeHost(apiHost) {
			issues = append(issues, DiagIssue{
				Category: "tmdb",
				Target:   "upstream",
				Severity: "critical",
				Title:    "TMDB API недоступен: " + apiHost,
				FixID:    "fix_tmdb_switch_host",
				FixLabel: "Переключить на alcopa",
			})
		}
	} else {
		// alcopa mode — check proxy host.
		if !probeHost(host) {
			issues = append(issues, DiagIssue{
				Category: "tmdb",
				Target:   "upstream",
				Severity: "critical",
				Title:    "TMDB прокси недоступен: " + host,
			})
		}
	}

	return issues
}

// ---------- 6. YouTube ----------

func inspectYouTube(cfg config.Config) []DiagIssue {
	var issues []DiagIssue

	// yt-dlp.
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		issues = append(issues, DiagIssue{
			Category: "youtube",
			Target:   "yt-dlp",
			Severity: "critical",
			Title:    "yt-dlp не найден",
			Detail:   "YouTube не будет работать без yt-dlp",
		})
	}

	// Node.js runtime (needed for yt-dlp signature deciphering).
	hasNode := false
	for _, bin := range []string{"node", "deno", "bun"} {
		if _, err := exec.LookPath(bin); err == nil {
			hasNode = true
			break
		}
	}
	if !hasNode {
		issues = append(issues, DiagIssue{
			Category: "youtube",
			Target:   "js_runtime",
			Severity: "warning",
			Title:    "JS runtime не найден (node/deno/bun)",
			Detail:   "yt-dlp может не расшифровать подписи YouTube",
		})
	}

	// OAuth.
	if cfg.YouTubeOAuth.ClientID == "" {
		issues = append(issues, DiagIssue{
			Category: "youtube",
			Target:   "oauth",
			Severity: "info",
			Title:    "YouTube OAuth не настроен",
			Detail:   "Без OAuth пользователи не смогут подключить YouTube подписки",
		})
	}

	return issues
}

// ---------- 7. Proxy ----------

func inspectProxy(cfg config.Config) []DiagIssue {
	var issues []DiagIssue

	if len(cfg.Proxy.Vless.Entries) == 0 && cfg.Proxy.Vless.URI == "" {
		issues = append(issues, DiagIssue{
			Category: "proxy",
			Target:   "config",
			Severity: "info",
			Title:    "Прокси не настроен",
			Detail:   "Балансеры с GeoIP блокировкой могут не работать",
		})
		return issues
	}

	// Check binary availability.
	binDir := filepath.Join(cfg.Compat.RepoRoot, "bin")
	for _, engine := range []string{"xray", "mihomo"} {
		binPath := filepath.Join(binDir, engine)
		if _, err := os.Stat(binPath); os.IsNotExist(err) {
			issues = append(issues, DiagIssue{
				Category: "proxy",
				Target:   engine,
				Severity: "warning",
				Title:    engine + " не найден",
				Detail:   "Будет скачан автоматически при запуске",
				FixID:    "fix_proxy_download",
				FixLabel: "Скачать",
			})
		}
	}

	// Check SOCKS5 ports.
	if pp := liveProxyPool(); pp != nil {
		statuses := pp.Status()
		for _, st := range statuses {
			if !st.Alive {
				issues = append(issues, DiagIssue{
					Category: "proxy",
					Target:   st.Label,
					Severity: "critical",
					Title:    fmt.Sprintf("Прокси %s не запущен", st.Label),
					FixID:    "fix_proxy_restart",
					FixLabel: "Перезапустить",
				})
			} else if st.SOCKSAddr != "" {
				// Check SOCKS5 port actually listens.
				host, port, _ := net.SplitHostPort(st.SOCKSAddr)
				if host == "" {
					host = "127.0.0.1"
				}
				conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 2*time.Second)
				if err != nil {
					issues = append(issues, DiagIssue{
						Category: "proxy",
						Target:   st.Label,
						Severity: "critical",
						Title:    fmt.Sprintf("SOCKS5 порт %s не отвечает", st.SOCKSAddr),
						Detail:   err.Error(),
						FixID:    "fix_proxy_restart",
						FixLabel: "Перезапустить",
					})
				} else {
					conn.Close()
				}
			}
		}
	}

	return issues
}

// ---------- Helpers ----------

// toFloat64Any coerces a JSON-decoded number to float64. Copied (pure) from the
// host's admin_panel_helpers.go — the inspector's stats-diff needs it locally.
func toFloat64Any(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	default:
		return 0, false
	}
}

// probeHost makes a quick HEAD request to check if a host is reachable.
func probeHost(host string) bool {
	if !strings.HasPrefix(host, "http") {
		host = "https://" + host
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Head(host)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// ---------- AutoFix Dispatcher ----------

func applyFix(cfg config.Config, fixID, target string) (string, error) {
	switch fixID {
	case "fix_cleanup_cache":
		return fixCleanupCache(cfg)
	case "fix_file_permissions":
		return fixFilePermissions(cfg)
	case "fix_balancer_reenable":
		return fixBalancerReenable(target)
	case "fix_balancer_enable":
		return fixBalancerEnable(target)
	case "fix_ts_password":
		return fixTSPassword(cfg)
	case "fix_ts_config":
		return fixTSConfig()
	case "fix_transcoding_enable":
		return fixTranscodingEnable()
	case "fix_transcoding_tempdir":
		return fixTranscodingTempdir(cfg)
	case "fix_transcoding_cleanup":
		return fixTranscodingCleanup()
	case "fix_tmdb_enable":
		return fixTMDBEnable()
	case "fix_tmdb_switch_host":
		return fixTMDBSwitchHost()
	case "fix_proxy_restart":
		return fixProxyRestart()
	default:
		return "", fmt.Errorf("unknown fix: %s", fixID)
	}
}

func fixCleanupCache(cfg config.Config) (string, error) {
	cacheDir := filepath.Join(cfg.Compat.RepoRoot, "cache")
	// Remove transcoding temp files.
	transDir := filepath.Join(cacheDir, "transcoding")
	if entries, err := os.ReadDir(transDir); err == nil {
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(transDir, e.Name()))
		}
	}
	// Remove image cache.
	imgDir := filepath.Join(cacheDir, "img")
	if entries, err := os.ReadDir(imgDir); err == nil {
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(imgDir, e.Name()))
		}
	}
	return "Кеш очищен", nil
}

func fixFilePermissions(cfg config.Config) (string, error) {
	repoRoot := cfg.Compat.RepoRoot
	fixed := 0
	for _, rel := range []string{"torrserver/accs.db", "cache/aeskey", "database/tgauth/tokens.json"} {
		path := filepath.Join(repoRoot, rel)
		if _, err := os.Stat(path); err == nil {
			if err := os.Chmod(path, 0o600); err == nil {
				fixed++
			}
		}
	}
	return fmt.Sprintf("Исправлено файлов: %d", fixed), nil
}

func fixBalancerReenable(target string) (string, error) {
	hc := balancerhealth.GetGlobalHealthChecker()
	if hc == nil {
		return "", fmt.Errorf("health checker не запущен")
	}
	if hc.ManualReEnable(target) {
		log.Info().Str("balancer", target).Msg("inspector: manually re-enabled balancer")
		return target + " включён обратно", nil
	}
	return "", fmt.Errorf("балансер %s не найден в healthcheck", target)
}

func fixBalancerEnable(target string) (string, error) {
	err := updateInitConfMap(func(root map[string]any) {
		section := ensureMapChild(root, target)
		section["enable"] = true
	})
	if err != nil {
		return "", err
	}
	return target + " включён в конфиге", nil
}

func fixTSPassword(cfg config.Config) (string, error) {
	homeDir := cfg.TorrServer.HomeDir
	if homeDir == "" {
		homeDir = filepath.Join(cfg.Compat.RepoRoot, "torrserver")
	}
	_ = os.MkdirAll(homeDir, 0o755)

	// Generate random password.
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	pw := hex.EncodeToString(b)[:16]

	accsPath := filepath.Join(homeDir, "accs.db")
	data := fmt.Sprintf(`{"ts":"%s"}`, pw)
	if err := os.WriteFile(accsPath, []byte(data), 0o600); err != nil {
		return "", err
	}

	log.Info().Str("path", accsPath).Msg("inspector: generated TorrServer password")
	return "Пароль сгенерирован в " + accsPath + ". Перезапустите TorrServer для применения.", nil
}

func fixTSConfig() (string, error) {
	err := updateConfigTOMLMap(func(root map[string]any) {
		section := setNestedMap(root, "torrserver")
		if _, ok := section["port"]; !ok {
			section["port"] = 9080
		}
	})
	if err != nil {
		return "", err
	}
	return "TorrServer порт установлен на 9080", nil
}

func fixTranscodingEnable() (string, error) {
	err := updateConfigTOMLMap(func(root map[string]any) {
		section := setNestedMap(root, "transcoding")
		section["enable"] = true
	})
	if err != nil {
		return "", err
	}
	return "Транскодирование включено", nil
}

func fixTranscodingTempdir(cfg config.Config) (string, error) {
	tempRoot := cfg.Transcoding.TempRoot
	if tempRoot == "" {
		tempRoot = filepath.Join(cfg.Compat.RepoRoot, "cache", "transcoding")
	}
	if err := os.MkdirAll(tempRoot, 0o755); err != nil {
		return "", err
	}
	return "Директория создана: " + tempRoot, nil
}

func fixTranscodingCleanup() (string, error) {
	if ts := liveTransSvc(); ts != nil {
		ts.StopAll()
		return "Все transcoding jobs остановлены", nil
	}
	return "", fmt.Errorf("transcoding service не запущен")
}

func fixTMDBEnable() (string, error) {
	err := updateConfigTOMLMap(func(root map[string]any) {
		section := setNestedMap(root, "tmdb_proxy")
		section["mode"] = "self"
	})
	if err != nil {
		return "", err
	}
	return "TMDB Proxy включён в режиме self", nil
}

func fixTMDBSwitchHost() (string, error) {
	err := updateConfigTOMLMap(func(root map[string]any) {
		section := setNestedMap(root, "tmdb_proxy")
		section["mode"] = "alcopa"
		section["host"] = "tmdb.alcopa.cc"
	})
	if err != nil {
		return "", err
	}
	return "TMDB Proxy переключён на alcopa (tmdb.alcopa.cc)", nil
}

func fixProxyRestart() (string, error) {
	if !serverReady() {
		return "", fmt.Errorf("server not initialized")
	}
	if err := reloadProxies(); err != nil {
		return "", err
	}
	return "Прокси перезапущен", nil
}
