package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/torrbalancer"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// tsProxy holds the reverse proxy state for TorrServer.
type tsProxy struct {
	client       *http.Client // short-lived requests (API, m3u, etc.)
	streamClient *http.Client // long-lived stream/play requests (no total timeout)
	apiClient    *http.Client // POST /torrents (add/get/rem): жёсткий короткий дедлайн — живой TorrServer отвечает за миллисекунды, зависание = сигнал полу-зомби

	// pool, when active (>=1 enabled backend), selects the upstream per-request
	// sticky-by-infohash. When the pool has no enabled backends the proxy falls
	// back to the single legacy upstream below (byte-for-byte the old behaviour).
	pool *torrbalancer.Pool

	upstream string // legacy single upstream, e.g. "http://127.0.0.1:9080"
	authHdr  string // legacy Basic auth header for upstream
}

// tsProxyPtr is the hot-reloadable pointer to the current TorrServer proxy.
// Updated by newTSProxy() on startup and reloadTSProxy() on config change.
var tsProxyPtr atomic.Pointer[tsProxy]

var tsConcatRe = regexp.MustCompile(`\.concat\((\w+),\s*"(/[^"]*)"`)

// reloadTSProxy recreates the TorrServer proxy from the current config.
// Called during hot-reload when torrserver settings change.
func reloadTSProxy(cfg config.Config) {
	if cfg.TorrServer.Port > 0 || cfg.TorrServer.URL != "" {
		p := newTSProxy(cfg)
		tsProxyPtr.Store(p)
		log.Info().Str("upstream", p.upstream).Bool("auth", p.authHdr != "").Msg("reload: torrserver proxy updated")
	} else {
		tsProxyPtr.Store(nil)
		log.Info().Msg("reload: torrserver proxy disabled (no port/url)")
	}
}

func newTSProxy(cfg config.Config) *tsProxy {
	var upstream string
	if u := strings.TrimSpace(cfg.TorrServer.URL); u != "" {
		// External TorrServer — use URL directly
		upstream = strings.TrimRight(u, "/")
	} else {
		// Local TorrServer — use localhost:port
		port := cfg.TorrServer.Port
		if port <= 0 {
			port = 9080
		}
		upstream = fmt.Sprintf("http://127.0.0.1:%d", port)
	}

	var authHdr string
	if pw := strings.TrimSpace(cfg.TorrServer.Password); pw != "" {
		login := strings.TrimSpace(cfg.TorrServer.Login)
		if login == "" {
			login = "ts" // default TorrServer username
		}
		cred := base64.StdEncoding.EncodeToString([]byte(login + ":" + pw))
		authHdr = "Basic " + cred
	}

	// streamClient has no total-request timeout so that long-lived
	// stream/play connections (can be multi-GB, hours long) are not
	// killed mid-playback.  Cancellation comes from the client
	// disconnecting (r.Context().Done()).  Short API requests use the
	// 60 s client as before.
	//
	// It DOES bound the wait for response HEADERS: a half-wedged TorrServer
	// (prod 2026-07-16: /echo fine, /stream never answered) used to hang every
	// viewer indefinitely — no timeout, no signal. 90s covers a legitimate
	// cold-torrent start (metadata + first pieces on a slow swarm); a backend
	// that can't produce headers in 90s yields a clean error that ALSO feeds
	// the pool's stream circuit-breaker (RecordStreamStrike in the caller).
	streamClient := httpclient.NewNoRedirect(0)
	streamTr := httpclient.SharedTransport.Clone() // never mutate the shared transport
	streamTr.ResponseHeaderTimeout = 90 * time.Second
	streamClient.Transport = streamTr

	return &tsProxy{
		client:       httpclient.NewNoRedirect(60 * time.Second),
		streamClient: streamClient,
		apiClient:    httpclient.NewNoRedirect(15 * time.Second),
		pool:         tsBalancerPoolRef,
		upstream:     upstream,
		authHdr:      authHdr,
	}
}

