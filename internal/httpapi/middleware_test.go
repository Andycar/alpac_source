package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A plugin URL stored with a trailing space (".../ts.js ") arrives as
// "/ts.js%20". Without path normalization chi can't match the "/ts.js" route
// and the auth gate's .js whitelist misses (filepath.Ext == ".js "), so the
// fetch gets a 302→/tg/auth HTML page that Lampa fails to parse as JS.
func TestNormalizeRequestPathMiddleware_TrailingSpaceRoutes(t *testing.T) {
	r := chi.NewRouter()
	r.Use(normalizeRequestPathMiddleware)
	r.Get("/ts.js", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ROUTED"))
	})

	for _, target := range []string{"/ts.js%20", "/ts.js%20%20"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "ROUTED" {
			t.Errorf("target %q: got status=%d body=%q, want 200/ROUTED", target, rec.Code, rec.Body.String())
		}
	}
}

func TestNormalizeRequestPathMiddleware_TrimsPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/ts.js ", "/ts.js"},
		{"/ts.js\t", "/ts.js"},
		{"/online.js   ", "/online.js"},
		{"/clean.js", "/clean.js"}, // untouched
		{"/", "/"},                 // untouched
	}
	for _, c := range cases {
		var seen string
		h := normalizeRequestPathMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen = r.URL.Path
		}))
		req := httptest.NewRequest(http.MethodGet, "http://x"+pathEscapeSpaces(c.in), nil)
		// Force the raw path to the literal (incl. trailing whitespace) so we
		// exercise the decoded URL.Path the server actually sees.
		req.URL.Path = c.in
		h.ServeHTTP(httptest.NewRecorder(), req)
		if seen != c.want {
			t.Errorf("in %q: got %q, want %q", c.in, seen, c.want)
		}
	}
}

func pathEscapeSpaces(p string) string {
	out := make([]rune, 0, len(p))
	for _, r := range p {
		switch r {
		case ' ':
			out = append(out, '%', '2', '0')
		case '\t':
			out = append(out, '%', '0', '9')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
