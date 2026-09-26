package litesrc

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"lampac-go/internal/httpclient"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/browsertmp"

	stdjson "encoding/json"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/rs/zerolog/log"
)

var allohaMoviesRe = regexp.MustCompile(`/movies/\d+`)

// allohaXHRHookJS intercepts XMLHttpRequest.setRequestHeader and window.fetch
// to capture CDN auth headers (Authorizations, Accepts-Controls) that the Alloha
// player JS adds programmatically. These headers are invisible to the CDP Fetch
// domain at RequestStageRequest, so we capture them via console.log messages.
const allohaXHRHookJS = `(function(){
window.__allohaCDNHeaders={};
var origSetHeader=XMLHttpRequest.prototype.setRequestHeader;
XMLHttpRequest.prototype.setRequestHeader=function(n,v){
var nl=n.toLowerCase();
if(nl==='authorizations'||nl==='accepts-controls'||nl==='borth'){
window.__allohaCDNHeaders[n]=v;
console.log('__ALLOHA_HDR__:'+n+':'+v);
}
return origSetHeader.call(this,n,v);
};
var origFetch=window.fetch;
if(origFetch){window.fetch=function(input,init){
if(init&&init.headers){
var entries;
if(init.headers instanceof Headers){entries=[];init.headers.forEach(function(v,k){entries.push([k,v]);});}
else if(Array.isArray(init.headers)){entries=init.headers;}
else{entries=Object.entries(init.headers);}
for(var i=0;i<entries.length;i++){
var nl=entries[i][0].toLowerCase();
if(nl==='authorizations'||nl==='accepts-controls'||nl==='borth'){
window.__allohaCDNHeaders[entries[i][0]]=entries[i][1];
console.log('__ALLOHA_HDR__:'+entries[i][0]+':'+entries[i][1]);
}}}
return origFetch.apply(this,arguments);
};}
})();`

// allohaResolveViaBrowser uses headless Chrome to intercept the /movies/ API
// call and CDN auth headers (Authorizations: Bearer, Accepts-Controls) generated
// by the Alloha player JS.
//
// Flow:
//  1. Fetch player HTML from linkhost with token_movie + translation params
//  2. Strip iframe check, inject into Chrome via Fetch domain
//  3. JS loads, generates auth headers, calls POST /bnsi/movies/{id}
//  4. Intercept the POST, proxy it through Go HTTP (CDN blocks headless Chrome)
//  5. Parse response for m3u8 URL, capture auth headers
//  6. If no m3u8 from /movies/, also intercept direct .m3u8 requests as fallback
//
// allohaResolveResult holds everything from a browser resolve session.
type allohaResolveResult struct {
	hlsURL        string
	headers       map[string]string
	moviesURL     string            // POST URL for lightweight refresh
	moviesHeaders map[string]string // headers for lightweight refresh
	moviesBody    []byte            // POST body for lightweight refresh
	// headerUpdates is non-nil when keepopen=true. The browser tab stays alive
	// and sends fresh header maps whenever the player's XHR hook captures new
	// Borth/AC (e.g. after WS edge_hash rotation). Closed when keepalive expires.
	headerUpdates <-chan map[string]string
	// browserCtx is the Chrome tab context when keepopen=true. Can be used
	// to run fetch() in the player tab for downloading segments with the
	// browser's native Guard + WS + TLS session.
	browserCtx context.Context
}

func allohaResolveViaBrowser(ctx context.Context, linkHost, token, tokenMovie, translation string, season, episode int) (hlsURL string, headers map[string]string, ok bool) {
	res := allohaResolveViaBrowserFull(ctx, linkHost, token, tokenMovie, translation, season, episode, 0, "")
	if res == nil {
		return "", nil, false
	}
	return res.hlsURL, res.headers, res.hlsURL != ""
}

// allohaResolveViaBrowserKP is the same as allohaResolveViaBrowser but with a
// kinopoisk ID that is used to pre-navigate Chrome through kinomix.web.app/movie/{kpID}
// before visiting the alloha linkhost page. This provides full Chrome masking:
// real kinomix browsing history, cookies, Service Worker registration, etc.
func allohaResolveViaBrowserKP(ctx context.Context, linkHost, token, tokenMovie, translation string, season, episode int, kpID string) (hlsURL string, headers map[string]string, ok bool) {
	res := allohaResolveViaBrowserFull(ctx, linkHost, token, tokenMovie, translation, season, episode, 0, kpID)
	if res == nil {
		return "", nil, false
	}
	return res.hlsURL, res.headers, res.hlsURL != ""
}

