package browser

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// chromedpEngine wraps github.com/chromedp/chromedp behind the Engine
// interface. Each NewSession spawns a fresh ExecAllocator + browser
// process and a single tab — there is no engine-wide browser pool yet.
// This keeps the implementation simple; balancers that need pool
// sharing (mirage) keep using chromedp directly until they migrate.
type chromedpEngine struct{}

func init() { Register(&chromedpEngine{}) }

func (*chromedpEngine) Name() string { return "chromedp" }

// Available checks that a Chrome binary is on PATH or in a known
// location. chromedp launches `google-chrome`/`chromium` by default;
// if neither resolves, NewSession would fail with a confusing exec
// error so we surface it eagerly.
func (*chromedpEngine) Available() error {
	for _, name := range chromeBinaryCandidates() {
		if _, err := exec.LookPath(name); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: no chrome/chromium binary in PATH", ErrEngineUnavailable)
}

func (*chromedpEngine) Close() error { return nil }

func chromeBinaryCandidates() []string {
	common := []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"chrome",
	}
	if runtime.GOOS == "darwin" {
		common = append(common,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	}
	return common
}

func (e *chromedpEngine) NewSession(ctx context.Context, opts SessionOptions) (Session, error) {
	if err := e.Available(); err != nil {
		return nil, err
	}
	opts = opts.withDefaults()
	ownedUserDataDir := ""
	if opts.UserDataDir == "" {
		dir, err := os.MkdirTemp("", "lampac-chromedp-")
		if err != nil {
			return nil, fmt.Errorf("chromedp: temp profile: %w", err)
		}
		ownedUserDataDir = dir
		opts.UserDataDir = dir
	}

	allocOpts := buildExecAllocatorOptions(opts)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)

	tabCtx, cancelTab := chromedp.NewContext(allocCtx)

	// Start the browser by navigating about:blank.
	//
	// The FIRST chromedp.Run on a context is what actually creates the tab, and
	// chromedp ties that tab's lifetime to the context it was run on. Running it
	// on a `context.WithTimeout(tabCtx, …)` child therefore binds the tab to the
	// CHILD — and the deferred cancel then closed the tab the moment NewSession
	// returned. The session looked healthy, but every later call on tabCtx failed
	// with "context canceled": that is what broke Hijack for every browser-sniff
	// source (vidlink/videasy/twoembed/hydraflix/flixcdn). vibix_browser.go warns
	// about this exact trap.
	//
	// So: run on tabCtx itself and bound the cold start with a watchdog instead.
	launched := make(chan error, 1)
	go func() { launched <- chromedp.Run(tabCtx, chromedp.Navigate("about:blank")) }()

	teardown := func() {
		cancelTab()
		cancelAlloc()
		if ownedUserDataDir != "" {
			_ = os.RemoveAll(ownedUserDataDir)
		}
	}

	select {
	case err := <-launched:
		if err != nil {
			teardown()
			return nil, fmt.Errorf("chromedp: start browser: %w", err)
		}
	case <-time.After(opts.NavigationTimeout):
		teardown()
		return nil, fmt.Errorf("chromedp: start browser: timed out after %s", opts.NavigationTimeout)
	}

	s := &chromedpSession{
		tabCtx:      tabCtx,
		cancelTab:   cancelTab,
		cancelAlloc: cancelAlloc,
		opts:        opts,
		ownedDir:    ownedUserDataDir,
	}

	// Inject init scripts (stealth, fingerprint patches). Each script is
	// added via Page.addScriptToEvaluateOnNewDocument so it runs before
	// any page JS on every navigation.
	if opts.UseStealth {
		if _, err := s.addInitScript(stealthJS()); err != nil {
			s.Close()
			return nil, fmt.Errorf("chromedp: stealth init: %w", err)
		}
	}
	for _, script := range opts.InitScripts {
		if strings.TrimSpace(script) == "" {
			continue
		}
		if _, err := s.addInitScript(script); err != nil {
			s.Close()
			return nil, fmt.Errorf("chromedp: addInitScript: %w", err)
		}
	}

	// Apply user-agent override post-launch — chromedp.UserAgent flag is
	// honoured only for the main process; per-context UA needs an
	// explicit Emulation.setUserAgentOverride.
	if opts.UserAgent != "" {
		if err := chromedp.Run(tabCtx, emulation.SetUserAgentOverride(opts.UserAgent)); err != nil {
			s.Close()
			return nil, fmt.Errorf("chromedp: set UA: %w", err)
		}
	}

	return s, nil
}

