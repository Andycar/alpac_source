//go:build playwright
// +build playwright

// Package browser — Playwright engine implementation.
//
// Build with `-tags playwright` to enable. The engine is registered at
// init() under the name "playwright"; without the tag the binary
// neither links playwright-go nor pulls its Node.js driver.
//
// Trade-offs:
//   - Heaviest backend by far. Adds ~10MB of Go dependencies and pulls
//     a Node.js driver subprocess on first use (~50MB more in
//     ~/.cache/ms-playwright). Chromium is another ~140MB.
//   - In return: most polished hijack API (Page.Route), real WebKit /
//     Firefox support if we ever need fingerprint diversity, and the
//     best devtools UX (trace viewer) for hunting CDN-side issues.
//
// First-call behaviour: NewSession runs `playwright.Install` if no
// driver is present. That's a long blocking operation (network +
// disk). The admin UI exposes the same install action explicitly so
// the wait happens at admin time rather than blocking a user
// request. See /api/browser/playwright-install in admin_stats.go.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	pw "github.com/playwright-community/playwright-go"
)

type playwrightEngine struct {
	mu         sync.Mutex
	driver     *pw.Playwright // lazily started; nil until first NewSession succeeds
	available  error          // memoised Available() result; nil = OK
	installed  bool           // true once playwright.Install ran (or driver verified)
	installErr error
}

func init() { Register(&playwrightEngine{}) }

func (*playwrightEngine) Name() string { return "playwright" }

// Available probes whether the driver and Chromium are installed.
// Without a successful install/probe NewSession will fail with
// ErrEngineUnavailable, so the admin UI surfaces this state and the
// "Install Playwright" button is offered.
func (e *playwrightEngine) Available() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.installed {
		return e.installErr
	}
	// Try a cheap probe: start the driver. If it fails the error is
	// invariably "please install the driver first" — surface it as
	// ErrEngineUnavailable wrapped.
	driver, err := pw.Run(&pw.RunOptions{Verbose: false})
	if err != nil {
		e.available = fmt.Errorf("%w: %v", ErrEngineUnavailable, err)
		return e.available
	}
	e.driver = driver
	e.installed = true
	e.available = nil
	return nil
}

