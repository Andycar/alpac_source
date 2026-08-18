package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// MockEngine is an in-memory engine implementation for unit tests.
// It records every interaction so tests can assert on navigation,
// hijack invocations and Eval calls without launching Chrome.
//
// Construct via NewMockEngine. Wire scripted responses with
// ProgramNavigate / ProgramEval / ProgramHijack.
type MockEngine struct {
	mu sync.Mutex

	name        string
	available   error
	navigations []string // every URL passed to Session.Navigate
	evals       []string // every JS expression passed to Session.Eval

	// Scripted: maps URL/JS expression → response.
	navResults  map[string]error
	evalResults map[string]any
	hijackScript HijackScript
}

// HijackScript provides scripted hijack responses keyed by URL prefix.
// The first matching entry wins. Set Default to a fallback action.
type HijackScript struct {
	ByPrefix map[string]HijackAction
	Default  HijackAction
}

// HijackAction is what the mock should do when a hijacked request is
// observed. Exactly one field should be set; other fields are ignored.
type HijackAction struct {
	Continue    bool
	Fulfill     *HijackFulfill
	Abort       string             // reason
	LoadAndPass *HijackFulfill     // simulate LoadResponse + ContinueResponse
}

type HijackFulfill struct {
	Status  int
	Headers http.Header
	Body    []byte
}

// NewMockEngine returns a fresh mock engine registered under name.
func NewMockEngine(name string) *MockEngine {
	return &MockEngine{
		name:        name,
		navResults:  map[string]error{},
		evalResults: map[string]any{},
	}
}

func (m *MockEngine) Name() string     { return m.name }
func (m *MockEngine) Available() error { return m.available }
func (m *MockEngine) SetAvailable(err error) {
	m.mu.Lock()
	m.available = err
	m.mu.Unlock()
}

func (m *MockEngine) Close() error { return nil }

func (m *MockEngine) NewSession(ctx context.Context, opts SessionOptions) (Session, error) {
	if err := m.Available(); err != nil {
		return nil, err
	}
	return &mockSession{engine: m, ctx: ctx, opts: opts.withDefaults()}, nil
}

// ProgramNavigate scripts the error returned for Session.Navigate(url).
// Nil means success. Unprogrammed URLs return nil.
func (m *MockEngine) ProgramNavigate(url string, err error) {
	m.mu.Lock()
	m.navResults[url] = err
	m.mu.Unlock()
}

// ProgramEval scripts the result decoded into Session.Eval's out
// parameter for the given JS expression.
func (m *MockEngine) ProgramEval(js string, result any) {
	m.mu.Lock()
	m.evalResults[js] = result
	m.mu.Unlock()
}

// ProgramHijack installs the script consulted by every hijack handler.
func (m *MockEngine) ProgramHijack(s HijackScript) {
	m.mu.Lock()
	m.hijackScript = s
	m.mu.Unlock()
}

// Navigations returns a copy of the recorded navigation URLs.
func (m *MockEngine) Navigations() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.navigations))
	copy(out, m.navigations)
	return out
}

// Evals returns a copy of the recorded Eval expressions.
func (m *MockEngine) Evals() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.evals))
	copy(out, m.evals)
	return out
}

// ----- mockSession -----

type mockSession struct {
	engine *MockEngine
	ctx    context.Context
	opts   SessionOptions
	closed bool

	cookies []*http.Cookie
}

func (s *mockSession) Navigate(url string) error {
	s.engine.mu.Lock()
	s.engine.navigations = append(s.engine.navigations, url)
	err := s.engine.navResults[url]
	s.engine.mu.Unlock()
	return err
}

func (s *mockSession) WaitNavigation(timeout time.Duration) error { return nil }