func buildExecAllocatorOptions(opts SessionOptions) []chromedp.ExecAllocatorOption {
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", opts.Headless || true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-translate", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
	)
	if opts.UserDataDir != "" {
		allocOpts = append(allocOpts, chromedp.UserDataDir(opts.UserDataDir))
	}
	allocOpts = append(allocOpts, chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
		cmd.Env = ChromeProcessEnv(cmd.Env, opts.UserDataDir)
	}))
	if opts.UserAgent != "" {
		allocOpts = append(allocOpts, chromedp.UserAgent(opts.UserAgent))
	}
	if opts.SocksProxy != "" {
		allocOpts = append(allocOpts, chromedp.ProxyServer("socks5://"+opts.SocksProxy))
	}
	if opts.IgnoreCertErrors {
		allocOpts = append(allocOpts, chromedp.Flag("ignore-certificate-errors", true))
	}
	for _, f := range opts.ExtraFlags {
		// Allow either "name" or "name=value" form.
		if k, v, ok := strings.Cut(f, "="); ok {
			allocOpts = append(allocOpts, chromedp.Flag(k, v))
		} else {
			allocOpts = append(allocOpts, chromedp.Flag(f, true))
		}
	}
	return allocOpts
}

// ---------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------

type chromedpSession struct {
	tabCtx      context.Context
	cancelTab   context.CancelFunc
	cancelAlloc context.CancelFunc
	opts        SessionOptions
	ownedDir    string

	closeOnce sync.Once
}

func (s *chromedpSession) Navigate(url string) error {
	ctx, cancel := context.WithTimeout(s.tabCtx, s.opts.NavigationTimeout)
	defer cancel()
	return chromedp.Run(ctx, chromedp.Navigate(url))
}

func (s *chromedpSession) WaitNavigation(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(s.tabCtx, timeout)
	defer cancel()
	// chromedp.WaitReady("body") is the closest portable wait — there
	// is no direct "next navigation" primitive.
	return chromedp.Run(ctx, chromedp.WaitReady("body"))
}

func (s *chromedpSession) Eval(js string, out any) error {
	ctx, cancel := context.WithTimeout(s.tabCtx, 15*time.Second)
	defer cancel()
	if out == nil {
		return chromedp.Run(ctx, chromedp.Evaluate(js, nil))
	}
	return chromedp.Run(ctx, chromedp.Evaluate(js, out))
}

func (s *chromedpSession) addInitScript(script string) (string, error) {
	ctx, cancel := context.WithTimeout(s.tabCtx, 5*time.Second)
	defer cancel()
	var id string
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		ident, err := page.AddScriptToEvaluateOnNewDocument(script).Do(c)
		if err != nil {
			return err
		}
		id = string(ident)
		return nil
	}))
	if err != nil {
		// Fall back via raw Evaluate so the script still applies to the
		// current document even if the init-script API failed.
		_ = chromedp.Run(s.tabCtx, chromedp.Evaluate(script, nil))
	}
	return id, err
}

func (s *chromedpSession) SetCookie(c *http.Cookie, domain string) error {
	if c == nil {
		return errors.New("nil cookie")
	}
	ctx, cancel := context.WithTimeout(s.tabCtx, 5*time.Second)
	defer cancel()
	return chromedp.Run(ctx, chromedp.ActionFunc(func(cc context.Context) error {
		action := network.SetCookie(c.Name, c.Value).
			WithDomain(domain).
			WithPath(c.Path).
			WithSecure(c.Secure).
			WithHTTPOnly(c.HttpOnly)
		if !c.Expires.IsZero() {
			expires := cdp.TimeSinceEpoch(c.Expires)
			action = action.WithExpires(&expires)
		}
		return action.Do(cc)
	}))
}

func (s *chromedpSession) Cookies(forURL string) ([]*http.Cookie, error) {
	ctx, cancel := context.WithTimeout(s.tabCtx, 5*time.Second)
	defer cancel()
	var cookies []*network.Cookie
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		got, err := network.GetCookies().WithURLs([]string{forURL}).Do(c)
		if err != nil {
			return err
		}
		cookies = got
		return nil
	}))
	if err != nil {
		return nil, err
	}
	out := make([]*http.Cookie, 0, len(cookies))
	for _, ck := range cookies {
		out = append(out, &http.Cookie{
			Name:     ck.Name,
			Value:    ck.Value,
			Domain:   ck.Domain,
			Path:     ck.Path,
			Secure:   ck.Secure,
			HttpOnly: ck.HTTPOnly,
		})
	}
	return out, nil
}