// EnsureInstalled is called by the admin UI to trigger a one-time
// install of the driver + Chromium. Blocks for the duration of the
// download (minutes). On success Available() will return nil
// afterwards. Returns the original install error for surfacing.
func (e *playwrightEngine) EnsureInstalled() error {
	e.mu.Lock()
	already := e.installed
	e.mu.Unlock()
	if already {
		return nil
	}
	if err := pw.Install(&pw.RunOptions{
		Browsers: []string{"chromium"},
		Verbose:  false,
	}); err != nil {
		e.mu.Lock()
		e.installErr = err
		e.mu.Unlock()
		return fmt.Errorf("playwright install: %w", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.driver == nil {
		driver, err := pw.Run(&pw.RunOptions{Verbose: false})
		if err != nil {
			e.installErr = err
			return fmt.Errorf("playwright run after install: %w", err)
		}
		e.driver = driver
	}
	e.installed = true
	e.installErr = nil
	e.available = nil
	return nil
}

// Close shuts the playwright driver if one was started. Called from
// server shutdown via Server.Close (graceful path).
func (e *playwrightEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.driver == nil {
		return nil
	}
	err := e.driver.Stop()
	e.driver = nil
	e.installed = false
	return err
}

func (e *playwrightEngine) NewSession(ctx context.Context, opts SessionOptions) (Session, error) {
	if err := e.Available(); err != nil {
		return nil, err
	}
	opts = opts.withDefaults()

	e.mu.Lock()
	driver := e.driver
	e.mu.Unlock()
	if driver == nil {
		return nil, fmt.Errorf("%w: playwright driver not running", ErrEngineUnavailable)
	}

	// One browser per session — symmetric with chromedp/rod wrappers.
	// playwright-go itself encourages context reuse, but our facade
	// runs short-lived resolve flows where teardown after the resolve
	// is cleaner.
	ownedUserDataDir := ""
	userDataDir := opts.UserDataDir
	if userDataDir == "" {
		dir, err := os.MkdirTemp("", "lampac-playwright-")
		if err != nil {
			return nil, fmt.Errorf("playwright temp profile: %w", err)
		}
		ownedUserDataDir = dir
		userDataDir = dir
	}
	launchOpts := pw.BrowserTypeLaunchOptions{
		Headless: pw.Bool(opts.Headless || true),
		Env:      ChromeProcessEnvMap(userDataDir),
	}
	if opts.SocksProxy != "" {
		launchOpts.Proxy = &pw.Proxy{Server: "socks5://" + opts.SocksProxy}
	}

	// Compose Chromium args from default-safe flags + caller extras.
	args := []string{
		"--disable-blink-features=AutomationControlled",
		"--disable-extensions",
		"--disable-default-apps",
		"--disable-popup-blocking",
		"--disable-background-networking",
		"--disable-translate",
		"--disable-sync",
		"--mute-audio",
		"--no-first-run",
	}
	if opts.IgnoreCertErrors {
		args = append(args, "--ignore-certificate-errors")
	}
	for _, f := range opts.ExtraFlags {
		if !strings.HasPrefix(f, "--") {
			f = "--" + f
		}
		args = append(args, f)
	}
	launchOpts.Args = args

	browser, err := driver.Chromium.Launch(launchOpts)
	if err != nil {
		if ownedUserDataDir != "" {
			_ = os.RemoveAll(ownedUserDataDir)
		}
		return nil, fmt.Errorf("playwright launch: %w", err)
	}

	ctxOpts := pw.BrowserNewContextOptions{}
	if opts.UserAgent != "" {
		ctxOpts.UserAgent = pw.String(opts.UserAgent)
	}
	if opts.IgnoreCertErrors {
		ctxOpts.IgnoreHttpsErrors = pw.Bool(true)
	}
	bctx, err := browser.NewContext(ctxOpts)
	if err != nil {
		_ = browser.Close()
		if ownedUserDataDir != "" {
			_ = os.RemoveAll(ownedUserDataDir)
		}
		return nil, fmt.Errorf("playwright NewContext: %w", err)
	}
	page, err := bctx.NewPage()
	if err != nil {
		_ = bctx.Close()
		_ = browser.Close()
		if ownedUserDataDir != "" {
			_ = os.RemoveAll(ownedUserDataDir)
		}
		return nil, fmt.Errorf("playwright NewPage: %w", err)
	}

	s := &playwrightSession{
		ctx:      ctx,
		browser:  browser,
		bctx:     bctx,
		page:     page,
		opts:     opts,
		ownedDir: ownedUserDataDir,
	}

	// Stealth and any caller-supplied init scripts run on every new
	// document (matches the rod/chromedp engines).
	if opts.UseStealth {
		if err := page.AddInitScript(pw.Script{Content: pw.String(stealthJS())}); err != nil {
			s.Close()
			return nil, fmt.Errorf("playwright stealth init: %w", err)
		}
	}
	for _, script := range opts.InitScripts {
		if strings.TrimSpace(script) == "" {
			continue
		}
		if err := page.AddInitScript(pw.Script{Content: pw.String(script)}); err != nil {
			s.Close()
			return nil, fmt.Errorf("playwright init script: %w", err)
		}
	}

	return s, nil
}

// ---------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------

type playwrightSession struct {
	ctx      context.Context
	browser  pw.Browser
	bctx     pw.BrowserContext
	page     pw.Page
	opts     SessionOptions
	ownedDir string

	closeOnce sync.Once
}

func (s *playwrightSession) Navigate(url string) error {
	_, err := s.page.Goto(url, pw.PageGotoOptions{
		Timeout: pw.Float(float64(s.opts.NavigationTimeout / time.Millisecond)),
	})
	return err
}

func (s *playwrightSession) WaitNavigation(timeout time.Duration) error {
	return s.page.WaitForLoadState(pw.PageWaitForLoadStateOptions{
		State:   pw.LoadStateNetworkidle,
		Timeout: pw.Float(float64(timeout / time.Millisecond)),
	})
}

func (s *playwrightSession) Eval(js string, out any) error {
	res, err := s.page.Evaluate(js)
	if err != nil {
		return err
	}
	if out == nil || res == nil {
		return nil
	}
	// Playwright returns native Go types decoded from JSON. To match
	// the rod/chromedp Eval contract (out is whatever JSON unmarshal
	// produces) we re-marshal/unmarshal — cheap and consistent.
	return playwrightCoerce(res, out)
}

func (s *playwrightSession) SetCookie(c *http.Cookie, domain string) error {
	if c == nil {
		return errors.New("nil cookie")
	}
	cookie := pw.OptionalCookie{
		Name:     c.Name,
		Value:    c.Value,
		Domain:   pw.String(domain),
		Path:     pw.String(c.Path),
		Secure:   pw.Bool(c.Secure),
		HttpOnly: pw.Bool(c.HttpOnly),
	}
	if !c.Expires.IsZero() {
		cookie.Expires = pw.Float(float64(c.Expires.Unix()))
	}
	return s.bctx.AddCookies([]pw.OptionalCookie{cookie})
}

func (s *playwrightSession) Cookies(forURL string) ([]*http.Cookie, error) {
	cs, err := s.bctx.Cookies(forURL)
	if err != nil {
		return nil, err
	}
	out := make([]*http.Cookie, 0, len(cs))
	for _, c := range cs {
		out = append(out, &http.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HttpOnly: c.HttpOnly,
		})
	}
	return out, nil
}

func (s *playwrightSession) HTML() (string, error) {
	return s.page.Content()
}

func (s *playwrightSession) Screenshot() ([]byte, error) {
	return s.page.Screenshot()
}

func (s *playwrightSession) Close() error {
	s.closeOnce.Do(func() {
		if s.page != nil {
			_ = s.page.Close()
		}
		if s.bctx != nil {
			_ = s.bctx.Close()
		}
		if s.browser != nil {
			_ = s.browser.Close()
		}
		if s.ownedDir != "" {
			_ = os.RemoveAll(s.ownedDir)
		}
	})
	return nil
}

// ---------------------------------------------------------------------
// Hijack
// ---------------------------------------------------------------------

func (s *playwrightSession) Hijack(patterns []string, h HijackHandler) (func(), error) {
	if h == nil {
		return func() {}, errors.New("nil hijack handler")
	}
	if len(patterns) == 0 {
		return func() {}, errors.New("no hijack patterns")
	}

	registered := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if strings.TrimSpace(p) == "" {
			continue
		}
		err := s.page.Route(p, func(route pw.Route) {
			req := &playwrightHijackReq{route: route, request: route.Request()}
			h(req)
			if !req.resolved {
				_ = req.Continue()
			}
		})
		if err != nil {
			return nil, fmt.Errorf("playwright Route(%q): %w", p, err)
		}
		registered = append(registered, p)
	}

	cancel := func() {
		for _, p := range registered {
			_ = s.page.Unroute(p)
		}
	}
	return cancel, nil
}