// tsEntryHandler serves the TorrServer web UI (GET /ts, GET /ts/static/js/{suffix}).
// It proxies to the backend and rewrites HTML/JS paths.
func tsEntryHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		proxy := tsProxyPtr.Load()
		if proxy == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<!doctype html><html><body>TorrServer bridge is not configured</body></html>"))
			return
		}

		// Strip /ts prefix for backend
		path := strings.TrimPrefix(r.URL.Path, "/ts")
		if path == "" {
			path = "/"
		}

		// Web UI / static assets have no infohash → primary backend.
		host, authHdr, _, ok := proxy.resolveTSBackend(r, "")
		if !ok {
			http.Error(w, "no TorrServer backend available", http.StatusServiceUnavailable)
			return
		}

		body, status, ct, err := proxy.doRequest(r, path, host, authHdr)
		if err != nil {
			log.Warn().Err(err).Str("path", path).Msg("ts: proxy entry failed")
			http.Error(w, "TorrServer unavailable", http.StatusBadGateway)
			return
		}

		content := string(body)

		// Rewrite HTML: href="/ → href="/ts/  and  src="/ → src="/ts/
		if strings.Contains(ct, "html") {
			content = strings.ReplaceAll(content, `href="/`, `href="/ts/`)
			content = strings.ReplaceAll(content, `src="/`, `src="/ts/`)
			content = strings.ReplaceAll(content, `src="./`, `src="/ts/`)
		}

		// Rewrite JS: .concat(KEY, "/ → .concat(KEY, "/ts/
		if strings.Contains(ct, "javascript") {
			content = tsConcatRe.ReplaceAllString(content, `.concat($1, "/ts$2"`)
		}

		w.Header().Set("Content-Type", ct)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(content))
	}
}

func tsStaticJSHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		proxy := tsProxyPtr.Load()
		if proxy == nil {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return
		}
		tsEntryHandler(cfg).ServeHTTP(w, r)
	}
}

