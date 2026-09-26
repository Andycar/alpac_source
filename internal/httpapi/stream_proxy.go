package httpapi

import (
	"lampac-go/internal/proxyapi"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
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
// ★★`stream_proxy_manifest_only` тут НЕ спасает (проверено 2026-08-22): CDN привязывает сегменты к
// IP, который скачал МАНИФЕСТ, а в этом режиме манифест качает сервер — зритель получает 403 на
// сегменты. Такие рипы обязаны идти через прокси целиком.
//
// Граница `([^a-z]|$)` обязательна: без неё `hdr` совпадал внутри озвучки «HDrezka» и гнал через
// сервер обычный SDR-рип.
var filmixFMP4Re = regexp.MustCompile(`(?i)(hdr(10)?[+p]?|hevc|dolby|dovi|dvp[0-9])([^a-z]|$)`)

// filmixNoHashDirRe — разделы CDN, которые НЕ дописывают `?hash=` в сегменты своего манифеста.
//
// Замер 2026-08-22 на «Project Hail Mary», один тайтл, один момент:
//
//	UHD_1313     → 940 сегментов, из них с hash — 0   → плеер идёт без hash и ловит 403
//	hd_ukr       → 939 сегментов, все с hash          → играет
//	hdr_018_rus  → 939 сегментов, все с hash          → играет
//
// Сегмент без hash отдаёт 403, он же с дописанным hash — 206. Починить это можно только переписав
// манифест, а переписать — только скачав; CDN же привязывает сегменты к IP, скачавшему манифест
// (проверено: манифест взял сервер → у зрителя 403; манифест взял зритель → его сегменты 206).
// Отсюда: такие разделы обязаны идти через прокси ЦЕЛИКОМ — и манифест, и сегменты с одного IP.
//
// ★Список ручной: правила, по которому дефектный раздел отличался бы от исправного, нет — `HD_45`
// и `UHD_1313` выглядят одинаково, а ведут себя по-разному. Новые ловятся по логу rewriteM3U.
var filmixNoHashDirRe = regexp.MustCompile(`(?i)/UHD_1313/`)

// forceStreamProxy отменяет no_stream_proxy там, где прямая ссылка заведомо не проиграется.
func forceStreamProxy(plugin, rawURL string) bool {
	if strings.ToLower(strings.TrimSpace(plugin)) != "filmix" {
		return false
	}
	return filmixFMP4Re.MatchString(rawURL) || filmixNoHashDirRe.MatchString(rawURL)
}

// streamProxyURL wraps a raw CDN URL through /proxy/ using proxylink
// encryption so the server can proxy the request with correct headers.
// If proxylink is unavailable or stream proxy is disabled for this plugin,
// returns the original URL as-is.
func streamProxyURL(req *http.Request, rawURL, plugin string, links *proxylink.Manager) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return rawURL
	}
	if links == nil {
		noteRawStreamLeak(plugin, rawURL, "нет менеджера ссылок")
		return rawURL
	}
	if isStreamProxyDisabled(plugin) && !forceStreamProxy(plugin, rawURL) {
		noteConfiguredBypass(plugin, rawURL)
		return rawURL // так настроено (no_stream_proxy) — это не утечка
	}
	host := streamHostFromRequest(req)
	reqIP := clientIP(req)
	encrypted := links.EncryptURI(rawURL, reqIP, plugin, false, false, false)
	if encrypted == "" {
		noteRawStreamLeak(plugin, rawURL, "ссылка не зашифровалась")
		return rawURL
	}
	out := proxyapi.WithEdge(host+"/proxy/"+encrypted, proxyapi.EdgeHintFrom(req), proxyapi.EdgeSkipEffective(req))
	noteStreamEmit(plugin, out)
	return out
}

// streamProxyURLWithHeaders wraps a raw CDN URL through /proxy/ with custom
// upstream headers embedded in the encrypted payload. Used when CDN requires
// specific headers (e.g. User-Agent for DASH streams).
func streamProxyURLWithHeaders(req *http.Request, rawURL, plugin string, links *proxylink.Manager, headers map[string]string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return rawURL
	}
	if links == nil {
		noteRawStreamLeak(plugin, rawURL, "нет менеджера ссылок")
		return rawURL
	}
	if isStreamProxyDisabled(plugin) && !forceStreamProxy(plugin, rawURL) {
		noteConfiguredBypass(plugin, rawURL)
		return rawURL // так настроено (no_stream_proxy) — это не утечка
	}
	host := streamHostFromRequest(req)
	reqIP := clientIP(req)
	encrypted := links.EncryptURIWithHeaders(rawURL, reqIP, plugin, headers)
	if encrypted == "" {
		noteRawStreamLeak(plugin, rawURL, "ссылка не зашифровалась")
		return rawURL
	}
	out := proxyapi.WithEdge(host+"/proxy/"+encrypted, proxyapi.EdgeHintFrom(req), proxyapi.EdgeSkipEffective(req))
	noteStreamEmit(plugin, out)
	return out
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

