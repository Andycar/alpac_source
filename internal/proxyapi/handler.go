// Package proxyapi is the /proxy/<aes-payload> endpoint that decrypts a
// URL+headers blob and streams the upstream response back to the client.
//
// The encrypted payload (see proxylink.Manager) carries: target URL, plugin
// name, optional upstream headers (Origin/Referer/Bearer for CDNs that 403
// without them), and optional CBCS/SAMPLE-AES key material.
//
// Hot-path responsibilities:
//
//   - HLS rewriter (handleM3U): rewrites segment URLs in playlists to point
//     back through /proxy so subsequent segments inherit our headers. Cached
//     short-TTL (8s) per playlist URL to absorb the player-reload burst at
//     bitrate switches.
//   - DASH rewriter (handleMPD): same idea for MPEG-DASH manifests.
//   - Segment streamer: chunked io.CopyBuffer from upstream → client, with
//     fmp4 DRM-box stripping (encv→avc1) and CBCS in-place decrypt for
//     Kinescope-style streams.
//   - copyHeadersFiltered: drops hop-by-hop + CDN-internal headers so the
//     browser sees a clean response.
//
// Caches:
//
//   - m3uCache: rewritten playlist cache (~2000 entries, 8s TTL, lazy sweep).
//   - cbcsKeyCache: license-key cache (TTL 30m, hard cap 4096) — was
//     unbounded sync.Map before 2026-05-18.
//
// Bypass for known-bad upstreams via pluginQuirks: e.g. Kinescope needs
// Origin/Referer; Mirage needs `Authorizations` bearer header from the
// encrypted payload.
package proxyapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lampac-go/internal/httpclient"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/cmcd"
	"lampac-go/internal/config"
	"lampac-go/internal/hlsprobe"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

var (
	reHTTPLinks      = regexp.MustCompile(`(https?://[^\n\r"\# ]+)`)
	reBaseURLTag     = regexp.MustCompile(`(?i)<BaseURL>([^<]+)</BaseURL>`)
	rePathNoFileName = regexp.MustCompile(`(?i)(https?://[^\n\r]+/)([^/]+)$`)
	reHLSHost        = regexp.MustCompile(`(?i)(https?://[^/]+)/`)
	reSampleAESKey   = regexp.MustCompile(`(?m)^#EXT-X-KEY:METHOD=SAMPLE-AES[^\n]*\n?`)
	// Extract URI and IV from #EXT-X-KEY:METHOD=SAMPLE-AES line
	reSampleAESURI = regexp.MustCompile(`URI="([^"]+)"`)
	reSampleAESIV  = regexp.MustCompile(`IV=0x([0-9a-fA-F]+)`)
)

// copyBufPool reuses 64 KB buffers for io.CopyBuffer, reducing GC pressure
// on high-concurrency proxy streaming.
var copyBufPool = sync.Pool{New: func() any { b := make([]byte, 64<<10); return &b }}

// m3uCache caches rewritten m3u8/mpd responses to avoid re-fetching and
// re-rewriting the same playlist for concurrent viewers of the same stream.
var m3uCache = struct {
	sync.RWMutex
	items      map[string]m3uCacheEntry
	totalBytes int64
}{items: make(map[string]m3uCacheEntry, 512)}

type m3uCacheEntry struct {
	body       []byte
	statusCode int
	headers    http.Header
	expiresAt  time.Time
}

const m3uCacheTTL = 8 * time.Second // increased from 3s; playlists don't change that fast
const m3uCacheMax = 2000            // increased from 500 for scale
// The count cap alone let the cache balloon: AES-token rewriting grows every segment line
// 2-4x, so one VOD/catchup manifest can be 10-15MB REWRITTEN — 100-150 of those = the
// 1.5GB heap we saw in prod. Bound BYTES too, and don't cache giants at all (catchup
// archives also carry per-seek from/to timestamps → unique keys that never hit again).
const m3uCacheMaxBytes = 96 << 20       // 96 MB total budget
const m3uCacheMaxEntryBytes = 512 << 10 // single entries above 512 KB are served uncached

// m3uLastGood — последний УДАЧНЫЙ манифест канала, отдельно от основного кэша и с более
// длинным сроком. Нужен для подмены при отказе апстрима (см. m3uServeStaleOnError).
//
// Зачем отдельное хранилище: у живого IPTV основной кэш живёт 2 секунды нарочно (иначе
// плеер получает тот же манифест дважды и встаёт на живом краю), а пережить отказ надо
// дольше этих двух секунд.
var m3uLastGood = struct {
	sync.RWMutex
	items map[string]m3uCacheEntry
}{items: make(map[string]m3uCacheEntry, 256)}

const (
	m3uStaleRetain   = 30 * time.Second // сколько храним последний удачный манифест
	m3uStaleServeMax = 12 * time.Second // насколько старый готовы отдать вместо отказа
	m3uLastGoodMax   = 512              // потолок записей
)

func m3uLastGoodSet(key string, e m3uCacheEntry) {
	if len(e.body) == 0 || len(e.body) > m3uCacheMaxEntryBytes {
		return
	}
	e.expiresAt = time.Now().Add(m3uStaleRetain)
	m3uLastGood.Lock()
	if len(m3uLastGood.items) >= m3uLastGoodMax {
		now := time.Now()
		for k, v := range m3uLastGood.items {
			if now.After(v.expiresAt) {
				delete(m3uLastGood.items, k)
			}
		}
		if len(m3uLastGood.items) >= m3uLastGoodMax {
			for k := range m3uLastGood.items { // всё ещё полно — освобождаем произвольную
				delete(m3uLastGood.items, k)
				break
			}
		}
	}
	m3uLastGood.items[key] = e
	m3uLastGood.Unlock()
}

func m3uLastGoodGet(key string) (m3uCacheEntry, bool) {
	m3uLastGood.RLock()
	e, ok := m3uLastGood.items[key]
	m3uLastGood.RUnlock()
	if !ok {
		return m3uCacheEntry{}, false
	}
	// expiresAt = момент конца хранения; отдавать готовы только заметно более свежее
	age := m3uStaleRetain - time.Until(e.expiresAt)
	if age > m3uStaleServeMax {
		return m3uCacheEntry{}, false
	}
	return e, true
}

// m3uStaleLogged гасит повтор одинаковых строк: подмена случается на каждый запрос
// зрителя, а знать надо лишь то, что канал сейчас едет на подмене.
var m3uStaleLogged sync.Map // cacheKey -> time.Time

func noteM3UStale(plugin, key string, status int) {
	now := time.Now()
	if v, ok := m3uStaleLogged.Load(key); ok {
		if t, ok2 := v.(time.Time); ok2 && now.Sub(t) < 30*time.Second {
			return
		}
	}
	m3uStaleLogged.Store(key, now)
	log.Warn().Str("plugin", plugin).Int("status", status).Str("key", truncStr(key, 90)).
		Msg("proxy: апстрим отказал — отдаём последний удачный манифест")
}

// m3uCacheJanitor evicts expired entries every minute. The write-path covers
// hot traffic, but on quiet servers between bursts entries can sit until the
// next write trips the cap. Time-based eviction caps RAM growth predictably.
func init() {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			now := time.Now()
			m3uCache.Lock()
			for k, v := range m3uCache.items {
				if now.After(v.expiresAt) {
					m3uCache.totalBytes -= int64(len(v.body))
					delete(m3uCache.items, k)
				}
			}
			m3uCache.Unlock()
		}
	}()
}

func m3uCacheGet(key string) (m3uCacheEntry, bool) {
	m3uCache.RLock()
	e, ok := m3uCache.items[key]
	m3uCache.RUnlock()
	if ok && time.Now().Before(e.expiresAt) {
		return e, true
	}
	return m3uCacheEntry{}, false
}

func m3uCacheSet(key string, e m3uCacheEntry) {
	m3uCacheSetTTL(key, e, m3uCacheTTL)
}

func m3uCacheSetTTL(key string, e m3uCacheEntry, ttl time.Duration) {
	if len(e.body) > m3uCacheMaxEntryBytes {
		return // giant rewritten manifest (VOD/catchup archive) — serve it, don't retain it
	}
	e.expiresAt = time.Now().Add(ttl)
	m3uCache.Lock()
	if len(m3uCache.items) >= m3uCacheMax || m3uCache.totalBytes+int64(len(e.body)) > m3uCacheMaxBytes {
		// Evict expired entries; if still over limit (count OR bytes), clear all.
		now := time.Now()
		for k, v := range m3uCache.items {
			if now.After(v.expiresAt) {
				m3uCache.totalBytes -= int64(len(v.body))
				delete(m3uCache.items, k)
			}
		}
		if len(m3uCache.items) >= m3uCacheMax || m3uCache.totalBytes+int64(len(e.body)) > m3uCacheMaxBytes {
			m3uCache.items = make(map[string]m3uCacheEntry, 512)
			m3uCache.totalBytes = 0
		}
	}
	if old, ok := m3uCache.items[key]; ok {
		m3uCache.totalBytes -= int64(len(old.body))
	}
	m3uCache.totalBytes += int64(len(e.body))
	m3uCache.items[key] = e
	m3uCache.Unlock()
}

// cbcsKeyCache caches CBCS decryption keys by KEY URI.
//
// Was previously sync.Map (unbounded) — under high DRM-stream traffic with
// many distinct license URIs the map grew indefinitely. Now bounded by size
// and TTL: keys live up to cbcsKeyTTL, with a hard cap to prevent OOM if a
// caller-controlled URI fanout overruns the TTL window.
const (
	cbcsKeyTTL = 30 * time.Minute
	cbcsKeyMax = 4096
)

type cbcsKeyEntry struct {
	key     []byte
	expires time.Time
}

type cbcsKeyStore struct {
	mu      sync.RWMutex
	entries map[string]cbcsKeyEntry
}

var cbcsKeyCache = &cbcsKeyStore{entries: make(map[string]cbcsKeyEntry, 256)}

func (s *cbcsKeyStore) Load(uri string) ([]byte, bool) {
	s.mu.RLock()
	e, ok := s.entries[uri]
	s.mu.RUnlock()
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.key, true
}

func (s *cbcsKeyStore) Store(uri string, key []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Lazy expiry sweep every ~64 writes; cheap when the map is small.
	if len(s.entries)%64 == 0 {
		now := time.Now()
		for k, e := range s.entries {
			if now.After(e.expires) {
				delete(s.entries, k)
			}
		}
	}
	// Hard cap: drop a random oldest-bucket entry when over the cap. Random
	// is fine here — license URIs see uniform access, no real LRU benefit.
	if len(s.entries) >= cbcsKeyMax {
		for k := range s.entries {
			delete(s.entries, k)
			break
		}
	}
	s.entries[uri] = cbcsKeyEntry{key: key, expires: time.Now().Add(cbcsKeyTTL)}
}

// pluginHeaderProviders stores optional dynamic upstream headers per plugin.
// Example: Rezka can inject fresh auth cookies obtained by auto-login.
var pluginHeaderProviders sync.Map // map[string]func() map[string]string

// DirectRedirect — хук httpapi: поток этого источника зритель может забрать у
// бэкенда напрямую, подписанной ссылкой, минуя и main, и ноды. Возвращает
// адрес для 302. Замер 21.09.2026: торренты через /proxy (pidtor) — 56 %
// входящего main в вечерний пик, при том что у /ts/stream прямая отдача уже
// есть; ноды такой поток отдать не могут (403 от secure_link бэкенда).
var DirectRedirect func(plugin, target string, r *http.Request) (string, bool)

// DirectRecorder — учёт потоков, ушедших напрямую (для сводки мощностей).
var DirectRecorder func(plugin string)

// TrafficRecorder is a callback set by httpapi to record proxy traffic stats.
// Parameters: plugin name, bytes transferred, whether the response was an error.
var TrafficRecorder func(plugin string, bytes int64, isError bool)

// ActiveStreamCounter is a callback set by httpapi to track active proxy streams.
// Returns a done function that must be called when the stream ends.
var ActiveStreamCounter func() func()

// LatencyRecorder is a callback set by httpapi to record per-plugin proxy latency.
// Parameters: plugin name, request elapsed duration, final HTTP status code.
var LatencyRecorder func(plugin string, elapsed time.Duration, statusCode int)

// OffloadRecorder is a callback set by httpapi, called when a stream is handed
// to a node instead of being served here. Lets the dashboard show how much the
// stream_edges allowlist actually takes off this instance.
var OffloadRecorder func(plugin string)

// CMCDRecorder is a callback set by httpapi to record Common Media Client Data
// (CTA-5004) that a player attached to a segment request. This is the only place
// in the server where the PLAYER's view of a stream — buffer level, measured
// throughput, whether it just stalled — is visible, and it costs nothing: the
// request was being made anyway.
var CMCDRecorder func(plugin, userAgent string, rep cmcd.Report)

// recordCMCD is a no-op for the overwhelming majority of requests: players that
// don't emit CMCD cost one header lookup and one substring scan.
func recordCMCD(r *http.Request, plugin string) {
	if CMCDRecorder == nil {
		return
	}
	if rep, ok := cmcd.Parse(r); ok {
		CMCDRecorder(plugin, r.Header.Get("User-Agent"), rep)
	}
}

