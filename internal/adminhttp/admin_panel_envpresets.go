package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"strings"

	"lampac-go/internal/envpresets"
	"lampac-go/internal/tgauth"
)

// tgAdminEnvPresetsHandler owns GET/POST /admin/api/env-presets{,/*}.
//
// Endpoints:
//
//	GET  /api/env-presets                — list installed presets + stored config
//	POST /api/env-presets/install        — body: {url|zip-bytes, config:{}}
//	                                       Возвращает план без exec post_install.
//	POST /api/env-presets/{id}/run       — выполнить post_install для уже
//	                                       установленного пресета (отдельный шаг,
//	                                       чтобы admin видел план перед exec).
//	POST /api/env-presets/{id}/uninstall — удалить файлы + post_uninstall
//	GET  /api/env-presets/{id}           — manifest + state + config
//	PUT  /api/env-presets/{id}/config    — обновить config (пере-рендер templating
//	                                       при следующем install/run)
func tgAdminEnvPresetsHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, mgr *envpresets.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore); !ok {
			return
		}
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "envpresets manager disabled"})
			return
		}

		tail := strings.TrimPrefix(r.URL.Path, "/")
		if i := strings.Index(tail, "/api/env-presets"); i >= 0 {
			tail = tail[i+len("/api/env-presets"):]
		}
		tail = strings.TrimPrefix(tail, "/")

		switch {
		case tail == "" && r.Method == http.MethodGet:
			adminEnvPresetsList(w, mgr)
		case tail == "install" && r.Method == http.MethodPost:
			adminEnvPresetsInstall(w, r, mgr)
		default:
			parts := strings.SplitN(tail, "/", 2)
			id := parts[0]
			action := ""
			if len(parts) > 1 {
				action = parts[1]
			}
			adminEnvPresetsItem(w, r, mgr, id, action)
		}
	}
}

// GET /api/env-presets — список installed presets.
func adminEnvPresetsList(w http.ResponseWriter, mgr *envpresets.Manager) {
	presets := mgr.List()
	out := make([]map[string]any, 0, len(presets))
	for _, p := range presets {
		entry := map[string]any{
			"id":         p.ID,
			"loaded_at":  p.LoadedAt,
			"last_error": p.LastErr,
		}
		if p.Manifest != nil {
			entry["name"] = p.Manifest.Name
			entry["version"] = p.Manifest.Version
			entry["description"] = p.Manifest.Description
			entry["author"] = p.Manifest.Author
			entry["params"] = p.Manifest.Params
			entry["files"] = p.Manifest.Files
			entry["deps"] = p.Manifest.Deps
			entry["post_install"] = p.Manifest.PostInstall
			entry["ports"] = p.Manifest.Ports
			entry["requires_root"] = p.Manifest.RequiresRoot
		}
		entry["installed"] = p.State != nil
		entry["config"] = p.Config
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": out})
}

// POST /api/env-presets/install — body:
//
//	{
//	  "url": "https://hub.alcopa.cc/hub/dl/{slug}/{ver}/module.zip",
//	  "config": { "socks_port": 40007, ... },
//	  "plan_only": true|false
//	}
//
// или multipart с file поле + form-fields.
func adminEnvPresetsInstall(w http.ResponseWriter, r *http.Request, mgr *envpresets.Manager) {
	contentType := r.Header.Get("Content-Type")
	var (
		zipBytes []byte
		userCfg  map[string]any
		planOnly bool
	)

	if strings.HasPrefix(contentType, "application/json") {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		var req struct {
			URL      string         `json:"url"`
			Config   map[string]any `json:"config"`
			PlanOnly bool           `json:"plan_only"`
		}
		if err := stdjson.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		userCfg = req.Config
		planOnly = req.PlanOnly
		if req.URL == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
			return
		}
		// Скачиваем ZIP с hub. Лимит — 8MB.
		resp, err := http.Get(req.URL)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "download: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "download status " + resp.Status})
			return
		}
		zipBytes, err = io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if len(zipBytes) > 8*1024*1024 {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "ZIP > 8MB"})
			return
		}
	} else {
		// multipart upload
		if err := r.ParseMultipartForm(8 * 1024 * 1024); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file field required"})
			return
		}
		defer f.Close()
		zipBytes, _ = io.ReadAll(io.LimitReader(f, 8*1024*1024+1))
		if cfgRaw := r.FormValue("config"); cfgRaw != "" {
			_ = stdjson.Unmarshal([]byte(cfgRaw), &userCfg)
		}
		planOnly = r.FormValue("plan_only") == "true"
	}

	res, err := mgr.InstallFromZIP(r.Context(), zipBytes, userCfg, planOnly)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": err.Error(),
			"plan":  res,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"plan": res,
	})
}

// /api/env-presets/{id}/* — per-preset actions.
func adminEnvPresetsItem(w http.ResponseWriter, r *http.Request, mgr *envpresets.Manager, id, action string) {
	p := mgr.Get(id)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "preset not installed: " + id})
		return
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"id":       p.ID,
			"manifest": p.Manifest,
			"state":    p.State,
			"config":   p.Config,
		})
	case action == "run" && r.Method == http.MethodPost:
		results, err := mgr.RunPostInstall(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"results": results,
		})
	case action == "uninstall" && r.Method == http.MethodPost:
		res, err := mgr.Uninstall(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":  err.Error(),
				"result": res,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
