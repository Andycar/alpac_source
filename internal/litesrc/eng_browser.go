package litesrc

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/browser"
	"lampac-go/internal/httpclient"

	"github.com/rs/zerolog/log"
)

// Shared browser-based m3u8/mp4 sniffer for the ENG balancers (vidsrc, videasy,
// vidlink, twoembed, hydraflix, …). These sites embed an obfuscated player that
// only reveals the real stream URL at runtime, so — exactly like the upstream
// C# `black_magic` methods — we load the embed page in a headless browser and
// intercept the first network request that looks like a manifest.
//
// The heavy lifting rides our browser.Engine facade (chromedp by default), so
// no Playwright/Node dependency. One session per resolve; callers cache the
// result. Streams are Cloudflare-fronted, so a resolve that works locally may
// still need the server's egress/proxy to fetch segments — that's why callers
// proxy the captured URL with its request headers.

// engSniffResult is a captured stream URL plus the request headers the browser
// sent for it (some CDNs sign on Origin/Referer, so we replay them).
type engSniffResult struct {
	URL     string
	Headers map[string]string
}

// engBrowserSniff loads embedURL headless, aborts obvious ads, and returns the
// first request URL matching wantRe together with its (filtered) headers. If
// clickJS is non-empty it is evaluated ~2.5s after navigation to trigger a
// play button that some players require before requesting the manifest.
func engBrowserSniff(ctx context.Context, plugin, embedURL string, wantRe, abortRe *regexp.Regexp, clickJS string, timeout time.Duration) (engSniffResult, bool) {
	if strings.TrimSpace(embedURL) == "" || wantRe == nil {
		return engSniffResult{}, false
	}
	eng := browser.ForBalancer(plugin)
	if eng == nil {
		log.Warn().Str("plugin", plugin).Msg("eng: no browser engine registered")
		return engSniffResult{}, false
	}
	if err := eng.Available(); err != nil {
		log.Warn().Str("plugin", plugin).Err(err).Msg("eng: browser engine unavailable")
		return engSniffResult{}, false
	}

	sessCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	session, err := eng.NewSession(sessCtx, browser.SessionOptions{
		Headless:          true,
		SocksProxy:        httpclient.SocksAddrForBalancer(plugin),
		UserAgent:         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		NavigationTimeout: timeout,
	})
	if err != nil {
		log.Warn().Str("plugin", plugin).Err(err).Msg("eng: NewSession failed")
		return engSniffResult{}, false
	}
	defer session.Close()

	var (
		mu   sync.Mutex
		got  engSniffResult
		done = make(chan struct{}, 1)
	)

	hijackCancel, err := session.Hijack([]string{"*"}, func(req browser.HijackRequest) {
		u := req.URL()
		if wantRe.MatchString(u) {
			mu.Lock()
			if got.URL == "" {
				got.URL = u
				got.Headers = engFilterHeaders(req.Headers())
				select {
				case done <- struct{}{}:
				default:
				}
			}
			mu.Unlock()
			// Abort so the browser doesn't download the manifest body — we
			// only wanted its URL + request headers.
			_ = req.Abort("Aborted")
			return
		}
		if abortRe != nil && abortRe.MatchString(u) {
			_ = req.Abort("BlockedByClient")
			return
		}
		// Drop heavy media/decoration that can't be the manifest; keep
		// document/script/xhr/fetch so the player can bootstrap.
		switch strings.ToLower(req.ResourceType()) {
		case "media", "image", "font", "stylesheet":
			_ = req.Abort("Aborted")
			return
		}
		_ = req.Continue()
	})
	if err != nil {
		log.Warn().Str("plugin", plugin).Err(err).Msg("eng: Hijack setup failed")
		return engSniffResult{}, false
	}
	defer hijackCancel()

	go func() {
		if err := session.Navigate(embedURL); err != nil {
			log.Debug().Str("plugin", plugin).Err(err).Msg("eng: Navigate returned (non-fatal)")
		}
	}()

	if strings.TrimSpace(clickJS) != "" {
		go func() {
			select {
			case <-time.After(2500 * time.Millisecond):
				_ = session.Eval(clickJS, nil)
			case <-sessCtx.Done():
			}
		}()
	}

	select {
	case <-done:
		mu.Lock()
		r := got
		mu.Unlock()
		return r, r.URL != ""
	case <-sessCtx.Done():
		return engSniffResult{}, false
	}
}

// engFilterHeaders flattens request headers into the map proxylink needs,
// dropping hop-by-hop/host headers that must not be replayed upstream.
func engFilterHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) == 0 {
			continue
		}
		switch strings.ToLower(k) {
		case "host", "accept-encoding", "connection", "range", "content-length":
			continue
		}
		out[k] = v[0]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
