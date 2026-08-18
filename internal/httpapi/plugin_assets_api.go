package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"lampac-go/internal/config"
)

func personalLampaHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
}

func invcRchJSHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		source, err := loadPluginTemplate("invc-rch.js", cfg)
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		source = strings.ReplaceAll(source, "{localhost}", hostFromRequest(r))
		source = "(function(){'use strict'; " + source + " })();"

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(source))
	}
}

func invcWsJSHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(chiURLParam(r, "token"))
		version := loadSyncUserVersion(cfg)

		template := "invc-ws.js"
		if version != 1 {
			template = filepath.Join("sync_v2", "invc-ws.js")
		}

		source, err := loadPluginTemplate(template, cfg)
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		invcRch, errRch := loadPluginTemplate("invc-rch.js", cfg)
		invcRchNWS, errNWS := loadPluginTemplate("invc-rch_nws.js", cfg)
		if errRch != nil || errNWS != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		source = strings.ReplaceAll(source, "{invc-rch}", invcRch)
		source = strings.ReplaceAll(source, "{invc-rch_nws}", invcRchNWS)
		source = strings.ReplaceAll(source, "{localhost}", hostFromRequest(r))
		source = strings.ReplaceAll(source, "{token}", url.QueryEscape(token))

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(source))
	}
}

func nwsClientJSHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		source, err := loadPluginTemplate("nws-client-es5.js", cfg)
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}
		if strings.Contains(source, "{localhost}") {
			source = strings.ReplaceAll(source, "{localhost}", hostFromRequest(r))
		}

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(source))
	}
}

func signalrES5Handler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		source, err := loadPluginTemplate("signalr-6.0.25_es5.js", cfg)
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(source))
	}
}

func startpageJSHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		source, err := loadPluginTemplate("startpage.js", cfg)
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		source = strings.ReplaceAll(source, "{localhost}", hostFromRequest(r))
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(source))
	}
}

// msxAppHostMatch reports whether the request arrived on the app's canonical
// host (capi.public_host) — i.e. the MSX start parameter WAS the app domain
// (e.g. "tv.alcopa.cc"), not the Lampa/server one. MSX start parameters are
// bare hosts (paths don't survive there), so per-host branching is how one
// instance serves both worlds: the app domain gets a pure ALPAC launcher, any
// other host gets the full one.
func msxAppHostMatch(cfg config.Config, r *http.Request) bool {
	ph := strings.TrimSpace(cfg.Capi.PublicHost)
	if ph == "" {
		return false
	}
	if u, err := url.Parse(ph); err == nil && u.Host != "" {
		ph = u.Host
	}
	req := hostFromRequest(r)
	if i := strings.Index(req, "://"); i >= 0 {
		req = req[i+3:]
	}
	return strings.EqualFold(strings.TrimSuffix(req, "/"), ph)
}

func msxStartJSONHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg = liveConfig(cfg) // admin edits to public_host/app_dir apply without restart
		candidates := []string{
			filepath.Join(cfg.Compat.RepoRoot, "msx.json"),
			"msx.json",
			"/home/msx.json",
		}
		var data []byte
		for _, p := range candidates {
			raw, err := os.ReadFile(p)
			if err == nil {
				data = raw
				break
			}
		}

		// Stale v1 auto-default on disk (the Lampa-era «Загрузчик приложений»
		// with third-party FXML/Fork tiles — older builds persisted the built-in
		// default verbatim, freezing it forever): that's generated content, not
		// an admin override, so ignore it and serve the current built-in below.
		// A hand-edited msx.json won't carry both v1 markers at once.
		if len(data) > 0 && strings.Contains(string(data), "Загрузчик приложений") && strings.Contains(string(data), "FXMLPlayer") {
			data = nil
		}

		// No admin override → serve the built-in launcher. Never persisted:
		// writing it to disk is what froze v1 defaults on every server. The
		// ALPAC tiles link to the /app SPA — skip them when app_dir is not
		// configured (pure-Lampa servers), or the first tile would 404.
		if len(data) == 0 {
			hasApp := strings.TrimSpace(cfg.Capi.AppDir) != ""
			switch {
			case hasApp && msxAppHostMatch(cfg, r):
				// The user pointed MSX at the app domain itself — no Lampa tile.
				data = []byte(msxAppOnlyJSON)
			case hasApp:
				data = []byte(msxDefaultJSON)
			default:
				data = []byte(msxLampaOnlyJSON)
			}
		}

		source := strings.ReplaceAll(string(data), "{localhost}", hostFromRequest(r))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(source))
	}
}

