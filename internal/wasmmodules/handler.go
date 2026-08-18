package wasmmodules

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
)

// Handler returns an http.Handler dispatching /lite/{id}/... to the guest's
// handle(). Mirrors jsmodules.Manager.Handler so dynamic_routes can swap one
// for the other transparently.
func (m *Manager) Handler(id string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mod, ok := m.Get(id)
		if !ok {
			http.Error(w, fmt.Sprintf(`{"error":"module %q not installed"}`, id), http.StatusNotFound)
			return
		}
		if !mod.Enabled {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(`{"rch":false,"error":"disabled"}`))
			return
		}
		if !mod.Manifest.HasServer() {
			http.Error(w, `{"error":"client-only module"}`, http.StatusBadRequest)
			return
		}

		checksearch := equalish(r.URL.Query().Get("checksearch"))
		life := equalish(r.URL.Query().Get("life"))

		qs := map[string]string{}
		for k, vs := range r.URL.Query() {
			if len(vs) > 0 {
				qs[k] = vs[0]
			}
		}
		hdr := map[string]string{}
		for k, vs := range r.Header {
			if len(vs) > 0 {
				hdr[strings.ToLower(k)] = vs[0]
			}
		}

		inv := Invocation{
			Query:       qs,
			Headers:     hdr,
			Host:        hostFromRequest(r),
			RequestIP:   clientIP(r),
			Path:        strings.TrimPrefix(r.URL.Path, "/lite/"),
			LifeMode:    life,
			Checksearch: checksearch,
			UserAgent:   r.Header.Get("User-Agent"),
		}

		resp, err := m.Invoke(r.Context(), id, inv)
		if err != nil {
			if m.Logger != nil {
				m.Logger.Warn().Str("module", id).Err(err).Msg("wasmmodules: invoke error")
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}
		ct := resp.ContentType
		if ct == "" {
			ct = "application/json; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(resp.Body)
	})
}

// ClientAssetHandler serves a small REST surface for the Lampa-side runtime:
//
//	GET /wasm/index.json            → catalogue of every client/both module
//	GET /wasm/{id}/manifest.json    → that module's manifest
//	GET /wasm/{id}/client.wasm      → the .wasm artefact
//
// Lampa fetches index.json on boot, then `WebAssembly.instantiateStreaming`s
// each client.wasm in turn.
func (m *Manager) ClientAssetHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/wasm/")
		// Catalog lookup: /wasm/index.json
		if path == "index.json" {
			m.serveCatalog(w)
			return
		}
		parts := strings.SplitN(path, "/", 2)
		if len(parts) < 2 {
			http.NotFound(w, r)
			return
		}
		mod, ok := m.Get(parts[0])
		if !ok || !mod.Manifest.HasClient() {
			http.NotFound(w, r)
			return
		}
		switch parts[1] {
		case "manifest.json":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=60")
			data, err := json.Marshal(mod.Manifest)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(data)
		case "client.wasm":
			full := filepath.Join(mod.Dir, mod.Manifest.EntryClient)
			w.Header().Set("Content-Type", "application/wasm")
			w.Header().Set("Cache-Control", "public, max-age=300")
			http.ServeFile(w, r, full)
		default:
			http.NotFound(w, r)
		}
	})
}

type catalogEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	WASMURL     string   `json:"wasm_url"`
	ManifestURL string   `json:"manifest_url"`
}

func (m *Manager) serveCatalog(w http.ResponseWriter) {
	out := []catalogEntry{}
	for _, mod := range m.List() {
		if !mod.Manifest.HasClient() || !mod.Enabled {
			continue
		}
		out = append(out, catalogEntry{
			ID:          mod.Manifest.ID,
			Name:        mod.Manifest.Name,
			Version:     mod.Manifest.Version,
			Description: mod.Manifest.Description,
			Author:      mod.Manifest.Author,
			Icon:        mod.Manifest.Icon,
			Tags:        mod.Manifest.Tags,
			WASMURL:     "/wasm/" + mod.Manifest.ID + "/client.wasm",
			ManifestURL: "/wasm/" + mod.Manifest.ID + "/manifest.json",
		})
	}
	body, _ := json.Marshal(map[string]any{"plugins": out})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=30")
	_, _ = w.Write(body)
}

func equalish(v string) bool {
	return strings.EqualFold(v, "true") || v == "1"
}

func hostFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if xh := r.Header.Get("X-Forwarded-Host"); xh != "" {
		host = xh
	}
	return scheme + "://" + host
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if i := strings.Index(xf, ","); i > 0 {
			return strings.TrimSpace(xf[:i])
		}
		return strings.TrimSpace(xf)
	}
	if h := r.Header.Get("X-Real-IP"); h != "" {
		return strings.TrimSpace(h)
	}
	if h, _, ok := strings.Cut(r.RemoteAddr, ":"); ok {
		return h
	}
	return r.RemoteAddr
}