// allohaResolveViaBrowserFull resolves the CDN stream via headless browser.
// keepopenSec > 0 enables keepopen mode: after the initial resolve the browser
// tab stays alive for keepopenSec seconds and continuously emits header updates
// via the returned result.headerUpdates channel.
func allohaResolveViaBrowserFull(ctx context.Context, linkHost, token, tokenMovie, translation string, season, episode int, keepopenSec int, kpID string) *allohaResolveResult {
	if !mirageBrowserSem.Acquire(ctx) {
		return nil
	}
	semReleased := false
	defer func() {
		if !semReleased {
			mirageBrowserSem.Release()
		}
	}()
	log.Info().Str("token_movie", tokenMovie).Str("translation", translation).Int("s", season).Int("e", episode).Msg("alloha: resolving video via browser")

	// Step 1: Fetch player HTML.
	htmlBody, pageURL, err := allohaFetchPlayerHTML(ctx, linkHost, token, tokenMovie, translation, season, episode)
	if err != nil {
		log.Warn().Err(err).Msg("alloha: failed to fetch player HTML")
		return nil
	}

	if !strings.Contains(htmlBody, "fileList") && !strings.Contains(htmlBody, "movie") {
		log.Warn().Int("htmlLen", len(htmlBody)).Msg("alloha: player HTML has no fileList")
		return nil
	}

	// Step 2: Strip iframe check.
	htmlBody = mirageStripIframeCheck(htmlBody)

	log.Info().Int("htmlLen", len(htmlBody)).Msg("alloha: fetched and patched player HTML")

	htmlB64 := base64.StdEncoding.EncodeToString([]byte(htmlBody))

	// Per-resolve Chrome (Spectre-style): fresh process per resolve, cleaned
	// up on return. Previously used shared mirageBrowser.ensureMaster() which
	// wedged after ~15min (same issue as Mirage).
	// Alloha's local SOCKS5 — DO NOT leak into mirageBrowser.socksProxy.
	// Previously we wrote it into the shared field, which caused mirage's WS
	// (mirage_ws.go reads mirageBrowser.socksProxy) to dial through alloha's
	// proxy. Result: mirage browser+WS went via alloha IP but CDN segments
	// went direct from VPS — IP mismatch → CDN 403 cascade.
	socksProxy := ""
	if addr := httpclient.SocksAddrForBalancer("alloha"); addr != "" {
		socksProxy = addr
	} else if addr := httpclient.SocksAddrForBalancer("mirage"); addr != "" {
		socksProxy = addr
	}

	tmpDir, tmpErr := browsertmp.New("alloha-chrome-")
	if tmpErr != nil {
		log.Error().Err(tmpErr).Msg("alloha: failed to create temp chrome data dir")
		return nil
	}
	defer func() { go browsertmp.Remove(tmpDir) }()

	// The player runs as a real cross-origin iframe inside kinomix.web.app (its
	// whitelisted parent). Disable site isolation so the iframe shares the main
	// renderer process and its /movies/ + m3u8 requests surface to our Fetch
	// listener (otherwise they go to an out-of-process target we never see).
	// Fixed 1280x720 window so the coordinate-based play/skip clicks land.
	allocOpts := append(mirageBrowser.chromeOptsForDir(tmpDir, socksProxy),
		chromedp.Flag("disable-site-isolation-trials", true),
		chromedp.Flag("disable-features", "IsolateOrigins,site-per-process"),
		chromedp.WindowSize(1280, 720),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer allocCancel()

	browserCtx, cancel := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(func(format string, args ...any) {
			log.Debug().Msgf("[chromedp-alloha] "+format, args...)
		}),
	)
	// The player inside this tab opens its own WS with the stream's sid, and the
	// CDN keeps ONE connection per sid: a second one is closed with 4005. So our
	// Go WS client (which outlives the tab and carries the heartbeat for the
	// proxied playback) must not run while the tab is alive. Any client left
	// from an earlier resolve of this stream is closed now, and the one minted
	// from this tab's /movies/ answer is parked in pendingWS and started only
	// after the tab is gone (below, or when a keepopen tab expires).
	streamWSKey := allohaStreamKey(tokenMovie, translation, season, episode)
	CloseEdgeHashClient(streamWSKey)
	var pendingWSMu sync.Mutex
	var pendingWS *mirageWSClient
	// For titles whose /movies/ answer carries no wsUrl/sid (only hlsSource/
	// tracks/skipTime — «playback will die when the fallback token expires», the
	// 6–7 minute deaths), the player STILL opens a WS: it knows the sid from
	// elsewhere. The WS spy below records the URL it used; on tab close we build
	// our client from that instead of running with no session at all.
	var spyWSURL, spySID string
	startPendingWS := func() {
		pendingWSMu.Lock()
		c := pendingWS
		pendingWS = nil
		if c == nil && spySID != "" && spyWSURL != "" {
			c = newMirageWSClient(spyWSURL, spySID, linkHost, socksProxy)
			RegisterEdgeHashClient(c, streamWSKey, tokenMovie, linkHost)
			log.Info().Str("wsUrl", spyWSURL).Int("sidLen", len(spySID)).Str("stream", streamWSKey).
				Msg("alloha: WS client built from the player's own socket (no params in /movies/)")
		}
		pendingWSMu.Unlock()
		if c != nil {
			go c.Run()
			// Hold the resolve until this client has ITS edge_hash (a few hundred
			// ms after connect). The tab's hash died with the tab, and the first
			// proxied master request must not go out under a foreign one: on
			// 2026-09-02 00:30 that single mismatched request (ac from another
			// stream) was answered 403, and every later request with the correct
			// hash on that URL was 403 too — the CDN drops the session.
			deadline := time.Now().Add(4 * time.Second)
			for c.EdgeHash() == "" && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
			log.Info().Str("token_movie", tokenMovie).Bool("hash_ready", c.EdgeHash() != "").
				Msg("alloha: browser tab closed — WS client for edge_hash started")
		}
	}
	browserCanceled := false
	defer func() {
		if !browserCanceled {
			cancel()
			startPendingWS()
		}
	}()

	timeoutCtx, timeoutCancel := context.WithTimeout(browserCtx, 35*time.Second)
	defer timeoutCancel()

	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-timeoutCtx.Done():
		}
	}()

	parsedURL, parseErr := url.Parse(pageURL)
	if parseErr != nil {
		log.Error().Err(parseErr).Msg("alloha: failed to parse pageURL")
		return nil
	}
	pageHost := parsedURL.Hostname()

	var (
		resultURL     string
		resultHeaders map[string]string
		pendingURL    string // m3u8 URL from /movies/ — waiting for CDN auth headers
		resultMu      sync.Mutex
		done          = make(chan struct{}, 1)
		docFulfilled  int32
		// Borth is captured from the /movies/ POST request headers and is required
		// for ALL CDN requests (m3u8 + segments). It is stored separately because
		// CDN auth headers arrive via two async paths (Fetch intercept + XHR hook
		// console events) and the race between them sometimes causes Borth to be
		// absent from the merged resultHeaders when signalDone fires.
		capturedBorth string
		// Captured /bnsi/movies/ POST params for lightweight refresh.
		capturedMoviesURL     string
		capturedMoviesHdrs    map[string]string
		capturedMoviesBodyRaw []byte
	)

	signalDone := func(u string, hdrs map[string]string) {
		resultMu.Lock()
		if resultURL == "" && u != "" {
			resultURL = u
			if len(hdrs) > 0 {
				resultHeaders = hdrs
			}
			select {
			case done <- struct{}{}:
			default:
			}
		}
		resultMu.Unlock()
	}

	// storePending saves m3u8 URL from /movies/ without signaling done —
	// we wait for the m3u8 request to capture CDN auth headers.
	storePending := func(u string, hdrs map[string]string) {
		resultMu.Lock()
		if pendingURL == "" && u != "" {
			pendingURL = u
		}
		// Merge any headers captured so far (e.g. Borth from /movies/).
		if resultHeaders == nil {
			resultHeaders = make(map[string]string)
		}
		maps.Copy(resultHeaders, hdrs)
		// Persist Borth separately — it's from the /movies/ POST and must be
		// included in all subsequent CDN requests (m3u8 + segments). Due to
		// async event delivery races, Borth might not be in resultHeaders when
		// signalDone fires, so we save it in a dedicated variable.
		if borth, ok := hdrs["Borth"]; ok && borth != "" && capturedBorth == "" {
			capturedBorth = borth
			log.Info().Str("borth_prefix", borth[:min(len(borth), 20)]).Msg("alloha: stored Borth from /movies/ capture")
		}
		resultMu.Unlock()
	}

	chromedp.ListenTarget(timeoutCtx, func(ev any) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			log.Debug().Str("method", e.Request.Method).Str("url", e.Request.URL).Msg("alloha: request sent")

		// WS spy: the real player's WebSocket session as the CDN sees it. Our own
		// Go WS client (mirage_ws.go) is closed by the server within a second on
		// virtually every connect (472/476 in 6 h, codes 4005/4003, 2026-09-02),
		// so playback dies once the edge_hash expires. The player JS is
		// obfuscated; this is the cheapest way to learn the live URL params,
		// handshake frames and close code to bring our client back in line.
		case *network.EventWebSocketCreated:
			log.Info().Str("url", e.URL).Msg("alloha-ws-spy: created")
			// Remember the player's real WS endpoint + sid (see spyWSURL above).
			if u, err := url.Parse(e.URL); err == nil && strings.Contains(u.Path, "/ws") {
				if sid := u.Query().Get("sid"); sid != "" {
					pendingWSMu.Lock()
					spySID = sid
					spyWSURL = u.Scheme + "://" + u.Host + u.Path
					pendingWSMu.Unlock()
				}
			}
		case *network.EventWebSocketHandshakeResponseReceived:
			if e.Response != nil {
				log.Info().Int64("status", e.Response.Status).Msg("alloha-ws-spy: handshake response")
			}
		case *network.EventWebSocketFrameSent:
			if e.Response != nil {
				log.Info().Str("payload", capiTruncBody([]byte(e.Response.PayloadData), 400)).Msg("alloha-ws-spy: frame sent")
			}
		case *network.EventWebSocketFrameReceived:
			if e.Response != nil {
				log.Info().Str("payload", capiTruncBody([]byte(e.Response.PayloadData), 400)).Msg("alloha-ws-spy: frame received")
			}
		case *network.EventWebSocketClosed:
			log.Info().Msg("alloha-ws-spy: closed")

		case *fetch.EventRequestPaused:
			go func() {
				reqURL := e.Request.URL
				log.Debug().Str("url", reqURL).Str("type", string(e.ResourceType)).Msg("alloha: fetch paused")

				// Fulfill the initial document request with our pre-fetched HTML.
				if e.ResourceType == network.ResourceTypeDocument && strings.Contains(reqURL, pageHost) && atomic.CompareAndSwapInt32(&docFulfilled, 0, 1) {
					log.Info().Str("url", reqURL).Msg("alloha: fulfilling Document with pre-fetched HTML")
					hdrs := []*fetch.HeaderEntry{
						{Name: "Content-Type", Value: "text/html; charset=utf-8"},
					}
					_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
						return fetch.FulfillRequest(e.RequestID, 200).
							WithResponseHeaders(hdrs).
							WithBody(htmlB64).
							Do(c)
					}))
					return
				}

				// Fulfill the kinomix.web.app parent document with a blank page so
				// navigation commits instantly. The real SPA pulls heavy web.kino.bz
				// assets that hang on some networks (prod VPS) and stalled the whole
				// resolve. We only need the kinomix origin as the player iframe's
				// parent — its content is irrelevant (the CDN requests carry the
				// player URL as referer, not kinomix).
				if strings.Contains(reqURL, "kinomix.web.app") {
					if e.ResourceType == network.ResourceTypeDocument {
						log.Debug().Str("url", reqURL).Msg("alloha: fulfilling kinomix parent with blank page")
						blank := base64.StdEncoding.EncodeToString([]byte("<!DOCTYPE html><html><head></head><body></body></html>"))
						_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
							return fetch.FulfillRequest(e.RequestID, 200).
								WithResponseHeaders([]*fetch.HeaderEntry{{Name: "Content-Type", Value: "text/html; charset=utf-8"}}).
								WithBody(blank).
								Do(c)
						}))
						return
					}
					_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
						return fetch.ContinueRequest(e.RequestID).Do(c)
					}))
					return
				}

				// Intercept /movies/ POST — proxy through Go HTTP to bypass headless detection.
				if strings.Contains(reqURL, "/movies/") && (e.ResourceType == network.ResourceTypeXHR || e.ResourceType == network.ResourceTypeFetch) {
					// Capture CDN auth headers from the JS-generated request.
					capturedHeaders := allohaExtractCDNHeaders(e.Request.Headers)
					if len(capturedHeaders) > 0 {
						log.Info().Interface("capturedHeaders", capturedHeaders).Msg("alloha: captured CDN auth headers from browser")
					}

					// Store Borth immediately into BOTH capturedBorth AND resultHeaders,
					// BEFORE any blocking call. Causal ordering guarantees this runs
					// before the m3u8 goroutine reads resultHeaders:
					//   FulfillRequest (step 3) → browser gets response → sends m3u8 GET → m3u8 goroutine
					// So resultHeaders["Borth"] is set before maps.Copy in the m3u8 goroutine.
					if borth, ok := capturedHeaders["Borth"]; ok && borth != "" {
						resultMu.Lock()
						if capturedBorth == "" {
							capturedBorth = borth
							// Log first 60 chars to verify timestamp changes between resolves.
							log.Info().Str("borth_60", borth[:min(len(borth), 60)]).Msg("alloha: capturedBorth set early from /movies/ goroutine")
						}
						if resultHeaders == nil {
							resultHeaders = make(map[string]string)
						}
						resultHeaders["Borth"] = borth
						resultMu.Unlock()
					}

					// Proxy the request through Go.
					body, status, proxyErr := allohaProxyMoviesRequest(
						timeoutCtx, reqURL, e.Request.Method, e.Request.PostDataEntries,
						e.Request.Headers, pageURL,
					)
					if proxyErr != nil {
						log.Warn().Err(proxyErr).Msg("alloha: proxy /movies/ failed")
						_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
							return fetch.ContinueRequest(e.RequestID).Do(c)
						}))
						return
					}

					// Capture POST params for lightweight refresh.
					resultMu.Lock()
					capturedMoviesURL = reqURL
					capturedMoviesHdrs = make(map[string]string)
					for k, v := range e.Request.Headers {
						capturedMoviesHdrs[k] = fmt.Sprint(v)
					}
					// Decode POST body from base64 entries.
					var postBody []byte
					for _, pe := range e.Request.PostDataEntries {
						if pe.Bytes != "" {
							if decoded, decErr := base64.StdEncoding.DecodeString(pe.Bytes); decErr == nil {
								postBody = append(postBody, decoded...)
							}
						}
					}
					capturedMoviesBodyRaw = postBody
					resultMu.Unlock()

					// Try to extract m3u8 URL from the /movies/ response.
					// Don't signal done yet — wait for the m3u8 request to capture
					// CDN auth headers (Authorizations, Accepts-Controls).
					if status >= 200 && status < 300 {
						if m3u8 := allohaExtractHLSFromJSON(body); m3u8 != "" {
							storePending(m3u8, capturedHeaders)
						}
						// Start a WS client for THIS stream's session (same CDN as
						// mirage). Reuse it while it's alive — restarting on every
						// re-resolve leaves a gap with no config_update, so
						// Accepts-Controls goes stale.
						//
						// Liveness is checked per-stream: keying it on linkHost too
						// meant one WS session covered every alloha stream, so only
						// that stream sent "playing" heartbeats while all the others
						// pulled segments with no telemetry behind their own sid —
						// the state the CDN answers with 403 minutes into playback.
						wsKey := allohaStreamKey(tokenMovie, translation, season, episode)
						if GetEdgeHash(wsKey) == "" {
							wsURL, wsSID := mirageExtractWSParams(body)
							if wsURL == "" || wsSID == "" {
								// No WS = no edge_hash rotation = every segment goes
								// out under the fallback Guard token, and the CDN
								// cuts that session a few minutes in. Say so loudly
								// with the shape of the body, because a silent miss
								// here looks exactly like "alloha randomly dies".
								log.Warn().Str("stream", wsKey).
									Bool("haveWsUrl", wsURL != "").Bool("haveSid", wsSID != "").
									Int("bodyLen", len(body)).
									Str("keys", allohaJSONTopKeys(body)).
									Str("preview", capiTruncBody(body, 300)).
									Msg("alloha: /movies/ carried no WS params — playback will die when the fallback token expires")
							}
							if wsURL != "" && wsSID != "" {
								// Pass alloha's local socks so WS shares the
								// browser's exit IP — see newMirageWSClient docs.
								wsClient := newMirageWSClient(wsURL, wsSID, linkHost, socksProxy)
								// Not started yet — the tab's own player holds this sid
								// until the tab closes (see pendingWS at the top).
								pendingWSMu.Lock()
								if pendingWS != nil {
									pendingWS.Close()
								}
								pendingWS = wsClient
								pendingWSMu.Unlock()
								// Identity = this stream; the rest are lookup aliases
								// (aliases never close a live session).
								RegisterEdgeHashClient(wsClient, wsKey, tokenMovie, linkHost)
								log.Info().Str("wsUrl", wsURL).Int("sidLen", len(wsSID)).Str("stream", wsKey).Msg("alloha: WS client for edge_hash parked until the tab closes")
							}
						} else {
							log.Debug().Msg("alloha: WS client alive, skipping restart")
						}
					}

					// Feed response back to the browser.
					respHeaders := []*fetch.HeaderEntry{
						{Name: "Content-Type", Value: "application/json"},
						{Name: "Access-Control-Allow-Origin", Value: "*"},
					}
					bodyB64 := base64.StdEncoding.EncodeToString(body)
					_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
						return fetch.FulfillRequest(e.RequestID, int64(status)).
							WithResponseHeaders(respHeaders).
							WithBody(bodyB64).
							Do(c)
					}))
					return
				}

				// Intercept master.m3u8 request — capture CDN auth headers.
				if strings.Contains(reqURL, ".m3u8") {
					// OPTIONS preflight — the browser sends this before the actual GET
					// to check CORS. We must respond with permissive CORS headers so
					// the actual GET (with Authorizations/Accepts-Controls) fires.
					if e.Request.Method == "OPTIONS" {
						log.Info().Str("url", reqURL).Msg("alloha: fulfilling m3u8 OPTIONS preflight with CORS")
						corsHeaders := []*fetch.HeaderEntry{
							{Name: "Access-Control-Allow-Origin", Value: "*"},
							{Name: "Access-Control-Allow-Methods", Value: "GET, OPTIONS"},
							{Name: "Access-Control-Allow-Headers", Value: "authorizations,accepts-controls,borth,origin,referer"},
							{Name: "Access-Control-Max-Age", Value: "86400"},
						}
						_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
							return fetch.FulfillRequest(e.RequestID, 204).
								WithResponseHeaders(corsHeaders).
								Do(c)
						}))
						return
					}

					// Actual GET request — extract CDN auth headers.
					hdrs := allohaExtractCDNHeaders(e.Request.Headers)
					log.Info().Str("method", e.Request.Method).Interface("cdnHeaders", hdrs).Int("count", len(hdrs)).Msg("alloha: intercepted m3u8 GET request")

					// Merge CDN auth headers with any previously captured (e.g. Borth from /movies/).
					resultMu.Lock()
					if resultHeaders == nil {
						resultHeaders = make(map[string]string)
					}
					maps.Copy(resultHeaders, hdrs)
					mergedHdrs := make(map[string]string, len(resultHeaders))
					maps.Copy(mergedHdrs, resultHeaders)
					pending := pendingURL
					resultMu.Unlock()

					// If we already have the m3u8 URL from /movies/, use it.
					// Otherwise use this m3u8 URL directly.
					if pending != "" {
						signalDone(pending, mergedHdrs)
					} else {
						signalDone(reqURL, mergedHdrs)
					}

					// Continue the request so the browser's player JS receives the m3u8
					// content and fetches variant playlists — this establishes the CDN
					// session so our Go client's subsequent segment requests succeed.
					_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
						return fetch.ContinueRequest(e.RequestID).Do(c)
					}))
					return
				}

				// Continue all other requests.
				_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
					return fetch.ContinueRequest(e.RequestID).Do(c)
				}))
			}()

		case *runtime.EventConsoleAPICalled:
			var args []string
			for _, arg := range e.Args {
				if arg.Value != nil {
					args = append(args, string(arg.Value))
				} else if arg.Description != "" {
					args = append(args, arg.Description)
				}
			}
			log.Debug().Str("type", string(e.Type)).Strs("args", args).Msg("alloha: console")

			// Parse CDN auth headers captured by our XHR/fetch hook.
			for _, a := range args {
				a = strings.Trim(a, "\"")
				if !strings.HasPrefix(a, "__ALLOHA_HDR__:") {
					continue
				}
				parts := strings.SplitN(a[len("__ALLOHA_HDR__:"):], ":", 2)
				if len(parts) != 2 {
					continue
				}
				name, value := parts[0], parts[1]
				logVal := value
				if len(logVal) > 40 {
					logVal = logVal[:40] + "..."
				}
				log.Info().Str("header", name).Str("value", logVal).Msg("alloha: captured CDN header via JS hook")

				resultMu.Lock()
				if resultHeaders == nil {
					resultHeaders = make(map[string]string)
				}
				resultHeaders[name] = value
				// Also persist Borth from the XHR hook path.
				if strings.ToLower(name) == "borth" && value != "" && capturedBorth == "" {
					capturedBorth = value
				}
				// Check if we can signal done: need pendingURL + both CDN auth headers.
				pending := pendingURL
				hasAuth := false
				hasAccepts := false
				for k := range resultHeaders {
					switch strings.ToLower(k) {
					case "authorizations", "authorization":
						hasAuth = true
					case "accepts-controls", "accept-controls":
						hasAccepts = true
					}
				}
				resultMu.Unlock()

				if pending != "" && hasAuth && hasAccepts {
					resultMu.Lock()
					mergedHdrs := make(map[string]string, len(resultHeaders))
					maps.Copy(mergedHdrs, resultHeaders)
					resultMu.Unlock()
					log.Info().Str("m3u8", pending).Int("headerCount", len(mergedHdrs)).Msg("alloha: signaling done with JS-captured CDN headers")
					signalDone(pending, mergedHdrs)
				}
			}

		case *runtime.EventExceptionThrown:
			if e.ExceptionDetails != nil && e.ExceptionDetails.Exception != nil {
				log.Warn().Str("exception", e.ExceptionDetails.Exception.Description).Msg("alloha: JS exception")
			}
		}
	})

	// Enable network monitoring and Fetch interception.
	// Only intercept Document, /movies/ POST, and .m3u8 — let JS/CSS/images
	// load directly without interception. The old pattern "*pageHost*" matched
	// all requests to the host and caused JS to hang on fetch.ContinueRequest
	// serialization (same fix as Mirage).
	docPattern := "*" + pageHost + "/?token_movie=*"
	err = chromedp.Run(timeoutCtx,
		network.Enable(),
		runtime.Enable(),
		fetch.Enable().
			WithPatterns([]*fetch.RequestPattern{
				{URLPattern: docPattern, RequestStage: fetch.RequestStageRequest},
				{URLPattern: "*kinomix.web.app/*", RequestStage: fetch.RequestStageRequest},
				{URLPattern: "*/bnsi/movies/*", RequestStage: fetch.RequestStageRequest},
				{URLPattern: "*.m3u8*", RequestStage: fetch.RequestStageRequest},
			}).
			WithHandleAuthRequests(false),
	)
	if err != nil {
		log.Error().Err(err).Msg("alloha: chromedp enable failed")
		return nil
	}

	// Inject stealth JS.
	if err := chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(mirageStealthJS).Do(ctx)
		return err
	})); err != nil {
		log.Warn().Err(err).Msg("alloha: stealth JS injection failed (non-fatal)")
	}

	// Inject XHR/fetch hook to capture CDN auth headers via console.log.
	if err := chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(allohaXHRHookJS).Do(ctx)
		return err
	})); err != nil {
		log.Warn().Err(err).Msg("alloha: XHR hook injection failed (non-fatal)")
	}

	// Clear CDN session cookies before every resolve to force a fresh session_id.
	// The CDN server-side session (embedded in Borth's session_id) expires after ~150s.
	// Chrome's persistent cookies would reuse the same session_id across resolves,
	// causing fresh Borth values to still fail after session expiry.
	// Clearing cookies forces the CDN to create a new session on every resolve.
	if err := chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return network.ClearBrowserCookies().Do(ctx)
	})); err != nil {
		log.Warn().Err(err).Msg("alloha: clear cookies failed (non-fatal)")
	}

	// Clear localStorage to force fresh Borth generation on every resolve.
	// Borth embeds a timestamp; stale Borth causes CDN 403 on segments after ~30s.
	// Also restore quality preference after clearing.
	qualityJS := `localStorage.clear(); localStorage.setItem('allplay', '{"captionParam":{"fontSize":"100%","colorText":"Белый","colorBackground":"Черный","opacityText":"100%","opacityBackground":"75%","styleText":"Без контура","weightText":"Обычный текст"},"quality":2160,"volume":0.5,"muted":false}');`
	if err := chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(qualityJS).Do(ctx)
		return err
	})); err != nil {
		log.Warn().Err(err).Msg("alloha: quality JS injection failed (non-fatal)")
	}

	// Navigate to kinomix.web.app — the player's whitelisted parent origin. This
	// gives Chrome the real browsing context (cookies, history, SW, origin) the
	// CDN expects and is the page we inject the player iframe into. We use the
	// root (not /movie/{kp}) so kinomix doesn't spawn its own competing player.
	_ = kpID // not needed for root nav; kept for wrapper API symmetry
	// Use page.Navigate (commit-only) instead of chromedp.Navigate (which blocks
	// on the load event). The kinomix SPA pulls heavy assets from web.kino.bz
	// that hang on some networks (prod VPS) and would block chromedp.Navigate for
	// the entire resolve budget — we only need the committed kinomix origin and a
	// parsed <body> to inject the player iframe into.
	if err := chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, _, _, _, navErr := page.Navigate("https://kinomix.web.app/").Do(ctx)
		return navErr
	})); err != nil {
		log.Warn().Err(err).Msg("alloha: kinomix navigation failed (non-fatal)")
	}
	_ = chromedp.Run(timeoutCtx, chromedp.Sleep(2500*time.Millisecond)) // let <body> parse

	// Inject the player as a real cross-origin iframe on the kinomix page. Its
	// document request is fulfilled with our pre-fetched, iframe-check-stripped
	// HTML; with site isolation disabled its /movies/ + m3u8 requests reach our
	// Fetch listener. Clear the SPA body first so the coordinate clicks land on
	// the iframe; retry a few times in case <body> isn't parsed yet.
	go func() {
		injectJS := `(function(){try{if(!document.body)return 'no-body';document.body.innerHTML='';var f=document.createElement('iframe');f.src=` + "`" + pageURL + "`" + `;f.allow='autoplay; fullscreen; encrypted-media';f.setAttribute('style','position:fixed;top:0;left:0;width:1280px;height:720px;border:0;z-index:2147483647');document.body.appendChild(f);return 'injected';}catch(e){return 'err:'+(e&&e.message);}})()`
		for i := 0; i < 5; i++ {
			var injRes string
			err := chromedp.Run(timeoutCtx, chromedp.Evaluate(injectJS, &injRes))
			if err == nil && injRes == "injected" {
				log.Info().Str("navURL", pageURL).Msg("alloha: player iframe injected, waiting for m3u8...")
				return
			}
			log.Debug().Str("res", injRes).Err(err).Msg("alloha: iframe inject retry")
			select {
			case <-timeoutCtx.Done():
				return
			case <-time.After(1 * time.Second):
			}
		}
	}()

	// Trigger playback + skip prerolls. The player ships autoplay:0 + preroll ads,
	// so it never POSTs the /movies/ manifest until playback starts — the missing
	// step that made Alloha never resolve. Clicks are coordinate-based because the
	// cross-origin iframe is unreachable from the parent via JS: centre (640,360)
	// = the big play button, bottom-right (1180,690) = the ad "skip" control.
	go func() {
		for i := 0; i < 16; i++ {
			select {
			case <-done:
				return
			case <-timeoutCtx.Done():
				return
			case <-time.After(1500 * time.Millisecond):
			}
			_ = chromedp.Run(timeoutCtx, chromedp.MouseClickXY(1180, 690)) // skip preroll
			_ = chromedp.Run(timeoutCtx, chromedp.MouseClickXY(640, 360))  // play
		}
	}()

	buildResult := func(u string, h map[string]string) *allohaResolveResult {
		resultMu.Lock()
		borth := capturedBorth
		if h == nil {
			h = make(map[string]string)
		}
		// Ensure Borth is always in the final headers. Borth is required for ALL
		// CDN requests (m3u8 + segments). Due to async event delivery races between
		// the Fetch intercept path and the XHR hook console.log path, Borth may be
		// missing from resultHeaders when signalDone fires even though it was
		// captured from the /movies/ POST. We stored it in capturedBorth separately.
		if borth != "" {
			if _, hasBorth := h["Borth"]; !hasBorth {
				h["Borth"] = borth
				log.Info().Msg("alloha: injected Borth into result headers from capturedBorth")
			}
		}
		res := &allohaResolveResult{
			hlsURL:        u,
			headers:       h,
			moviesURL:     capturedMoviesURL,
			moviesHeaders: capturedMoviesHdrs,
			moviesBody:    capturedMoviesBodyRaw,
		}
		resultMu.Unlock()

		// Capture cookies from Chrome and inject into Go HTTP client.
		allohaInjectChromeCookies(timeoutCtx, linkHost)

		return res
	}

	select {
	case <-done:
		resultMu.Lock()
		u := resultURL
		h := resultHeaders
		resultMu.Unlock()
		if u != "" {
			log.Info().Str("m3u8", u).Int("headerCount", len(h)).Msg("alloha: resolved video via browser, waiting 4s for CDN session...")
			// Wait for the browser's player JS to fetch variant playlists.
			// This establishes the CDN session so Go-client segment requests succeed.
			select {
			case <-time.After(4 * time.Second):
			case <-timeoutCtx.Done():
			}
			res := buildResult(u, h)

			// ---------- keepopen mode ----------
			if keepopenSec > 0 && res != nil {
				updateCh := make(chan map[string]string, 8)
				// Create a keepalive context from the browser tab context.
				keepCtx, keepCancel := context.WithTimeout(browserCtx, time.Duration(keepopenSec)*time.Second)

				// Accumulated live headers — seeded with initial captured values.
				var liveMu sync.Mutex
				liveHdrs := make(map[string]string, len(res.headers))
				maps.Copy(liveHdrs, res.headers)

				// Listen for new __ALLOHA_HDR__ console events after the initial resolve.
				// The player's WS rotates edge_hash → player generates new Borth+AC → XHR
				// hook fires → console log → we capture and push to updateCh.
				chromedp.ListenTarget(keepCtx, func(ev any) {
					e, ok := ev.(*runtime.EventConsoleAPICalled)
					if !ok {
						return
					}
					for _, arg := range e.Args {
						var rawVal string
						if arg.Value != nil {
							rawVal = strings.Trim(string(arg.Value), "\"")
						} else if arg.Description != "" {
							rawVal = arg.Description
						}
						if !strings.HasPrefix(rawVal, "__ALLOHA_HDR__:") {
							continue
						}
						parts := strings.SplitN(rawVal[len("__ALLOHA_HDR__:"):], ":", 2)
						if len(parts) != 2 {
							continue
						}
						name, value := parts[0], parts[1]

						liveMu.Lock()
						liveHdrs[name] = value
						snap := make(map[string]string, len(liveHdrs))
						maps.Copy(snap, liveHdrs)
						liveMu.Unlock()

						select {
						case updateCh <- snap:
						default:
							// Drain oldest and send fresh.
							select {
							case <-updateCh:
							default:
							}
							select {
							case updateCh <- snap:
							default:
							}
						}
					}
				})

				// Release semaphore immediately — others shouldn't wait 10 min for us.
				mirageBrowserSem.Release()
				semReleased = true
				// Don't cancel browser tab on function return.
				browserCanceled = true

				go func() {
					<-keepCtx.Done()
					keepCancel()
					cancel() // close browser tab
					close(updateCh)
					log.Info().Str("token_movie", tokenMovie).Int("sec", keepopenSec).Msg("alloha: keepopen session expired, browser tab closed")
					// The tab's WS is gone with it — our client takes over the sid.
					startPendingWS()
				}()

				res.headerUpdates = updateCh
				res.browserCtx = keepCtx // expose Chrome tab for segment fetch
				log.Info().Str("token_movie", tokenMovie).Int("sec", keepopenSec).Msg("alloha: keepopen session started")
			}
			// -----------------------------------

			return res
		}
	case <-timeoutCtx.Done():
		// Fallback: if we have a pending URL from /movies/ but never got the m3u8 request,
		// use it with whatever headers we have.
		resultMu.Lock()
		pending := pendingURL
		hdrs := resultHeaders
		resultMu.Unlock()
		if pending != "" {
			log.Warn().Str("m3u8", pending).Int("headerCount", len(hdrs)).Msg("alloha: browser timed out, using pending URL from /movies/")
			return buildResult(pending, hdrs)
		}
		log.Warn().Msg("alloha: browser resolve timed out")
	}

	return nil
}