// stripCMCDQuery removes only the CMCD argument, leaving every other query
// argument byte-identical — used on paths that deliberately forward the client
// query verbatim.
func stripCMCDQuery(rawQuery string) string {
	if rawQuery == "" || (!strings.Contains(rawQuery, "CMCD=") && !strings.Contains(rawQuery, "cmcd=")) {
		return rawQuery
	}
	parts := strings.Split(rawQuery, "&")
	kept := parts[:0]
	for _, p := range parts {
		if k, _, _ := strings.Cut(p, "="); strings.EqualFold(k, "cmcd") {
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, "&")
}

// RegisterPluginHeaderProvider registers a dynamic header provider for a plugin.
// The provider is called on every proxied request for that plugin.
func RegisterPluginHeaderProvider(plugin string, provider func() map[string]string) {
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin == "" {
		return
	}
	if provider == nil {
		pluginHeaderProviders.Delete(plugin)
		return
	}
	pluginHeaderProviders.Store(plugin, provider)
}

// balancerClientEntry stores a cached *http.Client with last-access timestamp
// for TTL-based eviction.
type balancerClientEntry struct {
	client   *http.Client
	lastUsed int64 // unix seconds (atomic)
}

type Handler struct {
	links  *proxylink.Manager
	client *http.Client
	// streamClient — клиент БЕЗ общего таймаута для живых IPTV-потоков: у
	// http.Client{Timeout} дедлайн продолжает тикать после получения заголовков
	// и обрывает чтение тела — raw-TS канал (один бесконечный HTTP-ответ)
	// умирал ровно на 35-й секунде. Фазу коннекта+заголовков страхует
	// контекстный таймер, тело — stallWatchdogBody (см. fetchWithMeta).
	streamClient   *http.Client
	proxiedClient  *http.Client    // proxied client (SOCKS5/VLESS) — legacy, first proxy
	proxiedPlugins map[string]bool // all plugins that have a proxy configured
	// manifestOnlyPlugins are plugins whose MANIFEST we rewrite but whose SEGMENTS
	// the client fetches straight from the CDN (cfg.Online.StreamProxyManifestOnly).
	manifestOnlyPlugins   map[string]bool
	balancerClients       sync.Map // map[string]*balancerClientEntry
	maxLengthM3U          int64
	responseContentLength bool
	pidorezkaHost         string
	cdnHTTPProxy          string // HTTP proxy for CDN plugins (e.g. "http://127.0.0.1:8888")

	// hostCfg carries the [host] section. Used by streamHostFromRequest to
	// rewrite emitted /proxy/ URLs onto a stream-only subdomain when the
	// main domain is fronted by a CDN that can't carry video (e.g. CF Free).
	hostCfg config.HostConfig

	// EdgeHashLookup returns the latest edge_hash (pc_hash) for CDN auth.
	// Called for mirage/alloha/aladdin plugins on every segment request.
	// Set by httpapi after handler creation. Key is typically tokenMovie.
	EdgeHashLookup func(tokenMovie string) string

	// nodeLabel — как этот сервер подписывает СВОИ ответы в X-Alpac-Node: плеер по нему
	// понимает, с какой ноды пришёл сегмент, когда разбирает просадку (StreamDiag).
	// edge_label из конфига, иначе хост edge_url, иначе hostname.
	nodeLabel string
}

func New(cfg config.Config, links *proxylink.Manager) *Handler {
	maxLen := cfg.ServerProxy.MaxLengthM3U
	if maxLen <= 0 {
		maxLen = 5_000_000
	}

	var proxiedClient *http.Client
	proxiedPlugins := map[string]bool{}
	// Build plugin set from all registered balancers (multi-proxy aware).
	for _, name := range httpclient.AllProxiedBalancers() {
		proxiedPlugins[strings.ToLower(name)] = true
	}
	if httpclient.ProxiedTransport != nil {
		proxiedClient = httpclient.NewProxiedNoRedirect(35 * time.Second)
	}

	manifestOnly := map[string]bool{}
	for _, name := range cfg.Online.StreamProxyManifestOnly {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			manifestOnly[name] = true
		}
	}
	// filmix can never use this mode, so refuse it rather than half-playing.
	//
	// ★★★2026-08-05: запрет manifest-only для filmix СНЯТ — вывод «CDN привязывает всё выше 480p к
	// IP, получившему hash» оказался неверен. Он был снят на ПРОТУХШЕМ hash, который отдаёт 403 на
	// что угодно и с любого адреса. С живым hash проверено с постороннего IP: манифест 200,
	// медиа-сегмент 206, то есть привязки нет.
	// Единственная настоящая поломка — hashless EXT-X-MAP (см. ниже): CDN дописывает ?hash= во все
	// сегменты, но НЕ в init-сегмент, и тот один запрос 403-ит всё воспроизведение. Манифест мы
	// переписываем и hash туда возвращаем, поэтому сегменты можно спокойно отдавать напрямую.
	// Ноды, которым разрешено отдавать поток вместо primary (см. edge.go).
	// Пустой список = поведение прежнее, всё отдаёт этот сервер.
	configureEdges(cfg.ProxyLink.StreamEdges, cfg.ProxyLink.StreamEdgePlugins, cfg.ProxyLink.StreamEdgeExclude)
	// Кому какую ноду не отдавать: имя ноды бывает закрыто провайдерами целой страны.
	configureEdgeGeo(cfg.ProxyLink.StreamEdgeGeoDeny)
	// Куда нода возвращает поток, который не смогла отдать. У primary это поле
	// пустое, поэтому возврат там не включается и петли не возникает.
	if cfg.Cluster.Mode == "node" {
		configureEdgeFallback(cfg.Cluster.PrimaryHost)
	}

	h := &Handler{
		links:                 links,
		maxLengthM3U:          maxLen,
		responseContentLength: cfg.ServerProxy.ResponseContentLength,
		client:                httpclient.NewNoRedirect(35 * time.Second),
		streamClient:          httpclient.NewNoRedirect(0),
		proxiedClient:         proxiedClient,
		proxiedPlugins:        proxiedPlugins,
		manifestOnlyPlugins:   manifestOnly,
		pidorezkaHost:         strings.TrimSpace(cfg.Online.PidoRezka.Host),
		cdnHTTPProxy:          strings.TrimSpace(cfg.Online.CDNHTTPProxy),
		hostCfg:               cfg.Host,
		nodeLabel:             nodeLabelFor(cfg),
	}
	// Evict stale per-balancer clients every 5 minutes (idle > 10 min).
	go h.evictStaleClients()
	return h
}

// parseContentType returns the lowercased media type portion of a
// Content-Type header (everything before the first ';', trimmed). Allocation-
// free compared to strings.Split — the latter allocates a slice header plus
// (in the multi-part case) a new backing array.
func parseContentType(raw string) string {
	if i := strings.IndexByte(raw, ';'); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSpace(raw)
	return strings.ToLower(raw)
}

// evictStaleClients removes per-balancer HTTP clients idle > 10 minutes.
// Runs every 5 minutes in a background goroutine. Closing idle connections
// frees file descriptors and memory held by keep-alive pools.
func (h *Handler) evictStaleClients() {
	const evictInterval = 5 * time.Minute
	const maxIdleSec = int64(10 * 60) // 10 minutes
	for {
		time.Sleep(evictInterval)
		cutoff := time.Now().Unix() - maxIdleSec
		h.balancerClients.Range(func(key, val any) bool {
			entry := val.(*balancerClientEntry)
			if atomic.LoadInt64(&entry.lastUsed) < cutoff {
				h.balancerClients.Delete(key)
				if t, ok := entry.client.Transport.(interface{ CloseIdleConnections() }); ok {
					t.CloseIdleConnections()
				}
				log.Debug().Str("plugin", key.(string)).Msg("proxy: evicted stale balancer client")
			}
			return true
		})
	}
}

func (h *Handler) HandleProxy(w http.ResponseWriter, r *http.Request) {
	if ActiveStreamCounter != nil {
		done := ActiveStreamCounter()
		defer done()
	}
	start := time.Now()
	statusCode := http.StatusInternalServerError
	pluginName := ""
	// upstreamDone = when the upstream answered (headers in hand) — the per-plugin latency
	// table records THIS, not full transfer time: a torrent/pidtor read-session held open for
	// a minute by the player showed up as "avg 56s" while the upstream was fine.
	var upstreamDone time.Time
	defer func() {
		l := time.Since(start)
		if !upstreamDone.IsZero() {
			l = upstreamDone.Sub(start)
		}
		recordLatency(pluginName, l, statusCode)
	}()

	reqIP := requestIP(r)
	raw := strings.TrimPrefix(r.URL.Path, "/proxy/")
	target, meta, ok := h.resolveTarget(raw, r.URL.RawQuery, reqIP)
	pluginName = meta.plugin
	if !ok {
		if meta.gone {
			// 410 Gone, not 404: the link is a pre-TTL one that will never
			// resolve again. 404 reads as "try again" to most players, and one
			// client hammered a single dead IPTV token 622 times in an hour
			// behind an endless spinner. The body names the fix so a human
			// looking at the response sees it too.
			statusCode = http.StatusGone
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "link expired — refresh the playlist to get a fresh one", http.StatusGone)
			return
		}
		log.Warn().Str("ip", reqIP).Str("raw", truncStr(raw, 80)).Msg("proxy: resolve failed")
		statusCode = http.StatusNotFound
		h.fallback(w, r)
		return
	}

	log.Debug().
		Str("method", r.Method).
		Str("ip", reqIP).
		Str("plugin", meta.plugin).
		Str("target", truncStr(target, 120)).
		Str("range", r.Header.Get("Range")).
		Str("ua", truncStr(r.Header.Get("User-Agent"), 80)).
		Msg("proxy: request")

	// Разгрузка primary: отдать поток нодой, а не собой (см. edge.go). Стоит
	// ЗДЕСЬ, потому что плагин известен только после расшифровки токена, а
	// выносить можно не всякий источник. Сессионные и привязанные к сети ссылки
	// (IPTV) сюда не попадают по белому списку: их состояние живёт в памяти
	// ВЫДАВШЕГО сервера, и на ноде такая ссылка получила бы «сессия неизвестна».
	if DirectRedirect != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		if loc, ok := DirectRedirect(meta.plugin, target, r); ok && loc != "" {
			statusCode = http.StatusFound
			if DirectRecorder != nil {
				DirectRecorder(meta.plugin)
			}
			// Подпись протухает, бэкенд может смениться — кэшировать редирект нельзя.
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, loc, http.StatusFound)
			return
		}
	}

	if edge := pickEdge(meta.plugin, raw, r, meta.edge); edge != "" && edgeAlive(h.client, edge) {
		dst := edge + r.URL.Path
		if r.URL.RawQuery != "" {
			dst += "?" + r.URL.RawQuery
		}
		statusCode = http.StatusFound
		if OffloadRecorder != nil {
			OffloadRecorder(meta.plugin)
		}
		log.Debug().Str("edge", edge).Str("plugin", meta.plugin).Msg("proxy: поток отдаёт нода")
		// 302, а не 301: выбор ноды зависит от её живости и может смениться,
		// а постоянный редирект клиенты и прокси кэшируют навсегда.
		http.Redirect(w, r, dst, http.StatusFound)
		return
	}

	// Player telemetry rides along on the request; record it before anything
	// upstream can fail, so a stalling source still reports its stalls.
	recordCMCD(r, meta.plugin)

	pathLower := strings.ToLower(r.URL.Path)

	target = choosePrimaryURL(h.client, target, r, h.maxLengthM3U)

	// Pre-fetch cache hit: handleM3U keys its cache on the FINAL (post-redirect) URL, which
	// is only known after the upstream round-trip — so cache "hits" still cost a full upstream
	// fetch whose body was closed unread. Manifests are also cached under the pre-redirect
	// target (see handleM3U), letting concurrent viewers skip the upstream entirely.
	if cached, ok := m3uCacheGet(target + "|" + meta.plugin); ok && r.Method == http.MethodGet && r.Header.Get("Range") == "" {
		for k, vs := range cached.headers {
			for _, v := range vs {
				w.Header().Set(k, v)
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(cached.body)))
		statusCode = cached.statusCode
		w.WriteHeader(cached.statusCode)
		_, _ = w.Write(cached.body)
		return
	}

	resp, baseURI, err := h.fetchWithMeta(target, r, meta)
	upstreamDone = time.Now()
	if err == nil && baseURI != nil && baseURI.String() != target {
		// Источник увёл нас на другой адрес (veoveo: список источников → плейлист
		// на ротируемом edge). Относительные сегменты внутри плейлиста считаются
		// от ТОГО адреса, откуда он реально пришёл, иначе они уйдут в никуда.
		meta.baseURI = baseURI.String()
	}
	if err != nil {
		log.Warn().Err(err).Str("target", truncStr(target, 120)).Msg("proxy request failed")
		// Транспортная ошибка апстрима (таймаут, обрыв TCP, DNS) — это 502/504,
		// а НЕ 404: для HLS-плеера 404 на сегменте означает «выпал из окна
		// навсегда» (фатал без ретрая), 5xx — «временно, повтори». Моргнувший
		// апстрим переставал быть поводом выбрасывать юзера из плеера.
		statusCode = upstreamErrStatus(err)
		http.Error(w, "upstream error", statusCode)
		return
	}

	// Протухший билет апстрима: 401/403 у источника, который умеет
	// переминтить. «Мир кино» отдаёт mediaSourceId со сроком ~60 секунд, а
	// наш /proxy-токен живёт 36 часов — зритель доигрывает до первой
	// перемотки и ловит 401. Здесь мы один раз просим у источника свежий
	// адрес и повторяем запрос: зритель видит продолжение фильма, а не
	// чёрный экран.
	//
	// Ровно один повтор и только пока клиенту ещё ничего не отправлено.
	// HEAD тоже лечим: плееры проверяют им длину перед перемоткой.
	//
	// 429 — тоже сюда: CDN Filmix закрепляет подпись поста за первым IP учётки и
	// остальным отвечает 429 на часы; тот же файл, подписанный ДРУГОЙ учёткой, с
	// этого адреса играет (litesrc/filmix_remint.go). Без этого нода возвращала
	// зрителя на primary, где подпись для main так же чужая.
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		(resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
			resp.StatusCode == http.StatusTooManyRequests) &&
		proxylink.HasTargetRefresher(meta.plugin) {
		if fresh, ok := proxylink.RefreshTarget(r.Context(), meta.plugin, target); ok {
			resp.Body.Close()
			log.Info().
				Str("plugin", meta.plugin).
				Int("was_status", resp.StatusCode).
				Str("stale", truncStr(target, 100)).
				Str("fresh", truncStr(fresh, 100)).
				Msg("proxy: билет апстрима протух — переминтили и повторяем")
			target = fresh
			meta.baseURI = fresh
			resp, baseURI, err = h.fetchWithMeta(target, r, meta)
			if err != nil {
				log.Warn().Err(err).Str("target", truncStr(target, 120)).
					Msg("proxy: повтор после переминта не удался")
				statusCode = upstreamErrStatus(err)
				http.Error(w, "upstream error", statusCode)
				return
			}
			if baseURI != nil && baseURI.String() != target {
				meta.baseURI = baseURI.String()
			}
		}
	}

	// Retry on upstream 5xx (GET only, before body is streamed to client).
	if r.Method == http.MethodGet && isRetryableStatus(resp.StatusCode) {
		for retry := range 3 {
			resp.Body.Close()
			delay := time.Duration(500+retry*500) * time.Millisecond
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				statusCode = http.StatusGatewayTimeout
				h.fallback(w, r)
				return
			}
			resp, baseURI, err = h.fetchWithMeta(target, r, meta)
			if err != nil {
				log.Warn().Err(err).Int("retry", retry+1).Str("target", truncStr(target, 120)).Msg("proxy: retry failed")
				statusCode = upstreamErrStatus(err)
				http.Error(w, "upstream error", statusCode)
				return
			}
			if !isRetryableStatus(resp.StatusCode) {
				break
			}
			log.Debug().Int("status", resp.StatusCode).Int("retry", retry+1).Msg("proxy: retrying 5xx")
		}
	}

	defer resp.Body.Close()

	if isRedirect(resp.StatusCode) || resp.Header.Get("Location") != "" {
		loc := resolveLocation(baseURI, resp.Header.Get("Location"))
		if loc == "" {
			statusCode = resp.StatusCode
			h.copyResponse(w, resp, false)
			return
		}
		log.Debug().
			Int("upstream_status", resp.StatusCode).
			Str("location", truncStr(loc, 120)).
			Msg("proxy: redirect")
		if strings.Contains(loc, "superdupercdn") {
			log.Warn().Str("full_location", loc).Msg("proxy: superdupercdn redirect (full)")
		}

		// For m3u8 targets, follow redirect server-side instead of sending 302
		// to the client. HLS players (hls.js) resolve relative URLs from the
		// *request* URL, not the redirect Location, so a client-side redirect
		// would break relative URL resolution in the rewritten playlist.
		//
		// Exception: superdupercdn redirects from obrut CDN — the superdupercdn
		// URL must be fetched as a clean separate request without obrut cookies/headers.
		// Re-wrap through /proxy/ with clean meta (like .NET Lampac does).
		isSuperDuperCDN := strings.Contains(loc, "superdupercdn")
		if !isSuperDuperCDN && (containsM3U(target) || containsM3U(loc)) {
			resp.Body.Close()
			meta.baseURI = loc
			resp2, _, err2 := h.fetchWithMeta(loc, r, meta)
			if err2 != nil {
				log.Warn().Err(err2).Str("target", truncStr(loc, 120)).Msg("proxy: redirect follow failed")
				statusCode = http.StatusNotFound
				h.fallback(w, r)
				return
			}
			resp = resp2
			defer resp.Body.Close()
			// fall through to content handling below
		} else {
			// For superdupercdn: strip obrut-specific headers (Cookie, Origin, Referer)
			// so the next proxy request goes clean. Keep only plugin for routing.
			cleanMeta := meta
			if isSuperDuperCDN {
				cleanMeta.headers = nil
			}
			proxyURI := h.proxyURI(r, loc, cleanMeta, false)
			statusCode = http.StatusFound
			http.Redirect(w, r, proxyURI, http.StatusFound)
			return
		}
	}

	// parseContentType avoids strings.Split (which allocates a []string for
	// every request) on a path called once per proxy response. Mostly cosmetic
	// on its own, but multiplies — on 1k req/min the slice-alloc shows up
	// in pprof allocs.
	ct := parseContentType(resp.Header.Get("Content-Type"))
	isTS := strings.HasSuffix(pathLower, ".ts") || strings.HasSuffix(pathLower, ".m4s") || strings.HasSuffix(pathLower, ".mp4")

	log.Debug().
		Int("upstream_status", resp.StatusCode).
		Str("ct", ct).
		Int64("cl", resp.ContentLength).
		Str("accept_ranges", resp.Header.Get("Accept-Ranges")).
		Str("content_range", resp.Header.Get("Content-Range")).
		Msg("proxy: upstream response")

	if resp.StatusCode == 404 {
		finalTarget := ""
		if baseURI != nil {
			finalTarget = baseURI.String()
		}
		logEvt := log.Warn().
			Str("full_target", target).
			Str("final_target", finalTarget).
			Str("plugin", meta.plugin).
			Str("method", r.Method)
		// Extra diagnostics for voidboost CDN 404 — log upstream response headers
		if isRezkaPlugin(meta.plugin) && baseURI != nil && isVoidboostCDNURL(baseURI) {
			logEvt = logEvt.
				Str("resp_server", resp.Header.Get("Server")).
				Str("resp_via", resp.Header.Get("Via")).
				Str("resp_x_cache", resp.Header.Get("X-Cache")).
				Str("resp_cf_ray", resp.Header.Get("CF-Ray")).
				Bool("proxied_transport", h.proxiedPlugins[strings.ToLower(meta.plugin)])
		}
		logEvt.Msg("proxy: upstream 404")
	}

	// Surface upstream "blocked" statuses (the proxy relays them verbatim, so these are the
	// SOURCE/CDN saying no — not our decrypt). meta.plugin names the culprit source; final_target
	// is the CDN URL. Grep "proxy: upstream blocked playback" to find what keeps breaking movies.
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusUnavailableForLegalReasons:
		finalTarget := ""
		if baseURI != nil {
			finalTarget = baseURI.String()
		}
		// Self-diagnosing: a CDN 403 is one of UA-gate / IP-bind / referer / token-expiry. Log the
		// UA we sent upstream (token header wins over the client UA), whether we left via the
		// balancer's proxy egress, and how many headers the token carried — so "filmix 403" tells
		// us WHICH cause without guessing.
		ua := meta.headers["User-Agent"]
		if ua == "" {
			ua = r.Header.Get("User-Agent")
		}
		logEvt := log.Warn().
			Int("status", resp.StatusCode).
			Str("plugin", meta.plugin).
			Str("final_target", finalTarget).
			Str("sent_ua", truncStr(ua, 60)).
			Bool("via_proxy", meta.plugin != "" && httpclient.TransportForBalancer(meta.plugin) != nil).
			Int("token_headers", len(meta.headers)).
			Str("method", r.Method)
		// A short text/plain body IS the reason on some CDNs — kino.pub answers a
		// stream open past the account's concurrent-session cap with 403 + "user
		// session limit reached", which is indistinguishable from a token/geo 403
		// without it. Peeked non-destructively: the bytes are pushed back so the
		// relay below still forwards the body verbatim.
		if ct := strings.ToLower(resp.Header.Get("Content-Type")); strings.HasPrefix(ct, "text/plain") {
			if peek, err := io.ReadAll(io.LimitReader(resp.Body, 256)); err == nil && len(peek) > 0 {
				resp.Body = struct {
					io.Reader
					io.Closer
				}{io.MultiReader(bytes.NewReader(peek), resp.Body), resp.Body}
				logEvt = logEvt.Str("upstream_reason", truncStr(strings.TrimSpace(string(peek)), 120))
			}
		}
		logEvt.Msg("proxy: upstream blocked playback")
	}

	// Не смогли отдать — вернуть запрос primary, пусть отдаст сам.
	// Ровно этим отличается «источник не пускает ноду» от сломанного просмотра:
	// зритель платит один переход, а не чёрный экран (см. edgeGiveBack).
	if back := edgeGiveBack(r, resp.StatusCode, resp.Header.Get("Content-Type")); back != "" {
		upHost := ""
		if baseURI != nil {
			upHost = baseURI.Host
		}
		// upstream_host — КТО именно не пустил ноду. Без него по журналу видно
		// лишь «hdvb вернулся», а решать, через какой выход вести источник с
		// этой ноды, можно только зная хост CDN.
		log.Warn().Int("status", resp.StatusCode).Str("plugin", meta.plugin).
			Str("content_type", resp.Header.Get("Content-Type")).
			Str("upstream_host", upHost).
			Bool("via_proxy", meta.plugin != "" && httpclient.TransportForBalancer(meta.plugin) != nil).
			Msg("proxy: нода не смогла отдать, возвращаю запрос primary")
		_ = resp.Body.Close()
		statusCode = http.StatusFound
		recordTraffic(meta.plugin, 0, resp.StatusCode)
		http.Redirect(w, r, back, http.StatusFound)
		return
	}

	targetLower := strings.ToLower(meta.baseURI)
	if !isTS && (strings.Contains(pathLower, ".m3u") || strings.Contains(targetLower, ".m3u") || ct == "application/x-mpegurl" || ct == "application/vnd.apple.mpegurl" || ct == "text/plain") {
		statusCode = resp.StatusCode
		h.handleM3U(w, r, resp, meta, target)
		recordTraffic(meta.plugin, resp.ContentLength, resp.StatusCode)
		return
	}

	if strings.Contains(pathLower, ".mpd") || ct == "application/dash+xml" {
		statusCode = resp.StatusCode
		h.handleMPD(w, r, resp, meta)
		recordTraffic(meta.plugin, resp.ContentLength, resp.StatusCode)
		return
	}

	// Strip fMP4 DRM boxes for Kinescope CDN (used by redheadsound).
	// Init segments: moov/stsd with encv/enca + sinf → avc1/mp4a + free.
	// Media segments: moof/traf with senc/saiz/saio → free.
	// All patching is in-place (same size) so BYTERANGE offsets stay valid.
	if strings.EqualFold(meta.plugin, "redheadsound") && isTS {
		statusCode = resp.StatusCode
		h.handleFMP4DRMStripStream(w, resp, meta)
		recordTraffic(meta.plugin, resp.ContentLength, resp.StatusCode)
		return
	}

	statusCode = resp.StatusCode
	n := h.copyResponse(w, resp, h.responseContentLength)
	recordTraffic(meta.plugin, n, resp.StatusCode)
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// dashSIDSep отделяет sid потока от зашифрованного base в пути /proxy-dash.
// Не точка: Decrypt срезает всё после последней точки как расширение файла.
const dashSIDSep = "~s~"

