package iptvhttp

import (
	"net"
	"net/http"
	"strings"

	"lampac-go/internal/tgauth"
)

// helpers.go — pure helpers copied verbatim from the former host. None of these
// touch server state, so they are duplicated (not injected) to keep the moved
// handlers' call sites unchanged.

// hostFromRequest reconstructs the public-facing scheme://host, honoring
// X-Forwarded-Proto / X-Forwarded-Host from a reverse proxy.
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

// calendarTgID resolves the caller's Telegram ID from a tgauth.Store via the
// lampac_token cookie, an explicit token query param, or the device UID. Named
// for its original calendar use but a pure tgauth resolver — no calendar state.
func calendarTgID(r *http.Request, store *tgauth.Store) int64 {
	if store == nil {
		return 0
	}
	if cookie, err := r.Cookie("lampac_token"); err == nil && cookie.Value != "" {
		if at, ok := store.Lookup(cookie.Value); ok && at.TelegramID != 0 {
			return at.TelegramID
		}
	}
	token := r.URL.Query().Get("token")
	if token != "" {
		if at, ok := store.Lookup(token); ok && at.TelegramID != 0 {
			return at.TelegramID
		}
	}
	uid := r.URL.Query().Get("uid")
	if uid != "" {
		if found := store.FindTokenByDeviceUID(uid); found != "" {
			if at, ok := store.Lookup(found); ok && at.TelegramID != 0 {
				return at.TelegramID
			}
		}
	}
	return 0
}

// loopbackHostPort normalizes a listen address to a dialable 127.0.0.1:port,
// mapping wildcard hosts (0.0.0.0, ::) to loopback.
func loopbackHostPort(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
