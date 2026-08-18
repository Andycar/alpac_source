package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/tgauth"
)

// TestTSSuffixRequiresAuth verifies the mutating-endpoint gate matches the
// in-process router policy (POST /torrents, /cache, GET /playlistall require
// token; everything else is open). /settings is body-dependent — see
// TestTSSettingsProbeOpen.
func TestTSSuffixRequiresAuth(t *testing.T) {
	cases := []struct {
		suffix string
		method string
		want   bool
	}{
		{"torrents", http.MethodPost, true},
		{"cache", http.MethodPost, true},
		{"cache", http.MethodGet, false}, // /cache is POST-only; GET unaffected
		{"playlistall/all.m3u", http.MethodGet, true},
		{"stream", http.MethodGet, false},
		{"stream/Movie.mp4", http.MethodGet, false},
		{"playlist", http.MethodGet, false},
		{"echo", http.MethodGet, false},
		{"static/js/main.js", http.MethodGet, false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, "/ts/"+tc.suffix, nil)
		if got := tsSuffixRequiresAuth(tc.suffix, req); got != tc.want {
			t.Errorf("tsSuffixRequiresAuth(%q,%q) = %v, want %v", tc.suffix, tc.method, got, tc.want)
		}
	}
}

// TestTSSettingsProbeOpen pins the liveness-check carve-out: Lampa's
// TorrServer connection check POSTs /settings {action:"get"} and must NOT be
// gated (else it 401s and shows the "не удалось подключиться" wizard), while
// {action:"set"} stays protected. The body must survive the peek.
func TestTSSettingsProbeOpen(t *testing.T) {
	cases := []struct {
		body    string
		reqAuth bool // want tsSuffixRequiresAuth == true
	}{
		{`{"action":"get"}`, false},
		{`{"action":"GET"}`, false},
		{``, false},                // empty body = read probe
		{`{"action":"set"}`, true}, // mutation → gated
		{`{"action":"set","settings":{"CacheSize":1}}`, true},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/ts/settings", strings.NewReader(tc.body))
		got := tsSuffixRequiresAuth("settings", req)
		if got != tc.reqAuth {
			t.Errorf("settings body %q: requiresAuth = %v, want %v", tc.body, got, tc.reqAuth)
		}
		// Body must be restored for downstream decode.
		b, _ := io.ReadAll(req.Body)
		if string(b) != tc.body {
			t.Errorf("settings body not restored after peek: got %q want %q", string(b), tc.body)
		}
	}
}