func (h *Handler) HandleDash(w http.ResponseWriter, r *http.Request) {
	reqIP := requestIP(r)
	rest := strings.TrimPrefix(r.URL.Path, "/proxy-dash/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 {
		h.fallback(w, r)
		return
	}

	baseRaw := parts[0]
	tail := parts[1]
	sid := ""
	if i := strings.LastIndex(baseRaw, dashSIDSep); i > 0 {
		if v, err := url.PathUnescape(baseRaw[i+len(dashSIDSep):]); err == nil {
			sid = v
		}
		baseRaw = baseRaw[:i]
	}
	var baseURI string
	meta := linkMeta{reqIP: reqIP, verifyIP: true, sid: sid}

	if target, ok := decodeDirectTarget(baseRaw, ""); ok {
		baseURI = target
	} else if h.links != nil {
		if model := h.links.Decrypt(baseRaw, reqIP); model != nil && strings.TrimSpace(model.URI) != "" {
			// Тот же гейт сессии, что и на /proxy: без него сегменты DASH
			// оставались бы голыми предъявительскими ссылками в обход всей
			// сверки. Ручка в проде пока не используется (0 запросов), но
			// незакрытой дырке незачем ждать первого DASH-канала.
			if h.links.RequiresSession(model.Plugin) {
				if reason := h.links.CheckSession(sid, reqIP); reason != "" {
					log.Info().Str("reqip", reqIP).Str("plugin", model.Plugin).
						Str("reason", reason).Bool("session", true).
						Msg("proxy-dash: поток погашен сверкой сессии")
					w.Header().Set("Cache-Control", "no-store")
					http.Error(w, "link expired — refresh the playlist to get a fresh one", http.StatusGone)
					return
				}
			}
			baseURI = strings.TrimSpace(model.URI)
			meta.plugin = model.Plugin
			meta.verifyIP = model.VerifyIP
			meta.headers = model.Headers
			meta.edge = model.Edge
		}
	}
	if baseURI == "" {
		h.fallback(w, r)
		return
	}

	recordCMCD(r, meta.plugin)

	target := baseURI + tail
	// Everything else in the query is passed through untouched (DASH segment
	// URLs carry upstream-significant arguments), but our own telemetry must
	// not reach the CDN.
	if rawQuery := stripCMCDQuery(r.URL.RawQuery); rawQuery != "" {
		if strings.Contains(target, "?") {
			target += "&" + rawQuery
		} else {
			target += "?" + rawQuery
		}
	}

	resp, _, err := h.fetchWithMeta(target, r, meta)
	if err != nil {
		log.Warn().Err(err).Str("target", target).Msg("proxy-dash request failed")
		h.fallback(w, r)
		return
	}
	defer resp.Body.Close()
	n := h.copyResponse(w, resp, h.responseContentLength)
	recordTraffic(meta.plugin, n, resp.StatusCode)
}

// isCDNPlugin returns true for plugins that proxy through CDNs which
// fingerprint TLS handshakes and reject Go's default crypto/tls — these
// upstream connections must go through the tls-client Chrome fingerprint
// transport (NewTLSClientViaSOCKS5). The pc_hash edge_hash injection
// (line ~621) only kicks in for the stloadi.live family that has an
// active EdgeHashLookup; femd/kinobadi/etc are unaffected by that branch
// because their balancers don't register an EdgeHashLookup.
func isCDNPlugin(plugin string) bool {
	switch strings.ToLower(plugin) {
	case "mirage", "alloha", "aladdin", "leproduction", "vibix",
		"femd", "kinobadi": // videostorage.xyz family — cdnr.interkh.com rejects Go TLS on .ts
		return true
	}
	return false
}

type linkMeta struct {
	reqIP    string
	plugin   string
	verifyIP bool
	baseURI  string
	headers  map[string]string // custom upstream headers from proxylink payload
	cbcsKey  []byte            // CBCS decryption key (16 bytes)
	cbcsIV   []byte            // CBCS constant IV (16 bytes)
	// gone marks a failure that can never succeed on retry (a pre-TTL link
	// for a plugin that now requires an expiry) so the caller answers 410.
	gone bool
	// sid — потоковая сессия запроса. Все ссылки, отчеканенные при разборе
	// этого манифеста, обязаны его унести: гейт fail-closed, и сегмент без
	// sid был бы отвергнут вместе с живым потоком.
	sid  string
	edge string // нода, добывшая ссылку (Model.Edge); "" = primary

}

// withSID вешает sid потока на готовую /proxy-ссылку. Отдельный помощник, а не
// три копии по месту: пропущенная копия молча выключила бы сверку на сегментах,
// оставив гейт только на манифесте.
func withSID(u, sid string) string {
	if sid == "" || u == "" {
		return u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "sid=" + url.QueryEscape(sid)
}

func (h *Handler) resolveTarget(raw, rawQuery, reqIP string) (string, linkMeta, bool) {
	meta := linkMeta{reqIP: reqIP, verifyIP: true}
	pluginFromQuery := ""
	sid := ""
	if q, err := url.ParseQuery(rawQuery); err == nil {
		pluginFromQuery = strings.ToLower(strings.TrimSpace(q.Get("pl")))
		sid = strings.TrimSpace(q.Get("sid"))
	}
	meta.sid = sid
	cleanQuery := sanitizeProxyRawQuery(rawQuery)
	if target, ok := decodeDirectTarget(raw, cleanQuery); ok {
		// Direct /proxy/<urlencoded-url> mode is used by several browser-facing
		// SISI sources. In this mode we don't have per-link plugin metadata, and
		// client IP can change between manifest and segment requests behind CDN/WAF.
		// Do not IP-bind rewritten segment tokens here to avoid false "not found".
		meta.verifyIP = false
		meta.plugin = pluginFromQuery
		meta.baseURI = target
		return target, meta, true
	}

	if h.links != nil {
		// Try raw token as-is first, then strip known media extensions
		// (.mp4, .m3u8, etc.) that may be appended for browser MIME detection.
		candidates := []string{raw}
		for _, ext := range []string{".mp4", ".m3u8", ".m4s", ".ts"} {
			if stripped, ok := strings.CutSuffix(raw, ext); ok {
				candidates = append(candidates, stripped)
				break
			}
		}
		for _, candidate := range candidates {
			if model := h.links.Decrypt(candidate, reqIP); model != nil && strings.TrimSpace(model.URI) != "" {
				target := strings.TrimSpace(model.URI)
				meta.plugin = model.Plugin
				// Гейт потоковой сессии: ссылка живёт, только пока живёт её
				// поток. Несостыковка (чужая/чередующаяся сеть, погашенная или
				// неизвестная сессия) гасит поток целиком — до перезапроса
				// /api/iptv/play. Проверяем ПОСЛЕ расшифровки: раньше не знаем
				// плагина, а гейт нужен не всем источникам.
				if h.links.RequiresSession(model.Plugin) {
					if reason := h.links.CheckSession(sid, reqIP); reason != "" {
						meta.gone = true
						log.Info().
							Str("reqip", reqIP).
							Str("plugin", model.Plugin).
							Str("reason", reason).
							Bool("session", true).
							Msg("proxy: поток погашен сверкой сессии")
						return "", meta, false
					}
				}
				meta.verifyIP = model.VerifyIP
				meta.baseURI = target
				meta.headers = model.Headers
				meta.edge = model.Edge
				meta.cbcsKey = model.CBCSKey
				meta.cbcsIV = model.CBCSIV
				if cleanQuery != "" {
					cleanQuery = sanitizeProxyTokenQuery(cleanQuery)
				}
				if cleanQuery != "" {
					if strings.Contains(target, "?") {
						target += "&" + cleanQuery
					} else {
						target += "?" + cleanQuery
					}
				}
				return target, meta, true
			}
		}
		// Debug: log why each candidate failed decryption.
		for _, candidate := range candidates {
			reason := h.links.DebugDecrypt(candidate, reqIP)
			if proxylink.IsEternalRejection(reason) || proxylink.IsForeignNetworkRejection(reason) ||
				proxylink.IsExpiredRejection(reason) {
				// Permanent: истёкший срок, чужая сеть или вечная ссылка у
				// плагина с обязательным TTL. Marked so the caller answers 410
				// instead of 404 — retrying can never help, and players that
				// treat 404 as "try again" spin forever on it. По 410 наш
				// клиент перезапрашивает /api/iptv/play и играет дальше
				// (замерено 2026-09-02: восстановление за 4 секунды).
				meta.gone = true
				// Info, не Debug: на проде debug выключен, и единственный
				// след того, ЧТО именно отбито — вечная ссылка или чужая
				// сеть — пропадал. Событие редкое (единицы в минуту) и это
				// ровно та метрика, ради которой привязка вводилась.
				log.Info().
					Str("candidate", truncStr(candidate, 120)).
					Str("reqip", reqIP).
					Str("reason", reason).
					Bool("foreign_net", proxylink.IsForeignNetworkRejection(reason)).
					Msg("proxy: link permanently rejected")
				continue
			}
			log.Warn().
				Str("candidate", truncStr(candidate, 120)).
				Str("reqip", reqIP).
				Str("reason", reason).
				Msg("proxy: decrypt debug")
		}
	} else {
		log.Debug().Str("raw", truncStr(raw, 120)).Msg("proxy: resolveTarget links is nil")
	}
	return "", meta, false
}

func sanitizeProxyRawQuery(rawQuery string) string {
	rawQuery = strings.TrimSpace(rawQuery)
	if rawQuery == "" {
		return ""
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}

	for key := range q {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "uid", "user_uid", "box_mac", "spid", "lampac_token", "pl", "sid":
			q.Del(key)
		case "cmcd":
			// Client telemetry is for us, not for the CDN. Forwarding it would
			// append an unexpected argument to signed upstream URLs — exactly
			// the class of change that turns a working stream into a 403.
			q.Del(key)
		case edgeNoRetry, edgeHint:
			// Служебные параметры выноса на ноды (см. edge.go). Тот же класс
			// ошибки: videodb подписывает URL (hash+expires), и «&_noedge=1»,
			// доехав до CDN, превращал рабочий поток в 403 — причём именно на
			// возврате с ноды, то есть у самого страховочного механизма.
			q.Del(key)
		}
	}
	return q.Encode()
}

// sanitizeProxyTokenQuery — query клиента для AES-ссылки: всё, что вырезает
// sanitizeProxyRawQuery, плюс наш токен пользователя. Внешние плееры (MX/ViMu/IPTV-плееры)
// получали ссылки с «?token=» — клиенты дописывали его к любому адресу своего хоста, включая
// /proxy, — и здесь он приклеивался к адресу апстрима: токен уезжал на чужой CDN, а подписанные
// ссылки (hash+expires) от лишнего параметра отвечали 403. Настоящий адрес апстрима целиком
// лежит в зашифрованном payload, так что «token» в query AES-ссылки — всегда наш.
// Прямой режим (/proxy/<url>) не трогаем: там query может быть частью самого адреса CDN.
func sanitizeProxyTokenQuery(rawQuery string) string {
	clean := sanitizeProxyRawQuery(rawQuery)
	if clean == "" || !strings.Contains(clean, "token") {
		return clean
	}
	q, err := url.ParseQuery(clean)
	if err != nil {
		return clean
	}
	for key := range q {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "token", "alpac_token":
			q.Del(key)
		}
	}
	return q.Encode()
}