func (s *mockSession) Eval(js string, out any) error {
	s.engine.mu.Lock()
	s.engine.evals = append(s.engine.evals, js)
	result, ok := s.engine.evalResults[js]
	s.engine.mu.Unlock()
	if !ok || out == nil {
		return nil
	}
	// Re-marshal so we get the same semantics as JSON-based engines.
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (s *mockSession) SetCookie(c *http.Cookie, domain string) error {
	s.cookies = append(s.cookies, c)
	return nil
}

func (s *mockSession) Cookies(forURL string) ([]*http.Cookie, error) {
	return append([]*http.Cookie(nil), s.cookies...), nil
}

func (s *mockSession) Hijack(patterns []string, h HijackHandler) (func(), error) {
	if h == nil {
		return func() {}, errors.New("nil hijack handler")
	}
	// In the mock we don't actually intercept network; tests drive
	// the handler manually via EmitHijack.
	return func() {}, nil
}

func (s *mockSession) HTML() (string, error)         { return "<html></html>", nil }
func (s *mockSession) Screenshot() ([]byte, error)   { return nil, nil }
func (s *mockSession) Close() error                  { s.closed = true; return nil }

// EmitHijack drives the hijack handler with a synthesised request,
// applying the engine's HijackScript. Tests use this to exercise
// balancer hijack logic without a real browser.
func (s *mockSession) EmitHijack(h HijackHandler, req MockHijackInput) {
	r := &mockHijackReq{input: req, script: s.engine.hijackScript}
	h(r)
	if !r.resolved {
		// Default behaviour matches real engines: untouched requests
		// continue to the network.
		_ = r.Continue()
	}
}

// EmitHijackOn is a convenience for mocks-of-mocks: callers that don't
// keep a reference to mockSession can fish it back out via the
// returned Session.
func EmitHijackOn(s Session, h HijackHandler, req MockHijackInput) error {
	ms, ok := s.(*mockSession)
	if !ok {
		return fmt.Errorf("EmitHijackOn: session %T is not a mock", s)
	}
	ms.EmitHijack(h, req)
	return nil
}

// MockHijackInput is the data describing an intercepted request when
// using EmitHijack.
type MockHijackInput struct {
	URL          string
	Method       string
	Headers      http.Header
	PostData     []byte
	ResourceType string
}

// mockHijackReq tracks which terminal action the handler took so
// EmitHijack can default to Continue when the handler forgets.
type mockHijackReq struct {
	input    MockHijackInput
	script   HijackScript
	resolved bool

	// Captured by the handler for test assertions.
	Resolution string // "continue", "continueWith", "fulfill", "abort", "loadResponse"
	NewMethod  string
	NewURL     string
	NewHeaders http.Header
	NewBody    []byte
	FulfillStatus  int
	FulfillHeaders http.Header
	FulfillBody    []byte
	AbortReason    string
}

func (r *mockHijackReq) URL() string           { return r.input.URL }
func (r *mockHijackReq) Method() string        { return r.input.Method }
func (r *mockHijackReq) Headers() http.Header  { return cloneHeader(r.input.Headers) }
func (r *mockHijackReq) PostData() []byte      { return append([]byte(nil), r.input.PostData...) }
func (r *mockHijackReq) ResourceType() string  { return r.input.ResourceType }

func (r *mockHijackReq) Continue() error {
	r.resolved = true
	r.Resolution = "continue"
	return nil
}

func (r *mockHijackReq) ContinueWith(method, url string, headers http.Header, body []byte) error {
	r.resolved = true
	r.Resolution = "continueWith"
	r.NewMethod, r.NewURL = method, url
	r.NewHeaders, r.NewBody = cloneHeader(headers), append([]byte(nil), body...)
	return nil
}

func (r *mockHijackReq) Abort(reason string) error {
	r.resolved = true
	r.Resolution = "abort"
	r.AbortReason = reason
	return nil
}

func (r *mockHijackReq) Fulfill(status int, headers http.Header, body []byte) error {
	r.resolved = true
	r.Resolution = "fulfill"
	r.FulfillStatus = status
	r.FulfillHeaders = cloneHeader(headers)
	r.FulfillBody = append([]byte(nil), body...)
	return nil
}

func (r *mockHijackReq) LoadResponse() (int, http.Header, []byte, error) {
	// Look up scripted response by URL prefix.
	action, ok := r.lookupAction()
	if !ok || action.LoadAndPass == nil {
		return 0, nil, nil, errors.New("mock: no LoadResponse scripted for " + r.input.URL)
	}
	r.Resolution = "loadResponse"
	return action.LoadAndPass.Status, cloneHeader(action.LoadAndPass.Headers),
		append([]byte(nil), action.LoadAndPass.Body...), nil
}

func (r *mockHijackReq) ContinueResponse(status int, headers http.Header, body []byte) error {
	r.resolved = true
	if r.Resolution != "loadResponse" {
		r.Resolution = "continueResponse"
	}
	r.FulfillStatus = status
	r.FulfillHeaders = cloneHeader(headers)
	r.FulfillBody = append([]byte(nil), body...)
	return nil
}

func (r *mockHijackReq) lookupAction() (HijackAction, bool) {
	for prefix, a := range r.script.ByPrefix {
		if prefix != "" && len(r.input.URL) >= len(prefix) && r.input.URL[:len(prefix)] == prefix {
			return a, true
		}
	}
	var zero HijackAction
	if r.script.Default != zero {
		return r.script.Default, true
	}
	return zero, false
}

func cloneHeader(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}