// tsAPIHandler handles all /ts/* requests as a reverse proxy to TorrServer.
func tsAPIHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		proxy := tsProxyPtr.Load()
		if proxy == nil {
			suffix := strings.TrimSpace(strings.Trim(chi.URLParam(r, "*"), "/"))
			if strings.ToLower(suffix) == "echo" {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("MatriX.API (lampac-go)"))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}

		suffix := strings.TrimSpace(strings.Trim(chi.URLParam(r, "*"), "/"))

		// Block /shutdown
		if strings.HasPrefix(strings.ToLower(suffix), "shutdown") {
			http.NotFound(w, r)
			return
		}

		// Auth gate for mutating endpoints — proxy mode mirrors the
		// in-process tsRequireTokenMiddleware policy.  External Basic-Auth
		// (already validated by tsExternalAuthMiddleware upstream) gets a
		// free pass on the cookie check.
		if tsTokenStoreRef != nil && tsSuffixRequiresAuth(suffix, r) && !hasExternalAuth(r) {
			if requestUserTokenFromAny(r) == "" {
				http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
				return
			}
		}

		// ?token= — НАШ auth (webOS-клиент без кук шлёт его в query); апстриму
		// он не нужен и не должен утекать на бэкенды (это девайс-токен юзера).
		if q := r.URL.Query(); q.Has("token") {
			q.Del("token")
			r.URL.RawQuery = q.Encode()
		}

		// Strip /ts prefix for backend
		path := strings.TrimPrefix(r.URL.Path, "/ts")
		if path == "" {
			path = "/"
		}

		// --- backend selection ----------------------------------------------
		// Derive the torrent infohash so the request lands on the backend that
		// already holds this torrent (TorrServer is stateful per torrent). For
		// POST torrents/cache the hash/link live in the JSON body, so buffer it,
		// then hand the bytes back to the upstream untouched.
		low := strings.ToLower(suffix)
		isTorrents := strings.HasPrefix(low, "torrents")
		isCache := strings.HasPrefix(low, "cache")
		var infohash, bodyAction string
		var bufferedBody []byte
		if r.Method == http.MethodPost && (isTorrents || isCache) {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 10<<20))
			_ = r.Body.Close()
			bufferedBody = body
			var pb struct {
				Action string `json:"action"`
				Link   string `json:"link"`
				Hash   string `json:"hash"`
			}
			_ = json.Unmarshal(body, &pb)
			bodyAction = strings.ToLower(strings.TrimSpace(pb.Action))
			infohash = torrbalancer.ExtractInfohash(pb.Link, pb.Hash)
			// Re-supply the body to the upstream — reset BOTH the reader and the
			// length, else the upstream sees an empty body and rejects "add".
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		} else {
			q := r.URL.Query()
			infohash = torrbalancer.ExtractInfohash(q.Get("link"), q.Get("hash"))
		}

		// Endpoints whose data spans backends (a user's torrents may be split
		// across the pool): fan out to all allowed+healthy backends and merge.
		// Only meaningful with >=2 backends — a single backend (the seeded /
		// legacy case) takes the normal passthrough below, unchanged.
		isListAll := strings.HasPrefix(low, "playlistall")
		if proxy.pool != nil && (isListAll || (isTorrents && bodyAction == "list")) {
			if bs := proxy.pool.AllEnabledHealthy(tsRequestBackendFilter(r)); len(bs) >= 2 {
				proxy.fanoutList(w, r, path, bs, isListAll)
				return
			}
		}

		host, authHdr, backend, ok := proxy.resolveTSBackend(r, infohash)
		if !ok {
			http.Error(w, `{"error":"no TorrServer backend available"}`, http.StatusServiceUnavailable)
			return
		}

		// POST /torrents (add/get/rem) — короткие API-вызовы: живой TorrServer
		// отвечает за миллисекунды. Раньше они шли 60-секундным стрим-клиентом,
		// и полу-зомби (Selectel 2026-08-13: /echo жив, /torrents висит) держал
		// «открываем…» до таймаута → «add http 502». Теперь: короткий дедлайн,
		// зависание = карантин бэкенда + один ретрай на другом здоровом.
		if r.Method == http.MethodPost && isTorrents && proxy.pool != nil && backend != nil {
			proxy.torrentsAPIWithFailover(w, r, path, bufferedBody, infohash, backend, tsRequestBackendFilter(r))
			return
		}

		// Special: /stream and /play with ?m3u — rewrite m3u content
		if (strings.HasPrefix(suffix, "stream") || strings.HasPrefix(suffix, "play")) &&
			r.URL.Query().Has("m3u") {
			proxy.handleM3U(w, r, path, host, authHdr)
			return
		}

		// Generic proxy
		proxy.proxyPassthrough(w, r, path, host, authHdr, backend, infohash)
	}
}

// tsSuffixRequiresAuth returns true for /ts/* endpoints that mutate server
// state or expose other users' data.  Read-only endpoints (stream, playlist,
// echo) stay open so set-top-box players without cookies keep working.
func tsSuffixRequiresAuth(suffix string, r *http.Request) bool {
	low := strings.ToLower(suffix)
	switch {
	case strings.HasPrefix(low, "torrents"),
		strings.HasPrefix(low, "playlistall"):
		return true
	case strings.HasPrefix(low, "settings"):
		// Lampa's "is TorrServer alive?" check POSTs /settings {action:"get"}
		// and treats ANY non-2xx (a 401) as "не удалось подключиться" → the
		// 6-step error wizard. A real MatriX server answers /settings reads
		// openly, so keep the read probe open (returns only non-sensitive
		// capacity fields — CacheSize, etc.) and gate only {action:"set"}.
		return !tsSettingsReadProbe(r)
	case strings.HasPrefix(low, "cache"):
		// /cache get is harmless; only protect when there's a body action != get.
		// The cheap heuristic: gate POST entirely.
		return r.Method == http.MethodPost
	}
	return false
}

// tsSettingsReadProbe reports whether a /settings request is a read-only
// capability probe ({action:"get"} or empty body) rather than a mutation
// ({action:"set"}). Buffers and restores the request body so downstream
// forwarding/decoding is unaffected.
func tsSettingsReadProbe(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return true // GET /settings is a read
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	var pb struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(body, &pb)
	action := strings.ToLower(strings.TrimSpace(pb.Action))
	return action == "" || action == "get"
}