// TestRequestUserTokenFromAny verifies cookie+query token extraction.
func TestRequestUserTokenFromAny(t *testing.T) {
	t.Run("cookie wins", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ts/echo?token=qtok", nil)
		req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "cookietok"})
		if got := requestUserTokenFromAny(req); got != "cookietok" {
			t.Errorf("got %q, want cookietok", got)
		}
	})
	t.Run("query fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ts/echo?token=qtok", nil)
		if got := requestUserTokenFromAny(req); got != "qtok" {
			t.Errorf("got %q, want qtok", got)
		}
	})
	t.Run("empty cookie falls through to query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ts/echo?token=qtok", nil)
		req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "   "})
		if got := requestUserTokenFromAny(req); got != "qtok" {
			t.Errorf("got %q, want qtok", got)
		}
	})
	t.Run("none", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ts/echo", nil)
		if got := requestUserTokenFromAny(req); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// TestRequestUserTokenFromAnyBasic verifies the Basic "uid:ts" fallback that
// Lampa's native TorrServer client relies on (ts.js sets login=<device uid>,
// password="ts"): a bound device uid must resolve to its user token; unknown
// uids, wrong passwords and the anonymous "ts" login must not.
func TestRequestUserTokenFromAnyBasic(t *testing.T) {
	prev := tsTokenStoreRef
	t.Cleanup(func() { tsTokenStoreRef = prev })

	store := tgauth.NewStore(t.TempDir())
	t.Cleanup(store.Close)
	if err := store.Add(tgauth.ApprovedToken{
		Token:      "usertok",
		TelegramID: 42,
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("store.Add: %v", err)
	}
	if _, err := store.AddDevice("usertok", tgauth.DeviceInfo{UID: "abcd1234", BoundAt: time.Now()}); err != nil {
		t.Fatalf("store.AddDevice: %v", err)
	}
	tsTokenStoreRef = store

	basicReq := func(login, pass string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/ts/torrents", nil)
		req.SetBasicAuth(login, pass)
		return req
	}

	if got := requestUserTokenFromAny(basicReq("abcd1234", "ts")); got != "usertok" {
		t.Errorf("bound uid: got %q, want usertok", got)
	}
	if got := requestUserTokenFromAny(basicReq("unknown0", "ts")); got != "" {
		t.Errorf("unknown uid: got %q, want empty", got)
	}
	if got := requestUserTokenFromAny(basicReq("abcd1234", "wrong")); got != "" {
		t.Errorf("wrong password: got %q, want empty", got)
	}
	if got := requestUserTokenFromAny(basicReq("ts", "ts")); got != "" {
		t.Errorf("anonymous ts login: got %q, want empty", got)
	}

	// Cookie still wins over Basic.
	req := basicReq("abcd1234", "ts")
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "cookietok"})
	if got := requestUserTokenFromAny(req); got != "cookietok" {
		t.Errorf("cookie priority: got %q, want cookietok", got)
	}

	// Nil store (TG auth disabled) — Basic is ignored entirely.
	tsTokenStoreRef = nil
	if got := requestUserTokenFromAny(basicReq("abcd1234", "ts")); got != "" {
		t.Errorf("nil store: got %q, want empty", got)
	}
}

// TestContentTypeByExt covers the MIME fallback table used by the in-process
// stream handler — the bug was .srt → text/plain (now application/x-subrip)
// and missing .ass/.ssa/.sup/.idx/.sub/.opus/.ogg.
func TestContentTypeByExt(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		{"Movie.mp4", "video/mp4"},
		{"Movie.mkv", "video/x-matroska"},
		{"Movie.mov", "video/quicktime"},
		{"track.m3u8", "application/x-mpegURL"},
		{"subs.srt", "application/x-subrip"},
		{"subs.vtt", "text/vtt"},
		{"subs.ass", "text/x-ssa"},
		{"subs.ssa", "text/x-ssa"},
		{"subs.sup", "application/x-pgs"},
		{"subs.idx", "application/x-subviewer"},
		{"subs.sub", "application/x-subviewer"},
		{"audio.opus", "audio/opus"},
		{"audio.ogg", "audio/ogg"},
		{"unknown.xyz", "application/octet-stream"},
	}
	for _, tc := range cases {
		if got := contentTypeByExt(tc.file); got != tc.want {
			t.Errorf("contentTypeByExt(%q) = %q, want %q", tc.file, got, tc.want)
		}
	}
}

// TestSkipUpstreamHeader pins the duplicate-CORS fix: a real TorrServer answers
// its API with `Access-Control-Allow-Origin: *`, and globalCORSMiddleware has
// already set our own echoed origin. Copying the upstream's copy produced two
// values and browsers rejected the response ("contains multiple values") on any
// cross-origin call — the reason /ts worked on our domain and nowhere else.
func TestSkipUpstreamHeader(t *testing.T) {
	skip := []string{
		"Access-Control-Allow-Origin",
		"access-control-allow-origin",
		"Access-Control-Allow-Credentials",
		"Access-Control-Expose-Headers",
		"Transfer-Encoding",
		"Connection",
		"Content-Security-Policy",
	}
	keep := []string{
		"Content-Type", "Content-Length", "Content-Range",
		"Accept-Ranges", "Set-Cookie", "Cache-Control", "ETag",
	}
	for _, h := range skip {
		if !skipUpstreamHeader(h) {
			t.Errorf("skipUpstreamHeader(%q) = false, want true", h)
		}
	}
	for _, h := range keep {
		if skipUpstreamHeader(h) {
			t.Errorf("skipUpstreamHeader(%q) = true, want false (must pass through)", h)
		}
	}
}