// allohaProxyMoviesRequest proxies a POST /movies/ request through Go HTTP.
// This bypasses CDN headless Chrome detection.
func allohaProxyMoviesRequest(ctx context.Context, reqURL, method string, postEntries []*network.PostDataEntry, headers network.Headers, pageURL string) ([]byte, int, error) {
	log.Info().Str("original", reqURL).Str("method", method).Msg("alloha: proxying /movies/ request via Go HTTP")

	var bodyReader io.Reader
	if len(postEntries) > 0 {
		var combined []byte
		for _, entry := range postEntries {
			if entry.Bytes != "" {
				decoded, err := base64.StdEncoding.DecodeString(entry.Bytes)
				if err == nil {
					combined = append(combined, decoded...)
				}
			}
		}
		if len(combined) > 0 {
			bodyReader = bytes.NewReader(combined)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, 0, err
	}

	// Copy headers from the browser request (includes JS-generated auth tokens).
	for k, v := range headers {
		val := fmt.Sprint(v)
		switch strings.ToLower(k) {
		case "host", "content-length":
			continue
		case "user-agent":
			req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
			continue
		case "sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform":
			continue
		}
		req.Header.Set(k, val)
	}

	if req.Header.Get("Referer") == "" {
		req.Header.Set("Referer", pageURL)
	}

	// Use shared client with cookie jar so CDN session cookies from the player
	// page GET are included in the /movies/ POST. This makes the CDN generate
	// a session-bound URL with a longer TTL (~5 min) instead of a short-lived
	// anonymous URL (~2 min).
	client := getPlayerProxyClient()
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("proxy /movies/: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read /movies/ body: %w", err)
	}

	log.Info().Int("status", resp.StatusCode).Int("bodyLen", len(body)).Msg("alloha: /movies/ proxy response")
	return body, resp.StatusCode, nil
}

