//go:build !no_rod
// +build !no_rod

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

// rodEngine wraps github.com/go-rod/rod behind the Engine interface.
//
// Each NewSession launches a fresh browser process via lib/launcher and
// returns a single page wrapped in a Session. No engine-wide pooling —
// matches the chromedp wrapper for parity. Mirage will keep its own
// pool until it migrates fully to the facade.
type rodEngine struct {
	mu       sync.Mutex
	binPath  string // resolved on first Available() call
	binErr   error
	binProbe sync.Once
}

func init() { Register(&rodEngine{}) }

func (*rodEngine) Name() string { return "rod" }

// Available probes the launcher for a usable browser binary. The first
// call may take a moment (lib/launcher will auto-download Chromium into
// the user cache if no system Chrome is present). Subsequent calls are
// memoised.
func (e *rodEngine) Available() error {
	e.binProbe.Do(func() {
		// launcher.NewBrowser().Get() reuses a system Chrome if one is
		// on PATH, otherwise downloads a pinned Chromium. We catch the
		// error rather than panicking so admin UI can show it.
		b := launcher.NewBrowser()
		path, err := b.Get()
		if err != nil {
			e.binErr = fmt.Errorf("%w: rod launcher: %v", ErrEngineUnavailable, err)
			return
		}
		e.mu.Lock()
		e.binPath = path
		e.mu.Unlock()
	})
	return e.binErr
}

func (*rodEngine) Close() error { return nil }

func (e *rodEngine) NewSession(ctx context.Context, opts SessionOptions) (Session, error) {
	if err := e.Available(); err != nil {
		return nil, err
	}
	opts = opts.withDefaults()

	l := launcher.New().
		Headless(opts.Headless || true).
		Leakless(true).
		Set(flags.Flag("disable-blink-features"), "AutomationControlled").
		Set(flags.Flag("disable-extensions")).
		Set(flags.Flag("disable-default-apps")).
		Set(flags.Flag("disable-popup-blocking")).
		Set(flags.Flag("disable-background-networking")).
		Set(flags.Flag("disable-translate")).
		Set(flags.Flag("disable-sync")).
		Set(flags.Flag("mute-audio")).
		Set(flags.Flag("no-first-run"))

	e.mu.Lock()
	bin := e.binPath
	e.mu.Unlock()
	if bin != "" {
		l = l.Bin(bin)
	}
	userDataDir := l.Get(flags.UserDataDir)
	if opts.UserDataDir != "" {
		l = l.UserDataDir(opts.UserDataDir)
		userDataDir = opts.UserDataDir
	}
	l = l.Env(ChromeProcessEnvList(userDataDir)...)
	if opts.SocksProxy != "" {
		l = l.Proxy("socks5://" + opts.SocksProxy)
	}
	if opts.IgnoreCertErrors {
		l = l.Set(flags.Flag("ignore-certificate-errors"))
	}
	for _, f := range opts.ExtraFlags {
		if k, v, ok := strings.Cut(f, "="); ok {
			l = l.Set(flags.Flag(strings.TrimPrefix(k, "--")), v)
		} else {
			l = l.Set(flags.Flag(strings.TrimPrefix(f, "--")))
		}
	}

	wsURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("rod: launch: %w", err)
	}

	browser := rod.New().ControlURL(wsURL).Context(context.Background())
	if err := browser.Connect(); err != nil {
		l.Cleanup()
		return nil, fmt.Errorf("rod: connect: %w", err)
	}

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		_ = browser.Close()
		l.Cleanup()
		return nil, fmt.Errorf("rod: open page: %w", err)
	}

	s := &rodSession{
		launcher: l,
		browser:  browser,
		page:     page,
		opts:     opts,
	}

	// Apply stealth.JS before user-provided init scripts so balancer
	// patches can still override individual properties.
	if opts.UseStealth {
		if _, err := page.EvalOnNewDocument(stealth.JS); err != nil {
			s.Close()
			return nil, fmt.Errorf("rod: stealth init: %w", err)
		}
	}
	for _, script := range opts.InitScripts {
		if strings.TrimSpace(script) == "" {
			continue
		}
		if _, err := page.EvalOnNewDocument(script); err != nil {
			s.Close()
			return nil, fmt.Errorf("rod: init script: %w", err)
		}
	}

	if opts.UserAgent != "" {
		if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: opts.UserAgent}); err != nil {
			s.Close()
			return nil, fmt.Errorf("rod: set UA: %w", err)
		}
	}

	return s, nil
}

