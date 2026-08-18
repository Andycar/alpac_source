package httpclient

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// flareReg holds the global FlareSolverr URL and two sets of opt-ins:
//
//   - system  — registered by code at init time (e.g. turbo.go calls
//     RegisterFlareSolverrBalancerSystem("turbo") because Turbo literally
//     cannot work without FlareSolverr). System entries survive any config
//     reload or admin UI save — the user can see them but not remove them.
//
//   - user    — populated from config.online.flaresolverr_balancers and from
//     the admin panel PUT. Replaced wholesale on each ApplyUserFlareSolverr
//     Balancers call so removing an entry in the UI actually takes effect.
//
// The effective opt-in set queried by FlareSolverrFetch is the union of both.
var flareReg = struct {
	sync.RWMutex
	url    string
	system map[string]struct{} // lowercase balancer names — set by code
	user   map[string]struct{} // lowercase balancer names — set by config/admin
}{
	system: make(map[string]struct{}),
	user:   make(map[string]struct{}),
}

// SetFlareSolverrURL configures the global FlareSolverr endpoint. Empty
// disables it. Called at startup with cfg.Online.FlareSolverr and on every
// admin PUT / config reload to keep the runtime in sync.
func SetFlareSolverrURL(url string) {
	flareReg.Lock()
	flareReg.url = strings.TrimSpace(url)
	flareReg.Unlock()
}

// FlareSolverrURL returns the configured endpoint or "" when disabled.
func FlareSolverrURL() string {
	flareReg.RLock()
	u := flareReg.url
	flareReg.RUnlock()
	return u
}

// RegisterFlareSolverrBalancer adds name to the user-managed set. Kept as the
// path for admin actions and config-driven opt-ins. For code-side opt-ins
// (balancers that literally require FS to function) use
// RegisterFlareSolverrBalancerSystem instead — those survive Reload().
func RegisterFlareSolverrBalancer(name string) {
	if name = strings.ToLower(strings.TrimSpace(name)); name == "" {
		return
	}
	flareReg.Lock()
	flareReg.user[name] = struct{}{}
	flareReg.Unlock()
}

// RegisterFlareSolverrBalancerSystem marks name as a system requirement. The
// entry is never wiped by a config reload or admin save — it persists for the
// lifetime of the process. Use only for balancers that genuinely cannot work
// without FlareSolverr (e.g. obrut.show needs the JS challenge solved).
func RegisterFlareSolverrBalancerSystem(name string) {
	if name = strings.ToLower(strings.TrimSpace(name)); name == "" {
		return
	}
	flareReg.Lock()
	flareReg.system[name] = struct{}{}
	flareReg.Unlock()
}

// UnregisterFlareSolverrBalancer removes name from the user-managed set only.
// System entries are intentionally untouched — admin UI is not allowed to
// disable a code-required dependency.
func UnregisterFlareSolverrBalancer(name string) {
	if name = strings.ToLower(strings.TrimSpace(name)); name == "" {
		return
	}
	flareReg.Lock()
	delete(flareReg.user, name)
	flareReg.Unlock()
}

// ApplyUserFlareSolverrBalancers replaces the user-managed set with names,
// without touching the system set. Called at startup with the config list,
// from Server.Reload when the config file changes, and from the admin PUT
// handler. Prior user entries not present in names are dropped — the UI's
// "remove this balancer" action actually takes effect.
func ApplyUserFlareSolverrBalancers(names []string) {
	flareReg.Lock()
	flareReg.user = make(map[string]struct{}, len(names))
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			flareReg.user[n] = struct{}{}
		}
	}
	flareReg.Unlock()
}

// IsBalancerFlareSolverr reports whether name is opted in by either the
// system or user set.
func IsBalancerFlareSolverr(name string) bool {
	if name = strings.ToLower(strings.TrimSpace(name)); name == "" {
		return false
	}
	flareReg.RLock()
	defer flareReg.RUnlock()
	if _, ok := flareReg.system[name]; ok {
		return true
	}
	_, ok := flareReg.user[name]
	return ok
}

// FlareSolverrBalancerEntry surfaces one opt-in for the admin UI. System=true
// means the entry was added by code and cannot be removed via the admin PUT.
type FlareSolverrBalancerEntry struct {
	Name   string `json:"name"`
	System bool   `json:"system"`
}