// ── Сторож непроксированных ссылок ──────────────────────────────────────────
//
// Источник, который ДОЛЖЕН идти через /proxy, иногда отдаёт зрителю сырой адрес CDN — и делает
// это молча. Отличить «сервер отдал сырое» от «клиент играет из старого кэша» без этого можно
// только раскопками по логам nginx: 2026-09-16 на выяснение такого случая с filmix ушёл час.
//
// Настроенный обход (`no_stream_proxy`) сюда НЕ попадает — это осознанное решение, а не утечка.
//
// Чтобы журнал не заливало, на каждый источник пишем не чаще раза в минуту: утечка либо
// системная (видно по первой же строке), либо её нет.
var rawLeakSeen sync.Map // plugin -> time.Time последней записи

func noteRawStreamLeak(plugin, rawURL, reason string) {
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin == "" {
		return
	}
	now := time.Now()
	if prev, ok := rawLeakSeen.Load(plugin); ok {
		if t, ok := prev.(time.Time); ok && now.Sub(t) < time.Minute {
			return
		}
	}
	rawLeakSeen.Store(plugin, now)

	host := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		host = u.Host
	}
	log.Warn().Str("plugin", plugin).Str("upstream_host", host).Str("причина", reason).
		Msg("stream: зрителю ушла НЕпроксированная ссылка")
}

// noteStreamEmit — что мы РЕАЛЬНО отдали зрителю: наш хост или чужой.
//
// Парная к noteRawStreamLeak половина. Сторож молчит, когда всё хорошо, и по его молчанию нельзя
// отличить «мы отдали проксированное» от «до нас вообще не дошли». Эта строка снимает вопрос:
// 2026-09-16 на выяснение, откуда у Лампы взялась прямая ссылка на CDN, ушло несколько часов
// именно потому, что доказательства «сервер отдал правильное» не существовало.
//
// Пишем хост, а не ссылку: в ней токен. Не чаще раза в минуту на источник.
var streamEmitSeen sync.Map // plugin -> time.Time

func noteStreamEmit(plugin, outURL string) {
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin == "" {
		return
	}
	now := time.Now()
	if prev, ok := streamEmitSeen.Load(plugin); ok {
		if t, ok := prev.(time.Time); ok && now.Sub(t) < time.Minute {
			return
		}
	}
	streamEmitSeen.Store(plugin, now)

	host := outURL
	if u, err := url.Parse(outURL); err == nil && u.Host != "" {
		host = u.Host
	}
	log.Info().Str("plugin", plugin).Str("отдали_хост", host).
		Msg("stream: ссылка выдана зрителю")
}

// noteConfiguredBypass — источник отдаётся без прокси ПО НАСТРОЙКЕ (`no_stream_proxy`).
//
// Это не утечка, поэтому не Warn. Но и молчать нельзя: 2026-09-16 у всех нод в их собственных
// config.toml стояло `no_stream_proxy = ['filmix']`, тогда как на main его там не было. Ноды
// собирали filmix сырым, Лампа играла CDN напрямую и ловила 429, а сторож утечек молчал —
// формально всё было «так настроено». Разъезд конфигов между main и нодами искали несколько
// часов по tcpdump. Одна строка в час на источник делает такой разъезд видимым в журнале.
var bypassSeen sync.Map // plugin -> time.Time

func noteConfiguredBypass(plugin, rawURL string) {
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin == "" {
		return
	}
	now := time.Now()
	if prev, ok := bypassSeen.Load(plugin); ok {
		if t, ok := prev.(time.Time); ok && now.Sub(t) < time.Hour {
			return
		}
	}
	bypassSeen.Store(plugin, now)
	host := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		host = u.Host
	}
	log.Info().Str("plugin", plugin).Str("upstream_host", host).
		Msg("stream: источник отдаётся БЕЗ прокси по настройке no_stream_proxy — сверь список с main")
}
