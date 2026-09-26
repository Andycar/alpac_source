package iptvhttp

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/auth"
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
// logIPTVDeny — почему отказано и ЧТО принёс запрос.
//
// Отказы IPTV выглядят снаружи одинаково («нет доступа»), а причин минимум
// три: не опознали человека, канал реестра без авторизации, не прошла
// аттестация приложения. Без записи причины разбор превращается в гадание —
// это тот же приём, что уже выручил на /capi.
//
// ЗНАЧЕНИЯ токенов не пишем никогда, только наличие источника: строка отказа
// должна помогать искать причину, а не становиться новой утечкой (в логах
// nginx живых ключей и так было 834 за сутки).
var iptvDenyThrottle sync.Map // ip → время последней записи, unixnano

func logIPTVDeny(r *http.Request, what string) {
	ip := clientIP(r)
	now := time.Now().UnixNano()
	if v, ok := iptvDenyThrottle.Load(ip); ok {
		if last, _ := v.(int64); now-last < int64(20*time.Second) {
			return // один и тот же клиент долбится десятками запросов в минуту
		}
	}
	iptvDenyThrottle.Store(ip, now)
	have := func(name string) bool {
		c, err := r.Cookie(name)
		return err == nil && strings.TrimSpace(c.Value) != ""
	}
	log.Warn().
		Str("почему", what).
		Str("path", r.URL.Path).
		Str("ip", ip).
		Str("uid", r.URL.Query().Get("uid")).
		Bool("token_в_адресе", r.URL.Query().Get("token") != "").
		Bool("кука_lampac_token", have("lampac_token")).
		Bool("кука_alpac_token", have("alpac_token")).
		Bool("кука_httponly", have("_lampac_auth")).
		Bool("заголовок_токена", r.Header.Get("X-Lampac-Token") != "" || r.Header.Get("X-Alpac-Token") != "").
		Str("ua", r.UserAgent()).
		Msg("iptv: отказано")
}

func calendarTgID(r *http.Request, store *tgauth.Store) int64 {
	if store == nil {
		return 0
	}
	// ★Общий разбор источников токена, а не одна кука `lampac_token`. IPTV в
	// обходном списке гейта и опознаёт человека сам; на телевизоре при
	// перезапуске приложения вычищается именно JS-видимая банка кук, а
	// HttpOnly `_lampac_auth` переживает — и человек, у которого /lite/*
	// работал через гейт, для IPTV оставался анонимом (жалоба 20.09.2026:
	// «в статусе авторизация есть, а торрентов и IPTV нет»).
	if tok := auth.ExtractToken(r); tok != "" {
		if at, ok := store.Lookup(tok); ok && at.TelegramID != 0 {
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