func (s *chromedpSession) HTML() (string, error) {
	ctx, cancel := context.WithTimeout(s.tabCtx, 5*time.Second)
	defer cancel()
	var html string
	err := chromedp.Run(ctx, chromedp.OuterHTML("html", &html))
	return html, err
}

func (s *chromedpSession) Screenshot() ([]byte, error) {
	ctx, cancel := context.WithTimeout(s.tabCtx, 10*time.Second)
	defer cancel()
	var buf []byte
	err := chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 80))
	return buf, err
}

func (s *chromedpSession) Close() error {
	s.closeOnce.Do(func() {
		if s.cancelTab != nil {
			s.cancelTab()
		}
		if s.cancelAlloc != nil {
			s.cancelAlloc()
		}
		if s.ownedDir != "" {
			_ = os.RemoveAll(s.ownedDir)
		}
	})
	return nil
}

// ---------------------------------------------------------------------
// Hijack — Fetch.requestPaused based.
// Patterns are passed verbatim to fetch.RequestPattern.URLPattern, so
// chromedp/Rod-style globs work ("*.m3u8", "https://x.com/*").
// ---------------------------------------------------------------------

// Encode a base64 string for binary response bodies.
var b64 = base64.StdEncoding

func (s *chromedpSession) Hijack(patterns []string, h HijackHandler) (func(), error) {
	if h == nil {
		return func() {}, errors.New("nil hijack handler")
	}
	if len(patterns) == 0 {
		return func() {}, errors.New("no hijack patterns")
	}

	listenCtx, cancelListen := context.WithCancel(s.tabCtx)

	fetchPatterns := make([]*fetch.RequestPattern, 0, len(patterns))
	for _, p := range patterns {
		if strings.TrimSpace(p) == "" {
			continue
		}
		fetchPatterns = append(fetchPatterns, &fetch.RequestPattern{
			URLPattern: p,
		})
	}

	enableCtx, cancelEnable := context.WithTimeout(s.tabCtx, 5*time.Second)
	defer cancelEnable()
	if err := chromedp.Run(enableCtx, fetch.Enable().WithPatterns(fetchPatterns)); err != nil {
		cancelListen()
		return nil, fmt.Errorf("chromedp: fetch.Enable: %w", err)
	}

	chromedp.ListenTarget(listenCtx, func(ev any) {
		e, ok := ev.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		// Run handler in its own goroutine so a slow handler doesn't
		// block the chromedp event loop.
		go func() {
			req := &chromedpHijackReq{
				tabCtx: s.tabCtx,
				event:  e,
			}
			h(req)
			if !req.resolved {
				_ = req.Continue()
			}
		}()
	})

	cancel := func() {
		cancelListen()
		// Best-effort disable. Use a fresh context because tabCtx may
		// already be cancelled if the session is being closed.
		disableCtx, cancelDis := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelDis()
		_ = chromedp.Run(disableCtx, fetch.Disable())
	}
	return cancel, nil
}

// ---------------------------------------------------------------------
// HijackRequest implementation
// ---------------------------------------------------------------------

type chromedpHijackReq struct {
	tabCtx context.Context
	event  *fetch.EventRequestPaused

	resolved bool
}

func (r *chromedpHijackReq) URL() string    { return r.event.Request.URL }
func (r *chromedpHijackReq) Method() string { return r.event.Request.Method }

func (r *chromedpHijackReq) Headers() http.Header {
	out := http.Header{}
	for k, v := range r.event.Request.Headers {
		switch s := v.(type) {
		case string:
			out.Add(k, s)
		case []any:
			for _, item := range s {
				if str, ok := item.(string); ok {
					out.Add(k, str)
				}
			}
		}
	}
	return out
}

func (r *chromedpHijackReq) PostData() []byte {
	if !r.event.Request.HasPostData {
		return nil
	}
	var combined []byte
	for _, entry := range r.event.Request.PostDataEntries {
		if entry == nil || entry.Bytes == "" {
			continue
		}
		// PostDataEntry.Bytes is base64-encoded per CDP spec.
		decoded, err := b64.DecodeString(entry.Bytes)
		if err != nil {
			// Fall back to raw bytes if decoding fails — better than
			// returning nil for diagnostic purposes.
			combined = append(combined, []byte(entry.Bytes)...)
			continue
		}
		combined = append(combined, decoded...)
	}
	return combined
}

