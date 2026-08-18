package httpapi

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"

	"lampac-go/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// registerSystemRoutes wires up health/readiness, Prometheus metrics,
// net/http/pprof endpoints, and small diagnostic handlers. These have no
// cross-dependencies with other subsystems — only cfg for paths and the
// syncAPIHandler.
//
// /metrics and /debug/pprof/* are gated by metricsAccessGuard — see
// ObservabilityConfig.MetricsAuth.
func registerSystemRoutes(router chi.Router, cfg config.Config) {
	router.Get(cfg.Observability.HealthPath, healthHandler)
	router.Get(cfg.Observability.ReadyPath, readyHandler())

	guard := metricsAccessGuard(cfg.Observability.MetricsAuth)
	router.Handle(cfg.Observability.MetricsPath, guard(promhttp.Handler()))

	router.Handle("/debug/pprof/", guard(http.HandlerFunc(pprof.Index)))
	router.Handle("/debug/pprof/cmdline", guard(http.HandlerFunc(pprof.Cmdline)))
	router.Handle("/debug/pprof/profile", guard(http.HandlerFunc(pprof.Profile)))
	router.Handle("/debug/pprof/symbol", guard(http.HandlerFunc(pprof.Symbol)))
	router.Handle("/debug/pprof/trace", guard(http.HandlerFunc(pprof.Trace)))
	router.Handle("/debug/pprof/heap", guard(pprof.Handler("heap")))
	router.Handle("/debug/pprof/goroutine", guard(pprof.Handler("goroutine")))
	router.Handle("/debug/pprof/allocs", guard(pprof.Handler("allocs")))

	router.Get("/headers", headersEchoHandler)
	router.Get("/geo", geoHandler)
	router.Get("/myip", myIPHandler)
	router.Get("/reqinfo", reqInfoHandler)
	router.Get("/api/chromium/ping", chromiumPingHandler)
	router.Get("/api/chromium/iframe", chromiumIframeHandler)
	router.Get("/api/sync", syncAPIHandler())
}

// metricsAccessGuard returns a middleware enforcing access control on
// /metrics + pprof. Behavior depends on cfg.Observability.MetricsAuth:
//
//   - ""        → localhost only (127.0.0.1, ::1). Safe default for any
//     server that may face the internet without a private VPN.
//   - "open"    → no check (legacy behavior for trusted internal LANs).
//   - <token>   → either `Authorization: Bearer <token>` OR HTTP Basic
//     ("metrics:<token>"). Constant-time compared.
func metricsAccessGuard(rule string) func(http.Handler) http.Handler {
	rule = strings.TrimSpace(rule)
	switch rule {
	case "open":
		return func(h http.Handler) http.Handler { return h }
	case "":
		return func(h http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !requestIsLoopback(r) {
					http.Error(w, "metrics endpoints are restricted to localhost; set [observability] metrics_auth=\"<token>\" or \"open\" to override", http.StatusForbidden)
					return
				}
				h.ServeHTTP(w, r)
			})
		}
	}
	// Token-based.
	expected := []byte(rule)
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if checkMetricsToken(r, expected) {
				h.ServeHTTP(w, r)
				return
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="metrics", Bearer`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}

func checkMetricsToken(r *http.Request, expected []byte) bool {
	// Bearer.
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
			if subtle.ConstantTimeCompare([]byte(tok), expected) == 1 {
				return true
			}
		}
		if _, pass, ok := r.BasicAuth(); ok {
			if subtle.ConstantTimeCompare([]byte(pass), expected) == 1 {
				return true
			}
		}
	}
	return false
}

// requestIsLoopback returns true if r originates from 127.0.0.1 / ::1 /
// Unix socket. Used by the default metrics guard. Trusts r.RemoteAddr
// directly — does NOT honor X-Forwarded-For (those are user-controlled).
func requestIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
