package litesrc

import (
	"context"
	"encoding/base64"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/browsertmp"

	"lampac-go/internal/browser"

	"github.com/chromedp/cdproto/network"
	"github.com/rs/zerolog/log"
)

// mirageResolveViaFacade is the browser.Engine-based port of
// mirageResolveViaBrowser. It is selected by mirage.go when the admin
// override picks a non-chromedp engine (rod, playwright) — the
// existing direct-chromedp path is left untouched and remains the
// default. Behaviour is intentionally a subset of the direct path:
//
//   - No mirageExperimentKeepBrowser / WS-blocking script (browser
//     always closes on return).
//   - No network.EventResponseReceived listener: capture happens
//     exclusively via Fetch.requestPaused (Hijack).
//   - Stealth.JS is injected via SessionOptions.UseStealth in addition
//     to mirageStealthJS, so the facade path gets the wider 16-evasion
//     coverage out of the box.
//
// The function is a near 1:1 transcript of the request-paused branch
// from mirageResolveViaBrowser; deviations are flagged in comments.
func mirageResolveViaFacade(ctx context.Context, linkHost, token string, idFile int64, tokenMovie, balancerName string) (*mirageResolveResult, bool) {
	if !mirageBrowserSem.Acquire(ctx) {
		return nil, false
	}
	defer mirageBrowserSem.Release()

	eng := browser.ForBalancer("mirage")
	if eng == nil {
		log.Warn().Msg("mirage: facade requested but no browser engine registered")
		return nil, false
	}
	log.Info().Int64("id_file", idFile).Str("token_movie", tokenMovie).
		Str("balancer", balancerName).Str("engine", eng.Name()).
		Msg("mirage: resolving via facade engine")

	htmlBody, pageURL, err := mirageFetchPlayerHTML(ctx, linkHost, token, tokenMovie, balancerName)
	if err != nil {
		log.Warn().Err(err).Str("balancer", balancerName).Msg("mirage: facade — fetch player HTML failed")
		return nil, false
	}
	if !strings.Contains(htmlBody, "fileList") {
		log.Warn().Int("htmlLen", len(htmlBody)).Str("balancer", balancerName).
			Msg("mirage: facade — player HTML has no fileList")
		return nil, false
	}

	htmlBody = mirageStripIframeCheck(htmlBody)
	idFileStr := strconv.FormatInt(idFile, 10)
	idFilePatchRe := regexp.MustCompile(`"id_file"\s*:\s*(?:null|"?\d+"?)`)
	htmlBody = idFilePatchRe.ReplaceAllString(htmlBody, `"id_file":"`+idFileStr+`"`)
	htmlBytes := []byte(htmlBody)

	parsedURL, parseErr := url.Parse(pageURL)
	if parseErr != nil {
		log.Error().Err(parseErr).Msg("mirage: facade — parse pageURL failed")
		return nil, false
	}
	pageHost := parsedURL.Hostname()

	// Fresh per-resolve temp user data dir, matching the chromedp path.
	tmpDir, tmpErr := browsertmp.New("mirage-facade-")
	if tmpErr != nil {
		log.Error().Err(tmpErr).Msg("mirage: facade — temp dir failed")
		return nil, false
	}
	// Deferred to a goroutine: Remove retries while the dying Chrome re-creates
	// files in the profile it still owns (see internal/browsertmp).
	defer func() { go browsertmp.Remove(tmpDir) }()

	sessionCtx, sessionCancel := context.WithTimeout(ctx, 20*time.Second)
	defer sessionCancel()

	session, err := eng.NewSession(sessionCtx, browser.SessionOptions{
		UserDataDir:       tmpDir,
		Headless:          true,
		UseStealth:        true,
		InitScripts:       []string{mirageStealthJS},
		NavigationTimeout: 20 * time.Second,
	})
	if err != nil {
		log.Warn().Err(err).Str("engine", eng.Name()).Msg("mirage: facade — NewSession failed")
		return nil, false
	}
	defer session.Close()

	// Capture state — same shape as the chromedp path so callers (and
	// future merging back into a single function) see the same fields.
	var (
		resultMu      sync.Mutex
		resultStream  string
		resultBody    []byte
		resultHeaders map[string]string
		done          = make(chan struct{}, 1)
		allHeadersCh  = make(chan struct{}, 1)
		docFulfilled  bool

		capturedMoviesURL     string
		capturedMoviesHeaders map[string]string
		capturedMoviesBody    []byte

		capturedRequestOrigin  string
		capturedRequestReferer string
	)

	signalDone := func(stream string, rawBody []byte, hdrs map[string]string) {
		resultMu.Lock()
		defer resultMu.Unlock()
		if resultStream == "" && stream != "" {
			resultStream = stream
			if len(rawBody) > 0 {
				resultBody = rawBody
			}
			select {
			case done <- struct{}{}:
			default:
			}
		}
		if len(hdrs) > 0 {
			if resultHeaders == nil {
				resultHeaders = make(map[string]string)
			}
			maps.Copy(resultHeaders, hdrs)
			for k := range resultHeaders {
				kl := strings.ToLower(k)
				if kl == "authorizations" || strings.Contains(kl, "accepts-controls") {
					select {
					case allHeadersCh <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}

	patterns := []string{
		"*" + pageHost + "/?token_movie=*",
		"*/bnsi/movies/*",
		"*.m3u8*",
	}
	hijackCancel, err := session.Hijack(patterns, func(req browser.HijackRequest) {
		mirageFacadeHandle(mirageFacadeCtx{
			ctx:                    sessionCtx,
			req:                    req,
			htmlBytes:              htmlBytes,
			pageHost:               pageHost,
			pageURL:                pageURL,
			idFile:                 idFile,
			balancerName:           balancerName,
			docFulfilled:           &docFulfilled,
			resultMu:               &resultMu,
			capturedMoviesURL:      &capturedMoviesURL,
			capturedMoviesHeaders:  &capturedMoviesHeaders,
			capturedMoviesBody:     &capturedMoviesBody,
			capturedRequestOrigin:  &capturedRequestOrigin,
			capturedRequestReferer: &capturedRequestReferer,
			signalDone:             signalDone,
		})
	})
	if err != nil {
		log.Warn().Err(err).Msg("mirage: facade — Hijack setup failed")
		return nil, false
	}
	defer hijackCancel()

	go func() {
		if err := session.Navigate(pageURL); err != nil {
			log.Debug().Err(err).Str("url", pageURL).Msg("mirage: facade — Navigate returned error (non-fatal)")
		}
	}()

	select {
	case <-done:
		select {
		case <-allHeadersCh:
			log.Info().Msg("mirage: facade — received stream auth headers from m3u8")
		case <-time.After(3 * time.Second):
			log.Warn().Msg("mirage: facade — timeout waiting for m3u8 auth headers")
		case <-sessionCtx.Done():
		}
		resultMu.Lock()
		s := resultStream
		b := resultBody
		h := resultHeaders
		mURL := capturedMoviesURL
		mHdrs := capturedMoviesHeaders
		mBody := capturedMoviesBody
		rOrigin := capturedRequestOrigin
		rReferer := capturedRequestReferer
		resultMu.Unlock()
		if s == "" {
			return nil, false
		}
		// DDoS-Guard cookie capture mirrors the chromedp path: ask the
		// session for cookies matching the stream URL host.
		if cookies := mirageFacadeCookies(session, s); cookies != "" {
			if h == nil {
				h = make(map[string]string)
			}
			h["Cookie"] = cookies
		}
		wsUrl, sid := mirageExtractWSParams(b)
		log.Info().Str("stream", s).Int("bodyLen", len(b)).Int("headerCount", len(h)).
			Str("wsUrl", wsUrl).Int("sidLen", len(sid)).Str("moviesURL", mURL).
			Str("reqOrigin", rOrigin).Str("reqReferer", rReferer).Str("engine", eng.Name()).
			Msg("mirage: facade — resolved")
		return &mirageResolveResult{
			RawBody:        b,
			Stream:         s,
			CDNHeaders:     h,
			WSUrl:          wsUrl,
			SID:            sid,
			RequestOrigin:  rOrigin,
			RequestReferer: rReferer,
			MoviesURL:      mURL,
			MoviesHeaders:  mHdrs,
			MoviesBody:     mBody,
		}, true
	case <-sessionCtx.Done():
		log.Warn().Msg("mirage: facade — resolve timed out")
		return nil, false
	}
}

// mirageFacadeCtx bundles the per-request state passed into the hijack
// handler — keeps the call site readable, avoids capturing 14 named
// variables in the closure.
type mirageFacadeCtx struct {
	ctx                    context.Context
	req                    browser.HijackRequest
	htmlBytes              []byte
	pageHost               string
	pageURL                string
	idFile                 int64
	balancerName           string
	docFulfilled           *bool
	resultMu               *sync.Mutex
	capturedMoviesURL      *string
	capturedMoviesHeaders  *map[string]string
	capturedMoviesBody     *[]byte
	capturedRequestOrigin  *string
	capturedRequestReferer *string
	signalDone             func(stream string, body []byte, hdrs map[string]string)
}

func mirageFacadeHandle(s mirageFacadeCtx) {
	reqURL := s.req.URL()
	method := s.req.Method()
	resType := strings.ToLower(s.req.ResourceType())

	// 1) Document — fulfil with the pre-fetched (id_file-patched) HTML.
	if (resType == "document" || resType == "") && strings.Contains(reqURL, s.pageHost) {
		s.resultMu.Lock()
		already := *s.docFulfilled
		if !already {
			*s.docFulfilled = true
		}
		s.resultMu.Unlock()
		if already {
			s.req.Continue()
			return
		}
		log.Info().Str("url", reqURL).Msg("mirage: facade — fulfilling Document with pre-fetched HTML")
		hdrs := http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}
		if err := s.req.Fulfill(200, hdrs, s.htmlBytes); err != nil {
			log.Warn().Err(err).Msg("mirage: facade — Fulfill Document failed")
			s.req.Continue()
		}
		return
	}

	// 2) /movies/ XHR — proxy via Go HTTP (Chrome won't honour URL
	// overrides on XHR), capture auth headers, fulfill response.
	if strings.Contains(reqURL, "/movies/") && (resType == "xhr" || resType == "fetch") {
		capturedHeaders := mirageFacadePickAuthHeaders(s.req.Headers())
		if len(capturedHeaders) > 0 {
			log.Info().Interface("hdrs", capturedHeaders).Msg("mirage: facade — captured /movies/ auth headers")
		}

		s.resultMu.Lock()
		*s.capturedMoviesURL = moviesIDRe.ReplaceAllString(reqURL, "/movies/"+strconv.FormatInt(s.idFile, 10))
		*s.capturedMoviesHeaders = headerToMap(s.req.Headers())
		*s.capturedMoviesBody = s.req.PostData()
		s.resultMu.Unlock()

		// Rebuild PostDataEntries for mirageProxyMoviesRequest. The
		// existing helper accepts the raw CDP shape, so wrap the body
		// in a single entry with base64-encoded payload.
		var entries []*network.PostDataEntry
		if pd := s.req.PostData(); len(pd) > 0 {
			entries = append(entries, &network.PostDataEntry{Bytes: base64.StdEncoding.EncodeToString(pd)})
		}
		cdpHeaders := network.Headers{}
		for k, v := range s.req.Headers() {
			if len(v) > 0 {
				cdpHeaders[k] = v[0]
			}
		}
		body, status, err := mirageProxyMoviesRequest(s.ctx, reqURL, method, entries, cdpHeaders, s.idFile, s.pageURL, s.balancerName)
		if err != nil {
			log.Warn().Err(err).Msg("mirage: facade — proxy /movies/ failed")
			s.req.Continue()
			return
		}
		if status >= 200 && status < 300 {
			if stream := mirageExtractHLSFromJSON(body); stream != "" {
				s.signalDone(stream, body, capturedHeaders)
			}
		}
		respHeaders := http.Header{
			"Content-Type":                []string{"application/json"},
			"Access-Control-Allow-Origin": []string{"*"},
		}
		_ = s.req.Fulfill(status, respHeaders, body)
		return
	}

	// 3) m3u8 — capture auth + Origin/Referer, continue to network.
	if strings.Contains(reqURL, ".m3u8") {
		if method == http.MethodOptions || method == "OPTIONS" {
			log.Info().Str("url", reqURL).Msg("mirage: facade — fulfilling m3u8 OPTIONS preflight")
			cors := http.Header{
				"Access-Control-Allow-Origin":  []string{"*"},
				"Access-Control-Allow-Methods": []string{"GET, OPTIONS"},
				"Access-Control-Allow-Headers": []string{"authorizations,accepts-controls,borth,origin,referer"},
				"Access-Control-Max-Age":       []string{"86400"},
			}
			_ = s.req.Fulfill(204, cors, nil)
			return
		}
		captured := mirageFacadePickAuthHeaders(s.req.Headers())
		origin := s.req.Headers().Get("Origin")
		referer := s.req.Headers().Get("Referer")
		if origin != "" || referer != "" {
			s.resultMu.Lock()
			if *s.capturedRequestOrigin == "" && origin != "" {
				*s.capturedRequestOrigin = origin
			}
			if *s.capturedRequestReferer == "" && referer != "" {
				*s.capturedRequestReferer = referer
			}
			s.resultMu.Unlock()
		}
		if len(captured) > 0 {
			log.Info().Interface("hdrs", captured).Str("url", reqURL).
				Msg("mirage: facade — captured m3u8 auth headers")
			s.signalDone("", nil, captured)
		}
		s.req.Continue()
		return
	}

	// Anything else — let it through.
	s.req.Continue()
}

// mirageFacadePickAuthHeaders extracts the CDN auth-related headers
// (Authorizations / Accepts-Controls / Borth …) from a generic
// http.Header — same predicate as the chromedp path uses on the raw
// CDP Headers map.
func mirageFacadePickAuthHeaders(h http.Header) map[string]string {
	if h == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range h {
		kl := strings.ToLower(k)
		if len(v) == 0 {
			continue
		}
		if kl == "borth" || kl == "authorization" || kl == "authorizations" ||
			strings.Contains(kl, "accepts-controls") || strings.Contains(kl, "accept-control") {
			out[k] = v[0]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func headerToMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) == 0 {
			continue
		}
		out[k] = v[0]
	}
	return out
}

// mirageFacadeCookies returns a Cookie-header value for the given stream
// URL, sourced from the browser session's jar. Used to attach DDoS-Guard
// __ddg* cookies to subsequent /proxy/ segment fetches.
func mirageFacadeCookies(session browser.Session, streamURL string) string {
	cookies, err := session.Cookies(streamURL)
	if err != nil || len(cookies) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, c := range cookies {
		if i > 0 {
			sb.WriteString("; ")
		}
		sb.WriteString(c.Name)
		sb.WriteByte('=')
		sb.WriteString(c.Value)
	}
	return sb.String()
}