func (r *chromedpHijackReq) ResourceType() string {
	return string(r.event.ResourceType)
}

func (r *chromedpHijackReq) Continue() error {
	r.resolved = true
	ctx, cancel := context.WithTimeout(r.tabCtx, 5*time.Second)
	defer cancel()
	return chromedp.Run(ctx, fetch.ContinueRequest(r.event.RequestID))
}

func (r *chromedpHijackReq) ContinueWith(method, url string, headers http.Header, body []byte) error {
	r.resolved = true
	action := fetch.ContinueRequest(r.event.RequestID)
	if method != "" {
		action = action.WithMethod(method)
	}
	if url != "" {
		action = action.WithURL(url)
	}
	if len(body) > 0 {
		action = action.WithPostData(b64.EncodeToString(body))
	}
	if len(headers) > 0 {
		entries := headersToFetch(headers)
		action = action.WithHeaders(entries)
	}
	ctx, cancel := context.WithTimeout(r.tabCtx, 5*time.Second)
	defer cancel()
	return chromedp.Run(ctx, action)
}

func (r *chromedpHijackReq) Abort(reason string) error {
	r.resolved = true
	er := mapAbortReason(reason)
	ctx, cancel := context.WithTimeout(r.tabCtx, 5*time.Second)
	defer cancel()
	return chromedp.Run(ctx, fetch.FailRequest(r.event.RequestID, er))
}

func (r *chromedpHijackReq) Fulfill(status int, headers http.Header, body []byte) error {
	r.resolved = true
	action := fetch.FulfillRequest(r.event.RequestID, int64(status))
	if len(headers) > 0 {
		action = action.WithResponseHeaders(headersToFetch(headers))
	}
	if len(body) > 0 {
		action = action.WithBody(b64.EncodeToString(body))
	}
	ctx, cancel := context.WithTimeout(r.tabCtx, 5*time.Second)
	defer cancel()
	return chromedp.Run(ctx, action)
}

// LoadResponse is intentionally unimplemented in the chromedp wrapper
// v1. The CDP flow requires re-enabling Fetch with RequestStage=Response
// for the URL, calling fetch.GetResponseBody, then continueResponse —
// which doesn't compose well with the request-stage handler we
// register. New code paths that need response inspection should use
// the rod or playwright engines (which expose a single LoadResponse
// call). When a real consumer arrives we'll wire this through.
func (r *chromedpHijackReq) LoadResponse() (int, http.Header, []byte, error) {
	return 0, nil, nil, errors.New("chromedp: LoadResponse not supported in v1; use rod/playwright engine")
}

func (r *chromedpHijackReq) ContinueResponse(status int, headers http.Header, body []byte) error {
	r.resolved = true
	// We approximate by Fulfill — the request has not actually been sent
	// at request-stage interception, so this still satisfies the page.
	return r.Fulfill(status, headers, body)
}

func headersToFetch(h http.Header) []*fetch.HeaderEntry {
	out := make([]*fetch.HeaderEntry, 0, len(h))
	for k, vs := range h {
		for _, v := range vs {
			out = append(out, &fetch.HeaderEntry{Name: k, Value: v})
		}
	}
	return out
}

func mapAbortReason(s string) network.ErrorReason {
	switch strings.ToLower(s) {
	case "aborted", "":
		return network.ErrorReasonAborted
	case "failed":
		return network.ErrorReasonFailed
	case "timedout", "timeout":
		return network.ErrorReasonTimedOut
	case "accessdenied", "blocked", "blockedbyclient":
		return network.ErrorReasonBlockedByClient
	case "addressunreachable":
		return network.ErrorReasonAddressUnreachable
	case "connectionaborted":
		return network.ErrorReasonConnectionAborted
	case "connectionclosed":
		return network.ErrorReasonConnectionClosed
	case "connectionfailed":
		return network.ErrorReasonConnectionFailed
	case "connectionrefused":
		return network.ErrorReasonConnectionRefused
	case "connectionreset":
		return network.ErrorReasonConnectionReset
	case "internetdisconnected":
		return network.ErrorReasonInternetDisconnected
	case "namenotresolved":
		return network.ErrorReasonNameNotResolved
	}
	return network.ErrorReasonFailed
}
