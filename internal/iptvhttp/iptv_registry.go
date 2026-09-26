package iptvhttp

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/iptv"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// iptv_registry.go — ручки СВОЕГО реестра каналов («Мои каналы»).
//
// После кражи экспортного плейлиста обе ручки ЗАКРЫТЫ (2026-08-31):
//
//	GET /api/iptv/stream/{id}?token=…&sig=…[&exp=…]
//	    — стабильный адрес потока. Требует валидный tgauth-токен И HMAC-подпись
//	      ссылки (см. iptv_stream_auth.go). 302 ВСЕГДА на /proxy — сырой URL
//	      апстрима не покидает сервер ни при каких условиях.
//	GET /api/iptv/export.m3u?token=…
//	    — реестр как M3U. Открывается только конфигом [iptv].registry_export
//	      (default false) и только владельцу валидного токена; ссылки внутри
//	      файла подписаны и привязаны к этому токену — отзыв токена убивает
//	      весь файл разом.

// ActiveStore returns the store built at startup (nil when IPTV is disabled).
// Used by the admin panel's registry management API.
func ActiveStore() *iptv.Store { return activeStore }

func iptvStreamHandler(store *iptv.Store, tgStore *tgauth.Store, streamKey []byte, proxyLinks *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		q := r.URL.Query()

		// Слой 1: живой tgauth-токен. Без него не раскрываем даже СУЩЕСТВОВАНИЕ
		// канала (сначала 401, потом 404 — не наоборот).
		token := streamAuthToken(r)
		if tgStore == nil || token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		approved, ok := tgStore.Lookup(token)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Слой 2: подпись ссылки. exp участвует в подписи, поэтому продлить
		// краденую ссылку правкой query нельзя.
		exp := q.Get("exp")
		if exp != "" {
			n, err := strconv.ParseInt(exp, 10, 64)
			if err != nil || time.Now().Unix() > n {
				http.Error(w, "link expired", http.StatusForbidden)
				return
			}
		}
		if !verifyStreamLink(streamKey, id, token, exp, q.Get("sig")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		ch, pl := store.GetRegistryChannel(id)
		if ch == nil {
			http.Error(w, "channel not found", http.StatusNotFound)
			return
		}
		regSID, sessErr := proxyLinks.OpenSession(clientIP(r), approved.TelegramID, deviceKey(r), deviceLimit(r))
		if sessErr == proxylink.ReasonTooManyStreams {
			http.Error(w, "слишком много одновременных просмотров", http.StatusTooManyRequests)
			return
		}
		// direct=false ЖЁСТКО: канал реестра всегда уходит через /proxy
		// (шифрованный, со сроком) — сырой CDN-адрес наружу не отдаём, иначе
		// один переход по редиректу восстанавливает украденный каталог.
		streamURL, resolveErr := iptvResolveStreamURL(ch.URL, ch, pl, proxyLinks, clientIP(r), hostFromRequest(r), false, regSID)
		if resolveErr != "" {
			status := http.StatusBadGateway
			if resolveErr == "stream proxy unavailable" {
				status = http.StatusServiceUnavailable
			}
			http.Error(w, resolveErr, status)
			return
		}
		http.Redirect(w, r, streamURL, http.StatusFound)
	}
}

func iptvExportM3UHandler(cfg config.Config, store *iptv.Store, tgStore *tgauth.Store, streamKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Экспорт — осознанный опт-ин. Выключен → ведём себя, будто ручки нет.
		if !cfg.IPTV.RegistryExport {
			http.NotFound(w, r)
			return
		}
		token := streamAuthToken(r)
		if tgStore == nil || token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if _, ok := tgStore.Lookup(token); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		host := hostFromRequest(r)
		body := store.ExportM3U(cfg.IPTV.EPGUrls, func(ch iptv.Channel) string {
			// Бессрочная (exp="") ссылка, но намертво привязанная к токену:
			// файл живёт, пока жив токен, и умирает целиком при его отзыве.
			return host + "/api/iptv/stream/" + ch.ID +
				"?token=" + url.QueryEscape(token) +
				"&sig=" + signStreamLink(streamKey, ch.ID, token, "")
		})
		w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="channels.m3u"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}
