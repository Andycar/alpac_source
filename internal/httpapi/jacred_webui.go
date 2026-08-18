package httpapi

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// jacredWebUIHandler exposes the *local* jacred-fdb web interface (search,
// stats, settings) under /jacred/ — the same pattern as /ts for TorrServer.
// The instance itself listens on 127.0.0.1 only; this proxy is the sole way
// in, and the path is NOT on gatePreAuthAllowed, so it sits behind the normal
// lampac authorization gate.
//
// The UI's own API calls need no rewriting: it hardcodes absolute
// /api/v1.0/... paths, which this server already proxies at top level.
// Only root-relative navigation links (/stats, /settings, …) in HTML pages
// are rewritten to stay under the /jacred/ prefix.
func jacredWebUIHandler() http.HandlerFunc {
	client := &http.Client{
		Timeout: 30 * time.Second,
		// Pass redirects through to the browser (rewritten below) instead of
		// following them server-side.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	return func(w http.ResponseWriter, r *http.Request) {
		mgr := liveJacredMgr()
		if mgr == nil || !liveConfig(config.Config{}).Parser.JacRedLocal {
			http.Error(w, "Локальный JacRed выключен. Включите [parser] jacred_local = true (см. wiki → Парсер).", http.StatusServiceUnavailable)
			return
		}

		// When a foreign process holds our port, proxying would forward to it and
		// surface its bare "404 page not found" (or whatever it serves) — which
		// looks like the panel is broken. Return the actionable cause instead.
		if mgr.PortConflict() {
			http.Error(w, "Конфликт порта: на порту локального JacRed отвечает другой процесс (внешний jac.red на этом сервере?). Остановите его или смените [parser] jacred_local_port на свободный порт.", http.StatusBadGateway)
			return
		}

		upstreamPath := strings.TrimPrefix(r.URL.Path, "/jacred")
		if upstreamPath == "" {
			upstreamPath = "/"
		}
		target := mgr.BaseURL() + upstreamPath
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}

		req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		for _, h := range []string{"Accept", "Content-Type", "If-None-Match", "If-Modified-Since", "Range"} {
			if v := r.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		// Uncompressed upstream so HTML can be rewritten.
		req.Header.Set("Accept-Encoding", "identity")

		resp, err := client.Do(req)
		if err != nil {
			log.Warn().Err(err).Str("target", target).Msg("jacred webui: upstream failed")
			http.Error(w, "Локальный JacRed не отвечает (устанавливается или перезапускается) — попробуйте позже.", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// Keep redirects inside the /jacred/ prefix.
		if loc := resp.Header.Get("Location"); loc != "" && strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") && !strings.HasPrefix(loc, "/jacred") {
			resp.Header.Set("Location", "/jacred"+loc)
		}

		for _, h := range []string{"Content-Type", "Cache-Control", "ETag", "Last-Modified", "Location", "Accept-Ranges", "Content-Range"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}

		if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
			if err != nil {
				http.Error(w, "upstream read error", http.StatusBadGateway)
				return
			}
			body = rewriteJacredHTML(body)
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(body)
			return
		}

		if cl := resp.Header.Get("Content-Length"); cl != "" {
			w.Header().Set("Content-Length", cl)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

// jacredRootLinkRe matches root-relative href/src/action attributes
// (protocol-relative "//…" URLs excluded by the [^/"] guard).
var jacredRootLinkRe = regexp.MustCompile(`(href|src|action)="/([^/"][^"]*)"`)

// rewriteJacredHTML re-roots absolute navigation links of the jacred web UI
// under the /jacred/ prefix. Static assets are referenced relatively ("./css…")
// and need no rewriting; API calls go to /api/v1.0/* which this server proxies
// at top level anyway. Idempotent: already-prefixed links are left alone.
func rewriteJacredHTML(body []byte) []byte {
	s := jacredRootLinkRe.ReplaceAllStringFunc(string(body), func(m string) string {
		sub := jacredRootLinkRe.FindStringSubmatch(m)
		if strings.HasPrefix(sub[2], "jacred/") {
			return m
		}
		return sub[1] + `="/jacred/` + sub[2] + `"`
	})
	// Bare root link (the regex requires a non-"/" first path char).
	s = strings.ReplaceAll(s, `href="/"`, `href="/jacred/"`)
	return []byte(s)
}