func (h *Handler) fetch(target string, in *http.Request) (*http.Response, *url.URL, error) {
	return h.fetchWithMeta(target, in, linkMeta{})
}

// nodeLabelFor — подпись этого сервера для X-Alpac-Node (см. Handler.nodeLabel).
func nodeLabelFor(cfg config.Config) string {
	if l := strings.TrimSpace(cfg.Cluster.EdgeLabel); l != "" {
		return l
	}
	if u, err := url.Parse(strings.TrimSpace(cfg.Cluster.EdgeURL)); err == nil && u.Host != "" {
		return u.Host
	}
	hn, _ := os.Hostname()
	return hn
}

// stampDiag дописывает в ответ апстрима заголовки диагностики для плеера:
//   - X-Alpac-Up-Ms — сколько ЭТОТ сервер ждал апстрим до заголовков (TTFB);
//   - X-Alpac-Node  — кто отдал (подпись ноды);
//   - X-Alpac-Src   — балансер (плеер и так знает, чей поток играет — это для сверки).
//
// Клиент меряет свой TTFB на том же сегменте; разница «у нас 3 с, апстрим 100 мс» —
// это путь до ноды (провайдер режет), а «апстрим 3 с» — балансер. Без этих цифр обе
// просадки выглядят одинаково: спиннер. Заголовки уходят через copyHeadersFiltered
// вместе с остальными, отдельных точек записи не нужно.
func stampDiag(resp *http.Response, since time.Time, node, plugin string) {
	if resp == nil {
		return
	}
	resp.Header.Set("X-Alpac-Up-Ms", strconv.FormatInt(time.Since(since).Milliseconds(), 10))
	if node != "" {
		resp.Header.Set("X-Alpac-Node", node)
	}
	if plugin != "" {
		resp.Header.Set("X-Alpac-Src", plugin)
	}
}

// ProbeUpstream fetches the first bytes of a resolved stream url EXACTLY as HandleProxy would
// (same per-balancer transport binding, per-plugin header quirks, and pre-fetch rewrites) and
// reports the upstream status — so an admin can see which sources 403 server-side WITHOUT
// waiting for a real play. The body is drained-and-closed, never streamed. Admin diagnostic only.
//
// Authoritative for proxied sources (the server IS the fetcher); for no_stream_proxy sources the
// client fetches the CDN directly from a different IP, so a server probe is only indicative.
func (h *Handler) ProbeUpstream(ctx context.Context, rawURL, plugin string, headers map[string]string) (status int, finalURL string, viaProxy bool, errStr string) {
	target, meta, err := h.probeTarget(rawURL, plugin, headers)
	if err != nil {
		return 0, rawURL, false, err.Error()
	}
	viaProxy = httpclient.TransportForBalancer(meta.plugin) != nil
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, target, viaProxy, err.Error()
	}
	req.Header.Set("Range", "bytes=0-1") // tiny — we only want the status line
	resp, base, err := h.fetchWithMeta(target, req, meta)
	if err != nil {
		return 0, target, viaProxy, err.Error()
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	final := target
	if base != nil {
		final = base.String()
	}
	return resp.StatusCode, final, viaProxy, ""
}

// probeTarget resolves what a probe should actually fetch. Many balancers
// PRE-MINT their own "/proxy/<token>" url (the token carries the real CDN
// target + the source's own headers); naively fetching that token url would
// just hit our own host. Decrypting it the way HandleProxy does makes the probe
// authoritative for the SOURCE rather than a self-request.
func (h *Handler) probeTarget(rawURL, plugin string, headers map[string]string) (string, linkMeta, error) {
	meta := linkMeta{plugin: plugin, headers: headers}
	i := strings.Index(rawURL, "/proxy/")
	if i < 0 {
		return rawURL, meta, nil
	}
	rest := rawURL[i+len("/proxy/"):]
	path, query := rest, ""
	if j := strings.IndexByte(rest, '?'); j >= 0 {
		path, query = rest[:j], rest[j+1:]
	}
	t, m, ok := h.resolveTarget(path, query, "")
	if !ok {
		return rawURL, meta, errors.New("token decrypt failed")
	}
	return t, m, nil
}

// ProbeStream walks a stream the way a PLAYER would — manifest, then a variant,
// then the first segment — through the exact transport and headers the proxy
// uses. This is the difference between "the source answers" and "the source
// plays": every source outage we have shipped through kept answering 200 on the
// manifest while the segment fetch 403'd, emptied out, or timed out.
//
// Same caveat as ProbeUpstream: authoritative for proxied sources, indicative
// for no_stream_proxy ones (the client fetches those from its own IP).
func (h *Handler) ProbeStream(ctx context.Context, rawURL, plugin string, headers map[string]string) (hlsprobe.Result, string, bool) {
	target, meta, err := h.probeTarget(rawURL, plugin, headers)
	if err != nil {
		return hlsprobe.Result{Stage: hlsprobe.StageManifest, Err: err.Error()}, rawURL, false
	}
	viaProxy := httpclient.TransportForBalancer(meta.plugin) != nil

	fetch := func(ctx context.Context, u, rangeHdr string) (int, []byte, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return 0, nil, "", err
		}
		if rangeHdr != "" {
			req.Header.Set("Range", rangeHdr)
		}
		resp, base, err := h.fetchWithMeta(u, req, meta)
		if err != nil {
			return 0, nil, "", err
		}
		defer resp.Body.Close()
		// Cap the read: a probe that accidentally pulls a whole 4K segment is a
		// bandwidth bill on every health check.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		final := u
		if base != nil {
			final = base.String()
		}
		return resp.StatusCode, body, final, nil
	}

	res := hlsprobe.Probe(ctx, target, fetch)
	return res, target, viaProxy
}

