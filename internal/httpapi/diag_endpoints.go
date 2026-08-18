package httpapi

import (
	stdjson "encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"lampac-go/internal/config"
)

func headersEchoHandler(w http.ResponseWriter, r *http.Request) {
	headers := make(map[string][]string, len(r.Header))
	for k, v := range r.Header {
		cp := make([]string, len(v))
		copy(cp, v)
		headers[k] = cp
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"headers": headers,
	})
}

func geoHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ip":      clientIP(r),
		"country": countryFromRequest(r),
	})
}

func myIPHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(clientIP(r)))
}

func reqInfoHandler(w http.ResponseWriter, r *http.Request) {
	uid := strings.TrimSpace(r.URL.Query().Get("uid"))

	writeJSON(w, http.StatusOK, map[string]any{
		"method":   r.Method,
		"path":     r.URL.Path,
		"query":    r.URL.RawQuery,
		"host":     r.Host,
		"scheme":   requestScheme(r),
		"ip":       clientIP(r),
		"country":  countryFromRequest(r),
		"agent":    r.UserAgent(),
		"referer":  r.Referer(),
		"remote":   r.RemoteAddr,
		"proto":    r.Proto,
		"user_uid": uid,
		"x-forward": map[string]string{
			"for":   strings.TrimSpace(r.Header.Get("X-Forwarded-For")),
			"proto": strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")),
			"host":  strings.TrimSpace(r.Header.Get("X-Forwarded-Host")),
		},
	})
}

func chromiumPingHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
	})
}

func chromiumIframeHandler(w http.ResponseWriter, r *http.Request) {
	src := strings.TrimSpace(r.URL.Query().Get("src"))
	src = sanitizeIframeSrc(src)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// CSP that disallows inline JS and pins frame-src to http(s) only — even
	// if a future bug lets a quote slip past sanitizeIframeSrc, the browser
	// won't execute injected JS.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-src https: http:; style-src 'unsafe-inline'")
	w.WriteHeader(http.StatusOK)
	// Use html.EscapeString as a belt-and-suspenders against any url.Parse
	// edge case (e.g. javascript: that somehow lives inside a parsed URL).
	_, _ = w.Write(fmt.Appendf(nil, `<!doctype html>
<html lang="ru">
<head>
  <meta charset="UTF-8">
  <meta http-equiv="X-UA-Compatible" content="IE=edge">
  <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no">
  <title>chromium iframe</title>
</head>
<body>
  <iframe width="560" height="400" src="%s" frameborder="0" allow="*" allowfullscreen></iframe>
</body>
</html>`, html.EscapeString(src)))
}

// sanitizeIframeSrc accepts an arbitrary user-supplied URL and returns it
// only if it parses cleanly with an http/https scheme. Otherwise returns
// "about:blank" — the browser shows an empty frame, which is the same
// visible behavior as if the user typed nothing.
//
// Defends against:
//   - javascript:/data:/file: schemes (XSS via iframe.src)
//   - Quote/angle-bracket injection that would break out of the attribute
//   - Empty strings (would crash some browsers in iframe context)
func sanitizeIframeSrc(raw string) string {
	if raw == "" {
		return "about:blank"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "about:blank"
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "about:blank"
	}
	if u.Host == "" {
		return "about:blank"
	}
	return u.String()
}

func syncAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Always read live config so hot-reloaded settings are picked up.
		if !serverReady() {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("error"))
			return
		}
		cfg := liveConfig(config.Config{})
		sync := cfg.Sync

		// Only master mode on local/authenticated requests.
		if !isLocalRequest(r, sync.APIPasswd) || !sync.Enable || sync.Type != "master" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("error"))
			return
		}

		// Determine which config file to serve.
		if sync.InitConf == "current" {
			// Serve current running config as JSON via TOML→JSON bridge.
			data, ok := readFileAny("current.conf")
			if !ok {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("error"))
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}

		// Serve from sync.conf or IP-specific override.
		confFile := "sync.conf"
		ip := clientIP(r)
		if sync.OverrideConf != nil {
			if override, ok := sync.OverrideConf[ip]; ok && override != "" {
				confFile = override
			}
		}

		confPath := relToRuntime(confFile)
		data, err := os.ReadFile(confPath)
		if err != nil {
			// File not found — serve empty init + accsdb users.
			data = []byte("{}")
		}

		// Inject accsdb.users from current config and replace {server_ip}.
		result := injectAccsdbUsers(data, cfg)
		result = strings.ReplaceAll(result, "{server_ip}", ip)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(result))
	}
}

