package httpapi

import (
	"bufio"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// normalizeRequestPathMiddleware strips trailing whitespace from the request
// path before routing and the auth gate run. Malformed plugin URLs stored in
// cub accounts sometimes carry a trailing space (".../ts.js "), which arrives
// as "/ts.js%20". Untrimmed, two things break at once: chi can't match the
// "/ts.js" route, and filepath.Ext("/ts.js ") returns ".js " so the auth gate's
// .js whitelist misses — the gate then 302-redirects the plugin fetch to the
// HTML /tg/auth page, and Lampa throws a SyntaxError trying to parse HTML as JS.
// Trailing whitespace is never meaningful for any of our routes, so trimming it
// is safe and makes every downstream layer see a clean path.
func normalizeRequestPathMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; p != "" {
			if trimmed := strings.TrimRight(p, " \t\r\n"); trimmed != p {
				r.URL.Path = trimmed
				r.URL.RawPath = "" // force EscapedPath() to recompute from Path
			}
		}
		next.ServeHTTP(w, r)
	})
}

// globalCORSMiddleware adds CORS headers to all responses.
// Lampa clients load plugins from one server (e.g. lampa.mx) but the plugins
// make AJAX calls to our server — this requires CORS. Without these headers
// Android WebView blocks cross-origin API requests from online.js.
func globalCORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		// X-Lampac-Token / X-Lampac-Profile-Session carry the TG device
		// token and the self-hosted profile session token respectively;
		// see auth.go resolveUser. Both must be in this allowlist or
		// CORS preflight will block any cross-origin XHR that sets them
		// (the canonical scenario: Lampa player on lampa.mx hitting a
		// lampac server on a different host — without these headers
		// listed here, the OPTIONS preflight 200s but the actual request
		// never fires).
		// X-Alpac-App/Sid/Ts/Nonce/Sig are the signed-/capi handshake+request headers. They were
		// absent here because the SIDELOAD web client is same-origin (no CORS/preflight). The STORE
		// widget (self-contained Tizen bundle → talks to the server CROSS-ORIGIN) sends these on
		// every signed request; the browser preflights any request with custom headers, so without
		// them listed the OPTIONS 204s but the real signed /capi call is blocked → store widget can't
		// authenticate or resolve streams after the user configures the server.
		// CMCD-* are the CTA-5004 telemetry headers a player may attach to segment
		// requests. Players that use header mode (ExoPlayer's default, and Shaka
		// when configured) preflight cross-origin — without these listed the
		// browser silently drops the telemetry while playback itself works, which
		// is the hardest kind of gap to notice.
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-Kit-AesGcm, X-Lampac-Go, X-Telegram-Init-Data, X-Lampac-Token, X-Lampac-Profile-Session, X-Alpac-Token, X-Alpac-Profile-Session, X-User-Uid, X-Alpac-App, X-Alpac-Sid, X-Alpac-Ts, X-Alpac-Nonce, X-Alpac-Sig, CMCD-Object, CMCD-Request, CMCD-Session, CMCD-Status")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Set-Cookie")

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-Id")
		if reqID == "" {
			reqID = strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.Itoa(rand.Intn(1000))
		}

		w.Header().Set("X-Request-Id", reqID)
		next.ServeHTTP(w, r.WithContext(log.With().Str("request_id", reqID).Logger().WithContext(r.Context())))
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		done := runtimeRequestStats.begin()

		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(ww, r)

		elapsed := time.Since(start)
		// Feed the percentiles/leaderboard with TIME-TO-FIRST-BYTE (server think time), not
		// time-to-last-byte: streamed bodies and WS sessions otherwise dominate Avg/P99 with
		// client bandwidth / session length. Full elapsed still goes to the request log.
		ttfb := elapsed
		if !ww.firstWrite.IsZero() {
			ttfb = ww.firstWrite.Sub(start)
		}
		done(ttfb)
		runtimeRequestStats.observeRoute(requestRouteKey(r), ww.status, ttfb)

		runtimeTrafficStats.RecordTotal(ww.bytes)

		log.Ctx(r.Context()).
			Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", ww.status).
			Dur("elapsed", elapsed).
			Msg("request")
	})
}

func requestRouteKey(r *http.Request) string {
	path := strings.ToLower(strings.TrimSpace(r.URL.Path))
	if path == "" || path == "/" {
		return "/"
	}

	if strings.HasPrefix(path, "/proxy/") {
		pl := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("pl")))
		if pl != "" {
			return "/proxy:" + pl
		}
		return "/proxy"
	}
	if strings.HasPrefix(path, "/proxyimg/") {
		return "/proxyimg"
	}
	if strings.HasPrefix(path, "/lite/") {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) >= 3 {
			return "/lite/" + parts[1] + "/" + parts[2]
		}
		if len(parts) >= 2 {
			return "/lite/" + parts[1]
		}
		return "/lite"
	}
	if strings.HasPrefix(path, "/cp_") {
		return "/admin"
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "/"
	}
	if len(parts) == 1 {
		return "/" + parts[0]
	}
	if looksLikeDynamicPathPart(parts[1]) {
		return "/" + parts[0] + "/{id}"
	}
	return "/" + parts[0] + "/" + parts[1]
}

func looksLikeDynamicPathPart(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if _, err := strconv.Atoi(s); err == nil {
		return true
	}
	if len(s) >= 24 {
		return true
	}

	digits := 0
	hexChars := 0
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			digits++
		}
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') {
			hexChars++
		}
	}
	if digits >= 4 {
		return true
	}
	// UUID/hash-like token.
	if strings.Contains(s, "-") && len(s) >= 16 && hexChars >= len(s)-2 {
		return true
	}
	return false
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Ctx(r.Context()).
					Error().
					Any("panic", rec).
					Bytes("stack", debug.Stack()).
					Msg("panic recovered")
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	// firstWrite = when the handler produced its first byte/headers. Latency stats use
	// THIS (server think time / TTFB) instead of full elapsed: measuring to last byte made
	// every streamed segment count the CLIENT's download speed — a TV pulling a 50MB chunk
	// over WiFi looked like a 40-second "slow request" (P99 35s, avg 1s on the dashboard)
	// while the server was perfectly healthy.
	firstWrite time.Time
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.firstWrite.IsZero() {
		w.firstWrite = time.Now()
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) WriteHeader(code int) {
	if w.firstWrite.IsZero() {
		w.firstWrite = time.Now()
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Hijack implements http.Hijacker so WebSocket upgrades work
// through the loggingMiddleware wrapper.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// Flush implements http.Flusher so SSE (Server-Sent Events) streaming works
// through the loggingMiddleware wrapper.
func (w *statusWriter) Flush() {
	if fl, ok := w.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}
