package httpapi

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

type cubProxy struct {
	client        *http.Client
	scheme        string
	customPlugins *CustomPluginRegistry

	// Passive CUB-anchor learning: proxied CUB calls from a device that also
	// carries OUR auth cookie reveal the cub-token↔account pair; the link is
	// recorded asynchronously so a wiped device can later recover via its CUB
	// login alone (see cub_auth.go). Both nil when TG auth is off.
	tgStore *tgauth.Store
	cubVal  *cubAuthValidator
}

func cubProxyHandler(cfg config.Config, customPlugins *CustomPluginRegistry, tgStore *tgauth.Store, cubVal *cubAuthValidator) http.Handler {
	scheme := strings.TrimSpace(cfg.Cub.Scheme)
	if scheme == "" {
		scheme = "https"
	}

	// CUB-proxy ferries user auth tokens and admin-managed settings. Default
	// to InsecureSkipVerify=true for back-compat with mirror CUB instances on
	// self-signed certs, but admins running against trusted CUBs SHOULD set
	// [security] strict_admin_tls=true to detect MITM against user credentials.
	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	if cfg.Security.StrictAdminTLS {
		tlsCfg = &tls.Config{InsecureSkipVerify: false}
	}
	var transport http.RoundTripper
	if t := httpclient.TransportForBalancer("cub"); t != nil {
		t.TLSClientConfig = tlsCfg
		transport = t
	} else {
		transport = &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: tlsCfg,
		}
	}

	return &cubProxy{
		scheme:        strings.ToLower(scheme),
		customPlugins: customPlugins,
		tgStore:       tgStore,
		cubVal:        cubVal,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}
}

func (h *cubProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	domain, uri, ok := parseCubPath(r.URL.Path)
	if !ok {
		h.fallbackOrNotFound(w, r)
		return
	}
	if !isValidCubHost(domain) {
		http.Error(w, "invalid cub host", http.StatusBadRequest)
		return
	}

	h.maybeLearnCubLink(r)

	lowerURI := strings.ToLower(uri)
	switch {
	case lowerURI == "api/checker" || strings.HasPrefix(lowerURI, "api/checker/"):
		h.handleChecker(w, r)
		return
	case strings.HasPrefix(lowerURI, "api/plugins/blacklist"):
		writeJSON(w, http.StatusOK, []any{})
		return
	case strings.HasPrefix(lowerURI, "api/metric/"), strings.HasPrefix(lowerURI, "api/ad/stat"),
		strings.HasPrefix(lowerURI, "api/v1.0/events/secuses"):
		writeJSON(w, http.StatusOK, map[string]any{"secuses": true, "translations": map[string]any{}})
		return
	case strings.HasPrefix(lowerURI, "api/ad/vast"):
		now := time.Now()
		writeJSON(w, http.StatusOK, map[string]any{
			"secuses":       true,
			"ad":            []any{},
			"day_of_month":  now.Day(),
			"days_in_month": 31,
			"month":         int(now.Month()),
		})
		return
	case strings.HasPrefix(lowerURI, "api/extensions/list"):
		h.handleExtensionsList(w, r, domain, uri)
		return
	case strings.HasPrefix(lowerURI, "api/shots/list/"):
		if !hasCubAuth(r) {
			// Legacy clients often call these endpoints unauthenticated.
			// Upstream returns 500 "Вход не выполнен". Cub plugin expects
			// response.results to be an array; return an empty compatible payload.
			writeJSON(w, http.StatusOK, map[string]any{
				"results": []any{},
			})
			return
		}
	}

	target := h.scheme + "://" + domain + "/" + uri
	if raw := strings.TrimSpace(r.URL.RawQuery); raw != "" {
		target += "?" + raw
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad proxy request", http.StatusBadRequest)
		return
	}

	copyProxyHeaders(req.Header, r.Header)

	resp, err := h.client.Do(req)
	if err != nil {
		log.Debug().Err(err).Str("target", target).Msg("cub proxy upstream failed")
		writeCubEmptyFallback(w, lowerURI)
		return
	}
	defer resp.Body.Close()

	// When upstream returns server error (5xx), return valid JSON fallback
	// so Lampa doesn't crash on json.results.forEach with undefined.
	if resp.StatusCode >= 500 {
		log.Debug().Int("status", resp.StatusCode).Str("target", target).Msg("cub proxy upstream error")
		writeCubEmptyFallback(w, lowerURI)
		return
	}

	copyProxyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleExtensionsList proxies CUB extensions/list and injects public custom plugins.
func (h *cubProxy) handleExtensionsList(w http.ResponseWriter, r *http.Request, domain, uri string) {
	host := hostFromRequest(r)

	// Build community section from public custom plugins.
	var communitySection map[string]any
	if h.customPlugins != nil {
		pub := h.customPlugins.PublicPlugins()
		if len(pub) > 0 {
			items := make([]any, 0, len(pub))
			for _, p := range pub {
				img := resolvePluginImageURL(p.Image, host)
				items = append(items, map[string]any{
					"name":            p.Name,
					"author":          p.Author,
					"descr":           p.Descr,
					"image":           img,
					"link":            host + "/" + p.Name + ".js",
					"available_lampa": 1,
				})
			}
			communitySection = map[string]any{
				"title":   "Рекомендует комьюнити",
				"hpu":     "recomend",
				"results": items,
			}
		}
	}

	// Proxy upstream request.
	target := h.scheme + "://" + domain + "/" + uri
	if raw := strings.TrimSpace(r.URL.RawQuery); raw != "" {
		target += "?" + raw
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		// No upstream — return just our plugins if any.
		if communitySection != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"secuses": true,
				"results": []any{communitySection},
			})
			return
		}
		http.Error(w, "bad proxy request", http.StatusBadRequest)
		return
	}

	copyProxyHeaders(req.Header, r.Header)

	resp, err := h.client.Do(req)
	if err != nil {
		log.Debug().Err(err).Str("target", target).Msg("cub proxy upstream failed (extensions)")
		// Upstream failed — return just our plugins.
		if communitySection != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"secuses": true,
				"results": []any{communitySection},
			})
			return
		}
		http.Error(w, "cub upstream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// If no custom plugins to inject, pass through as-is.
	if communitySection == nil {
		copyProxyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}

	// Read and parse upstream response to inject our section.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		// Can't read upstream — return just our plugins.
		writeJSON(w, http.StatusOK, map[string]any{
			"secuses": true,
			"results": []any{communitySection},
		})
		return
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		// Bad JSON from upstream — return just our plugins.
		writeJSON(w, http.StatusOK, map[string]any{
			"secuses": true,
			"results": []any{communitySection},
		})
		return
	}

	// Ensure secuses is set so Lampa accepts the response.
	data["secuses"] = true
	if _, ok := data["translations"]; !ok {
		data["translations"] = map[string]any{}
	}

	// Inject community section at the beginning of results.
	if results, ok := data["results"].([]any); ok {
		data["results"] = append([]any{communitySection}, results...)
	} else {
		data["results"] = []any{communitySection}
	}

	writeJSON(w, http.StatusOK, data)
}