// msxDefaultJSON is the default MSX start.json generated when no msx.json
// exists — the ALPAC launcher for TVs where the widget can't be installed:
// the user points the Media Station X start parameter at this server, MSX
// fetches /msx/start.json and shows launch tiles; `link:` navigates the MSX
// webview to the web app itself, so no widget/dev-mode is ever needed. The
// file doubles as its own content page (parameter self-reference) — one URL
// serves both the start object and the menu. Admins may override it by placing
// msx.json in the repo root (served verbatim unless it's a stale v1 default).
const msxDefaultJSON = `{
  "name": "ALPAC",
  "headline": "ALPAC",
  "extension": "запуск без установки виджета",
  "version": "2.0.0",
  "parameter": "content:{localhost}/msx/start.json",
  "action": "[settings:validate_links:0|home]",
  "pages": [
    {
      "items": [
        {
          "id": "description",
          "type": "space",
          "layout": "5,0,5,5",
          "text": "Выберите приложение и нажмите {txt:msx-white:OK}.{br}{br}Оно откроется прямо в этом окне — установка виджета не требуется."
        },
        {
          "type": "control",
          "layout": "0,0,5,1",
          "image": "{localhost}/app/icon-192.png",
          "label": "ALPAC",
          "action": "link:{localhost}/app",
          "selection": {
            "important": true,
            "action": "update:content:description",
            "data": {
              "text": [
                "{txt:msx-white:ALPAC} — фильмы, сериалы и ТВ в современном интерфейсе для телевизора.{br}{br}Кнопка {txt:msx-white:Return} — назад."
              ]
            }
          }
        },
        {
          "type": "control",
          "layout": "0,1,5,1",
          "label": "Lampa",
          "action": "link:{localhost}",
          "selection": {
            "important": true,
            "action": "update:content:description",
            "data": {
              "text": [
                "{txt:msx-white:Lampa} — классический интерфейс Lampa с этого же сервера."
              ]
            }
          }
        }
      ]
    }
  ]
}`

// msxAppOnlyJSON is served when the MSX start parameter was the app's own
// canonical domain (request host == capi.public_host, e.g. "tv.alcopa.cc"):
// the user asked for the app by name — one focused tile, OK → ALPAC, no Lampa.
// {localhost} keeps the scheme MSX actually reached us with (http for old TVs).
const msxAppOnlyJSON = `{
  "name": "ALPAC",
  "headline": "ALPAC",
  "extension": "запуск без установки виджета",
  "version": "2.0.0",
  "parameter": "content:{localhost}/msx/start.json",
  "action": "[settings:validate_links:0|home]",
  "pages": [
    {
      "items": [
        {
          "id": "description",
          "type": "space",
          "layout": "5,0,5,5",
          "text": "Нажмите {txt:msx-white:OK} — ALPAC откроется прямо в этом окне.{br}{br}Установка виджета не требуется."
        },
        {
          "type": "control",
          "layout": "0,0,5,1",
          "image": "{localhost}/app/icon-192.png",
          "label": "ALPAC",
          "action": "link:{localhost}/app"
        }
      ]
    }
  ]
}`

// msxLampaOnlyJSON is the built-in launcher for servers without the /app SPA
// (capi app_dir not configured) — Lampa at the web root is all they serve.
const msxLampaOnlyJSON = `{
  "name": "Lampa",
  "headline": "Lampa",
  "extension": "запуск без установки виджета",
  "version": "2.0.0",
  "parameter": "content:{localhost}/msx/start.json",
  "action": "[settings:validate_links:0|home]",
  "pages": [
    {
      "items": [
        {
          "id": "description",
          "type": "space",
          "layout": "5,0,5,5",
          "text": "Нажмите {txt:msx-white:OK} — Lampa откроется прямо в этом окне. Установка виджета не требуется."
        },
        {
          "type": "control",
          "layout": "0,0,5,1",
          "label": "Lampa",
          "action": "link:{localhost}"
        }
      ]
    }
  ]
}`

func loadSyncUserVersion(cfg config.Config) int {
	paths := make([]string, 0, 5)
	if root := strings.TrimSpace(cfg.Compat.RepoRoot); root != "" {
		paths = append(paths, filepath.Join(root, "init.conf"))
		paths = append(paths, filepath.Join(root, "config", "init.conf"))
	}
	if home := strings.TrimSpace(os.Getenv("LAMPAC_GO_HOME")); home != "" {
		paths = append(paths, filepath.Join(home, "init.conf"))
	}
	paths = append(paths, "init.conf")

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if version, ok := parseSyncUserVersion(data); ok {
			return version
		}
	}

	return 2
}

func parseSyncUserVersion(data []byte) (int, bool) {
	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return 0, false
	}

	node, ok := root["sync_user"].(map[string]any)
	if !ok {
		return 0, false
	}
	raw, ok := node["version"]
	if !ok {
		return 0, false
	}

	switch t := raw.(type) {
	case float64:
		if int(t) > 0 {
			return int(t), true
		}
	case int:
		if t > 0 {
			return t, true
		}
	case string:
		switch strings.TrimSpace(t) {
		case "1":
			return 1, true
		case "2":
			return 2, true
		}
	}

	return 0, false
}