func (h *Handler) fetchWithMeta(target string, in *http.Request, meta linkMeta) (*http.Response, *url.URL, error) {
	// Per-plugin URL transforms applied BEFORE we open the upstream
	// request. zona/TAKEDWN segments, for instance, must be re-routed
	// through the CDN's /x-en-x/<cipher> path or the server returns 410.
	target = applyPreFetchRewrite(target, meta)

	u, err := url.Parse(target)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(in.Context(), in.Method, u.String(), in.Body)
	if err != nil {
		return nil, nil, err
	}
	// CDNVideoHub (okcdn.ru) uses DDoS-Guard which rejects requests with
	// unusual headers. Send only minimal headers for these requests.
	if isCleanHeaderPlugin(meta.plugin) {
		// Start with clean headers — only essentials.
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "*/*")
		req.Host = u.Host
		req.Header.Set("Host", u.Host)
		// Forward Range header for partial content (video segments).
		if rng := in.Header.Get("Range"); rng != "" {
			req.Header.Set("Range", rng)
		}
		// Apply custom upstream headers from proxylink payload.
		for k, v := range meta.headers {
			if isZonaCipherHeader(k) {
				continue
			}
			req.Header.Set(k, v)
		}
	} else {
		copyHeaders(req.Header, in.Header)
		// Never relay the end-user's IP to upstream CDNs. The front-end nginx
		// stamps X-Forwarded-For / X-Real-IP = the real client IP onto the
		// inbound request and copyHeaders faithfully relays it. Besides the
		// privacy / shared-playlist ban risk, some CDNs bind their signed
		// tokens to the IP they see at manifest-fetch time: HDVB's
		// sevstar933krop edge mints the .ts md5 against the forwarded client
		// IP, so the later segment fetch (server IP, no XFF) 403s on EVERY
		// segment. Present only this server's own egress IP. Plugins that need
		// a specific X-Forwarded-For (e.g. rezka = resolver IP) re-set it below.
		stripClientIdentityHeaders(req.Header)
		// Obrut CDN breaks when browser-side lampac cookies leak upstream
		// (e.g. stale turbo_player_session). For zetflix/videodb requests, strip incoming
		// cookies and rely on explicit proxylink headers (meta.headers["Cookie"]).
		if isObrutCDNPlugin(meta.plugin) {
			req.Header.Del("Cookie")
		}
		req.Header.Set("X-Lampac-Go", "1")
		req.Host = u.Host
		req.Header.Set("Host", u.Host)

		// Strip browser-injected Sec-Fetch-* headers. These are auto-added by
		// the browser (e.g. Sec-Fetch-Mode: no-cors, Sec-Fetch-Site: same-origin)
		// and cannot be forged by JavaScript. When proxying to upstream CDNs,
		// these leak the proxy origin and cause CDNs like VK to reject with 400.
		for k := range req.Header {
			if strings.HasPrefix(k, "Sec-Fetch-") || strings.HasPrefix(k, "sec-fetch-") {
				req.Header.Del(k)
			}
			// CMCD-* is the player's telemetry for US. Relaying it to the CDN
			// buys nothing (these edges do no CMCD-aware routing for our
			// traffic) and costs real risk: strict origins reject requests
			// carrying unexpected headers outright, which surfaces as a 403 on
			// a stream that worked a moment ago.
			if strings.HasPrefix(k, "CMCD-") || strings.HasPrefix(k, "Cmcd-") || strings.HasPrefix(k, "cmcd-") {
				req.Header.Del(k)
			}
		}

		// Ensure a browser-like User-Agent is always sent upstream.
		if ua := req.Header.Get("User-Agent"); ua == "" || strings.HasPrefix(ua, "Go-http-client") {
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
		}

		// Apply custom upstream headers from proxylink payload (override
		// client headers). X-Zona-Cipher-* are internal pseudo-headers —
		// they carry cipher state between the balancer and this handler
		// and must never leak to the CDN.
		for k, v := range meta.headers {
			if isZonaCipherHeader(k) {
				continue
			}
			req.Header.Set(k, v)
		}
	}

	// Inject live edge_hash as pc_hash AND Accepts-Controls for CDN auth.
	// CDN expects Accepts-Controls = pc_hash = fresh edge_hash from WS.
	// The proxylink payload may contain a stale Accepts-Controls from resolve
	// time; override it with the live value. For .ts segments, CDN doesn't
	// need Authorizations or Borth — only Accepts-Controls + pc_hash.
	if h.EdgeHashLookup != nil && isCDNPlugin(meta.plugin) {
		if hash := h.EdgeHashLookup(""); hash != "" {
			req.Header["pc_hash"] = []string{hash}
			req.Header.Set("Accepts-Controls", hash)
		}
		// Segments (.ts/.m4s) don't need Authorizations/Borth — only m3u8 does.
		// Sending stale Authorizations on segments can trigger 403.
		path := strings.ToLower(req.URL.Path)
		if strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".m4s") {
			req.Header.Del("Authorizations")
			req.Header.Del("Authorization")
			req.Header.Del("Borth")
		}
	}
	if isRezkaPlugin(meta.plugin) {
		pidorezkaHost := strings.TrimSpace(h.pidorezkaHost)
		if pidorezkaHost == "" {
			pidorezkaHost = "https://hdrzk.org"
		}
		pidorezkaHost = strings.TrimRight(pidorezkaHost, "/")

		if isVoidboostCDNURL(u) {
			// Voidboost CDN: send CLEAN browser-like headers only.
			// Voidboost CDN: send CLEAN browser-like headers only.
			// Do NOT send rezka auth cookies, app headers, or X-Forwarded-For
			// — these mark the request as automated/proxy and the CDN may reject it.
			req.Header.Del("Cookie")
			req.Header.Del("X-Forwarded-For")
			req.Header.Del("X-Hdrezka-Android-App")
			req.Header.Del("X-Hdrezka-Android-App-Version")
			req.Header.Set("Accept", "*/*")
			req.Header.Set("Origin", pidorezkaHost)
			req.Header.Set("Referer", pidorezkaHost+"/")
			req.Host = u.Host
		} else {
			// Rezka site requests: full header set with auth cookies.
			req.Header.Del("Cookie")
			req.Header.Set("Accept", "*/*")
			req.Header.Set("Cache-Control", "no-cache")
			req.Header.Set("Pragma", "no-cache")
			req.Header.Set("DNT", "1")
			req.Header.Set("Origin", pidorezkaHost)
			req.Header.Set("Referer", pidorezkaHost+"/")
			req.Header.Set("Sec-Fetch-Dest", "empty")
			req.Header.Set("Sec-Fetch-Mode", "cors")
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			req.Header.Set("X-Hdrezka-Android-App", "1")
			req.Header.Set("X-Hdrezka-Android-App-Version", "2.2.5")
			req.Host = u.Host
			if meta.reqIP != "" {
				req.Header.Set("X-Forwarded-For", meta.reqIP)
			}
			// Rezka site: apply auth cookies
			applyDynamicPluginHeaders(meta.plugin, req.Header)
		}
	} else {
		applyDynamicPluginHeaders(meta.plugin, req.Header)
	}
	// PornHub CDN (phncdn) may return 404 for hotlink-like segment requests
	// without PornHub referer/origin. Force stable browser-like headers.
	if isPornhubCDNHost(u.Host) {
		req.Header.Set("Referer", "https://www.pornhub.com/")
		req.Header.Set("Origin", "https://www.pornhub.com")
		req.Header.Set("Cookie", "platform=pc; accessAgeDisclaimerPH=1")
	}
	// Videoseed CDN (storage.videoseedcdn.com): send CLEAN browser-like headers.
	// The CDN uses TLS fingerprinting and rejects requests with non-browser
	// markers like X-Lampac-Go, unknown cookies, or X-Forwarded-For.
	// Probe confirmed: clean uTLS request → 302, dirty proxy request → 404.
	if strings.EqualFold(meta.plugin, "videoseed") {
		req.Header.Del("Cookie")
		req.Header.Del("X-Lampac-Go")
		req.Header.Del("X-Forwarded-For")
		req.Header.Set("Accept", "*/*")
		req.Host = u.Host
	}
	// cdn.fancdn.net behaves like the videoseed CDN: it 404s any request that
	// carries proxy/auth markers — the player's Lampa cookies, X-Lampac-Go,
	// X-Forwarded-For. Verified on a single host: a clean curl/uTLS fetch of the
	// exact signed URL = 200, but the proxied request (which forwards those
	// headers) = 404. Strip them so /proxy/ looks like the plain browser fetch.
	if strings.EqualFold(meta.plugin, "fancdn") {
		req.Header.Del("Cookie")
		req.Header.Del("X-Lampac-Go")
		req.Header.Del("X-Forwarded-For")
		req.Header.Set("Accept", "*/*")
		req.Host = u.Host
	}
	// videostorage.xyz family (api.femd.ws + cdnr.interkh.com / x-bc.interkh.com):
	// .ts segments return 410 without a femd-domain Referer, even when m3u8
	// playlists return 200. Strip our auth cookies that leak upstream, set
	// browser-like Referer/Origin. Probe confirmed: same SOCKS5 exit + same
	// Chrome TLS, plus Referer = 200; without Referer = 410.
	if p := strings.ToLower(meta.plugin); p == "femd" || p == "kinobadi" {
		req.Header.Del("Cookie")
		req.Header.Del("X-Lampac-Go")
		req.Header.Del("X-Forwarded-For")
		req.Header.Set("Referer", "https://api.femd.ws/")
		req.Header.Set("Origin", "https://api.femd.ws")
		req.Header.Set("Accept", "*/*")
		req.Host = u.Host
	}
	// IPTV: the upstream is a paid playlist provider that BANS playlists it detects as
	// "shared" — the tell-tale being many distinct client IPs (or mismatched fingerprints)
	// on one account. The general copyHeaders path above faithfully relays whatever the
	// front-end nginx stamped on the inbound request (X-Real-IP / X-Forwarded-For = the
	// end-user's real IP) straight to the CDN. Strip every client-IP-bearing header so the
	// provider only ever sees THIS server's single egress IP — i.e. one device.
	if isIPTVPlugin(meta.plugin) {
		stripClientIdentityHeaders(req.Header)
		applyIPTVHeaderPolicy(req.Header, meta.headers)
	}
	if isObrutCDNPlugin(meta.plugin) && isZetflixObrutCDNURL(u) {
		// Obrut CDN stream endpoint is a redirect resolver; byte ranges here often
		// cause 404 and are not needed on this hop.
		req.Header.Del("Range")
		req.Header.Set("Sec-Fetch-Dest", "empty")
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("Sec-Fetch-Site", "same-site")
	}

	// Use proxied client for configured balancers (routes through VLESS/SOCKS5 proxy).
	// Multi-proxy: look up per-balancer transport first, fall back to legacy proxiedClient.
	// Clients are cached per balancer name to avoid allocation per request.
	client := h.client
	// Check proxy registry at runtime (not the startup snapshot) so hot-reloaded
	// VLESS entries are picked up immediately without server restart.
	//
	// Also honor a balancer that registered a custom RoundTripper (e.g. fancdn's
	// IPv4-pinned uTLS transport) even with no SOCKS5: its CDN (cdn.fancdn.net)
	// rejects Go's stock TLS fingerprint with 404, so /proxy/ MUST reuse the same
	// uTLS transport that signed the URL via /film.php — otherwise the segment
	// fetch is both a different fingerprint and a different egress.
	if plugin := strings.ToLower(meta.plugin); h.proxiedPlugins[plugin] || httpclient.IsBalancerProxied(plugin) || isCDNPlugin(meta.plugin) || httpclient.RoundTripperForBalancer(plugin) != nil {
		now := time.Now().Unix()
		if cached, ok := h.balancerClients.Load(plugin); ok {
			entry := cached.(*balancerClientEntry)
			atomic.StoreInt64(&entry.lastUsed, now)
			client = entry.client
		} else if isCDNPlugin(meta.plugin) {
			// CDN plugins (mirage/alloha/aladdin) need full Chrome HTTP/2
			// fingerprint via tls-client to avoid CDN bot detection.
			// Priority: SOCKS5 → HTTP proxy (V2Box) → direct.
			socksAddr := httpclient.SocksAddrForLabel(plugin)
			if socksAddr == "" {
				socksAddr = httpclient.SocksAddrForBalancer(plugin)
			}
			var c *http.Client
			if socksAddr != "" {
				log.Debug().Str("plugin", plugin).Str("socks", socksAddr).Msg("proxy: CDN plugin using SOCKS5")
				c = httpclient.NewTLSClientViaSOCKS5(socksAddr, 35*time.Second)
			} else if h.cdnHTTPProxy != "" {
				// V2Box/Clash HTTP proxy — proven to work with CDN
				// (same proxy the iframe mode uses successfully).
				proxyURL, _ := url.Parse(h.cdnHTTPProxy)
				if proxyURL != nil {
					log.Info().Str("plugin", plugin).Str("proxy", h.cdnHTTPProxy).Msg("proxy: CDN plugin using HTTP proxy (V2Box)")
					c = &http.Client{
						Transport: &http.Transport{
							Proxy:                 http.ProxyURL(proxyURL),
							TLSHandshakeTimeout:   10 * time.Second,
							ResponseHeaderTimeout: 30 * time.Second,
							MaxIdleConns:          10,
							IdleConnTimeout:       90 * time.Second,
							DisableCompression:    true,
						},
						Timeout:       35 * time.Second,
						CheckRedirect: noRedirectFunc,
					}
				}
			}
			if c == nil {
				log.Warn().Str("plugin", plugin).Msg("proxy: CDN plugin using direct (no proxy)")
				c = httpclient.NewTLSClientDirect(35 * time.Second)
			}
			h.balancerClients.Store(plugin, &balancerClientEntry{client: c, lastUsed: now})
			client = c
		} else if t := httpclient.UTLSTransportForBalancer(plugin); t != nil {
			// uTLS transport with Chrome TLS fingerprint via SOCKS5.
			// Now properly handles both HTTP/2 and HTTP/1.1 based on ALPN
			// negotiation. CDNs like storage.videoseedcdn.com (nginx/1.18.0,
			// HTTP/1.1 only) that fingerprint TLS need Chrome fingerprint but
			// HTTP/1.1 protocol — the uTLS round tripper auto-detects this.
			c := &http.Client{
				Transport:     t,
				Timeout:       35 * time.Second,
				CheckRedirect: noRedirectFunc,
			}
			h.balancerClients.Store(plugin, &balancerClientEntry{client: c, lastUsed: now})
			client = c
		} else if t := httpclient.TransportForBalancer(plugin); t != nil {
			// Standard SOCKS5 transport (no uTLS) — fallback for balancers
			// without uTLS SOCKS5 configured.
			c := &http.Client{
				Transport:     t,
				Timeout:       35 * time.Second,
				CheckRedirect: noRedirectFunc,
			}
			h.balancerClients.Store(plugin, &balancerClientEntry{client: c, lastUsed: now})
			client = c
		} else if h.proxiedClient != nil {
			client = h.proxiedClient
		}
	}
	if isObrutCDNPlugin(meta.plugin) && isZetflixObrutCDNURL(u) {
		if resolved := h.resolveZetflixCDNRedirect(in.Context(), client, u, req.Header); resolved != "" {
			if ru, err := url.Parse(resolved); err == nil {
				u = ru
				req.URL = ru
				req.Host = ru.Host
				req.Header.Set("Host", ru.Host)
			}
		} else {
			// If redirect couldn't be resolved explicitly, try direct HLS manifest
			// variant instead of raw /stream/... URL.
			variants := zetflixCDNVariants(u.String())
			if len(variants) > 0 && variants[0] != u.String() {
				if ru, err := url.Parse(variants[0]); err == nil {
					u = ru
					req.URL = ru
					req.Host = ru.Host
					req.Header.Set("Host", ru.Host)
				}
			}
		}
	}

	// Detailed header logging for CDN plugins to trace 403 issues.
	if isCDNPlugin(meta.plugin) {
		hdrs := make(map[string]string)
		for k, v := range req.Header {
			if len(v) > 0 {
				val := v[0]
				if len(val) > 60 {
					val = val[:60] + "..."
				}
				hdrs[k] = val
			}
		}
		log.Debug().
			Str("plugin", meta.plugin).
			Str("method", req.Method).
			Str("url", req.URL.String()[:min(len(req.URL.String()), 120)]).
			Str("host", req.Host).
			Interface("headers", hdrs).
			Msg("proxy: CDN upstream request")
	}

	// Debug vibix CDN routing.
	if strings.ToLower(meta.plugin) == "vibix" {
		log.Info().
			Str("upstream", req.URL.Host+req.URL.Path[:min(len(req.URL.Path), 80)]).
			Str("plugin", meta.plugin).
			Msg("vibix: proxy upstream request")
	}

	// IPTV: живой эфир бывает ОДНИМ бесконечным HTTP-ответом (raw MPEG-TS,
	// Xtream-панели) — общий client.Timeout=35s рубил такой канал ровно на
	// 35-й секунде. Меняем клиент на безлимитный: заголовки страхует таймер на
	// контексте, тело — stall-watchdog (ни байта streamStallTimeout → Close).
	var armStreamGuard func(*http.Response)
	if isIPTVPlugin(meta.plugin) {
		switch {
		case client == h.client:
			client = h.streamClient
		case client.Timeout != 0:
			// IPTV через прокси (proxycore/SOCKS5): клиент тут НЕ h.client, и
			// раньше подмена не срабатывала — проксированный живой эфир рвался
			// на 35-й секунде. Берём тот же транспорт (пул соединений общий),
			// снимая только общий таймаут; заголовки и стоп-кадр по-прежнему
			// страхуют hdrTimer и stall-watchdog ниже.
			noTimeout := *client
			noTimeout.Timeout = 0
			client = &noTimeout
		}
		guardCtx, guardCancel := context.WithCancel(in.Context())
		hdrTimer := time.AfterFunc(streamHeaderTimeout, guardCancel)
		req = req.Clone(guardCtx)
		armStreamGuard = func(resp *http.Response) {
			hdrTimer.Stop()
			if resp == nil {
				guardCancel()
				return
			}
			resp.Body = newStallWatchdogBody(resp.Body, streamStallTimeout, guardCancel)
		}
	}

	upSince := time.Now() // TTFB апстрима для X-Alpac-Up-Ms (stampDiag на каждом успешном выходе)
	resp, err := client.Do(req)
	if armStreamGuard != nil {
		if err != nil {
			armStreamGuard(nil)
		} else {
			armStreamGuard(resp)
		}
	}
	if err != nil {
		return nil, nil, err
	}

	// remux (Megaoblako/cloud.mail.ru): the weblink_view URL 301-redirects to a
	// fresh signed datacloudmail.ru URL on every request. The shared proxy client
	// uses noRedirectFunc (HLS sub-URL rewriting relies on the original base), so
	// follow the hop(s) manually here — forwarding all headers incl. Range so
	// seeking keeps working — and stream the final file through the proxy instead
	// of leaking a cross-origin 301 to the browser.
	if strings.EqualFold(meta.plugin, "remux") {
		for hops := 0; hops < 5 && isHTTPRedirect(resp.StatusCode); hops++ {
			loc := strings.TrimSpace(resp.Header.Get("Location"))
			if loc == "" {
				break
			}
			next, perr := u.Parse(loc)
			if perr != nil {
				break
			}
			req2, err2 := http.NewRequestWithContext(in.Context(), http.MethodGet, next.String(), nil)
			if err2 != nil {
				break
			}
			for k, vals := range req.Header {
				for _, v := range vals {
					req2.Header.Add(k, v)
				}
			}
			req2.Host = next.Host
			resp.Body.Close()
			resp2, err2 := client.Do(req2)
			if err2 != nil {
				return nil, nil, err2
			}
			resp, u, req = resp2, next, req2
		}
	}

	// veoveo: часть вариантов вместо плейлиста отдаёт 200 и JSON вида
	// {"sources":[{"link":<content-router>,"isDefault":..}]}, а сам плейлист
	// лежит за 307 внутри link. Плееру такой ответ не годится — он говорит
	// «источник отказал» и роняет просмотр (инцидент 0903-232615). Идём по
	// ссылке сами и отдаём уже плейлист; базой для относительных сегментов
	// станет конечный адрес (возвращаемый u), см. вызов fetchWithMeta.
	if strings.EqualFold(meta.plugin, "veoveo") && resp.StatusCode >= 200 && resp.StatusCode < 300 &&
		strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "json") {
		peek, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if rerr != nil {
			return nil, nil, rerr
		}
		link := pickSourceLink(peek)
		if link == "" {
			// Не наш формат — отдаём как было, пусть возврат на primary/плеер решают.
			resp.Body = io.NopCloser(bytes.NewReader(peek))
		} else {
			cur, perr := u.Parse(link)
			if perr != nil {
				return nil, nil, perr
			}
			var resp2 *http.Response
			for hops := 0; hops < 6; hops++ {
				req2, err2 := http.NewRequestWithContext(in.Context(), http.MethodGet, cur.String(), nil)
				if err2 != nil {
					return nil, nil, err2
				}
				for k, vals := range req.Header {
					for _, v := range vals {
						req2.Header.Add(k, v)
					}
				}
				req2.Host = cur.Host
				r2, e2 := client.Do(req2)
				if e2 != nil {
					return nil, nil, e2
				}
				if isHTTPRedirect(r2.StatusCode) {
					loc := strings.TrimSpace(r2.Header.Get("Location"))
					r2.Body.Close()
					next, lerr := cur.Parse(loc)
					if loc == "" || lerr != nil {
						return nil, nil, fmt.Errorf("veoveo: bad redirect from source-list link")
					}
					cur = next
					continue
				}
				resp2, req = r2, req2
				break
			}
			if resp2 == nil {
				return nil, nil, fmt.Errorf("veoveo: too many redirects behind source-list link")
			}
			log.Info().Str("final", truncStr(cur.String(), 120)).Msg("veoveo: source-list followed to playlist")
			resp, u = resp2, cur
		}
	}

	// Debug vibix CDN response.
	if strings.ToLower(meta.plugin) == "vibix" {
		log.Info().
			Int("status", resp.StatusCode).
			Str("upstream", req.URL.Host).
			Str("ct", resp.Header.Get("Content-Type")).
			Msg("vibix: proxy upstream response")
	}

	// Log CDN response details.
	if isCDNPlugin(meta.plugin) {
		respHdrs := make(map[string]string)
		for _, k := range []string{"Content-Type", "Content-Length", "Accept-Ranges",
			"Access-Control-Allow-Origin", "Access-Control-Allow-Headers"} {
			if v := resp.Header.Get(k); v != "" {
				respHdrs[k] = v
			}
		}
		log.Debug().
			Str("plugin", meta.plugin).
			Int("status", resp.StatusCode).
			Interface("resp_headers", respHdrs).
			Msg("proxy: CDN upstream response")
	}

	if isObrutCDNPlugin(meta.plugin) && resp.StatusCode == http.StatusNotFound {
		for _, alt := range zetflixCDNVariants(req.URL.String()) {
			if strings.TrimSpace(alt) == "" || alt == req.URL.String() {
				continue
			}
			req2, err2 := http.NewRequestWithContext(in.Context(), in.Method, alt, nil)
			if err2 != nil {
				continue
			}
			for k, vals := range req.Header {
				for _, v := range vals {
					req2.Header.Add(k, v)
				}
			}
			altBase := u
			if au, err2 := url.Parse(alt); err2 == nil {
				req2.Host = au.Host
				req2.Header.Set("Host", au.Host)
				altBase = au
				if isZetflixObrutCDNURL(au) || isZetflixSuperduperURL(au) {
					req2.Header.Del("Range")
				}
			}
			resp2, err2 := client.Do(req2)
			if err2 != nil {
				continue
			}
			if resp2.StatusCode != http.StatusNotFound {
				resp.Body.Close()
				stampDiag(resp2, upSince, h.nodeLabel, meta.plugin)
				return resp2, altBase, nil
			}
			resp2.Body.Close()
		}
	}
	// Voidboost CDN retry: when a voidboost stream URL returns 404,
	// try other known subdomains. Similar to zetflix CDN fallback above.
	if isRezkaPlugin(meta.plugin) && resp.StatusCode == http.StatusNotFound && isVoidboostCDNURL(u) {
		alts := voidboostCDNAlternatives(req.URL.String())
		if len(alts) > 0 {
			log.Debug().Str("url", truncStr(req.URL.String(), 120)).Int("alts", len(alts)).Msg("proxy: voidboost 404, trying alternative subdomains")
		}
		for _, alt := range alts {
			if strings.TrimSpace(alt) == "" || alt == req.URL.String() {
				continue
			}
			req2, err2 := http.NewRequestWithContext(in.Context(), in.Method, alt, nil)
			if err2 != nil {
				continue
			}
			for k, vals := range req.Header {
				for _, v := range vals {
					req2.Header.Add(k, v)
				}
			}
			altBase := u
			if au, err2 := url.Parse(alt); err2 == nil {
				req2.Host = au.Host
				req2.Header.Set("Host", au.Host)
				altBase = au
			}
			resp2, err2 := client.Do(req2)
			if err2 != nil {
				continue
			}
			if resp2.StatusCode != http.StatusNotFound {
				log.Info().Str("alt", truncStr(alt, 120)).Int("status", resp2.StatusCode).Msg("proxy: voidboost fallback succeeded")
				resp.Body.Close()
				stampDiag(resp2, upSince, h.nodeLabel, meta.plugin)
				return resp2, altBase, nil
			}
			resp2.Body.Close()
		}
	}
	stampDiag(resp, upSince, h.nodeLabel, meta.plugin)
	return resp, u, nil
}

// relayVerbatim отдаёт ответ апстрима без изменений. head — уже вычитанные байты
// (может быть nil), они уходят перед остатком тела.
func relayVerbatim(w http.ResponseWriter, resp *http.Response, head []byte) {
	copyHeadersFiltered(w.Header(), resp.Header)
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	w.WriteHeader(resp.StatusCode)
	if len(head) > 0 {
		if _, err := w.Write(head); err != nil {
			return
		}
	}
	_, _ = io.Copy(w, resp.Body)
}

