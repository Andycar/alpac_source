package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/jsmodules"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// tgAdminJSModulesHandler owns GET/POST /admin/api/modules{,/*}.
func tgAdminJSModulesHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, mgr *jsmodules.Manager) http.HandlerFunc {
	return jsModulesHandlerWithPrefix(tgStore, adminStore, mgr, "/api/modules")
}

// tgAdminSISISourcesHandler owns GET/POST /admin/api/sisi-sources{,/*}.
// Identical logic to tgAdminJSModulesHandler but targets a separate goja
// Manager that scans the sisi_sources/ directory.
func tgAdminSISISourcesHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, mgr *jsmodules.Manager) http.HandlerFunc {
	return jsModulesHandlerWithPrefix(tgStore, adminStore, mgr, "/api/sisi-sources")
}

// tgAdminMusicSourcesHandler owns GET/POST /admin/api/music-sources{,/*}.
func tgAdminMusicSourcesHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, mgr *jsmodules.Manager) http.HandlerFunc {
	return jsModulesHandlerWithPrefix(tgStore, adminStore, mgr, "/api/music-sources")
}

// jsModulesHandlerWithPrefix is the shared switchboard used by both
// tgAdminJSModulesHandler and tgAdminSISISourcesHandler.
func jsModulesHandlerWithPrefix(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, mgr *jsmodules.Manager, apiPrefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore); !ok {
			return
		}
		if mgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "jsmodules manager disabled"})
			return
		}

		tail := strings.TrimPrefix(r.URL.Path, "/")
		if i := strings.Index(tail, apiPrefix); i >= 0 {
			tail = tail[i+len(apiPrefix):]
		}
		tail = strings.TrimPrefix(tail, "/")

		switch {
		case tail == "" && r.Method == http.MethodGet:
			adminJSModulesList(w, mgr)
		case tail == "install" && r.Method == http.MethodPost:
			adminJSModulesInstall(w, r, mgr)
		case tail == "logs" && r.Method == http.MethodGet:
			adminJSModulesLogs(w, r, mgr)
		case strings.HasPrefix(tail, "logs/stream") && r.Method == http.MethodGet:
			adminJSModulesLogStream(w, r, mgr)
		default:
			// Everything else is /api/modules/{id}[/action]
			parts := strings.Split(tail, "/")
			if len(parts) == 0 || parts[0] == "" {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			id := parts[0]
			action := ""
			if len(parts) > 1 {
				action = parts[1]
			}
			adminJSModulesItem(w, r, mgr, id, action)
		}
	}
}

type moduleDTO struct {
	ID           string                        `json:"id"`
	Name         string                        `json:"name"`
	Version      string                        `json:"version"`
	Author       string                        `json:"author,omitempty"`
	Description  string                        `json:"description,omitempty"`
	Icon         string                        `json:"icon,omitempty"`
	Quality      string                        `json:"quality,omitempty"`
	ContentTypes []string                      `json:"content_types,omitempty"`
	Languages    []string                      `json:"languages,omitempty"`
	Tags         []string                      `json:"tags,omitempty"`
	Repository   string                        `json:"repository,omitempty"`
	Anime        bool                          `json:"anime,omitempty"`
	Ukrainian    bool                          `json:"ukrainian,omitempty"`
	ConfigSchema []jsmodules.ConfigField       `json:"config_schema,omitempty"`
	Enabled      bool                          `json:"enabled"`
	Error        string                        `json:"error,omitempty"`
	LoadedAt     time.Time                     `json:"loaded_at,omitempty"`
	Stats        jsmodules.ModuleStatsSnapshot `json:"stats"`
	Config       map[string]any                `json:"config,omitempty"`
}

func toModuleDTO(m *jsmodules.Module) moduleDTO {
	cfg := m.Config()
	// Hide secret fields from listing.
	for _, f := range m.Manifest.ConfigSchema {
		if f.Secret || f.Type == "secret" {
			if _, ok := cfg[f.Key]; ok {
				cfg[f.Key] = "••••••"
			}
		}
	}
	return moduleDTO{
		ID: m.Manifest.ID, Name: m.Manifest.Name, Version: m.Manifest.Version,
		Author: m.Manifest.Author, Description: m.Manifest.Description,
		Icon: m.Manifest.Icon, Quality: m.Manifest.Quality,
		ContentTypes: m.Manifest.ContentTypes, Languages: m.Manifest.Languages,
		Tags: m.Manifest.Tags, Repository: m.Manifest.Repository,
		Anime: m.Manifest.Anime, Ukrainian: m.Manifest.Ukrainian,
		ConfigSchema: m.Manifest.ConfigSchema,
		Enabled:      m.Enabled,
		Error:        m.Error,
		LoadedAt:     m.LoadedAt,
		Config:       cfg,
	}
}