// ListFlareSolverrBalancers returns the effective opt-in set (system ∪ user)
// in sorted order. The annotated companion below preserves the system flag
// for the UI; this one is used internally when only the names are needed.
func ListFlareSolverrBalancers() []string {
	flareReg.RLock()
	defer flareReg.RUnlock()
	seen := make(map[string]struct{}, len(flareReg.system)+len(flareReg.user))
	out := make([]string, 0, len(seen))
	for n := range flareReg.system {
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	for n := range flareReg.user {
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sortStringsInPlace(out)
	return out
}

// ListFlareSolverrBalancersAnnotated returns the effective set with each
// entry tagged as system or user-managed. System entries come first so the UI
// can group them visually.
func ListFlareSolverrBalancersAnnotated() []FlareSolverrBalancerEntry {
	flareReg.RLock()
	system := make([]string, 0, len(flareReg.system))
	for n := range flareReg.system {
		system = append(system, n)
	}
	user := make([]string, 0, len(flareReg.user))
	for n := range flareReg.user {
		if _, ok := flareReg.system[n]; ok {
			continue
		}
		user = append(user, n)
	}
	flareReg.RUnlock()

	sortStringsInPlace(system)
	sortStringsInPlace(user)

	out := make([]FlareSolverrBalancerEntry, 0, len(system)+len(user))
	for _, n := range system {
		out = append(out, FlareSolverrBalancerEntry{Name: n, System: true})
	}
	for _, n := range user {
		out = append(out, FlareSolverrBalancerEntry{Name: n, System: false})
	}
	return out
}

// sortStringsInPlace is a tiny local helper to keep the FlareSolverr file free
// of an extra import of sort everywhere we just need a sorted slice.
func sortStringsInPlace(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// FlareSolverrFetchOptions tunes the universal helper. Zero values are fine.
type FlareSolverrFetchOptions struct {
	// Method is "GET" (default) or "POST".
	Method string
	// PostData is the form-encoded body for POST.
	PostData string
	// Cookies are sent to FlareSolverr as the starting cookie set.
	Cookies []FlareSolverrCookie
	// MaxTimeoutMS overrides the 30s default.
	MaxTimeoutMS int
	// Proxy overrides the balancer-registered SOCKS5. Mostly useful for tests
	// or one-off calls to a different upstream.
	Proxy string
	// Force routes through FlareSolverr even if the balancer isn't registered.
	// Useful when the caller wants FS unconditionally (e.g. a known CF site).
	Force bool
	// Headers are forwarded to the page load so target sees them on the
	// initial document request (Referer, Accept-Language, etc.).
	Headers map[string]string
	// Session is an existing FlareSolverr session ID. Reusing a session keeps
	// the Chrome browser context (cookie jar, local storage) across calls —
	// required for async-cookie challenges like Wallarm wsdk.js where the
	// first request triggers the challenge and the second request, in the
	// same browser context, sends back the cookie wsdk just set.
	Session string
}

// FlareSolverrFetch is the universal entry point used by built-in balancers
// and (indirectly) by JS modules. It returns the rendered HTML when:
//   - FlareSolverr is configured globally (SetFlareSolverrURL), AND
//   - the balancer is opted-in (or opts.Force=true).
//
// Otherwise returns ok=false so the caller can fall back to direct HTTP.
//
// When a SOCKS5 proxy is registered for the same balancer (see
// RegisterDirectProxy), it is automatically attached to the FlareSolverr
// request so Chrome dials the upstream through that proxy — solving JS
// challenges AND geoblocks in one round trip.
func FlareSolverrFetch(ctx context.Context, balancer, targetURL string, opts FlareSolverrFetchOptions) (FlareSolverrResult, bool) {
	url := FlareSolverrURL()
	if url == "" {
		return FlareSolverrResult{}, false
	}
	if !opts.Force && !IsBalancerFlareSolverr(balancer) {
		return FlareSolverrResult{}, false
	}

	method := strings.ToUpper(strings.TrimSpace(opts.Method))
	if method == "" {
		method = "GET"
	}
	cmd := "request.get"
	if method == "POST" {
		cmd = "request.post"
	}

	// Resolve the proxy: explicit override → balancer-registered SOCKS5.
	// FlareSolverr accepts socks5://host:port; the registry stores host:port,
	// so prepend the scheme.
	proxyURL := strings.TrimSpace(opts.Proxy)
	if proxyURL == "" {
		if addr := SocksAddrForBalancer(balancer); addr != "" {
			proxyURL = "socks5://" + addr
		}
	}

	return FlareSolverrDo(ctx, url, FlareSolverrRequest{
		Cmd:          cmd,
		URL:          targetURL,
		PostData:     opts.PostData,
		Cookies:      opts.Cookies,
		MaxTimeoutMS: opts.MaxTimeoutMS,
		Proxy:        proxyURL,
		Headers:      opts.Headers,
		Session:      opts.Session,
	})
}

// FlareSolverrCookie is one cookie returned by FlareSolverr after solving the
// challenge. Mirrors the relevant fields of the upstream JSON.
type FlareSolverrCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// FlareSolverrSolution is the payload FlareSolverr returns inside `solution`.
type FlareSolverrSolution struct {
	URL       string               `json:"url"`
	Status    int                  `json:"status"`
	Headers   map[string]string    `json:"headers"`
	Response  string               `json:"response"`
	Cookies   []FlareSolverrCookie `json:"cookies"`
	UserAgent string               `json:"userAgent"`
}

// FlareSolverrResult is the top-level FlareSolverr v1 response.
type FlareSolverrResult struct {
	Status   string               `json:"status"`
	Message  string               `json:"message"`
	Solution FlareSolverrSolution `json:"solution"`
}

// FlareSolverrRequest is the input to FlareSolverrDo. cmd is the FlareSolverr
// command ("request.get" or "request.post"); URL is the upstream target. Body
// (form-encoded) is only used for request.post. MaxTimeoutMS defaults to 30s.
//
// Proxy is the upstream proxy FlareSolverr's headless Chrome should dial
// through. Format follows FlareSolverr's API:
//
//	socks5://host:port      // SOCKS5 (residential / VLESS / etc.)
//	socks5://user:pw@host:port
//	http://host:port        // HTTP CONNECT
//	http://user:pw@host:port
//
// When set, requests bypass both the JS challenge AND the geoblock — Chrome
// fingerprint comes through, and the upstream sees the proxy's IP/country.
type FlareSolverrRequest struct {
	Cmd          string
	URL          string
	PostData     string
	MaxTimeoutMS int
	Cookies      []FlareSolverrCookie
	Proxy        string
	// Headers are forwarded to the Chrome page-load — useful when the target
	// behaves differently based on Referer / X-* / accept-language. The map
	// is rendered as `{"headers":[{"name":"Referer","value":"..."}]}` in the
	// FlareSolverr v1 payload.
	Headers map[string]string
	// Session reuses a previously-created FlareSolverr session (see
	// FlareSolverrCreateSession). When set, the same Chrome browser context
	// is used — cookies, local storage, and bot-detection fingerprint
	// persist across calls.
	Session string
}

// FlareSolverrDo issues a request through FlareSolverr and returns the parsed
// solution. ok=false if anything fails (network, non-200, status!="ok",
// undersized HTML). Logs are intentionally absent — caller decides verbosity.
func FlareSolverrDo(ctx context.Context, solverrURL string, req FlareSolverrRequest) (FlareSolverrResult, bool) {
	var empty FlareSolverrResult
	if strings.TrimSpace(solverrURL) == "" || strings.TrimSpace(req.URL) == "" {
		return empty, false
	}
	cmd := strings.TrimSpace(req.Cmd)
	if cmd == "" {
		cmd = "request.get"
	}
	timeoutMS := req.MaxTimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = 30000
	}

	payload := map[string]any{
		"cmd":        cmd,
		"url":        req.URL,
		"maxTimeout": timeoutMS,
	}
	if cmd == "request.post" && req.PostData != "" {
		payload["postData"] = req.PostData
	}
	if len(req.Cookies) > 0 {
		cookies := make([]map[string]string, 0, len(req.Cookies))
		for _, c := range req.Cookies {
			cookies = append(cookies, map[string]string{"name": c.Name, "value": c.Value})
		}
		payload["cookies"] = cookies
	}
	if proxy := strings.TrimSpace(req.Proxy); proxy != "" {
		payload["proxy"] = map[string]string{"url": proxy}
	}
	if len(req.Headers) > 0 {
		hs := make([]map[string]string, 0, len(req.Headers))
		for k, v := range req.Headers {
			if k == "" {
				continue
			}
			hs = append(hs, map[string]string{"name": k, "value": v})
		}
		payload["headers"] = hs
	}
	if sess := strings.TrimSpace(req.Session); sess != "" {
		payload["session"] = sess
	}

	body, err := stdjson.Marshal(payload)
	if err != nil {
		return empty, false
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(solverrURL, "/")+"/v1", bytes.NewReader(body))
	if err != nil {
		return empty, false
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// FlareSolverr typically responds within ~10s for simple challenges; cap
	// the client at maxTimeout + 5s slack so we don't wait forever on a stuck
	// solverr.
	client := &http.Client{Timeout: time.Duration(timeoutMS+5000) * time.Millisecond}
	resp, err := client.Do(httpReq)
	if err != nil {
		return empty, false
	}
	defer resp.Body.Close()

	var result FlareSolverrResult
	if err := stdjson.NewDecoder(resp.Body).Decode(&result); err != nil {
		return empty, false
	}
	if !strings.EqualFold(result.Status, "ok") {
		return result, false
	}
	if len(result.Solution.Response) < 100 {
		return result, false
	}
	return result, true
}

// Convenience helper used by older code paths — kept thin so callers that only
// need HTML can stay one-liner.
func FlareSolverrGet(ctx context.Context, solverrURL, targetURL string) (string, bool) {
	r, ok := FlareSolverrDo(ctx, solverrURL, FlareSolverrRequest{
		Cmd: "request.get",
		URL: targetURL,
	})
	if !ok {
		return "", false
	}
	return r.Solution.Response, true
}

// FlareSolverrCreateSession asks FlareSolverr to spawn a long-lived Chrome
// browser context and returns its session ID. The session keeps its own
// cookie jar and local storage across subsequent request.get calls — needed
// for async cookie challenges (Wallarm wsdk.js, some Cloudflare flavours)
// where one request triggers the challenge and the next request must send
// back the cookie the first one received.
//
// proxy is forwarded to FlareSolverr in the same socks5://host:port / http://
// format as a regular request, so Chrome dials upstreams through it.
//
// Sessions consume memory on the FlareSolverr side. Pair every Create with a
// Destroy (or rotate periodically) — they survive until FlareSolverr restarts
// otherwise.
func FlareSolverrCreateSession(ctx context.Context, solverrURL, proxy string) (string, error) {
	solverrURL = strings.TrimRight(strings.TrimSpace(solverrURL), "/")
	if solverrURL == "" {
		return "", fmt.Errorf("flaresolverr: empty URL")
	}
	payload := map[string]any{"cmd": "sessions.create"}
	if proxy = strings.TrimSpace(proxy); proxy != "" {
		payload["proxy"] = map[string]string{"url": proxy}
	}
	body, _ := stdjson.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, solverrURL+"/v1", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	// sessions.create is fast (a few hundred ms) — keep the timeout tight so
	// a hung FlareSolverr doesn't stall the caller.
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Session string `json:"session"`
	}
	if err := stdjson.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("flaresolverr: decode sessions.create: %w", err)
	}
	if !strings.EqualFold(out.Status, "ok") || out.Session == "" {
		return "", fmt.Errorf("flaresolverr: sessions.create failed: %s", out.Message)
	}
	return out.Session, nil
}

// FlareSolverrDestroySession releases a session created by
// FlareSolverrCreateSession. Best-effort — errors only matter for logging.
func FlareSolverrDestroySession(ctx context.Context, solverrURL, session string) error {
	solverrURL = strings.TrimRight(strings.TrimSpace(solverrURL), "/")
	session = strings.TrimSpace(session)
	if solverrURL == "" || session == "" {
		return nil
	}
	body, _ := stdjson.Marshal(map[string]any{
		"cmd":     "sessions.destroy",
		"session": session,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, solverrURL+"/v1", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// CookiesAsHeader builds a "Cookie:" header value from FlareSolverr cookies.
// Useful when the caller wants to replay a solved session via direct HTTP.
func CookiesAsHeader(cookies []FlareSolverrCookie) string {
	if len(cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c.Name == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", c.Name, c.Value))
	}
	return strings.Join(parts, "; ")
}