// firstLineWith возвращает первую строку, содержащую подстроку — для образца в логе.
func firstLineWith(src, needle string) string {
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// looksLikePlaylist reports whether a body is an HLS playlist. RFC 8216 требует
// первой строкой #EXTM3U, поэтому проверка дешёвая и надёжная; допускаются лишь
// BOM и ведущие пробелы, которые встречаются у части CDN.
// gunzipUnsolicited распаковывает плейлист, который источник сжал БЕЗ запроса
// (мы шлём Accept-Encoding: identity, и Go такой ответ сам не распаковывает).
// Так делает заглушка cinerama (`/blocked/index.m3u8`, 175 байт `1f 8b`): без
// распаковки бинарник уезжал зрителю как плейлист, и mpv/ExoPlayer падали с
// «unrecognized file format». Сегменты не трогаем — только текст плейлистов.
func gunzipUnsolicited(body []byte, resp *http.Response) []byte {
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		return body
	}
	if resp != nil && resp.Uncompressed {
		return body
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return body
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, 4<<20))
	if err != nil || len(out) == 0 {
		return body
	}
	return out
}

func looksLikePlaylist(body []byte) bool {
	b := bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})
	return bytes.HasPrefix(bytes.TrimLeft(b, " \t\r\n"), []byte("#EXTM3U"))
}

func (h *Handler) handleM3U(w http.ResponseWriter, r *http.Request, resp *http.Response, meta linkMeta, preTarget string) {
	// Cache key: upstream URL + plugin (different plugins rewrite differently).
	cacheKey := resp.Request.URL.String() + "|" + meta.plugin

	// Апстрим отказал по лимиту или своей аварии, а у нас есть свежий удачный манифест
	// ЭТОГО же канала — отдаём его вместо ошибки.
	//
	// Зачем: у живых IPTV-источников лимит считается на IP, и весь наш зал зрителей уходит
	// к ним с ОДНОГО адреса main. Замерено 2026-09-08 на stream.mcquack.net: с main проходит
	// 4 запроса за 8 секунд, пятый — 429, при этом с любой ноды (другой IP) сразу 200. Отказ
	// доезжал до плеера как ERROR_CODE_IO_BAD_HTTP_STATUS, зритель жал «повторить», и это
	// давало новый запрос — лимит не отпускал никогда: за сутки 433 обращения к этому хосту
	// и 433 отказа, а каналы Okko дали треть всех жалоб на просадки в netdiag.
	//
	// Подменённый манифест кладём и в основной кэш на пару секунд: тогда следующие зрители
	// обслуживаются из него ДО похода наверх (предвыборка в HandleProxy смотрит туда же), и
	// наша частота запросов сама опускается под лимит источника.
	if isIPTVPlugin(meta.plugin) && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) {
		if stale, ok := m3uLastGoodGet(cacheKey); ok {
			_ = resp.Body.Close()
			noteM3UStale(meta.plugin, cacheKey, resp.StatusCode)
			serve := stale
			serve.statusCode = http.StatusOK
			m3uCacheSetTTL(cacheKey, serve, 2*time.Second)
			if preTarget != "" {
				if preKey := preTarget + "|" + meta.plugin; preKey != cacheKey {
					m3uCacheSetTTL(preKey, serve, 2*time.Second)
				}
			}
			for k, vs := range serve.headers {
				for _, v := range vs {
					w.Header().Set(k, v)
				}
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(serve.body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(serve.body)
			return
		}
	}

	// Check cache first — saves both upstream fetch AND regex rewrite.
	if cached, ok := m3uCacheGet(cacheKey); ok {
		_ = resp.Body.Close()
		for k, vs := range cached.headers {
			for _, v := range vs {
				w.Header().Set(k, v)
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(cached.body)))
		w.WriteHeader(cached.statusCode)
		_, _ = w.Write(cached.body)
		return
	}

	// Крупное тело — это НЕ плейлист. Сюда попадают и медиа-сегменты: выбор ветки
	// делается по подстроке ".m3u" в пути, а у HLS-сегментов YouTube путь выглядит
	// как «…/file/index.m3u8/sq/7/…». Раньше такой сегмент отбивался 503 «bigfile»
	// и воспроизведение вставало. Плейлисты столько не весят, поэтому отдаём как
	// есть, ничего не переписывая.
	if resp.ContentLength > h.maxLengthM3U && resp.ContentLength > 0 {
		relayVerbatim(w, resp, nil)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, h.maxLengthM3U+1))
	body = gunzipUnsolicited(body, resp)
	if err != nil {
		http.Error(w, "error array m3u8", http.StatusServiceUnavailable)
		return
	}
	if int64(len(body)) > h.maxLengthM3U {
		// Размер узнали только по факту чтения (апстрим не прислал Content-Length):
		// уже прочитанное отдаём вместе с остатком, иначе получился бы обрезанный файл.
		relayVerbatim(w, resp, body)
		return
	}

	// Сюда попадают и НЕ-плейлисты: выбор делается по подстроке ".m3u" в пути или
	// в апстрим-URL, а у HLS-сегментов YouTube путь выглядит как
	// «…/file/index.m3u8/sq/7/…» — то есть содержит .m3u8, оставаясь двоичным
	// видео. Переписывание такого тела вставляло прокси-ссылки прямо внутрь
	// MP4/TS: ffmpeg потом ругался «Invalid data found when processing input»,
	// а плеер не находил кодек («Video: none, none»).
	//
	// Плейлист по RFC 8216 обязан начинаться с #EXTM3U, поэтому решаем по
	// СОДЕРЖИМОМУ, а не по имени: не плейлист — отдаём байт в байт как есть.
	if !looksLikePlaylist(body) {
		// Единственный оставшийся путь, где наружу может уйти НЕпереписанный
		// плейлист: тело не опознано как плейлист и отдаётся как есть. Для
		// медиа-сегмента это правильно, но если внутри лежат ссылки на CDN —
		// значит опознание промахнулось, и клиент сейчас получит CORS.
		if bytes.Contains(body, []byte("googlevideo.com")) {
			head := body
			if len(head) > 80 {
				head = head[:80]
			}
			log.Warn().Int("байт", len(body)).Str("plugin", meta.plugin).
				Str("начало", truncStr(string(head), 80)).
				Str("образец", truncStr(firstLineWith(string(body), "googlevideo.com"), 160)).
				Msg("proxy: тело не опознано как плейлист, но содержит ссылки на CDN — отдаю как есть")
		}
		copyHeadersFiltered(w.Header(), resp.Header)
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	rewritten := h.rewriteM3U(string(body), r, meta)
	// После переписывания прямых googlevideo-ссылок остаться не должно: у CDN нет
	// Access-Control-Allow-Origin, и браузер упрётся в CORS. Если что-то уцелело —
	// значит строка не подошла под reHTTPLinks; печатаем образец, иначе причину
	// не восстановить (в логах виден только запрос клиента, уже ушедший мимо нас).
	if n := strings.Count(rewritten, "googlevideo.com"); n > 0 {
		log.Warn().Int("осталось", n).Str("plugin", meta.plugin).
			Str("образец", truncStr(firstLineWith(rewritten, "googlevideo.com"), 200)).
			Msg("proxy: в переписанном плейлисте остались прямые ссылки на CDN")
	}
	out := []byte(rewritten)
	copyHeadersFiltered(w.Header(), resp.Header)
	// Always force correct Content-Type for m3u8. Some CDNs return text/html
	// for m3u8 content, which confuses HLS players.
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	// Always set Content-Length for rewritten m3u8 — native players need it.
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	// Cache the rewritten m3u8 for concurrent viewers. Live IPTV manifests roll over every
	// target-duration (~4-6s) — the default 8s TTL would hand players the SAME manifest twice
	// in a row and stall the live edge, so cache those much shorter.
	// Only cache SUCCESSFUL playlists. A CDN that gates on IP/UA/Referer returns its "denied"
	// page as content-type m3u8 with a 403 (filmix's werkecdn does exactly this) — caching that
	// served the rewritten error body to every later viewer for the whole TTL, so a single
	// transient block looked like a hard outage. Errors must always re-hit the upstream.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		hdr := make(http.Header)
		hdr.Set("Content-Type", "application/vnd.apple.mpegurl")
		ttl := m3uCacheTTL
		if isIPTVPlugin(meta.plugin) && !strings.Contains(rewritten, "#EXT-X-ENDLIST") {
			ttl = 2 * time.Second
		}
		entry := m3uCacheEntry{body: out, statusCode: resp.StatusCode, headers: hdr}
		m3uCacheSetTTL(cacheKey, entry, ttl)
		// Копия на случай отказа апстрима: живёт дольше основного кэша (см. m3uLastGood).
		if isIPTVPlugin(meta.plugin) {
			m3uLastGoodSet(cacheKey, entry)
		}
		// Also cache under the PRE-redirect target: HandleProxy's pre-fetch lookup uses it, letting
		// concurrent viewers skip the upstream round-trip entirely (the entry body is shared, not copied).
		if preTarget != "" {
			if preKey := preTarget + "|" + meta.plugin; preKey != cacheKey {
				m3uCacheSetTTL(preKey, entry, ttl)
			}
		}
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
}

func (h *Handler) handleMPD(w http.ResponseWriter, r *http.Request, resp *http.Response, meta linkMeta) {
	if resp.ContentLength > h.maxLengthM3U && resp.ContentLength > 0 {
		http.Error(w, "bigfile", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, h.maxLengthM3U+1))
	body = gunzipUnsolicited(body, resp)
	if err != nil {
		http.Error(w, "error array mpd", http.StatusServiceUnavailable)
		return
	}
	if int64(len(body)) > h.maxLengthM3U {
		http.Error(w, "bigfile", http.StatusServiceUnavailable)
		return
	}

	mpd := string(body)
	mpd = reBaseURLTag.ReplaceAllStringFunc(mpd, func(match string) string {
		m := reBaseURLTag.FindStringSubmatch(match)
		if len(m) < 2 {
			return match
		}
		base := m[1]
		enc := h.encrypt(base, meta, true, false)
		// sid едет в ПУТИ, а не в query: плеер клеит имя сегмента прямо к
		// BaseURL, и "?sid=X" слилось бы с именем файла в мусор.
		if meta.sid != "" {
			enc += dashSIDSep + url.PathEscape(meta.sid)
		}
		return strings.Replace(match, base, h.streamHostFromRequest(r)+"/proxy-dash/"+enc+"/", 1)
	})
	// Живой DASH сплошь и рядом идёт БЕЗ <BaseURL>, а пути сегментов лежат
	// относительными прямо в SegmentTemplate (Первый канал: media="../../../
	// dash-live2/…$Number%09d$.mp4"). Без правки плеер резолвит их относительно
	// НАШЕГО /proxy/<hash> и уходит в никуда. Делаем их абсолютными на
	// вещателя: манифест остаётся проксированным (у него нет CORS), а сегменты
	// плеер берёт напрямую — там CORS отдают, и наш трафик на них не тратится.
	mpd = absolutizeMPDPaths(mpd, meta.baseURI)

	out := []byte(mpd)
	copyHeadersFiltered(w.Header(), resp.Header)
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/dash+xml")
	}
	// Always set Content-Length for rewritten MPD — native players need it.
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
}

func (h *Handler) rewriteM3U(src string, r *http.Request, meta linkMeta) string {
	proxyHost := h.streamHostFromRequest(r) + "/proxy"
	edgeHintOf := EdgeHintFrom(r)      // выбор ноды зрителем едет и в сегменты (см. WithEdge)
	edgeSkipOf := EdgeSkipEffective(r) // отключённые зрителем ноды — туда же
	hlsHost := extractHLSHost(meta.baseURI)
	hlsPatch := extractHLSPatch(meta.baseURI)

	// filmix /hls playlists carry ?hash= on every segment line but NOT on the
	// EXT-X-MAP init URI — the CDN 403s the hashless init fetch. Only fMP4 rips
	// (HDR/HEVC) have an init segment, which is why SDR (TS) plays while HDR
	// dies with 403. Inherit the playlist's own hash into inner URIs that lack
	// one; the web player does the same client-side (Shaka filter) for direct
	// unproxied links.
	var inheritHash string
	if strings.EqualFold(meta.plugin, "filmix") {
		if u, err := url.Parse(meta.baseURI); err == nil {
			inheritHash = u.Query().Get("hash")
		}
	}

	// Manifest-only mode: keep rewriting the playlist (that is what repairs the
	// hashless EXT-X-MAP), but hand SEGMENTS to the client as direct CDN links so
	// the video bytes skip this server entirely. Nested PLAYLISTS stay proxied —
	// each level needs the same rewrite, and a direct variant playlist would reach
	// the client unrepaired.
	directSegments := h.manifestOnlyPlugins[strings.ToLower(strings.TrimSpace(meta.plugin))]

	// ★Диагностика: CDN-раздел, не дописывающий hash в сегменты, нельзя отдавать зрителю прямой
	// ссылкой — его плеер пойдёт без hash и получит 403 (2026-08-22, раздел UHD_1313). Ручной
	// список таких разделов живёт в httpapi/stream_proxy.go (filmixNoHashDirRe) и пополняется по
	// этому логу: правила, отличающего дефектный раздел от исправного, у CDN нет.
	if strings.EqualFold(meta.plugin, "filmix") && inheritHash != "" &&
		strings.Contains(src, "http") && !strings.Contains(src, "hash=") {
		if m := filmixCDNDirRe.FindStringSubmatch(meta.baseURI); len(m) == 2 {
			log.Warn().Str("dir", m[1]).
				Msg("filmix: CDN-раздел отдаёт сегменты БЕЗ hash — прямая отдача зрителю сломается")
		}
	}

	// fMP4 (EXT-X-MAP) раньше здесь безусловно возвращался в полное проксирование — считалось, что
	// его сегменты IP-привязаны. Для filmix измерено обратное (см. New): с постороннего IP и init
	// с дописанным hash, и сегменты отдают 206. Ломает воспроизведение не маршрут, а hashless init.
	//
	// Поэтому гейт остаётся ровно для тех, кому init починить НЕЧЕМ: hash наследуется только у
	// filmix, а чужой CDN с hashless EXT-X-MAP, отданный напрямую, встанет на первом же init.
	if directSegments && inheritHash == "" && strings.Contains(src, "#EXT-X-MAP") {
		directSegments = false
	}

	// Live IPTV: bound a huge sliding DVR window BEFORE the rewrite multiplies its size —
	// old TV-native HLS parsers take up to a minute on a megabyte playlist (see iptv_live.go).
	if isIPTVPlugin(meta.plugin) {
		src = trimLiveDVR(src, iptvLiveKeepSegments)
	}

	// For Kinescope CDN (redheadsound): extract SAMPLE-AES key and IV,
	// fetch the decryption key from the license server, embed key+IV in
	// segment URLs for server-side CBCS decryption, then strip the KEY tag.
	if strings.EqualFold(meta.plugin, "redheadsound") {
		cbcsKey, cbcsIV := h.extractCBCSKeyFromM3U(src, meta)
		if len(cbcsKey) == 16 && len(cbcsIV) == 16 {
			meta.cbcsKey = cbcsKey
			meta.cbcsIV = cbcsIV
		}
		src = reSampleAESKey.ReplaceAllString(src, "")
	}

	// Single-pass line-by-line rewriter. Replaces three sequential regex
	// passes (reHTTPLinks, reLineURI, reQuotedURI) with one scan, reducing
	// allocations from O(3*lines) to O(1) via strings.Builder.
	//
	// Order per line (matches old 3-pass order):
	//   1. Rewrite absolute http(s):// URLs (same as old pass 1)
	//   2. Rewrite relative URIs on non-comment lines (same as old pass 2)
	//   3. Rewrite relative URI="..." in tags (same as old pass 3)
	var b strings.Builder
	b.Grow(len(src) + len(src)/4) // m3u grows ~25% after rewrite

	rest := src
	for len(rest) > 0 {
		// Find end of current line.
		idx := strings.IndexAny(rest, "\r\n")
		var line, sep string
		if idx < 0 {
			line = rest
			rest = ""
		} else {
			line = rest[:idx]
			// Preserve \r\n as-is.
			if idx+1 < len(rest) && rest[idx] == '\r' && rest[idx+1] == '\n' {
				sep = "\r\n"
				rest = rest[idx+2:]
			} else {
				sep = rest[idx : idx+1]
				rest = rest[idx+1:]
			}
		}

		if line == "" {
			b.WriteString(sep)
			continue
		}

		// 1. Rewrite absolute HTTP(S) URLs anywhere on the line.
		if strings.Contains(line, "http://") || strings.Contains(line, "https://") {
			line = reHTTPLinks.ReplaceAllStringFunc(line, func(link string) string {
				link = appendMissingHash(link, inheritHash)
				if directSegments && !isManifestURI(link) {
					return link
				}
				return WithEdge(withSID(proxyHost+"/"+h.encrypt(link, meta, false, false), meta.sid), edgeHintOf, edgeSkipOf)
			})
		} else if line[0] != '#' {
			// 2. Non-comment, non-HTTP line → relative URI segment/playlist ref.
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.Contains(trimmed, "\"") {
				abs := appendMissingHash(resolveRelativeURI(trimmed, hlsHost, hlsPatch), inheritHash)
				if directSegments && !isManifestURI(abs) {
					line = abs
				} else {
					line = withSID(proxyHost+"/"+h.encrypt(abs, meta, false, false), meta.sid)
				}
			}
		}

		// 3. Rewrite relative URI="..." attributes (e.g. #EXT-X-KEY, #EXT-X-MAP).
		// Absolute URIs inside quotes were already rewritten in step 1.
		if strings.Contains(line, `URI="`) {
			line = rewriteRelativeQuotedURIs(line, proxyHost, hlsHost, hlsPatch, inheritHash, directSegments, h, meta)
		}

		b.WriteString(line)
		b.WriteString(sep)
	}
	return b.String()
}