func moduleStats(mgr *jsmodules.Manager, id string) jsmodules.ModuleStatsSnapshot {
	s, _ := mgr.StatsSnapshot(id)
	return s
}

func adminJSModulesList(w http.ResponseWriter, mgr *jsmodules.Manager) {
	mods := mgr.List()
	out := make([]moduleDTO, 0, len(mods))
	for _, m := range mods {
		dto := toModuleDTO(m)
		dto.Stats = moduleStats(mgr, m.PluginKey())
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

func adminJSModulesItem(w http.ResponseWriter, r *http.Request, mgr *jsmodules.Manager, id, action string) {
	id = strings.ToLower(id)
	mod, ok := mgr.Get(id)
	if !ok && action != "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "module not found"})
		return
	}

	switch {
	case r.Method == http.MethodGet && action == "":
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "module not found"})
			return
		}
		dto := toModuleDTO(mod)
		dto.Stats = moduleStats(mgr, id)
		writeJSON(w, http.StatusOK, dto)

	case r.Method == http.MethodGet && action == "source":
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     mod.Manifest.ID,
			"source": mod.Source,
			"error":  mod.Error,
		})

	case r.Method == http.MethodPut && action == "source":
		body, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		var req struct {
			Source string `json:"source"`
		}
		if stdjson.Unmarshal(body, &req) != nil || req.Source == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
			return
		}
		if err := mod.SetSource(req.Source); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case r.Method == http.MethodPut && action == "config":
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var req map[string]any
		if stdjson.Unmarshal(body, &req) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
			return
		}
		if err := mod.SetConfig(req); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case r.Method == http.MethodPost && action == "toggle":
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		var req struct {
			Enabled bool `json:"enabled"`
		}
		_ = stdjson.Unmarshal(body, &req)
		if err := mgr.SetEnabled(id, req.Enabled); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case r.Method == http.MethodPost && action == "reload":
		if err := mgr.Reload(id); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case r.Method == http.MethodDelete && action == "":
		if err := mgr.Remove(id); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func adminJSModulesInstall(w http.ResponseWriter, r *http.Request, mgr *jsmodules.Manager) {
	ctype := r.Header.Get("Content-Type")
	if strings.HasPrefix(ctype, "application/json") {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var req struct {
			URL string `json:"url"`
		}
		if stdjson.Unmarshal(body, &req) != nil || strings.TrimSpace(req.URL) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
			return
		}
		mod, err := mgr.InstallFromURL(r.Context(), req.URL)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": mod.Manifest.ID})
		return
	}

	// Otherwise treat as multipart upload or raw zip body.
	data, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil || len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty upload"})
		return
	}
	mod, err := mgr.InstallFromZip(data)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": mod.Manifest.ID})
}

func adminJSModulesLogs(w http.ResponseWriter, r *http.Request, mgr *jsmodules.Manager) {
	module := r.URL.Query().Get("module")
	n := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 && v <= 2000 {
		n = v
	}
	writeJSON(w, http.StatusOK, mgr.LogTail(module, n))
}

// adminJSModulesLogStream streams log entries as Server-Sent Events.
func adminJSModulesLogStream(w http.ResponseWriter, r *http.Request, mgr *jsmodules.Manager) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	module := r.URL.Query().Get("module")
	id, ch := mgr.LogSubscribe(64)
	defer mgr.LogUnsubscribe(id)

	// Send initial tail so the UI gets context immediately.
	for _, e := range mgr.LogTail(module, 50) {
		writeSSEEntry(w, e)
	}
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			if module != "" && e.Module != module {
				continue
			}
			writeSSEEntry(w, e)
			flusher.Flush()
		}
	}
}

func writeSSEEntry(w http.ResponseWriter, e jsmodules.LogEntry) {
	b, _ := stdjson.Marshal(e)
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(b)
	_, _ = w.Write([]byte("\n\n"))
}

// Silence unused-import warning for chi until routes are registered.
var _ = chi.NewRouter
