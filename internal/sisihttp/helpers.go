package sisihttp

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// helpers.go — pure, self-contained utility copies from the former host (httpapi).
// No host state; identical logic so the moved handlers compile unchanged.

func submatch1(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func hostFromRequest(r *http.Request) string {
	scheme := "http"
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
		scheme = xf
	} else if r.TLS != nil {
		scheme = "https"
	}
	var host string
	if xfh := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xfh != "" {
		if i := strings.IndexByte(xfh, ','); i > 0 {
			xfh = strings.TrimSpace(xfh[:i])
		}
		host = xfh
	}
	if host == "" {
		host = r.Host
	}
	if host == "" {
		host = r.RemoteAddr
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}
	return scheme + "://" + host
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func toBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return parseBoolLike(t)
	default:
		return parseBoolLike(toString(v))
	}
}

func parseBoolParam(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseBoolLike(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return false
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return v == "1" || v == "yes" || v == "on"
}

func maxInt(a, b int) int {
	if a >= b {
		return a
	}
	return b
}

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func relToRuntime(rel string) string {
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_HOME")); root != "" {
		return filepath.Join(root, rel)
	}
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_REPO_ROOT")); root != "" {
		return filepath.Join(root, rel)
	}
	return rel
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func writeRawJSON(w http.ResponseWriter, payload []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}