// ---------------------------------------------------------------------
// HijackRequest
// ---------------------------------------------------------------------

type playwrightHijackReq struct {
	route    pw.Route
	request  pw.Request
	resolved bool
}

func (r *playwrightHijackReq) URL() string    { return r.request.URL() }
func (r *playwrightHijackReq) Method() string { return r.request.Method() }

func (r *playwrightHijackReq) Headers() http.Header {
	out := http.Header{}
	for k, v := range r.request.Headers() {
		out.Add(k, v)
	}
	return out
}

func (r *playwrightHijackReq) PostData() []byte {
	buf, err := r.request.PostDataBuffer()
	if err != nil || buf == nil {
		return nil
	}
	return buf
}

func (r *playwrightHijackReq) ResourceType() string { return r.request.ResourceType() }

func (r *playwrightHijackReq) Continue() error {
	r.resolved = true
	return r.route.Continue()
}

func (r *playwrightHijackReq) ContinueWith(method, url string, headers http.Header, body []byte) error {
	r.resolved = true
	opts := pw.RouteContinueOptions{}
	if method != "" {
		opts.Method = pw.String(method)
	}
	if url != "" {
		opts.URL = pw.String(url)
	}
	if len(body) > 0 {
		opts.PostData = body
	}
	if len(headers) > 0 {
		opts.Headers = headerMapFromHeader(headers)
	}
	return r.route.Continue(opts)
}

func (r *playwrightHijackReq) Abort(reason string) error {
	r.resolved = true
	if reason == "" {
		return r.route.Abort()
	}
	return r.route.Abort(reason)
}

func (r *playwrightHijackReq) Fulfill(status int, headers http.Header, body []byte) error {
	r.resolved = true
	opts := pw.RouteFulfillOptions{}
	if status != 0 {
		opts.Status = pw.Int(status)
	}
	if len(headers) > 0 {
		opts.Headers = headerMapFromHeader(headers)
	}
	if len(body) > 0 {
		opts.Body = body
	}
	return r.route.Fulfill(opts)
}

// LoadResponse uses route.Fetch to perform the real network request
// without delivering the response to the page. Caller then has the
// option to modify and dispatch via ContinueResponse.
func (r *playwrightHijackReq) LoadResponse() (int, http.Header, []byte, error) {
	resp, err := r.route.Fetch()
	if err != nil {
		return 0, nil, nil, err
	}
	body, err := resp.Body()
	if err != nil {
		return 0, nil, nil, err
	}
	hdrs := http.Header{}
	for k, v := range resp.Headers() {
		hdrs.Add(k, v)
	}
	return resp.Status(), hdrs, body, nil
}

func (r *playwrightHijackReq) ContinueResponse(status int, headers http.Header, body []byte) error {
	// Playwright models "continue with modified response" as fulfill
	// with the new payload.
	return r.Fulfill(status, headers, body)
}

func headerMapFromHeader(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

// playwrightCoerce re-marshals a playwright eval result into the user's
// target type, matching the rod/chromedp engines' contract that out
// receives JSON-decoded data. Fast path for common scalar types so we
// don't go through encoding/json when we can avoid it.
func playwrightCoerce(in any, out any) error {
	switch dst := out.(type) {
	case *string:
		if s, ok := in.(string); ok {
			*dst = s
			return nil
		}
	case *bool:
		if b, ok := in.(bool); ok {
			*dst = b
			return nil
		}
	case *int:
		if f, ok := in.(float64); ok {
			*dst = int(f)
			return nil
		}
	case *int64:
		if f, ok := in.(float64); ok {
			*dst = int64(f)
			return nil
		}
	case *float64:
		if f, ok := in.(float64); ok {
			*dst = f
			return nil
		}
	}
	// Fallback: JSON roundtrip.
	b, err := playwrightJSONMarshal(in)
	if err != nil {
		return err
	}
	return playwrightJSONUnmarshal(b, out)
}
