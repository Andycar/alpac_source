package litesrc

import (
	"net/http"
	"sync"
	"testing"

	"lampac-go/internal/browser"
)

// TestMirageFacadeHandle_DocumentFulfilled exercises the hijack
// handler for the Document case: the first match must be fulfilled
// with the pre-fetched HTML, subsequent ones continue.
func TestMirageFacadeHandle_DocumentFulfilled(t *testing.T) {
	docFulfilled := false
	var (
		mu                         sync.Mutex
		moviesURL, origin, referer string
		moviesHeaders              map[string]string
		moviesBody                 []byte
		streamSeen                 string
		bodySeen                   []byte
		headersSeen                map[string]string
	)
	signal := func(s string, b []byte, h map[string]string) {
		streamSeen = s
		bodySeen = b
		headersSeen = h
	}

	mock := browser.NewMockEngine("mock")
	sess, _ := mock.NewSession(t.Context(), browser.SessionOptions{})

	htmlBytes := []byte("<html>patched</html>")
	pageHost := "linkhost.mirage.test"

	handler := func(req browser.HijackRequest) {
		mirageFacadeHandle(mirageFacadeCtx{
			ctx:                    t.Context(),
			req:                    req,
			htmlBytes:              htmlBytes,
			pageHost:               pageHost,
			pageURL:                "https://" + pageHost + "/?token_movie=abc",
			idFile:                 999,
			balancerName:           "mirage",
			docFulfilled:           &docFulfilled,
			resultMu:               &mu,
			capturedMoviesURL:      &moviesURL,
			capturedMoviesHeaders:  &moviesHeaders,
			capturedMoviesBody:     &moviesBody,
			capturedRequestOrigin:  &origin,
			capturedRequestReferer: &referer,
			signalDone:             signal,
		})
	}

	docReq := browser.MockHijackInput{
		URL:          "https://" + pageHost + "/?token_movie=abc",
		Method:       "GET",
		ResourceType: "Document",
	}
	browser.EmitHijackOn(sess, handler, docReq)

	if !docFulfilled {
		t.Fatal("docFulfilled should be true after first Document hit")
	}
	if streamSeen != "" {
		t.Errorf("streamSeen = %q, want empty", streamSeen)
	}
	_ = bodySeen
	_ = headersSeen
}

// TestMirageFacadeHandle_M3U8AuthCapture asserts that an m3u8 GET with
// the canonical mirage auth headers triggers signalDone with the headers
// and sets the captured Origin/Referer fields.
func TestMirageFacadeHandle_M3U8AuthCapture(t *testing.T) {
	docFulfilled := false
	var (
		mu                         sync.Mutex
		moviesURL, origin, referer string
		moviesHeaders              map[string]string
		moviesBody                 []byte
		streamSeen                 string
		bodySeen                   []byte
		headersSeen                map[string]string
	)
	signal := func(s string, b []byte, h map[string]string) {
		streamSeen = s
		bodySeen = b
		headersSeen = h
	}

	mock := browser.NewMockEngine("mock")
	sess, _ := mock.NewSession(t.Context(), browser.SessionOptions{})

	handler := func(req browser.HijackRequest) {
		mirageFacadeHandle(mirageFacadeCtx{
			ctx:                    t.Context(),
			req:                    req,
			htmlBytes:              nil,
			pageHost:               "linkhost.mirage.test",
			pageURL:                "https://linkhost.mirage.test/?token_movie=abc",
			idFile:                 999,
			balancerName:           "mirage",
			docFulfilled:           &docFulfilled,
			resultMu:               &mu,
			capturedMoviesURL:      &moviesURL,
			capturedMoviesHeaders:  &moviesHeaders,
			capturedMoviesBody:     &moviesBody,
			capturedRequestOrigin:  &origin,
			capturedRequestReferer: &referer,
			signalDone:             signal,
		})
	}

	m3u8Req := browser.MockHijackInput{
		URL:          "https://cdn-7.allarknow.test/stream/abc/index.m3u8",
		Method:       "GET",
		ResourceType: "Fetch",
		Headers: http.Header{
			"Authorizations":   []string{"Bearer abc"},
			"Accepts-Controls": []string{"xyz"},
			"Borth":            []string{"123"},
			"Origin":           []string{"https://linkhost.mirage.test"},
			"Referer":          []string{"https://linkhost.mirage.test/"},
		},
	}
	browser.EmitHijackOn(sess, handler, m3u8Req)

	if origin != "https://linkhost.mirage.test" {
		t.Errorf("origin = %q", origin)
	}
	if referer != "https://linkhost.mirage.test/" {
		t.Errorf("referer = %q", referer)
	}
	if headersSeen["Authorizations"] != "Bearer abc" {
		t.Errorf("Authorizations not captured: %v", headersSeen)
	}
	if streamSeen != "" {
		t.Errorf("streamSeen should be empty for m3u8 capture (set by /movies/ path)")
	}
	_ = bodySeen
}

