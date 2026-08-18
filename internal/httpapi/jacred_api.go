package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/rutracker"

	"github.com/rs/zerolog/log"
)

// defaultJacRedHost is the fallback parser upstream when Parser.JacRedHost is
// cleared in the config. Mirrors the config default (config.go) — the routes
// are always registered, so the proxy must always have somewhere to forward.
const defaultJacRedHost = "https://jacred.stream"

// jacredProxyHandler proxies torrent search requests to a JacRed API.
// Routes:
//   - /api/v2.0/indexers/{status}/results  (Jackett-compatible format)
//   - /api/v1.0/torrents                   (simplified format)
//   - /api/v1.0/conf                       (configuration endpoint)
//   - /parse/{tracker}/{id}                (magnet link resolution)
//
// Upstream selection: when [parser] jacred_local is enabled and the managed
// local jacred-fdb instance is healthy, requests go to 127.0.0.1; otherwise
// (and on local connection failure) they fall back to Parser.JacRedHost.
func jacredProxyHandler(cfg config.Config) http.HandlerFunc {
	client := httpclient.New(15 * time.Second)

	return func(w http.ResponseWriter, r *http.Request) {
		// Live config so an admin upstream change applies without restart.
		liveCfg := liveConfig(cfg)
		var localBase string
		if mgr := liveJacredMgr(); liveCfg.Parser.JacRedLocal && mgr != nil && mgr.Healthy() {
			localBase = mgr.BaseURL()
		}
		external := strings.TrimRight(liveCfg.Parser.JacRedHost, "/")
		if external == "" {
			external = defaultJacRedHost
		}
		apiKey := liveCfg.Parser.JacRedKey

		// Build upstream path + query, inject apikey if configured.
		q := r.URL.Query()
		// Fold Latin diacritics in the title params before they reach jacred —
		// "Déjà Vu" matches nothing on trackers and drags in DJVU e-books instead
		// (see stripLatinDiacritics). Done here so every client benefits.
		if normalizeJacredSearchQuery(q) {
			log.Debug().Str("title", q.Get("title")).Str("original", q.Get("title_original")).
				Str("search", q.Get("search")).Msg("jacred: folded diacritics in search query")
		}
		if apiKey != "" {
			q.Set("apikey", apiKey)
		}
		pathAndQuery := r.URL.Path
		if encoded := q.Encode(); encoded != "" {
			pathAndQuery += "?" + encoded
		}

		// Buffer the (tiny) request body so a fallback retry can resend it.
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		}

		// --- second parser fan-out ------------------------------------
		// [parser] jacred_host2: search endpoints are queried on BOTH
		// upstreams at once; the secondary's rows are merged into the
		// primary answer below. Fired before the primary loop so both
		// requests overlap instead of running back to back.
		var secondaryCh chan []byte
		if host2 := strings.TrimRight(liveCfg.Parser.JacRedHost2, "/"); host2 != "" && jacredSearchPath(r.URL.Path) {
			q2 := r.URL.Query()
			normalizeJacredSearchQuery(q2)
			if liveCfg.Parser.JacRedKey2 != "" {
				q2.Set("apikey", liveCfg.Parser.JacRedKey2)
			} else {
				q2.Del("apikey")
			}
			target := host2 + r.URL.Path
			if encoded := q2.Encode(); encoded != "" {
				target += "?" + encoded
			}
			secondaryCh = make(chan []byte, 1)
			go func() {
				defer close(secondaryCh)
				req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
				if err != nil {
					return
				}
				req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
				resp2, err := client.Do(req)
				if err != nil {
					log.Warn().Err(err).Str("target", target).Msg("jacred: second parser failed")
					return
				}
				defer resp2.Body.Close()
				if resp2.StatusCode >= 400 {
					log.Warn().Int("status", resp2.StatusCode).Str("target", target).Msg("jacred: second parser answered an error")
					return
				}
				b, _ := io.ReadAll(io.LimitReader(resp2.Body, 32<<20))
				secondaryCh <- b
			}()
		}

		upstreams := make([]string, 0, 2)
		if localBase != "" {
			upstreams = append(upstreams, localBase)
		}
		upstreams = append(upstreams, external)

		var resp *http.Response
		for i, upstream := range upstreams {
			target := upstream + pathAndQuery
			req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
			if err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
			if ct := r.Header.Get("Content-Type"); ct != "" {
				req.Header.Set("Content-Type", ct)
			}
			if acc := r.Header.Get("Accept"); acc != "" {
				req.Header.Set("Accept", acc)
			}

			log.Debug().Str("target", target).Msg("jacred: proxying request")
			resp, err = client.Do(req)
			if err == nil {
				break
			}
			resp = nil
			if i < len(upstreams)-1 {
				log.Warn().Err(err).Str("target", target).Msg("jacred: local instance failed, falling back to external")
				continue
			}
			log.Warn().Err(err).Str("target", target).Msg("jacred: upstream request failed")
		}
		// --- merge: second parser + RuTracker ----------------------------
		// jacred structurally cannot return rutracker rows (the tracker needs
		// an account), so we search it ourselves and append to the upstream
		// answer; the second parser's rows ride the same buffered path. The
		// merge owns the response body from here on, which is why the
		// upstream is buffered instead of streamed. It degrades in every
		// direction: nothing to merge → the upstream body ships untouched;
		// a dead upstream → the merged rows ship alone.
		var secondary []byte
		if secondaryCh != nil {
			secondary = <-secondaryCh
		}
		rows := jacredRutrackerRows(r, liveCfg)
		if len(rows) > 0 || len(secondary) > 0 {
			var upstream []byte
			var status int
			if resp != nil {
				upstream, _ = io.ReadAll(io.LimitReader(resp.Body, 32<<20))
				status = resp.StatusCode
				resp.Body.Close()
			}

			base := hostFromRequest(r)
			v2 := strings.HasPrefix(r.URL.Path, "/api/v2.0/indexers/")
			merged := upstream
			if len(secondary) > 0 {
				if v2 {
					merged = mergeParserV2(merged, secondary)
				} else {
					merged = mergeParserV1(merged, secondary)
				}
			}
			if len(rows) > 0 {
				if v2 {
					merged = mergeRutrackerV2(merged, rows, base)
				} else {
					merged = mergeRutrackerV1(merged, rows, base)
				}
			}

			// An upstream error must not shadow rows we do have.
			if status == 0 || status >= 400 {
				status = http.StatusOK
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			jacredCORSHeaders(w)
			w.WriteHeader(status)
			_, _ = w.Write(merged)
			log.Debug().Int("rutracker_rows", len(rows)).Int("secondary_bytes", len(secondary)).
				Int("upstream_bytes", len(upstream)).Str("path", r.URL.Path).Msg("jacred: merged parser answers")
			return
		}

		if resp == nil {
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// Copy response headers
		for _, key := range []string{"Content-Type", "Cache-Control"} {
			if v := resp.Header.Get(key); v != "" {
				w.Header().Set(key, v)
			}
		}
		// Allow CORS for Lampa client
		jacredCORSHeaders(w)

		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

// jacredSearchPath reports whether the proxied path is a torrent search — the
// endpoints that fan out to the second parser and take the rutracker merge
// (as opposed to /api/v1.0/conf and /parse/*, which stay primary-only).
func jacredSearchPath(p string) bool {
	return strings.HasPrefix(p, "/api/v2.0/indexers/") || p == "/api/v1.0/torrents"
}

// jacredRutrackerRows runs the native rutracker search for a proxied parser
// request. Returns nil whenever the source is off, not allowed for this
// request, or the endpoint is not a search (e.g. /api/v1.0/conf, /parse/*).
func jacredRutrackerRows(r *http.Request, cfg config.Config) []rutracker.Release {
	if !jacredSearchPath(r.URL.Path) {
		return nil
	}
	client := rutrackerReady(cfg)
	if client == nil || !rutrackerAllowed(r, cfg) {
		return nil
	}

	q := r.URL.Query()
	normalizeJacredSearchQuery(q) // rutracker spells releases in ASCII too — see stripLatinDiacritics
	// v2 uses title/title_original; v1 uses search.
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		title = strings.TrimSpace(q.Get("search"))
	}
	original := strings.TrimSpace(q.Get("title_original"))
	if title == "" {
		title = original
	}
	if title == "" {
		return nil
	}

	ctx, cancel := rutrackerCtx(r)
	defer cancel()
	return rutrackerSearch(ctx, client, title, original)
}

func jacredCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
}

// jacredCORSHandler handles OPTIONS preflight for jacred endpoints.
func jacredCORSHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.WriteHeader(http.StatusNoContent)
	}
}
