package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"lampac-go/internal/config"
	"lampac-go/internal/modules"
)

var lampaTypeSanitizer = regexp.MustCompile(`[^a-z0-9\-]`)

type lampaWebRuntimeSettings struct {
	Path  string
	Index string
}

type appRuntimeSettings struct {
	PlayerInnerEnabled bool
	TranscodingEnabled bool
}

func appMinJSHandler(cfg config.Config, manifest []modules.RootModule) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		typ := resolveLampaType(r, cfg.Compat.RepoRoot)
		if typ == "" {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(""))
			return
		}

		raw, ok := readLampaWebAsset(cfg.Compat.RepoRoot, typ, "app.min.js")
		if !ok {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		host := hostFromRequest(r)
		out := raw
		out = strings.ReplaceAll(out, "http://lite.lampa.mx", host+"/"+typ)
		out = strings.ReplaceAll(out, "https://yumata.github.io/lampa-lite", host+"/"+typ)
		out = strings.ReplaceAll(out, "http://lampa.mx", host+"/"+typ)
		out = strings.ReplaceAll(out, "https://yumata.github.io/lampa", host+"/"+typ)
		out = strings.ReplaceAll(out, "{localhost}", host)

		out = strings.ReplaceAll(out, "{jachost}", resolveJacHost(host))

		// Best-effort player-inner injection, equivalent to legacy intent.
		if playerInner, ok := loadPlayerInnerSnippet(cfg.Compat.RepoRoot, cfg); ok {
			out = strings.ReplaceAll(out, "Player.play(element);", playerInner)
		}

		// Apply user-defined regex replacements (appReplace).
		// Read from live config so hot-reload picks up changes immediately.
		liveCfg := liveConfig(cfg)
		for _, rule := range liveCfg.Web.AppReplace {
			if !rule.Enabled || rule.Pattern == "" {
				continue
			}
			re, err := regexp.Compile(rule.Pattern)
			if err != nil {
				continue
			}
			out = re.ReplaceAllString(out, rule.Replacement)
		}

		// Append custom JS code from admin panel.
		if liveCfg.Web.CustomJS != "" {
			out += "\n;" + liveCfg.Web.CustomJS
		}

		// Append Yandex.Metrika counter. app.min.js is loaded by EVERY client
		// (web + Android WebView + Tizen + webOS all pull the same bundle), so
		// injecting here — rather than via an autoloaded plugin — guarantees the
		// counter fires on all channels regardless of the plugin manager. The
		// snippet self-detects and tags the client channel for segmentation.
		if snippet := yandexMetrikaSnippet(liveCfg.Web.YandexMetrika); snippet != "" {
			out += "\n" + snippet
		}

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out))
	}
}

func appCSSHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		typ := resolveLampaType(r, cfg.Compat.RepoRoot)
		if typ == "" {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(""))
			return
		}

		raw, ok := readLampaWebAsset(cfg.Compat.RepoRoot, typ, filepath.Join("css", "app.css"))
		if !ok {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		host := hostFromRequest(r)
		hostNoScheme := strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		out := raw
		out = strings.ReplaceAll(out, "{localhost}", host)
		out = strings.ReplaceAll(out, "{host}", hostNoScheme)

		// Append custom CSS from admin panel.
		liveCfg := liveConfig(cfg)
		if liveCfg.Web.CustomCSS != "" {
			out += "\n" + liveCfg.Web.CustomCSS
		}

		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out))
	}
}

func resolveLampaType(r *http.Request, cfgRoot string) string {
	typ := strings.TrimSpace(chiURLParam(r, "type"))
	if typ != "" {
		return sanitizeLampaType(typ)
	}

	settings := loadLampaWebRuntimeSettings(cfgRoot)
	if settings.Path != "" {
		return sanitizeLampaType(settings.Path)
	}

	index := strings.TrimSpace(settings.Index)
	if index == "" {
		// No LampaWeb config — auto-detect from wwwroot the same way
		// lampaIndexHandler does.
		ls := loadLampaIndexSettings(cfgRoot)
		index = strings.TrimSpace(ls.Index)
		if index == "" {
			index = detectDefaultLampaIndex(cfgRoot, ls)
		}
	}

	if idx := strings.Index(index, "/"); idx > 0 {
		return sanitizeLampaType(index[:idx])
	}
	return ""
}

func sanitizeLampaType(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = lampaTypeSanitizer.ReplaceAllString(v, "")
	return v
}

func loadLampaWebRuntimeSettings(cfgRoot string) lampaWebRuntimeSettings {
	data, ok := readFileAny("init.conf")
	if !ok {
		data, _ = os.ReadFile(filepath.Join(cfgRoot, "init.conf"))
	}
	if len(data) == 0 {
		return lampaWebRuntimeSettings{}
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return lampaWebRuntimeSettings{}
	}

	node, ok := root["LampaWeb"].(map[string]any)
	if !ok {
		return lampaWebRuntimeSettings{}
	}
	return lampaWebRuntimeSettings{
		Path:  strings.TrimSpace(toString(node["path"])),
		Index: strings.TrimSpace(toString(node["index"])),
	}
}

func readLampaWebAsset(cfgRoot, typ, rel string) (string, bool) {
	candidates := []string{
		filepath.Join(cfgRoot, "wwwroot", typ, rel),
		filepath.Join("wwwroot", typ, rel),
		filepath.Join("/home/wwwroot", typ, rel),
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil {
			return string(data), true
		}
	}
	return "", false
}

// ★Здесь обязаны подставляться ВСЕ плейсхолдеры player-inner.js. Это второй путь его инъекции
// (первый — plugins.go, ветка "{player-inner}"), и пропущенный плейсхолдер уезжает в app.min.js как
// есть. `{filmix-direct}` так и уехал: JS прочитал `{filmix-direct}` как блок с выражением
// `filmix - direct` и весь плеер падал с «filmix is not defined» ещё до открытия торрента.
func loadPlayerInnerSnippet(cfgRoot string, cfg config.Config) (string, bool) {
	candidates := []string{
		filepath.Join(cfgRoot, "plugins", "player-inner.js"),
		filepath.Join("plugins", "player-inner.js"),
		filepath.Join("/home/plugins", "player-inner.js"),
	}
	var source string
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil {
			source = string(data)
			break
		}
	}
	if source == "" {
		return "", false
	}

	runtime := loadAppRuntimeSettings()
	source = strings.ReplaceAll(source, "{useplayer}", strings.ToLower(strconv.FormatBool(runtime.PlayerInnerEnabled)))
	source = strings.ReplaceAll(source, "{notUseTranscoding}", strings.ToLower(strconv.FormatBool(!runtime.TranscodingEnabled)))

	fxDirect := ""
	if cfg.Online.Filmix.DirectLampa {
		if raw, err := loadPluginTemplate("filmix-direct.js", cfg); err == nil {
			fxDirect = raw
		}
	}
	source = strings.ReplaceAll(source, "{filmix-direct}", fxDirect)
	return source, true
}

func loadAppRuntimeSettings() appRuntimeSettings {
	out := appRuntimeSettings{}
	data, ok := readFileAny("init.conf")
	if !ok {
		return out
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return out
	}

	out.PlayerInnerEnabled = strings.TrimSpace(toString(root["playerInner"])) != ""
	if transcoding, ok := root["transcoding"].(map[string]any); ok {
		out.TranscodingEnabled = toBool(transcoding["enable"])
	}
	return out
}