// allohaExtractHLSFromJSON extracts the best m3u8 URL from a /movies/ JSON response.
// The response format has hlsSource array with quality → URL maps.
func allohaExtractHLSFromJSON(body []byte) string {
	var root struct {
		Data struct {
			File struct {
				HLSSource []struct {
					Default bool              `json:"default"`
					Quality map[string]string `json:"quality"`
				} `json:"hlsSource"`
			} `json:"file"`
		} `json:"data"`
	}
	if err := stdjson.Unmarshal(body, &root); err != nil {
		// Try alternate format: direct m3u8 URL in the response.
		if m3u8 := mirageAnyM3URe.FindString(string(body)); m3u8 != "" {
			return m3u8
		}
		log.Debug().Err(err).Msg("alloha: failed to parse /movies/ JSON")
		return ""
	}

	// Find the best quality from the default or first hlsSource.
	for _, src := range root.Data.File.HLSSource {
		if len(src.Quality) == 0 {
			continue
		}
		// Pick highest quality.
		for _, q := range []string{"2160", "1440", "1080", "720", "480", "360"} {
			if u, ok := src.Quality[q]; ok && u != "" {
				// URLs may contain " or " separator for reserve CDN.
				parts := strings.SplitN(u, " or ", 2)
				return strings.TrimSpace(parts[0])
			}
		}
		// Fallback: any quality.
		for _, u := range src.Quality {
			parts := strings.SplitN(u, " or ", 2)
			return strings.TrimSpace(parts[0])
		}
	}

	// Fallback: find any m3u8 URL in the body.
	if m3u8 := mirageAnyM3URe.FindString(string(body)); m3u8 != "" {
		return m3u8
	}

	return ""
}