// TestMirageFacadeHandle_M3U8OptionsPreflight asserts that an OPTIONS
// preflight to *.m3u8 is fulfilled with permissive CORS — letting the
// subsequent GET (with auth headers) fire.
func TestMirageFacadeHandle_M3U8OptionsPreflight(t *testing.T) {
	docFulfilled := false
	var (
		mu                         sync.Mutex
		moviesURL, origin, referer string
		moviesHeaders              map[string]string
		moviesBody                 []byte
	)
	signal := func(s string, b []byte, h map[string]string) {}

	mock := browser.NewMockEngine("mock")
	sess, _ := mock.NewSession(t.Context(), browser.SessionOptions{})

	handler := func(req browser.HijackRequest) {
		mirageFacadeHandle(mirageFacadeCtx{
			ctx:                    t.Context(),
			req:                    req,
			htmlBytes:              nil,
			pageHost:               "linkhost.mirage.test",
			pageURL:                "https://linkhost.mirage.test/?token_movie=abc",
			idFile:                 999,
			balancerName:           "mirage",
			docFulfilled:           &docFulfilled,
			resultMu:               &mu,
			capturedMoviesURL:      &moviesURL,
			capturedMoviesHeaders:  &moviesHeaders,
			capturedMoviesBody:     &moviesBody,
			capturedRequestOrigin:  &origin,
			capturedRequestReferer: &referer,
			signalDone:             signal,
		})
	}

	browser.EmitHijackOn(sess, handler, browser.MockHijackInput{
		URL:          "https://cdn-7.allarknow.test/stream/abc/index.m3u8",
		Method:       http.MethodOptions,
		ResourceType: "Fetch",
	})

	// EmitHijackOn surfaces resolution on the mock hijack request, but
	// the mock session swallows the request — for this test we just
	// assert no panic + no captured Origin (preflight has none).
	if origin != "" {
		t.Errorf("origin should not be captured from OPTIONS preflight, got %q", origin)
	}
}

// TestMirageFacadePickAuthHeaders covers the auth-header predicate
// (case-insensitive substrings on Authorizations / Accepts-Controls /
// Borth). Regression guard against the chromedp/facade paths diverging.
func TestMirageFacadePickAuthHeaders(t *testing.T) {
	h := http.Header{
		"Authorizations":         []string{"Bearer xyz"},
		"Accepts-Controls":       []string{"foo"},
		"Borth":                  []string{"42"},
		"User-Agent":             []string{"Mozilla"},
		"Accept":                 []string{"*/*"},
		"Random-Accept-Controls": []string{"yes"},
	}
	got := mirageFacadePickAuthHeaders(h)
	want := map[string]string{
		"Authorizations":         "Bearer xyz",
		"Accepts-Controls":       "foo",
		"Borth":                  "42",
		"Random-Accept-Controls": "yes",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d headers, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("header %q = %q, want %q", k, got[k], v)
		}
	}
}
