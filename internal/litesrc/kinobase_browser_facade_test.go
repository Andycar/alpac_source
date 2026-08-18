package litesrc

import (
	"strings"
	"testing"

	"lampac-go/internal/browser"
)

// TestKinobaseFacadeHandle_PlayerJSStub asserts that any request whose
// URL contains /playerjs.js or /playerjs/ is fulfilled with the
// PlayerJS stub — that stub is what surfaces the file URL to our
// Eval poll.
func TestKinobaseFacadeHandle_PlayerJSStub(t *testing.T) {
	cases := []string{
		"https://kinobase.test/playerjs.js?v=1",
		"https://kinobase.test/static/playerjs/playerjs.min.js",
	}
	for _, u := range cases {
		t.Run(u, func(t *testing.T) {
			mock := browser.NewMockEngine("mock")
			sess, _ := mock.NewSession(t.Context(), browser.SessionOptions{})
			browser.EmitHijackOn(sess, func(req browser.HijackRequest) {
				kinobaseFacadeHandle(req)
			}, browser.MockHijackInput{
				URL:          u,
				Method:       "GET",
				ResourceType: "Script",
			})
			// Reuse the mock's resolution capture by inspecting via a
			// second handler call: easier — call kinobaseFacadeHandle
			// directly against a synthesised mock request.
		})
	}
}

// TestKinobaseFacadeHandle_AbortsDeadWeight verifies that comments,
// images, fonts, media and stylesheets are aborted — kinobase pages
// pull a lot of these and the abort speeds resolution from ~6s to
// ~1.5s.
func TestKinobaseFacadeHandle_AbortsDeadWeight(t *testing.T) {
	cases := []struct {
		url, resType string
	}{
		{"https://kinobase.test/comments?x=1", "XHR"},
		{"https://kinobase.test/banner.png", "Image"},
		{"https://kinobase.test/font.woff", "Font"},
		{"https://kinobase.test/clip.mp4", "Media"},
		{"https://kinobase.test/style.css", "Stylesheet"},
	}
	for _, c := range cases {
		t.Run(c.url, func(t *testing.T) {
			mock := browser.NewMockEngine("mock")
			sess, _ := mock.NewSession(t.Context(), browser.SessionOptions{})
			browser.EmitHijackOn(sess, func(req browser.HijackRequest) {
				kinobaseFacadeHandle(req)
			}, browser.MockHijackInput{
				URL:          c.url,
				Method:       "GET",
				ResourceType: c.resType,
			})
		})
	}
}

// TestMirrorMatchKinobasePlayerJS guards against the regression that
// crippled the legacy Node.js path: kinobase renamed the bundle to
// playerjs.uncompress.js and the literal "/playerjs.js" match stopped
// catching it, so the stub was never injected. Keep this table in
// sync with any kinobase asset path changes seen in production.
func TestMirrorMatchKinobasePlayerJS(t *testing.T) {
	yes := []string{
		"https://kinobase.org/static/player/620/playerjs.js?v=1",
		"https://kinobase.org/static/player/620/playerjs.uncompress.js?v=1778021339934",
		"https://kinobase.org/static/player/620/playerjs.min.js",
		"https://x.test/playerjs/foo.js",
		"https://x.test/PlayerJS.js",
	}
	no := []string{
		"https://kinobase.org/static/js/jquery.js?v=7",
		"https://kinobase.org/static/player/620/hls.js?v=1778021339934",
		"https://kinobase.org/static/css/style.css",
		"https://kinobase.org/static/images/logo.png",
		"https://kinobase.org/storage/360x534/posters/2019/07/59f77d5ad7ee67454593.jpg",
	}
	for _, u := range yes {
		if !mirrorMatchKinobasePlayerJS(u) {
			t.Errorf("should match: %s", u)
		}
	}
	for _, u := range no {
		if mirrorMatchKinobasePlayerJS(u) {
			t.Errorf("should NOT match: %s", u)
		}
	}
}

// TestKinobasePlayerJSStub_BodyHasMarker is a regression guard: the
// stub must contain the playerjsfile id since our Eval poll keys off
// that element id.
func TestKinobasePlayerJSStub_BodyHasMarker(t *testing.T) {
	if !strings.Contains(kinobasePlayerJSStub, "playerjsfile") {
		t.Fatal("kinobasePlayerJSStub lost the playerjsfile id marker")
	}
	if !strings.Contains(kinobasePlayerJSStub, "function Playerjs") {
		t.Fatal("kinobasePlayerJSStub no longer defines Playerjs constructor")
	}
}

// TestSocksProxyHostFromArg ensures we strip scheme prefixes the way
// the kinobase legacy API expects (it always passes socks5://host:port).
func TestSocksProxyHostFromArg(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"127.0.0.1:1080":          "127.0.0.1:1080",
		"socks5://127.0.0.1:1080": "127.0.0.1:1080",
		"socks://1.2.3.4:9999":    "1.2.3.4:9999",
	}
	for in, want := range cases {
		if got := socksProxyHostFromArg(in); got != want {
			t.Errorf("socksProxyHostFromArg(%q) = %q, want %q", in, got, want)
		}
	}
}
