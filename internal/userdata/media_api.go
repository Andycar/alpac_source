package userdata

import (
	stdjson "encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"lampac-go/internal/proxylink"

	"github.com/go-chi/chi/v5"
)

type mediaRequestBase struct {
	AuthToken string `json:"auth_token"`
	Type      string `json:"type"`
	Width     *int   `json:"width"`
	Height    *int   `json:"height"`
}

type mediaRequest struct {
	mediaRequestBase
	URLs []string `json:"urls"`
}

type mediaSettings struct {
	Tokens            map[string]struct{}
	ServerProxyEnable bool
	VerifyIP          bool
}

func MediaRsizeHandler(links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(chi.URLParam(r, "token"))
		width, _ := strconv.Atoi(strings.TrimSpace(chi.URLParam(r, "width")))
		height, _ := strconv.Atoi(strings.TrimSpace(chi.URLParam(r, "height")))
		rawURL := strings.TrimSpace(chi.URLParam(r, "*"))
		if rawURL == "" {
			writeMediaError(w, http.StatusBadRequest, "invalid url")
			return
		}
		if r.URL.RawQuery != "" {
			rawURL += "?" + r.URL.RawQuery
		}

		req := mediaRequestBase{
			AuthToken: token,
			Type:      "img",
			Width:     &width,
			Height:    &height,
		}
		redirectMediaURL(w, r, req, rawURL, links)
	}
}

func MediaTypeHandler(links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(chi.URLParam(r, "token"))
		typ := strings.TrimSpace(strings.ToLower(chi.URLParam(r, "type")))
		rawURL := strings.TrimSpace(chi.URLParam(r, "*"))
		if rawURL == "" {
			writeMediaError(w, http.StatusBadRequest, "invalid url")
			return
		}
		if r.URL.RawQuery != "" {
			rawURL += "?" + r.URL.RawQuery
		}

		req := mediaRequestBase{
			AuthToken: token,
			Type:      typ,
		}
		redirectMediaURL(w, r, req, rawURL, links)
	}
}

func MediaGetHandler(links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		rawURL := strings.TrimSpace(q.Get("url"))
		if rawURL == "" {
			writeMediaError(w, http.StatusBadRequest, "invalid url")
			return
		}

		req := mediaRequestBase{
			AuthToken: strings.TrimSpace(q.Get("auth_token")),
			Type:      strings.TrimSpace(strings.ToLower(q.Get("type"))),
		}
		if wv := strings.TrimSpace(q.Get("width")); wv != "" {
			if n, err := strconv.Atoi(wv); err == nil {
				req.Width = &n
			}
		}
		if hv := strings.TrimSpace(q.Get("height")); hv != "" {
			if n, err := strconv.Atoi(hv); err == nil {
				req.Height = &n
			}
		}

		redirectMediaURL(w, r, req, rawURL, links)
	}
}

func MediaPostHandler(links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req mediaRequest
		if err := stdjson.NewDecoder(r.Body).Decode(&req); err != nil {
			writeMediaError(w, http.StatusBadRequest, "invalid request")
			return
		}

		settings := loadMediaSettings()
		if !isMediaTokenAllowed(req.AuthToken, settings) {
			writeMediaError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if len(req.URLs) == 0 {
			writeMediaError(w, http.StatusBadRequest, "invalid urls")
			return
		}

		out := make([]string, 0, len(req.URLs))
		for _, u := range req.URLs {
			u = strings.TrimSpace(u)
			if u == "" {
				continue
			}
			out = append(out, buildMediaURL(r, req.mediaRequestBase, u, settings, links))
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"urls":    out,
		})
	}
}

func redirectMediaURL(w http.ResponseWriter, r *http.Request, req mediaRequestBase, rawURL string, links *proxylink.Manager) {
	settings := loadMediaSettings()
	if !isMediaTokenAllowed(req.AuthToken, settings) {
		writeMediaError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	location := buildMediaURL(r, req, rawURL, settings, links)
	http.Redirect(w, r, location, http.StatusFound)
}

func buildMediaURL(r *http.Request, req mediaRequestBase, rawURL string, settings mediaSettings, links *proxylink.Manager) string {
	rawURL = strings.TrimSpace(rawURL)
	if !settings.ServerProxyEnable || links == nil {
		return rawURL
	}

	reqIP := deps.ClientIP(r)
	host := hostFromRequest(r)
	typ := strings.ToLower(strings.TrimSpace(req.Type))
	if typ == "" {
		typ = "video"
	}

	if typ == "img" {
		width := 0
		height := 0
		if req.Width != nil && *req.Width > 0 {
			width = *req.Width
		}
		if req.Height != nil && *req.Height > 0 {
			height = *req.Height
		}

		encrypted := links.EncryptURI(rawURL, reqIP, "posterapi", false, false, true)
		if width > 0 || height > 0 {
			return host + "/proxyimg:" + strconv.Itoa(width) + ":" + strconv.Itoa(height) + "/" + encrypted
		}
		return host + "/proxyimg/" + encrypted
	}

	encrypted := links.EncryptURI(rawURL, reqIP, "media", settings.VerifyIP, false, false)
	return host + "/proxy/" + encrypted
}

func loadMediaSettings() mediaSettings {
	settings := mediaSettings{
		Tokens:            map[string]struct{}{},
		ServerProxyEnable: true,
		VerifyIP:          true,
	}

	data, ok := deps.ReadFileAny("init.conf")
	if !ok {
		return settings
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return settings
	}

	if media, ok := root["media"].(map[string]any); ok {
		if tokens, ok := media["tokens"].([]any); ok {
			for _, token := range tokens {
				val := strings.TrimSpace(toString(token))
				if val != "" {
					settings.Tokens[val] = struct{}{}
				}
			}
		}
	}

	if sp, ok := root["serverproxy"].(map[string]any); ok {
		if enabled, ok := sp["enable"].(bool); ok {
			settings.ServerProxyEnable = enabled
		}
		if verify, ok := sp["verifyip"].(bool); ok {
			settings.VerifyIP = verify
		}
	}

	return settings
}

func isMediaTokenAllowed(token string, settings mediaSettings) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	if len(settings.Tokens) == 0 {
		return false
	}
	_, ok := settings.Tokens[token]
	return ok
}

func writeMediaError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"error":   msg,
	})
}

func normalizeMediaURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		return raw
	}
	return raw
}