// filmixCDNDirRe вытаскивает имя CDN-раздела из ссылки (`/hls/<dir>/…`) для диагностики.
var filmixCDNDirRe = regexp.MustCompile(`/hls/([^/]+)/`)

// appendMissingHash adds ?hash=<h> to a URL that carries none. filmix-only
// (inheritHash is empty for every other plugin): the EXT-X-MAP init URI in
// filmix /hls playlists is emitted hashless and the CDN 403s it.
func appendMissingHash(link, hash string) string {
	if hash == "" || strings.Contains(link, "hash=") {
		return link
	}
	if strings.Contains(link, "?") {
		return link + "&hash=" + hash
	}
	return link + "?hash=" + hash
}

// isManifestURI reports whether a URI points at another playlist/manifest rather
// than at media bytes. Manifest-only mode keeps these proxied so every level gets
// rewritten; everything else (segments, fMP4 init, keys) goes direct.
func isManifestURI(u string) bool {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	u = strings.ToLower(u)
	return strings.HasSuffix(u, ".m3u8") || strings.HasSuffix(u, ".m3u") || strings.HasSuffix(u, ".mpd")
}

// rewriteRelativeQuotedURIs rewrites relative (non-http) URI="..." attributes.
// Absolute http:// URIs are already rewritten by reHTTPLinks in the main loop.
func rewriteRelativeQuotedURIs(line, proxyHost, hlsHost, hlsPatch, inheritHash string, directSegments bool, h *Handler, meta linkMeta) string {
	const prefix = `URI="`
	var b strings.Builder
	b.Grow(len(line) + 64)
	for {
		idx := strings.Index(line, prefix)
		if idx < 0 {
			b.WriteString(line)
			break
		}
		b.WriteString(line[:idx+len(prefix)])
		line = line[idx+len(prefix):]
		end := strings.IndexByte(line, '"')
		if end < 0 {
			b.WriteString(line)
			break
		}
		uri := line[:end]
		line = line[end:]
		lowerURI := strings.ToLower(uri)
		if strings.HasPrefix(lowerURI, "http") || strings.Contains(uri, "\"") {
			// Already rewritten (absolute) or malformed — leave as-is.
			b.WriteString(uri)
		} else if uri != "" {
			// Relative → resolve, then encrypt (or hand over direct in manifest-only mode).
			abs := appendMissingHash(resolveRelativeURI(uri, hlsHost, hlsPatch), inheritHash)
			if directSegments && !isManifestURI(abs) {
				b.WriteString(abs)
			} else {
				b.WriteString(withSID(proxyHost+"/"+h.encrypt(abs, meta, false, false), meta.sid))
			}
		}
	}
	return b.String()
}

func (h *Handler) encrypt(uri string, meta linkMeta, forceMD5, isProxyImg bool) string {
	if h.links == nil {
		enc := url.QueryEscape(uri)
		plugin := strings.ToLower(strings.TrimSpace(meta.plugin))
		if plugin != "" {
			enc += "?pl=" + url.QueryEscape(plugin)
		}
		return enc
	}
	// Keep direct /proxy/<urlencoded-url> flow keyless for rewritten segment URLs.
	// This avoids cross-instance decrypt failures when requests are load-balanced
	// between workers with different proxylink AES keys.
	if !forceMD5 &&
		!isProxyImg &&
		!meta.verifyIP &&
		len(meta.headers) == 0 &&
		len(meta.cbcsKey) == 0 &&
		len(meta.cbcsIV) == 0 {
		plugin := strings.ToLower(strings.TrimSpace(meta.plugin))
		if plugin == "" || plugin == "pornhub" || plugin == "pornhubpremium" || plugin == "eporner" || plugin == "rezka" || plugin == "fancdn" {
			enc := url.QueryEscape(uri)
			// Propagate plugin name via query param so that rewritten
			// segment URLs still carry plugin metadata for the proxy
			// handler. Without this, segments lose transport routing
			// (e.g. VLESS for rezka) and header injection.
			if plugin != "" {
				enc += "?pl=" + url.QueryEscape(plugin)
			}
			return enc
		}
	}
	// If CBCS key is present, embed key+IV+headers in segment URLs for
	// server-side decryption of Kinescope SAMPLE-AES (CBCS) fMP4 segments.
	if len(meta.cbcsKey) == 16 && len(meta.cbcsIV) == 16 && !forceMD5 && !isProxyImg {
		return h.links.EncryptURIWithCBCS(uri, meta.reqIP, meta.plugin, meta.headers, meta.cbcsKey, meta.cbcsIV)
	}
	// If custom upstream headers are set, embed them in the sub-URL payloads too
	// so that segment/variant requests also send the required headers.
	if len(meta.headers) > 0 && !isProxyImg {
		return h.links.EncryptURIWithHeaders(uri, meta.reqIP, meta.plugin, meta.headers)
	}
	return h.links.EncryptURI(uri, meta.reqIP, meta.plugin, meta.verifyIP, forceMD5, isProxyImg)
}

func (h *Handler) proxyURI(r *http.Request, uri string, meta linkMeta, forceMD5 bool) string {
	// Сегменты наследуют sid манифеста: поток сверяется целиком, а не по
	// отдельным запросам.
	return withSID(h.streamHostFromRequest(r)+"/proxy/"+h.encrypt(uri, meta, forceMD5, false), meta.sid)
}

func choosePrimaryURL(client *http.Client, target string, in *http.Request, maxLen int64) string {
	parts := strings.SplitN(target, " or ", 2)
	if len(parts) < 2 {
		return strings.TrimSpace(target)
	}
	a := strings.TrimSpace(parts[0])
	b := strings.TrimSpace(strings.Split(parts[1], " ")[0])

	ctx, cancel := context.WithTimeout(in.Context(), 7*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a, nil)
	if err != nil {
		return b
	}
	req.Header.Set("Range", "bytes=0-1")
	resp, err := client.Do(req)
	if err != nil {
		return b
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxLen))
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		return a
	}
	return b
}

func resolveLocation(base *url.URL, loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		return u.String()
	}
	if base == nil {
		return ""
	}
	return base.ResolveReference(u).String()
}

// isObrutCDNPlugin returns true if the plugin uses the obrut.show CDN
// (shared infrastructure between zetflix and videodb).
func isObrutCDNPlugin(plugin string) bool {
	p := strings.ToLower(strings.TrimSpace(plugin))
	return p == "zetflix" || p == "videodb"
}

// isCleanHeaderPlugin returns true for plugins whose upstream CDN (e.g. okcdn.ru
// with DDoS-Guard) rejects requests with non-standard headers. For these plugins
// the proxy sends only minimal headers instead of copying browser headers.
func isCleanHeaderPlugin(plugin string) bool {
	p := strings.ToLower(strings.TrimSpace(plugin))
	return p == "cdnvideohub" || p == "vibix" || p == "turbo"
}

func isZetflixObrutCDNURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Host))
	if !strings.HasPrefix(host, "cdn-") || !strings.Contains(host, "obrut.show") {
		return false
	}
	return strings.Contains(strings.ToLower(u.Path), "/stream/")
}

func isZetflixSuperduperURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Host))
	return strings.Contains(host, "superdupercdn.com")
}

func isPornhubCDNHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return strings.Contains(host, "phncdn.com")
}

// ---------------------------------------------------------------------------
// Voidboost CDN helpers (used by rezka plugin)
// ---------------------------------------------------------------------------

// voidboostCDNSubdomains contains confirmed working Voidboost CDN subdomains.
// Only fiber and blitz are confirmed to serve video content and accept stream tokens.
var voidboostCDNSubdomains = []string{
	"fiber.stream.voidboost.cc",
	"blitz.stream.voidboost.cc",
}

func isRezkaPlugin(plugin string) bool {
	p := strings.ToLower(strings.TrimSpace(plugin))
	return p == "rezka" || p == "rhsprem" || p == "rhs"
}

func isVoidboostCDNURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Host))
	return strings.HasSuffix(host, "stream.voidboost.cc") || host == "stream.voidboost.cc"
}

// voidboostCDNAlternatives returns alternative URLs by swapping the voidboost subdomain.
func voidboostCDNAlternatives(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil || !isVoidboostCDNURL(u) {
		return nil
	}
	currentHost := strings.ToLower(u.Host)
	var alts []string
	for _, sub := range voidboostCDNSubdomains {
		if strings.ToLower(sub) == currentHost {
			continue // skip current
		}
		alt := strings.Replace(rawURL, u.Host, sub, 1)
		alts = append(alts, alt)
	}
	return alts
}

// resolveZetflixCDNRedirect tries to resolve cdn-*.obrut.show stream URL into
// an actual CDN location (usually superdupercdn) before the main request.
func (h *Handler) resolveZetflixCDNRedirect(ctx context.Context, client *http.Client, base *url.URL, headers http.Header) string {
	if base == nil {
		return ""
	}
	baseRaw := strings.TrimSpace(base.String())
	variants := zetflixCDNVariants(base.String())
	try := func(method string) (string, bool) {
		for _, target := range variants {
			req, err := http.NewRequestWithContext(ctx, method, target, nil)
			if err != nil {
				continue
			}
			tBase, _ := url.Parse(target)
			if tBase == nil {
				tBase = base
			}
			for k, vals := range headers {
				for _, v := range vals {
					req.Header.Add(k, v)
				}
			}
			req.Header.Del("Range")
			req.Header.Set("Sec-Fetch-Dest", "empty")
			req.Header.Set("Sec-Fetch-Mode", "cors")
			req.Header.Set("Sec-Fetch-Site", "same-site")
			req.Host = tBase.Host
			req.Header.Set("Host", tBase.Host)

			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()

			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				loc := resolveLocation(tBase, resp.Header.Get("Location"))
				if loc != "" {
					if lu, err := url.Parse(loc); err == nil && isZetflixSuperduperURL(lu) {
						if lvars := zetflixCDNVariants(loc); len(lvars) > 0 {
							return lvars[0], true
						}
					}
					return loc, true
				}
			}
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				// Raw /stream/... URL can return non-useful 2xx for probes while
				// playback GET with Range still fails. Prefer explicit HLS variants.
				if strings.TrimSpace(target) == baseRaw {
					continue
				}
				return target, true
			}
		}
		return "", false
	}
	if loc, ok := try(http.MethodHead); ok {
		return loc
	}
	if loc, ok := try(http.MethodGet); ok {
		return loc
	}
	return ""
}

func zetflixCDNVariants(target string) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	out := make([]string, 0, 8)
	seen := map[string]struct{}{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	addVariants := func(base string) {
		base = strings.TrimSpace(base)
		if base == "" {
			return
		}
		lt := strings.ToLower(base)
		switch {
		case strings.Contains(lt, ":hls:manifest-v1-a2.m3u8"):
			add(base)
			add(strings.Replace(base, ":hls:manifest-v1-a2.m3u8", ":hls:manifest.m3u8", 1))
		case strings.Contains(lt, ":hls:manifest.m3u8"):
			add(base)
			add(strings.Replace(base, ":hls:manifest.m3u8", ":hls:manifest-v1-a2.m3u8", 1))
		case strings.Contains(lt, ":hls:"):
			add(base)
		case strings.HasSuffix(lt, ".m3u8"):
			add(base)
		default:
			// Prefer explicit manifest variants before probing raw stream URL.
			add(base + ":hls:manifest-v1-a2.m3u8")
			add(base + ":hls:manifest.m3u8")
			add(base)
		}
	}

	// Some obrut /stream/AO/... URLs are encoded wrappers that can be decoded
	// into a direct superdupercdn manifest URL without redirect/cookie dance.
	if decoded, ok := decodeZetflixObrutStreamURL(target); ok {
		addVariants(decoded)
	}
	addVariants(target)
	return out
}

func decodeZetflixObrutStreamURL(target string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || !isZetflixObrutCDNURL(u) {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	idx := -1
	for i := range parts {
		if parts[i] == "stream" {
			idx = i
			break
		}
	}
	// Expected: /stream/<key>/<enc-scheme>/<enc-host>/<enc-path>
	if idx < 0 || idx+4 >= len(parts) {
		return "", false
	}
	scheme, ok := decodeReversedB64(parts[idx+2])
	if !ok {
		return "", false
	}
	host, ok := decodeReversedB64(parts[idx+3])
	if !ok {
		return "", false
	}
	path, ok := decodeReversedB64(parts[idx+4])
	if !ok {
		return "", false
	}

	scheme = strings.ToLower(strings.TrimSpace(scheme))
	host = strings.TrimSpace(host)
	path = strings.TrimSpace(path)
	if scheme == "" || host == "" || path == "" {
		return "", false
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	decoded := scheme + "://" + host + path
	du, err := url.Parse(decoded)
	if err != nil || du.Scheme == "" || du.Host == "" {
		return "", false
	}
	return decoded, true
}

func decodeReversedB64(seg string) (string, bool) {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return "", false
	}
	b := []byte(seg)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	rev := string(b)
	if out, err := base64.RawURLEncoding.DecodeString(rev); err == nil {
		return string(out), true
	}
	padded := rev + strings.Repeat("=", (4-len(rev)%4)%4)
	if out, err := base64.URLEncoding.DecodeString(padded); err == nil {
		return string(out), true
	}
	if out, err := base64.StdEncoding.DecodeString(padded); err == nil {
		return string(out), true
	}
	return "", false
}

// skipHopByHopHeaders is a set of hop-by-hop headers that must not be forwarded.
// Keys are in canonical (http.CanonicalHeaderKey) form for O(1) lookup.
var skipHopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	// Accept-Encoding must be removed so that Go's http.Client
	// handles content negotiation itself (adds its own gzip and
	// transparently decompresses the response).
	"Accept-Encoding": true,
}

// stripClientIdentityHeaders removes every header that could carry the end-user's real IP
// (or our internal proxy marker) before the request leaves for the upstream. nginx in front of
// lampac-go stamps X-Real-IP / X-Forwarded-For with the client's address, and the general
// copyHeaders path forwards them verbatim — so without this an IPTV/CDN provider would see a
// different IP per user on one account and ban the playlist as "shared". After this call the
// upstream sees only this server's single egress IP.
func stripClientIdentityHeaders(h http.Header) {
	h.Del("X-Forwarded-For")
	h.Del("X-Real-IP")
	h.Del("X-Client-IP")
	h.Del("Forwarded")
	h.Del("Via")
	h.Del("True-Client-IP")
	h.Del("CF-Connecting-IP")
	h.Del("Fastly-Client-IP")
	h.Del("X-Lampac-Go") // internal marker — no reason to advertise it upstream
}