// allohaFetchPlayerHTML fetches the player page HTML from linkhost.
func allohaFetchPlayerHTML(ctx context.Context, linkHost, token, tokenMovie, translation string, season, episode int) (string, string, error) {
	base := strings.TrimRight(linkHost, "/")
	pageURL := base + "/?token_movie=" + url.QueryEscape(tokenMovie) + "&token=" + url.QueryEscape(strings.TrimSpace(token))
	if translation != "" {
		pageURL += "&translation=" + url.QueryEscape(translation)
	}
	if season > 0 {
		pageURL += "&season=" + fmt.Sprint(season) + "&episode=" + fmt.Sprint(episode)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Referer", base+"/")
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	// Use shared client with cookie jar so CDN session cookies are stored and
	// reused for /movies/ POST and subsequent segment requests.
	client := getPlayerProxyClient()
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch player HTML: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("fetch player HTML: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	if err != nil {
		return "", "", fmt.Errorf("read player HTML: %w", err)
	}

	log.Info().Str("url", pageURL).Int("cookies", len(client.Jar.Cookies(req.URL))).Msg("alloha: player page fetched, cookies in jar")
	return string(body), pageURL, nil
}

// allohaExtractCDNHeaders extracts CDN auth headers from a network request.
// The Alloha CDN uses non-standard header names:
// - "Authorizations" (not "Authorization")
// - "Accepts-Controls" (not "Accept-Control")
func allohaExtractCDNHeaders(headers network.Headers) map[string]string {
	result := make(map[string]string)
	for k, v := range headers {
		kl := strings.ToLower(k)
		if kl == "authorizations" || kl == "authorization" ||
			kl == "accepts-controls" || kl == "accept-controls" ||
			kl == "borth" {
			result[k] = fmt.Sprint(v)
		}
	}
	return result
}

// allohaInjectChromeCookies captures all cookies from the current Chrome
// browser context and injects them into the shared Go HTTP player proxy client.
// This ensures CDN session cookies (set when Chrome loads stloadi.live) are
// present in subsequent Go HTTP segment requests.
func allohaInjectChromeCookies(ctx context.Context, linkHost string) {
	var cookies []*network.Cookie
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		var err error
		cookies, err = network.GetCookies().Do(c)
		return err
	}))
	if err != nil {
		log.Warn().Err(err).Msg("alloha: failed to get Chrome cookies")
		return
	}
	if len(cookies) == 0 {
		return
	}

	client := getPlayerProxyClient()
	if client.Jar == nil {
		log.Warn().Msg("alloha: player proxy client has no cookie jar")
		return
	}

	// Group cookies by domain for bulk SetCookies call.
	byDomain := make(map[string][]*http.Cookie)
	for _, c := range cookies {
		if c.Value == "" {
			continue
		}
		domain := strings.TrimPrefix(c.Domain, ".")
		httpC := &http.Cookie{
			Name:   c.Name,
			Value:  c.Value,
			Path:   c.Path,
			Secure: c.Secure,
		}
		byDomain[domain] = append(byDomain[domain], httpC)
	}

	injected := 0
	for domain, cs := range byDomain {
		cookieURL, parseErr := url.Parse("https://" + domain + "/")
		if parseErr != nil {
			continue
		}
		client.Jar.SetCookies(cookieURL, cs)
		injected += len(cs)
	}
	log.Info().
		Int("total", len(cookies)).
		Int("injected", injected).
		Int("domains", len(byDomain)).
		Msg("alloha: injected Chrome cookies into Go HTTP client")
}