func (h *cubProxy) handleChecker(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err == nil {
			if vals, perr := url.ParseQuery(string(body)); perr == nil {
				if data := strings.TrimSpace(vals.Get("data")); data != "" {
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(data))
					return
				}
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *cubProxy) fallbackOrNotFound(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

// maybeLearnCubLink records the cub-token↔account association when a proxied
// CUB call carries both the CUB token and our own auth cookie. Cheap on the
// hot path: LearnAsync no-ops via cache once the link exists.
func (h *cubProxy) maybeLearnCubLink(r *http.Request) {
	if h.tgStore == nil || h.cubVal == nil {
		return
	}
	cubToken := strings.TrimSpace(r.Header.Get("token"))
	if cubToken == "" {
		cubToken = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if cubToken == "" {
		return
	}
	for _, cookieName := range []string{"_lampac_auth", "lampac_token"} {
		if c, err := r.Cookie(cookieName); err == nil {
			if ourToken := strings.TrimSpace(c.Value); ourToken != "" {
				if _, ok := h.tgStore.Lookup(ourToken); ok {
					h.cubVal.LearnAsync(h.tgStore, ourToken, cubToken, clientIP(r))
				}
				return
			}
		}
	}
}

// writeCubEmptyFallback returns a valid CUB-style JSON response so Lampa's
// client code (json.results.forEach) doesn't crash when upstream is down.
func writeCubEmptyFallback(w http.ResponseWriter, lowerURI string) {
	switch {
	case strings.HasPrefix(lowerURI, "api/shots/list/"):
		writeJSON(w, http.StatusOK, map[string]any{
			"secuses": true,
			"results": []any{},
		})
	case strings.HasPrefix(lowerURI, "api/extensions/list"):
		writeJSON(w, http.StatusOK, map[string]any{
			"secuses": true,
			"results": []any{},
		})
	default:
		// Generic fallback — most CUB endpoints expect {"results": []}.
		writeJSON(w, http.StatusOK, map[string]any{
			"secuses": true,
			"results": []any{},
		})
	}
}

func parseCubPath(path string) (domain string, uri string, ok bool) {
	tail := path
	if after, ok0 := strings.CutPrefix(tail, "/cubproxy/cub/"); ok0 {
		tail = after
	} else if after, ok0 := strings.CutPrefix(tail, "/cub/"); ok0 {
		tail = after
	} else {
		return "", "", false
	}

	tail = strings.TrimLeft(strings.TrimSpace(tail), "/")
	if tail == "" {
		return "", "", false
	}

	parts := strings.SplitN(tail, "/", 2)
	domain = strings.TrimSpace(parts[0])
	if domain == "" {
		return "", "", false
	}

	if len(parts) == 1 {
		return domain, "", true
	}
	uri = strings.TrimLeft(parts[1], "/")
	return domain, uri, true
}

func isValidCubHost(host string) bool {
	if strings.Contains(host, "://") {
		return false
	}
	if strings.ContainsAny(host, " /\\\t\r\n") {
		return false
	}

	name := host
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		p := host[idx+1:]
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
		name = host[:idx]
	}

	if name == "" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, ch := range name {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

func copyProxyHeaders(dst, src http.Header) {
	for k := range dst {
		dst.Del(k)
	}
	for k, vv := range src {
		kl := strings.ToLower(strings.TrimSpace(k))
		if kl == "host" || kl == "content-length" {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func hasCubAuth(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("token")) != "" || strings.TrimSpace(r.Header.Get("profile")) != "" {
		return true
	}

	q := r.URL.Query()
	if strings.TrimSpace(q.Get("token")) != "" || strings.TrimSpace(q.Get("profile")) != "" {
		return true
	}

	return false
}