func copyHeaders(dst, src http.Header) {
	for k, vals := range src {
		if skipHopByHopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

// skipUpstreamHeaders lists headers that must NOT be copied from upstream
// responses. Our globalCORSMiddleware already sets correct CORS headers;
// copying the upstream CDN's CORS headers (e.g. "Access-Control-Allow-Origin:
// https://kinescope.io") creates duplicates that violate the spec and cause
// browsers to block the response entirely.
var skipUpstreamHeaders = map[string]bool{
	// Hop-by-hop / framing headers
	"transfer-encoding": true,
	"connection":        true,
	"content-length":    true,
	// Go http.Client transparently decompresses gzip/br responses, so the
	// body we read is already decompressed. Forwarding the upstream
	// Content-Encoding header would tell the browser to decompress again,
	// causing ERR_CONTENT_DECODING_ERROR.
	"content-encoding": true,
	// CORS headers — set by our globalCORSMiddleware; upstream CDN values
	// (e.g. "Access-Control-Allow-Origin: https://kinescope.io") would
	// create duplicates that violate the spec and break browser playback.
	"access-control-allow-origin":      true,
	"access-control-allow-methods":     true,
	"access-control-allow-headers":     true,
	"access-control-allow-credentials": true,
	"access-control-expose-headers":    true,
	"access-control-max-age":           true,
}

// copyHeadersFiltered copies upstream headers to dst, skipping anything in
// skipUpstreamHeaders. Called per proxy response → hot path. Go's stdlib
// `http.Header` map keys are *already* canonicalised by Add/Set/Get
// ("content-type" → "Content-Type"), so iteration yields canonical keys
// even when the upstream sent lowercase. textproto.CanonicalMIMEHeaderKey
// is allocation-free for already-canonical input — much cheaper than a
// per-iteration strings.ToLower allocation.
//
// Was: strings.ToLower(k) on every header on every response — pprof shows
// this called ~25× per proxy response, and proxyapi.HandleProxy serves the
// bulk of /proxy/* traffic.
func copyHeadersFiltered(dst, src http.Header) {
	for k, vals := range src {
		// Map keys in http.Header are stored canonicalised by stdlib. Our
		// skipUpstreamHeaders is keyed lowercase, so we compare against the
		// canonical form. Cheap: it's a hash lookup either way; the win is
		// avoiding the ToLower allocation when the value is already canonical.
		if skipUpstreamHeadersCanonical[k] {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

// skipUpstreamHeadersCanonical is skipUpstreamHeaders re-keyed by Go's
// canonical MIME form ("Content-Type", not "content-type"), built once at
// package init. Matches how http.Header stores keys.
var skipUpstreamHeadersCanonical = func() map[string]bool {
	out := make(map[string]bool, len(skipUpstreamHeaders))
	for k, v := range skipUpstreamHeaders {
		out[textproto.CanonicalMIMEHeaderKey(k)] = v
	}
	return out
}()

// extractCBCSKeyFromM3U parses #EXT-X-KEY:METHOD=SAMPLE-AES from an m3u8
// playlist, fetches the 16-byte AES key from the license server, and returns
// (key, iv). Results are cached by KEY URI. Returns (nil, nil) on failure.
func (h *Handler) extractCBCSKeyFromM3U(src string, meta linkMeta) ([]byte, []byte) {
	// Find the SAMPLE-AES KEY line.
	keyLine := reSampleAESKey.FindString(src)
	if keyLine == "" {
		return nil, nil
	}

	// Extract URI="..." and IV=0x...
	uriMatch := reSampleAESURI.FindStringSubmatch(keyLine)
	ivMatch := reSampleAESIV.FindStringSubmatch(keyLine)
	if len(uriMatch) < 2 || len(ivMatch) < 2 {
		log.Debug().Str("keyLine", truncStr(keyLine, 200)).Msg("cbcs: could not parse KEY URI or IV")
		return nil, nil
	}

	keyURI := uriMatch[1]
	ivHex := ivMatch[1]

	log.Debug().Str("keyURI", truncStr(keyURI, 120)).Str("ivHex", ivHex).Msg("cbcs: parsed KEY tag")

	// Decode IV from hex.
	ivBytes, err := hex.DecodeString(ivHex)
	if err != nil || len(ivBytes) != 16 {
		log.Debug().Str("ivHex", ivHex).Msg("cbcs: invalid IV hex")
		return nil, nil
	}

	// Check cache first.
	if key, ok := cbcsKeyCache.Load(keyURI); ok && len(key) == 16 {
		return key, ivBytes
	}

	// Fetch key from license server. Kinescope requires Origin: https://kinescope.io
	log.Debug().Str("uri", truncStr(keyURI, 120)).Msg("cbcs: fetching CBCS key")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, keyURI, nil)
	if err != nil {
		log.Debug().Err(err).Msg("cbcs: failed to create key request")
		return nil, nil
	}
	req.Header.Set("Origin", "https://kinescope.io")
	req.Header.Set("Referer", "https://kinescope.io/")

	resp, err := h.client.Do(req)
	if err != nil {
		log.Debug().Err(err).Msg("cbcs: key fetch failed")
		return nil, nil
	}
	defer resp.Body.Close()

	keyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil || len(keyBytes) != 16 {
		log.Debug().Int("len", len(keyBytes)).Int("status", resp.StatusCode).Msg("cbcs: unexpected key length")
		return nil, nil
	}

	// Cache the key.
	cbcsKeyCache.Store(keyURI, keyBytes)

	log.Debug().Str("uri", truncStr(keyURI, 80)).Msg("cbcs: key fetched and cached")
	return keyBytes, ivBytes
}

// handleFMP4DRMStripStream handles both init and media fMP4 segments.
// If meta contains CBCS key+IV, it buffers the entire segment and performs
// full CBCS decryption before stripping DRM metadata.
// Otherwise, it reads the first part of the response (up to 64KB) which
// contains either moov (init) or moof (media) boxes, patches DRM boxes
// in-place, then streams the remainder (mdat) without buffering.
// All patching preserves byte sizes so BYTERANGE offsets stay valid.
func (h *Handler) handleFMP4DRMStripStream(w http.ResponseWriter, resp *http.Response, meta linkMeta) {
	// If we have CBCS key+IV, buffer entire segment and decrypt.
	// Skip for full-file requests (no Range → ContentLength is the entire mp4,
	// typically 50-100MB). Only decrypt reasonable segment sizes.
	const maxSegmentSize = 20_000_000 // 20MB max segment
	if len(meta.cbcsKey) == 16 && len(meta.cbcsIV) == 16 &&
		(resp.ContentLength > 0 && resp.ContentLength <= maxSegmentSize || resp.ContentLength < 0) {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxSegmentSize))
		if err != nil {
			log.Debug().Err(err).Int("bodyLen", len(body)).Msg("cbcs: failed to buffer segment")
			// If we got partial data, still try to serve what we have.
			if len(body) == 0 {
				http.Error(w, "error reading segment", http.StatusServiceUnavailable)
				return
			}
		}
		if len(body) > 0 {
			decryptFMP4CBCS(body, meta.cbcsKey, meta.cbcsIV)
		}
		copyHeadersFiltered(w.Header(), resp.Header)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	// Peek buffer: moof is typically <2KB, moov (init) <1KB.
	// 64KB is more than enough to cover any moof.
	const peekSize = 64_000
	peek := make([]byte, peekSize)
	n, peekErr := io.ReadFull(resp.Body, peek)

	if peekErr == io.ErrUnexpectedEOF || peekErr == io.EOF {
		// Entire segment fits in peek buffer (init segments, small segments).
		peek = peek[:n]
		stripFMP4DRM(peek) // patches moov/stsd + moof/traf in-place
		copyHeadersFiltered(w.Header(), resp.Header)
		w.Header().Set("Content-Length", strconv.Itoa(len(peek)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(peek)
		return
	}
	if peekErr != nil {
		http.Error(w, "error reading fmp4", http.StatusServiceUnavailable)
		return
	}

	// Large segment: peek contains first 64KB (moof + start of mdat).
	// Patch moof encryption boxes in the peek buffer, then stream the rest.
	stripFMP4DRM(peek[:n]) // patches moof/traf saiz/saio/senc → free

	copyHeadersFiltered(w.Header(), resp.Header)
	// Preserve original Content-Length — we didn't change sizes.
	// Same caveat as copyResponse: skip when Go decompressed (resp.Uncompressed),
	// because resp.ContentLength would be the compressed byte count and the
	// player would truncate the stream.
	if resp.ContentLength > 0 && !resp.Uncompressed {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(peek[:n])
	bp := copyBufPool.Get().(*[]byte)
	_, _ = io.CopyBuffer(w, resp.Body, *bp)
	copyBufPool.Put(bp)
}

func (h *Handler) copyResponse(w http.ResponseWriter, resp *http.Response, withCL bool) int64 {
	copyHeadersFiltered(w.Header(), resp.Header)
	// Forward Content-Length only when it actually matches the bytes we'll
	// write. resp.ContentLength is the COMPRESSED length when Go transparently
	// decompresses gzip/br (resp.Uncompressed=true) — forwarding it makes
	// players read N bytes and stop, truncating the stream and breaking
	// playback ("video damaged" / "Content-Length mismatch" in the old lampac).
	// When skipped, Go falls back to chunked encoding which streams correctly.
	if resp.ContentLength > 0 && !resp.Uncompressed {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	// Forward Accept-Ranges if upstream supports it (needed for video seeking)
	if ar := resp.Header.Get("Accept-Ranges"); ar != "" {
		w.Header().Set("Accept-Ranges", ar)
	}
	w.WriteHeader(resp.StatusCode)
	bp := copyBufPool.Get().(*[]byte)
	n, _ := io.CopyBuffer(w, resp.Body, *bp)
	copyBufPool.Put(bp)
	return n
}

// recordTraffic reports proxy traffic stats via the TrafficRecorder callback.
// isRetryableStatus returns true for upstream status codes worth retrying.
// 500 included: CDNs (Alloha, Mirage) return 500 on overloaded/stale segments.
func isRetryableStatus(code int) bool {
	return code == 500 || code == 502 || code == 503 || code == 504
}

func recordTraffic(plugin string, bytes int64, statusCode int) {
	if TrafficRecorder != nil && bytes > 0 {
		TrafficRecorder(strings.ToLower(plugin), bytes, statusCode >= 400)
	}
}

func recordLatency(plugin string, elapsed time.Duration, statusCode int) {
	if LatencyRecorder != nil {
		LatencyRecorder(strings.ToLower(strings.TrimSpace(plugin)), elapsed, statusCode)
	}
}

func applyDynamicPluginHeaders(plugin string, hdr http.Header) {
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin == "" {
		return
	}
	v, ok := pluginHeaderProviders.Load(plugin)
	if !ok {
		return
	}
	provider, ok := v.(func() map[string]string)
	if !ok || provider == nil {
		return
	}
	extra := provider()
	for k, vv := range extra {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		vv = strings.TrimSpace(vv)
		if vv == "" {
			continue
		}
		hdr.Set(k, vv)
	}
}

func extractHLSHost(uri string) string {
	m := reHLSHost.FindStringSubmatch(uri)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func extractHLSPatch(uri string) string {
	m := rePathNoFileName.FindStringSubmatch(uri)
	if len(m) > 1 {
		return m[1]
	}
	if strings.HasSuffix(uri, "/") {
		return uri
	}
	return ""
}

func resolveRelativeURI(uri, hlsHost, hlsPatch string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	if strings.HasPrefix(uri, "//") {
		return "https:" + uri
	}

	if u, err := url.Parse(uri); err == nil && u.IsAbs() {
		return u.String()
	}

	base := strings.TrimSpace(hlsPatch)
	if base == "" {
		base = strings.TrimSpace(hlsHost)
		if base != "" && !strings.HasSuffix(base, "/") {
			base += "/"
		}
	}
	if base != "" {
		if bu, err := url.Parse(base); err == nil {
			if ru, err := url.Parse(uri); err == nil {
				return bu.ResolveReference(ru).String()
			}
		}
	}

	// Fallback for malformed input.
	switch {
	case strings.HasPrefix(uri, "/"):
		return hlsHost + uri
	case strings.HasPrefix(uri, "./"):
		return hlsPatch + strings.TrimPrefix(uri, "./")
	default:
		return hlsPatch + uri
	}
}

func decodeDirectTarget(pathPart, rawQuery string) (string, bool) {
	pathPart = strings.TrimSpace(pathPart)
	if pathPart == "" {
		return "", false
	}
	if i := strings.Index(pathPart, " or "); i > 0 {
		pathPart = strings.TrimSpace(pathPart[:i])
	}

	candidates := []string{pathPart}
	if u, err := url.PathUnescape(pathPart); err == nil && u != pathPart {
		candidates = append(candidates, u)
	}
	if u, err := url.QueryUnescape(pathPart); err == nil && u != pathPart {
		candidates = append(candidates, u)
	}

	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if !strings.HasPrefix(c, "http://") && !strings.HasPrefix(c, "https://") {
			continue
		}
		if rawQuery != "" {
			if strings.Contains(c, "?") {
				c += "&" + rawQuery
			} else {
				c += "?" + rawQuery
			}
		}
		return c, true
	}
	return "", false
}

func hostFromRequest(r *http.Request) string {
	scheme := "http"
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
		scheme = xf
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// streamHostFromRequest is the stream-bound variant of hostFromRequest:
// it applies the [host.stream_aliases] map so the emitted /proxy/ URL
// uses a DNS-only stream subdomain (bypassing a fronting CDN). For
// requests on unmapped hosts the original host is returned unchanged.
func (h *Handler) streamHostFromRequest(r *http.Request) string {
	return h.hostCfg.StreamHostFor(hostFromRequest(r))
}

func requestIP(r *http.Request) string {
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xf != "" {
		if i := strings.Index(xf, ","); i > 0 {
			return strings.TrimSpace(xf[:i])
		}
		return xf
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// noRedirectFunc is a shared CheckRedirect function to prevent per-request allocation.
var noRedirectFunc = func(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// isHTTPRedirect reports whether status is a Location-bearing redirect that the
// proxy may follow manually (used for remux/cloud.mail.ru weblink_view hops).
func isHTTPRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// containsM3U checks if s contains ".m3u" case-insensitively without allocating.
func containsM3U(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, ".m3u")
}

func isRedirect(code int) bool {
	return code == http.StatusMovedPermanently || code == http.StatusFound || code == http.StatusSeeOther
}

func (h *Handler) fallback(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

// upstreamErrStatus маппит транспортную ошибку апстрима в клиентский статус:
// дедлайн/таймаут → 504, всё остальное (обрыв, DNS, TLS) → 502. Оба —
// ретраябельны для HLS-плееров, в отличие от прежнего 404.
func upstreamErrStatus(err error) int {
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// Стриминговые таймауты streamClient'а: заголовки апстрима обязаны прийти за
// streamHeaderTimeout (та же страховка, что давал старый client.Timeout), а
// тело живёт сколько угодно — пока апстрим шлёт хоть что-то раз в
// streamStallTimeout.
const (
	streamHeaderTimeout = 35 * time.Second
	streamStallTimeout  = 60 * time.Second
)

// stallWatchdogBody принудительно закрывает тело ответа, когда апстрим замолчал
// (ни одного байта за stall): io.Copy разблокируется с ошибкой вместо вечно
// висящей горутины и залипшего клиента. Замена client.Timeout для бесконечных
// live-ответов. cancel отпускает контекст запроса при любом закрытии.
type stallWatchdogBody struct {
	rc     io.ReadCloser
	timer  *time.Timer
	stall  time.Duration
	cancel context.CancelFunc
}

func newStallWatchdogBody(rc io.ReadCloser, stall time.Duration, cancel context.CancelFunc) io.ReadCloser {
	b := &stallWatchdogBody{rc: rc, stall: stall, cancel: cancel}
	b.timer = time.AfterFunc(stall, func() { _ = b.Close() })
	return b
}

func (b *stallWatchdogBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if err == nil {
		b.timer.Reset(b.stall)
	}
	return n, err
}

func (b *stallWatchdogBody) Close() error {
	b.timer.Stop()
	if b.cancel != nil {
		defer b.cancel()
	}
	return b.rc.Close()
}

// pickSourceLink достаёт ссылку на поток из JSON-списка источников вида
// {"sources":[{"link":"...","isDefault":true}]}. Предпочитает isDefault,
// иначе берёт первую непустую. "" — если это не список источников.
func pickSourceLink(body []byte) string {
	var doc struct {
		Sources []struct {
			Link      string `json:"link"`
			IsDefault bool   `json:"isDefault"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || len(doc.Sources) == 0 {
		return ""
	}
	first := ""
	for _, s := range doc.Sources {
		l := strings.TrimSpace(s.Link)
		if l == "" || !strings.HasPrefix(l, "http") {
			continue
		}
		if s.IsDefault {
			return l
		}
		if first == "" {
			first = l
		}
	}
	return first
}