// skipUpstreamHeader reports whether a header from the upstream TorrServer must
// NOT be copied into our response.
//
// The CORS family is the load-bearing part: a real TorrServer answers its API
// with `Access-Control-Allow-Origin: *`. globalCORSMiddleware has already set
// our own `Access-Control-Allow-Origin: <origin>` on the ResponseWriter, so
// copying the upstream's value ADDS a second one. The browser then rejects the
// response outright ("header contains multiple values 'http://lampa.mx, *',
// but only one is allowed") even though the request itself returned 200. This
// only bites CROSS-ORIGIN callers — a Lampa hosted on lampa.mx talking to our
// host — which is why /ts worked on our own domain and nowhere else: on the
// same origin the browser never runs the CORS check.
func skipUpstreamHeader(k string) bool {
	lk := strings.ToLower(k)
	switch lk {
	case "transfer-encoding", "connection", "content-security-policy":
		return true
	}
	// We own CORS end-to-end; never let the upstream's copy through.
	return strings.HasPrefix(lk, "access-control-")
}

// tsAuthOK reports whether the request carries a valid TorrServer credential
// (user token from cookie/query, Basic uid:ts resolved to a token, or an
// external Basic-Auth pass). When TG auth is disabled (tsTokenStoreRef nil)
// everything is open — single-user/box installs. Shared by the proxy gate and
// the in-process handlers so both enforce the same policy.
func tsAuthOK(r *http.Request) bool {
	if tsTokenStoreRef == nil {
		return true
	}
	if hasExternalAuth(r) {
		return true
	}
	return requestUserTokenFromAny(r) != ""
}

// requestUserTokenFromAny extracts the lampac_token from cookie or query.
// As a fallback it resolves the Basic credentials Lampa's native TorrServer
// client sends (ts.js configures login=<device uid>, password="ts"): the uid
// was bound to a token during device registration, so it maps back to the
// same user identity a cookie would. Without this, any client whose WebView
// drops cookies gets 401 on /ts mutations despite being fully authed.
func requestUserTokenFromAny(r *http.Request) string {
	if c, err := r.Cookie("lampac_token"); err == nil {
		if v := strings.TrimSpace(c.Value); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("token")); v != "" {
		return v
	}
	if tsTokenStoreRef != nil {
		if login, pass, ok := r.BasicAuth(); ok && pass == "ts" {
			if uid := strings.TrimSpace(login); uid != "" && uid != "ts" {
				if tok := tsTokenStoreRef.FindTokenByDeviceUID(uid); tok != "" {
					return tok
				}
			}
		}
	}
	return ""
}

// contentTypeByExt returns MIME type for common media extensions.  Used by
// the in-process stream handler; lives here (no build tag) so it stays
// callable from tests built without the torrs tag.
func contentTypeByExt(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".ts":
		return "video/mp2t"
	case ".m3u8":
		return "application/x-mpegURL"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mp3":
		return "audio/mpeg"
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".opus":
		return "audio/opus"
	case ".srt":
		return "application/x-subrip"
	case ".vtt":
		return "text/vtt"
	case ".ass", ".ssa":
		return "text/x-ssa"
	case ".sup":
		return "application/x-pgs"
	case ".idx", ".sub":
		return "application/x-subviewer"
	default:
		return "application/octet-stream"
	}
}

// handleM3U fetches an m3u playlist from the chosen backend and rewrites
// /stream/ to /ts/stream/.
func (p *tsProxy) handleM3U(w http.ResponseWriter, r *http.Request, path, host, authHdr string) {
	body, status, _, err := p.doRequest(r, path, host, authHdr)
	if err != nil {
		log.Warn().Err(err).Str("path", path).Msg("ts: m3u proxy failed")
		http.Error(w, "TorrServer unavailable", http.StatusBadGateway)
		return
	}

	original := string(body)
	log.Debug().Str("path", path).Int("status", status).Int("bodyLen", len(original)).Msg("ts: m3u proxied")

	content := strings.ReplaceAll(original, "/stream/", "/ts/stream/")

	w.Header().Set("Content-Type", "audio/x-mpegurl")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(content))
}

