// Package browser is a pluggable abstraction over headless-browser engines
// (chromedp, go-rod, Playwright) used by balancer scrapers like mirage,
// kinobase, turbo, zetflix.
//
// Goals:
//   - Default build keeps chromedp; alternative engines (rod, playwright)
//     are opt-in via build tags and register themselves at init().
//   - Existing code that imports chromedp directly continues to work; the
//     facade is for new code paths and migrations.
//   - The admin UI picks an engine by name. browser.List() returns engines
//     that are actually compiled into the binary, so the dropdown is
//     dynamic.
package browser

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Engine is a headless-browser implementation registered globally.
//
// Implementations must be safe for concurrent NewSession calls; sessions
// themselves are not required to be concurrent-safe.
type Engine interface {
	// Name is the canonical identifier ("chromedp", "rod", "playwright").
	// Returned name must match the registration key.
	Name() string

	// Available reports whether the engine can serve sessions right now.
	// nil means OK. Non-nil means the binary was compiled with this engine
	// but a runtime dependency is missing (e.g. Playwright's Chromium not
	// installed). The admin UI surfaces this error verbatim.
	Available() error

	// NewSession opens a new browser tab/context. The returned session is
	// closed by the caller (defer session.Close()).
	NewSession(ctx context.Context, opts SessionOptions) (Session, error)

	// Close releases engine-wide resources (browser pool, allocator).
	// After Close, NewSession must return an error.
	Close() error
}

// Session represents a single page in a browser. Sessions are not safe
// for concurrent use; one goroutine per session.
type Session interface {
	// Navigate loads the URL and waits for the load event with the default
	// timeout for the engine. Use WaitNavigation afterwards for custom
	// waits.
	Navigate(url string) error

	// WaitNavigation blocks until the next navigation completes or the
	// timeout elapses.
	WaitNavigation(timeout time.Duration) error

	// Eval runs the JavaScript expression in the page context. If out is
	// non-nil, the result is JSON-decoded into it.
	Eval(js string, out any) error

	// SetCookie installs a cookie into the browser's jar before navigation.
	SetCookie(c *http.Cookie, domain string) error

	// Cookies returns cookies visible to the given URL.
	Cookies(forURL string) ([]*http.Cookie, error)

	// Hijack starts intercepting requests matching any of the URL patterns
	// (chromedp/Rod glob style: "*.m3u8", "https://example.com/*"). The
	// handler is invoked for each matching request. The returned cancel
	// closes the interception channel; it is safe to call multiple times.
	Hijack(patterns []string, h HijackHandler) (cancel func(), err error)

	// HTML returns the current document's outerHTML.
	HTML() (string, error)

	// Screenshot returns a PNG capture of the viewport. Empty result if
	// the engine does not support screenshots.
	Screenshot() ([]byte, error)

	// Close terminates the session. Safe to call multiple times.
	Close() error
}

// HijackHandler receives intercepted requests. The handler MUST call
// exactly one of Continue / ContinueWith / Fulfill / Abort on the
// request before returning, otherwise the page will hang.
type HijackHandler func(req HijackRequest)

// HijackRequest is the API surface for a single intercepted request.
// Implementations wrap chromedp's Fetch.requestPaused, Rod's
// page.HijackRequests handler, and Playwright's route handler.
type HijackRequest interface {
	URL() string
	Method() string
	Headers() http.Header
	PostData() []byte
	ResourceType() string

	// Continue lets the request proceed unchanged to the network.
	Continue() error

	// ContinueWith forwards the request with a modified method/URL/
	// headers/body. Empty headers means "keep original headers".
	ContinueWith(method, url string, headers http.Header, body []byte) error

	// Abort cancels the request with the given reason ("Failed",
	// "Aborted", "BlockedByClient"; engines normalise unknown values).
	Abort(reason string) error

	// Fulfill responds to the request without hitting the network.
	Fulfill(status int, headers http.Header, body []byte) error

	// LoadResponse fetches the response body from the network without
	// delivering it to the page. After LoadResponse the caller must call
	// ContinueResponse or Fulfill to release the page.
	LoadResponse() (status int, headers http.Header, body []byte, err error)

	// ContinueResponse delivers a (possibly modified) response to the
	// page. Pass the values returned by LoadResponse unchanged to forward
	// as-is.
	ContinueResponse(status int, headers http.Header, body []byte) error
}

// Installer is implemented by engines that can lazily fetch their
// runtime dependencies (driver binaries, headless browser, etc).
//
// Today only the playwright engine implements this — it downloads its
// Node driver + Chromium into ~/.cache/ms-playwright on first call.
// The admin UI surfaces an "Install" button for engines that satisfy
// this interface AND currently report ErrEngineUnavailable; pressing
// it invokes EnsureInstalled and refreshes the engine status.
//
// rod auto-downloads on first NewSession (no admin action needed);
// chromedp expects Chrome from the OS (no install path) — neither
// implements this interface.
type Installer interface {
	EnsureInstalled() error
}

// ErrEngineUnavailable is returned by Engine.Available when the engine
// is compiled in but cannot serve requests (missing browser binary,
// missing playwright install, etc).
var ErrEngineUnavailable = errors.New("browser engine not available")

// ErrUnknownEngine is returned by Get for an engine name that has not
// been registered (not compiled into this binary).
var ErrUnknownEngine = errors.New("unknown browser engine")