// allohaFetchInPlayerTab runs fetch() INSIDE the keepopen player tab.
// Unlike fetchSegmentViaBrowser (which creates a new tab), this runs in the
// existing player tab where Guard is verified and WS is connected.
// The player tab has the same session as kinomix — real Chrome TLS, cookies,
// Guard tokens, WebSocket heartbeats.
func allohaFetchInPlayerTab(playerCtx context.Context, segmentURL string) ([]byte, error) {
	if playerCtx == nil || playerCtx.Err() != nil {
		return nil, fmt.Errorf("player tab context dead")
	}

	// Use fetch() with credentials:'omit' (same as the browser player).
	// No custom headers needed — the player tab already has the session.
	js := fmt.Sprintf(`
		(async () => {
			try {
				const resp = await fetch(%q, {
					mode: 'cors',
					credentials: 'omit'
				});
				if (!resp.ok) {
					return {error: 'HTTP ' + resp.status, status: resp.status};
				}
				const buf = await resp.arrayBuffer();
				const bytes = new Uint8Array(buf);
				let binary = '';
				const chunkSize = 32768;
				for (let i = 0; i < bytes.length; i += chunkSize) {
					const chunk = bytes.subarray(i, Math.min(i + chunkSize, bytes.length));
					binary += String.fromCharCode.apply(null, chunk);
				}
				return {ok: true, data: btoa(binary), size: bytes.length};
			} catch (e) {
				return {error: e.message};
			}
		})()
	`, segmentURL)

	ctx, cancel := context.WithTimeout(playerCtx, 15*time.Second)
	defer cancel()

	var res struct {
		OK     bool   `json:"ok"`
		Data   string `json:"data"`
		Size   int    `json:"size"`
		Error  string `json:"error"`
		Status int    `json:"status"`
	}
	err := chromedp.Run(ctx, chromedp.Evaluate(js, &res, chromedp.EvalAsValue,
		func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}))
	if err != nil {
		return nil, fmt.Errorf("chromedp eval: %w", err)
	}
	if res.Error != "" {
		return nil, fmt.Errorf("fetch: %s (status %d)", res.Error, res.Status)
	}
	if !res.OK {
		return nil, fmt.Errorf("fetch: unknown error")
	}

	data, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	return data, nil
}

// allohaJSONTopKeys lists the top-level keys of a JSON body (sorted, truncated)
// so a changed upstream contract is visible in one log line.
func allohaJSONTopKeys(body []byte) string {
	var root map[string]any
	if err := stdjson.Unmarshal(body, &root); err != nil {
		return "(not a json object)"
	}
	keys := make([]string, 0, len(root))
	for k, v := range root {
		switch v.(type) {
		case string:
			keys = append(keys, k+":str")
		case map[string]any:
			keys = append(keys, k+":obj")
		case []any:
			keys = append(keys, k+":arr")
		default:
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := strings.Join(keys, ",")
	if len(out) > 400 {
		out = out[:400] + "…"
	}
	return out
}

// capiTruncBody renders a short, single-line preview of a response body.
func capiTruncBody(body []byte, n int) string {
	s := strings.TrimSpace(string(body))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