// proxyPassthrough proxies the request to the chosen backend and streams the
// response directly. host/authHdr identify the backend; backend (may be nil for
// the legacy single-upstream path) is used for connection stats + debug header.
func (p *tsProxy) proxyPassthrough(w http.ResponseWriter, r *http.Request, path, host, authHdr string, backend *torrbalancer.Backend, infohash string) {
	target := host + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Detect stream/play requests — these must not use conditional caching
	isStream := strings.Contains(path, "/stream/") || strings.Contains(path, "/play/")

	// Copy headers from client (except Authorization — we use our own)
	for k, vals := range r.Header {
		switch strings.ToLower(k) {
		case "authorization", "host", "connection":
			continue
		// Strip conditional/caching headers for stream requests
		// to prevent 304 responses that break audio/subtitle detection
		case "if-none-match", "if-modified-since", "if-range", "if-match":
			if isStream {
				log.Debug().Str("header", k).Str("value", vals[0]).Msg("ts: stripped cache header from stream request")
				continue
			}
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	req.Host = req.URL.Host

	// Set the chosen backend's auth.
	if authHdr != "" {
		req.Header.Set("Authorization", authHdr)
	}

	// Use streamClient (no total timeout) for stream/play so that
	// external players like VIMU/VLC don't get killed mid-playback.
	cl := p.client
	if isStream {
		cl = p.streamClient
	}

	// Track active connections on the chosen backend (stats + load metric).
	if backend != nil && p.pool != nil {
		p.pool.IncrConns(backend)
		defer p.pool.DecrConns(backend)
	}

	resp, err := cl.Do(req)
	if err != nil {
		if backend != nil && p.pool != nil {
			p.pool.MarkFailed(backend)
			// Stream circuit-breaker: the backend produced NO response for a
			// stream request (header timeout / reset). Client-side cancels are
			// excluded — a viewer zapping away says nothing about the backend.
			if isStream && !errors.Is(err, context.Canceled) && r.Context().Err() == nil {
				p.pool.RecordStreamStrike(backend, infohash)
			}
		}
		log.Warn().Err(err).Str("target", target).Msg("ts: proxy request failed")
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "timeout awaiting response headers") {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, "TorrServer unavailable", status)
		return
	}
	defer resp.Body.Close()

	// Response headers arrived — the backend is serving; clear breaker strikes.
	if isStream && backend != nil && p.pool != nil {
		p.pool.RecordStreamOK(backend)
	}

	// Log request/response details for stream debugging
	log.Debug().
		Str("method", r.Method).
		Str("target", target).
		Int("status", resp.StatusCode).
		Bool("isStream", isStream).
		Str("contentType", resp.Header.Get("Content-Type")).
		Str("contentLength", resp.Header.Get("Content-Length")).
		Str("rangeReq", r.Header.Get("Range")).
		Str("contentRange", resp.Header.Get("Content-Range")).
		Msg("ts: proxyPassthrough")

	// Copy response headers (skip some internal ones)
	for k, vals := range resp.Header {
		if skipUpstreamHeader(k) {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	if backend != nil && p.pool != nil && p.pool.CurrentSettings().DebugHeader {
		w.Header().Set("X-Lampac-TS-Backend", backend.Name)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// tsTokenStoreRef is a package-level reference to the TG token store,
// set during server startup. Used by tsAccessMiddleware to check per-user access.
var tsTokenStoreRef *tgauth.Store

// tsAccessMiddleware checks per-user TorrServer access.
// If the user's token has torrserver_disabled=true, returns 403.
func tsAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /ts/echo is used for health checks — always allow.
		path := r.URL.Path
		if path == "/ts/echo" || path == "/echo" {
			next.ServeHTTP(w, r)
			return
		}
		if tsTokenStoreRef != nil {
			// Same resolution order as the mutation guard (cookie → query →
			// Basic uid:ts) so per-user/group bans hold for cookie-less clients.
			token := requestUserTokenFromAny(r)
			if token != "" && tsTokenStoreRef.IsTorrServerDisabled(token) {
				http.Error(w, `{"error":"TorrServer access disabled for this user"}`, http.StatusForbidden)
				return
			}
			// Check group-level TorrServer access. Uses effective group
			// (premium overlay takes precedence over base GroupID while
			// PremiumUntil is in the future).
			if token != "" && groupStoreRef != nil && tgTokenStoreRef != nil {
				groupID := tgTokenStoreRef.GetEffectiveGroup(token, currentPremiumGroupID())
				if g, _ := groupStoreRef.Get(groupID); !g.TorrServer {
					http.Error(w, `{"error":"TorrServer access disabled for your group"}`, http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// registerTSProxyRoutes registers TorrServer routes in reverse-proxy mode.
// Used when running without in-process torrent server (no torrs build tag)
// or when an external TorrServer URL is configured.
func registerTSProxyRoutes(router chi.Router, cfg config.Config) {
	if cfg.TorrServer.Port > 0 || cfg.TorrServer.URL != "" {
		tsProxyPtr.Store(newTSProxy(cfg))
		initTorrServerProcess(cfg)
	}
	router.Group(func(r chi.Router) {
		r.Use(tsExternalAuthMiddleware)
		r.Use(tsAccessMiddleware)
		r.Get("/ts", tsEntryHandler(cfg))
		r.Get("/ts/static/js/{suffix}", tsStaticJSHandler(cfg))
		r.HandleFunc("/ts/*", tsAPIHandler(cfg))
	})
}

// doRequest makes a buffered request to the given backend and returns the full body.
func (p *tsProxy) doRequest(r *http.Request, path, host, authHdr string) ([]byte, int, string, error) {
	return p.doRequestWith(p.client, r, path, host, authHdr)
}

func (p *tsProxy) doRequestWith(cl *http.Client, r *http.Request, path, host, authHdr string) ([]byte, int, string, error) {
	target := host + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		return nil, 0, "", err
	}

	for k, vals := range r.Header {
		switch strings.ToLower(k) {
		case "authorization", "host", "connection":
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	req.Host = req.URL.Host

	if authHdr != "" {
		req.Header.Set("Authorization", authHdr)
	}

	resp, err := cl.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, resp.StatusCode, "", err
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	return body, resp.StatusCode, ct, nil
}

// torrentsAPIWithFailover proxies a buffered POST /torrents call with the short
// apiClient deadline. A transport failure (в норме — зависший полу-зомби, у
// живого TorrServer это миллисекунды) карантинит бэкенд и ОДИН раз ретраит на
// следующем здоровом, чтобы «открываем…» у зрителя не умирал вместе с больным
// сервером. Отвал клиента (context canceled) бэкенду в вину не ставится.
func (p *tsProxy) torrentsAPIWithFailover(w http.ResponseWriter, r *http.Request, path string, body []byte, infohash string, first *torrbalancer.Backend, allow func(string) bool) {
	tried := map[string]bool{}
	backend := first
	for attempt := 0; attempt < 2 && backend != nil; attempt++ {
		tried[backend.ID] = true
		host, authHdr := backend.Target()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		p.pool.IncrConns(backend)
		respBody, status, ct, err := p.doRequestWith(p.apiClient, r, path, host, authHdr)
		p.pool.DecrConns(backend)
		if err == nil {
			w.Header().Set("Content-Type", ct)
			if p.pool.CurrentSettings().DebugHeader {
				w.Header().Set("X-Lampac-TS-Backend", backend.Name)
			}
			w.WriteHeader(status)
			_, _ = w.Write(respBody)
			return
		}
		if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
			return // зритель ушёл — это не отказ бэкенда
		}
		p.pool.MarkFailed(backend)
		p.pool.QuarantineWedged(backend, "torrents API не ответил: "+err.Error())
		log.Warn().Err(err).Str("backend", host).Str("path", path).
			Msg("ts: torrents API failed — backend quarantined, retrying on another")
		backend = p.pool.PickForHash(infohash, func(id string) bool {
			if tried[id] {
				return false
			}
			return allow == nil || allow(id)
		})
	}
	http.Error(w, "TorrServer unavailable", http.StatusBadGateway)
}
