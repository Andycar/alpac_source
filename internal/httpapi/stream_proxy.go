package httpapi

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

// isStreamProxyDisabled returns true if the given plugin is listed in
// config.Online.NoStreamProxy — meaning its stream URLs should bypass /proxy/
// and be served directly to the client. Reads live config via liveConfig.
func isStreamProxyDisabled(plugin string) bool {
	if !serverReady() {
		return false
	}
	cfg := liveConfig(config.Config{})
	lp := strings.ToLower(strings.TrimSpace(plugin))
	for _, p := range cfg.Online.NoStreamProxy {
		if strings.ToLower(strings.TrimSpace(p)) == lp {
			return true
		}
	}
	return false
}

// filmixFMP4Re опознаёт fMP4-рипы filmix (HDR/Dolby Vision/HEVC) по имени файла или каталога.
//
// Такой плейлист несёт EXT-X-MAP, а CDN отдаёт его init-сегмент БЕЗ `?hash=` (во все остальные
// сегменты hash дописывает) — и этот один запрос 403-ит всё воспроизведение. Чинится это в
// rewriteM3U, но только если манифест вообще прошёл через нас.
//
// Поэтому: если filmix целиком выведен из проксирования (`no_stream_proxy`), манифест уходит
// зрителю сырым и HDR гарантированно не играет. Здесь мы такой случай перехватываем.
// ★Правильная настройка — не `no_stream_proxy`, а `stream_proxy_manifest_only = ['filmix']`:
// тогда через сервер идёт только манифест (килобайты), а медиа зритель тянет прямо с CDN.
// IP-привязки у сегментов нет — проверено с постороннего адреса (206 на сегмент и на init с hash).
//
// Граница `([^a-z]|$)` обязательна: без неё `hdr` совпадал внутри озвучки «HDrezka» и гнал через
// сервер обычный SDR-рип.
var filmixFMP4Re = regexp.MustCompile(`(?i)(hdr(10)?[+p]?|hevc|dolby|dovi|dvp[0-9])([^a-z]|$)`)

// forceStreamProxy отменяет no_stream_proxy там, где прямая ссылка заведомо не проиграется.
func forceStreamProxy(plugin, rawURL string) bool {
	if strings.ToLower(strings.TrimSpace(plugin)) != "filmix" {
		return false
	}
	return filmixFMP4Re.MatchString(rawURL)
}

// streamProxyURL wraps a raw CDN URL through /proxy/ using proxylink
// encryption so the server can proxy the request with correct headers.
// If proxylink is unavailable or stream proxy is disabled for this plugin,
// returns the original URL as-is.
func streamProxyURL(req *http.Request, rawURL, plugin string, links *proxylink.Manager) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || links == nil {
		return rawURL
	}
	if isStreamProxyDisabled(plugin) && !forceStreamProxy(plugin, rawURL) {
		return rawURL
	}
	host := streamHostFromRequest(req)
	reqIP := clientIP(req)
	encrypted := links.EncryptURI(rawURL, reqIP, plugin, false, false, false)
	if encrypted == "" {
		return rawURL
	}
	return host + "/proxy/" + encrypted
}

// streamProxyURLWithHeaders wraps a raw CDN URL through /proxy/ with custom
// upstream headers embedded in the encrypted payload. Used when CDN requires
// specific headers (e.g. User-Agent for DASH streams).
func streamProxyURLWithHeaders(req *http.Request, rawURL, plugin string, links *proxylink.Manager, headers map[string]string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || links == nil {
		return rawURL
	}
	if isStreamProxyDisabled(plugin) && !forceStreamProxy(plugin, rawURL) {
		return rawURL
	}
	host := streamHostFromRequest(req)
	reqIP := clientIP(req)
	encrypted := links.EncryptURIWithHeaders(rawURL, reqIP, plugin, headers)
	if encrypted == "" {
		return rawURL
	}
	return host + "/proxy/" + encrypted
}

// streamProxyDirectURL wraps a raw URL through /proxy/ in direct mode
// (/proxy/<urlencoded-url>) and optionally passes plugin metadata via query.
// This mode is keyless and survives multi-instance deployments.
func streamProxyDirectURL(req *http.Request, rawURL, plugin string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return rawURL
	}
	if isStreamProxyDisabled(plugin) && !forceStreamProxy(plugin, rawURL) {
		return rawURL
	}
	host := streamHostFromRequest(req)
	proxyURL := host + "/proxy/" + url.QueryEscape(rawURL)
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin != "" {
		proxyURL += "?pl=" + url.QueryEscape(plugin)
	}
	return proxyURL
}

// internalProxyStreamHandler creates proxy URLs for custom balancer subprocesses.
// Only accessible from localhost. Custom balancers POST {"url":"...","plugin":"...","headers":{...}}
// and receive {"proxy_url":"http://host/proxy/encrypted..."} back.
func internalProxyStreamHandler(links *proxylink.Manager) http.HandlerFunc {
	type request struct {
		URL     string            `json:"url"`
		Plugin  string            `json:"plugin"`
		Headers map[string]string `json:"headers,omitempty"`
	}
	type response struct {
		ProxyURL string `json:"proxy_url"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		// Only allow from localhost (custom balancer subprocesses).
		ip := r.RemoteAddr
		if idx := strings.LastIndex(ip, ":"); idx >= 0 {
			ip = ip[:idx]
		}
		ip = strings.Trim(ip, "[]")
		if ip != "127.0.0.1" && ip != "::1" && ip != "localhost" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		if req.Plugin == "" {
			req.Plugin = "custbal"
		}

		// Use X-Forwarded-For from the original client request if present.
		reqIP := r.Header.Get("X-Forwarded-For")
		if reqIP == "" {
			reqIP = "0.0.0.0" // no IP verification for internal requests
		}

		// Build host URL: prefer X-Forwarded-Host (set by custom balancer
		// from the original client request) over r.Host (which is localhost).
		host := hostFromRequest(r)
		if fh := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); fh != "" {
			scheme := "http"
			if fp := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); fp != "" {
				scheme = fp
			}
			host = scheme + "://" + fh
		}
		// Apply stream-host alias so /proxy/ URLs bypass the fronting CDN.
		if serverReady() {
			host = liveConfig(config.Config{}).Host.StreamHostFor(host)
		}

		var proxyURL string
		if links == nil {
			proxyURL = req.URL
		} else if len(req.Headers) > 0 {
			encrypted := links.EncryptURIWithHeaders(req.URL, reqIP, req.Plugin, req.Headers)
			if encrypted == "" {
				proxyURL = req.URL
			} else {
				proxyURL = host + "/proxy/" + encrypted
			}
		} else {
			encrypted := links.EncryptURI(req.URL, reqIP, req.Plugin, false, false, false)
			if encrypted == "" {
				proxyURL = req.URL
			} else {
				proxyURL = host + "/proxy/" + encrypted
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response{ProxyURL: proxyURL})
	}
}