// isLocalRequest checks if the request originates from localhost or has a valid
// localrequest header with the sync password (used by slave instances).
func isLocalRequest(r *http.Request, apiPasswd string) bool {
	ip := clientIP(r)
	if ip == "127.0.0.1" || ip == "::1" || ip == "localhost" {
		return true
	}
	// Slave instances authenticate via "localrequest" header containing api_passwd.
	if pw := strings.TrimSpace(r.Header.Get("localrequest")); pw != "" && apiPasswd != "" && pw == apiPasswd {
		return true
	}
	return false
}

// injectAccsdbUsers merges the accsdb.users from the running config into the
// config JSON being served to a slave. This ensures slaves receive the latest
// user list even when serving from a static sync.conf file.
func injectAccsdbUsers(confData []byte, cfg config.Config) string {
	var root map[string]any
	if err := stdjson.Unmarshal(confData, &root); err != nil {
		root = map[string]any{}
	}

	// Read current accsdb.users from init.conf.
	users := loadAccsdbUsers(cfg)
	if len(users) > 0 {
		accsdb, _ := root["accsdb"].(map[string]any)
		if accsdb == nil {
			accsdb = map[string]any{}
		}
		accsdb["users"] = users
		root["accsdb"] = accsdb
	}

	out, err := stdjson.Marshal(root)
	if err != nil {
		return string(confData)
	}
	return string(out)
}

// loadAccsdbUsers reads the user list for sync distribution.
// Priority: users.json (current format) → init.conf/init.json (legacy).
func loadAccsdbUsers(cfg config.Config) []any {
	// Current format: users.json is a JSON array of user objects.
	for _, dir := range []string{cfg.Compat.RepoRoot, filepath.Join(cfg.Compat.RepoRoot, "config"), "."} {
		path := filepath.Join(dir, "users.json")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var users []any
		if err := stdjson.Unmarshal(data, &users); err == nil && len(users) > 0 {
			return users
		}
	}

	// Legacy: accsdb.users inside init.conf / init.json.
	for _, name := range []string{"init.conf", "init.json"} {
		for _, dir := range []string{cfg.Compat.RepoRoot, filepath.Join(cfg.Compat.RepoRoot, "config"), "."} {
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var root map[string]any
			if err := stdjson.Unmarshal(data, &root); err != nil {
				continue
			}
			accsdb, _ := root["accsdb"].(map[string]any)
			if accsdb == nil {
				continue
			}
			users, _ := accsdb["users"].([]any)
			if len(users) > 0 {
				return users
			}
		}
	}
	return nil
}

// clientIP returns the effective client IP. X-Forwarded-For and X-Real-IP
// are honoured ONLY when r.RemoteAddr falls inside [security] trusted_proxies.
// Without that gate, any internet client could spoof their IP by sending
// "X-Forwarded-For: 1.2.3.4" — that breaks per-IP rate limits, geoIP
// matching, and audit logs.
func clientIP(r *http.Request) string {
	directIP := directRemoteIP(r)

	if isRemoteFromTrustedProxy(directIP) {
		if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
			parts := strings.Split(xff, ",")
			if len(parts) > 0 {
				ip := strings.TrimSpace(parts[0])
				if ip != "" {
					return ip
				}
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}

	if directIP != "" {
		return directIP
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// directRemoteIP extracts the IP from r.RemoteAddr (no proxy headers).
func directRemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func requestScheme(r *http.Request) string {
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
		return xf
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