// ---------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------

type rodSession struct {
	launcher *launcher.Launcher
	browser  *rod.Browser
	page     *rod.Page
	opts     SessionOptions

	hijackMu     sync.Mutex
	hijackRouter *rod.HijackRouter

	closeOnce sync.Once
}

func (s *rodSession) Navigate(url string) error {
	p := s.page.Timeout(s.opts.NavigationTimeout)
	if err := p.Navigate(url); err != nil {
		return err
	}
	return p.WaitLoad()
}

func (s *rodSession) WaitNavigation(timeout time.Duration) error {
	wait := s.page.Timeout(timeout).WaitNavigation(proto.PageLifecycleEventNameNetworkAlmostIdle)
	wait()
	return nil
}

func (s *rodSession) Eval(js string, out any) error {
	// rod's page.Eval expects a JS *function expression* (it appends
	// .apply(this, args) under the hood). Wrap any user expression in
	// an arrow function so chromedp-style "document.title" works the
	// same way it does on the chromedp engine.
	wrapped := "() => (" + js + ")"
	res, err := s.page.Timeout(15 * time.Second).Eval(wrapped)
	if err != nil {
		return err
	}
	if out == nil || res == nil {
		return nil
	}
	// gson.JSON.Unmarshal can only decode the raw []byte once. Once a
	// typed getter (Str, Num, Map, …) has been called on the value it
	// returns "value has been parsed". Going through MarshalJSON ↔
	// json.Unmarshal works regardless of state.
	data, err := res.Value.MarshalJSON()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (s *rodSession) SetCookie(c *http.Cookie, domain string) error {
	if c == nil {
		return errors.New("nil cookie")
	}
	param := &proto.NetworkCookieParam{
		Name:     c.Name,
		Value:    c.Value,
		Domain:   domain,
		Path:     c.Path,
		Secure:   c.Secure,
		HTTPOnly: c.HttpOnly,
	}
	if !c.Expires.IsZero() {
		t := proto.TimeSinceEpoch(c.Expires.Unix())
		param.Expires = t
	}
	return s.page.SetCookies([]*proto.NetworkCookieParam{param})
}

func (s *rodSession) Cookies(forURL string) ([]*http.Cookie, error) {
	cookies, err := s.page.Cookies([]string{forURL})
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

func (s *rodSession) HTML() (string, error) {
	return s.page.HTML()
}

func (s *rodSession) Screenshot() ([]byte, error) {
	return s.page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
}

func (s *rodSession) Close() error {
	s.closeOnce.Do(func() {
		s.hijackMu.Lock()
		if s.hijackRouter != nil {
			_ = s.hijackRouter.Stop()
			s.hijackRouter = nil
		}
		s.hijackMu.Unlock()

		if s.page != nil {
			_ = s.page.Close()
		}
		if s.browser != nil {
			_ = s.browser.Close()
		}
		if s.launcher != nil {
			s.launcher.Cleanup()
		}
	})
	return nil
}

// ---------------------------------------------------------------------
// Hijack
// ---------------------------------------------------------------------

func (s *rodSession) Hijack(patterns []string, h HijackHandler) (func(), error) {
	if h == nil {
		return func() {}, errors.New("nil hijack handler")
	}
	if len(patterns) == 0 {
		return func() {}, errors.New("no hijack patterns")
	}

	s.hijackMu.Lock()
	if s.hijackRouter != nil {
		s.hijackMu.Unlock()
		return nil, errors.New("rod: hijack already active on this session")
	}
	router := s.page.HijackRequests()
	s.hijackRouter = router
	s.hijackMu.Unlock()

	for _, p := range patterns {
		if strings.TrimSpace(p) == "" {
			continue
		}
		err := router.Add(p, "", func(rh *rod.Hijack) {
			req := &rodHijackReq{ctx: rh}
			h(req)
			if !req.resolved {
				rh.ContinueRequest(&proto.FetchContinueRequest{})
			}
		})
		if err != nil {
			s.hijackMu.Lock()
			s.hijackRouter = nil
			s.hijackMu.Unlock()
			_ = router.Stop()
			return nil, fmt.Errorf("rod: hijack add %q: %w", p, err)
		}
	}

	go router.Run()

	return func() {
		s.hijackMu.Lock()
		defer s.hijackMu.Unlock()
		if s.hijackRouter == router {
			_ = router.Stop()
			s.hijackRouter = nil
		}
	}, nil
}

// ---------------------------------------------------------------------
// HijackRequest implementation
// ---------------------------------------------------------------------

type rodHijackReq struct {
	ctx      *rod.Hijack
	resolved bool
}

func (r *rodHijackReq) URL() string {
	if u := r.ctx.Request.URL(); u != nil {
		return u.String()
	}
	return ""
}

func (r *rodHijackReq) Method() string { return r.ctx.Request.Method() }

func (r *rodHijackReq) Headers() http.Header {
	out := http.Header{}
	for k, v := range r.ctx.Request.Headers() {
		out.Add(k, v.String())
	}
	return out
}

func (r *rodHijackReq) PostData() []byte {
	body := r.ctx.Request.Body()
	if body == "" {
		return nil
	}
	return []byte(body)
}

func (r *rodHijackReq) ResourceType() string { return string(r.ctx.Request.Type()) }

func (r *rodHijackReq) Continue() error {
	r.resolved = true
	r.ctx.ContinueRequest(&proto.FetchContinueRequest{})
	return nil
}

func (r *rodHijackReq) ContinueWith(method, url string, headers http.Header, body []byte) error {
	r.resolved = true
	cq := &proto.FetchContinueRequest{}
	if method != "" {
		cq.Method = method
	}
	if url != "" {
		cq.URL = url
	}
	if len(body) > 0 {
		// proto.FetchContinueRequest.PostData is base64-encoded per CDP.
		cq.PostData = body
	}
	if len(headers) > 0 {
		cq.Headers = headersToProto(headers)
	}
	r.ctx.ContinueRequest(cq)
	return nil
}

func (r *rodHijackReq) Abort(reason string) error {
	r.resolved = true
	r.ctx.Response.Fail(mapAbortReasonProto(reason))
	return nil
}

func (r *rodHijackReq) Fulfill(status int, headers http.Header, body []byte) error {
	r.resolved = true
	resp := r.ctx.Response
	resp.Payload().ResponseCode = status
	if len(headers) > 0 {
		for k, vs := range headers {
			for _, v := range vs {
				resp.SetHeader(k, v)
			}
		}
	}
	if len(body) > 0 {
		resp.SetBody(body)
	}
	return nil
}

// LoadResponse uses rod's built-in helper that fires the real request
// via the supplied http.Client (here: default) and stores body+headers
// on the response payload. After this the caller may modify and
// ContinueResponse.
func (r *rodHijackReq) LoadResponse() (int, http.Header, []byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	if err := r.ctx.LoadResponse(client, true); err != nil {
		return 0, nil, nil, err
	}
	payload := r.ctx.Response.Payload()
	return payload.ResponseCode, r.ctx.Response.Headers(), payload.Body, nil
}

func (r *rodHijackReq) ContinueResponse(status int, headers http.Header, body []byte) error {
	// In rod's model "continue response" after LoadResponse == fulfill
	// with the (potentially modified) payload.
	return r.Fulfill(status, headers, body)
}

func headersToProto(h http.Header) []*proto.FetchHeaderEntry {
	out := make([]*proto.FetchHeaderEntry, 0, len(h))
	for k, vs := range h {
		for _, v := range vs {
			out = append(out, &proto.FetchHeaderEntry{Name: k, Value: v})
		}
	}
	return out
}

func mapAbortReasonProto(s string) proto.NetworkErrorReason {
	switch strings.ToLower(s) {
	case "", "aborted":
		return proto.NetworkErrorReasonAborted
	case "failed":
		return proto.NetworkErrorReasonFailed
	case "timedout", "timeout":
		return proto.NetworkErrorReasonTimedOut
	case "accessdenied", "blocked", "blockedbyclient":
		return proto.NetworkErrorReasonBlockedByClient
	case "addressunreachable":
		return proto.NetworkErrorReasonAddressUnreachable
	case "connectionaborted":
		return proto.NetworkErrorReasonConnectionAborted
	case "connectionclosed":
		return proto.NetworkErrorReasonConnectionClosed
	case "connectionfailed":
		return proto.NetworkErrorReasonConnectionFailed
	case "connectionrefused":
		return proto.NetworkErrorReasonConnectionRefused
	case "connectionreset":
		return proto.NetworkErrorReasonConnectionReset
	case "internetdisconnected":
		return proto.NetworkErrorReasonInternetDisconnected
	case "namenotresolved":
		return proto.NetworkErrorReasonNameNotResolved
	}
	return proto.NetworkErrorReasonFailed
}
