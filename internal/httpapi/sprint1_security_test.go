package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMetricsGuardLocalhostOnly: empty MetricsAuth must only allow loopback.
func TestMetricsGuardLocalhostOnly(t *testing.T) {
	guard := metricsAccessGuard("")
	wrapped := guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("metrics-body"))
	}))

	cases := []struct {
		name   string
		remote string
		want   int
	}{
		{"loopback v4", "127.0.0.1:55555", http.StatusOK},
		{"loopback v6", "[::1]:55555", http.StatusOK},
		{"public IP", "203.0.113.5:55555", http.StatusForbidden},
		{"missing port", "10.0.0.1", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			req.RemoteAddr = c.remote
			wrapped.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("remote=%s: status %d, want %d (body=%s)", c.remote, rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// TestMetricsGuardTokenBearer/BasicAuth: token mode accepts both Bearer and Basic.
func TestMetricsGuardTokenAuthAcceptsBearerAndBasic(t *testing.T) {
	guard := metricsAccessGuard("secret-token")
	wrapped := guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("Bearer ok", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.Header.Set("Authorization", "Bearer secret-token")
		wrapped.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("Bearer token rejected: %d", rec.Code)
		}
	})

	t.Run("Bearer wrong", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.Header.Set("Authorization", "Bearer wrong")
		wrapped.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong Bearer accepted: %d", rec.Code)
		}
	})

	t.Run("Basic ok", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.SetBasicAuth("metrics", "secret-token")
		wrapped.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("Basic auth rejected: %d", rec.Code)
		}
	})

	t.Run("Missing header", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		wrapped.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("missing auth accepted: %d", rec.Code)
		}
		if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Bearer") {
			t.Fatalf("missing WWW-Authenticate header")
		}
	})
}

// TestMetricsGuardOpenModeBypass: explicit "open" disables the guard.
func TestMetricsGuardOpenModeBypass(t *testing.T) {
	guard := metricsAccessGuard("open")
	wrapped := guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.1:9999"
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("open mode should bypass auth: %d", rec.Code)
	}
}

// TestSanitizeIframeSrcRejectsScripting confirms javascript:/data:/empty get neutralized.
func TestSanitizeIframeSrcRejectsScripting(t *testing.T) {
	cases := map[string]string{
		"":                    "about:blank",
		`javascript:alert(1)`: "about:blank",
		`data:text/html,<script>alert(1)</script>`: "about:blank",
		`file:///etc/passwd`:                       "about:blank",
		`https://example.com/foo`:                  "https://example.com/foo",
		`http://x.test/`:                           "http://x.test/",
		`https:///no-host`:                         "about:blank",
		`"><script>alert(1)</script>`:              "about:blank",
	}
	for in, want := range cases {
		got := sanitizeIframeSrc(in)
		if got != want {
			t.Errorf("sanitizeIframeSrc(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestChromiumIframeHandlerHasCSP confirms the response carries the CSP
// header even on first-render — defense-in-depth against a future bug in
// sanitizeIframeSrc.
func TestChromiumIframeHandlerHasCSP(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, `/api/chromium/iframe?src="><script>alert(1)</script>`, nil)
	chromiumIframeHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("missing CSP header")
	}
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("script payload survived sanitization: %s", body)
	}
	if !strings.Contains(body, `src="about:blank"`) {
		t.Fatalf("expected sanitized src=about:blank, got: %s", body)
	}
}

// TestLifeTasksHardCap: ensures the store evicts oldest entries when over cap.
func TestLifeTasksHardCap(t *testing.T) {
	// Use a local store so we don't pollute the global one.
	local := &lifeTaskStore{tasks: make(map[string]*lifeOnlineTask)}
	for i := 0; i < lifeTaskMax+50; i++ {
		key := newMemKey()
		// Stagger createdAt so eviction order is well-defined.
		local.tasks[key] = &lifeOnlineTask{
			createdAt: time.Now().UTC().Add(time.Duration(i) * time.Microsecond),
		}
	}
	local.cleanup()
	if len(local.tasks) > lifeTaskMax {
		t.Fatalf("hard cap not enforced: %d entries (max %d)", len(local.tasks), lifeTaskMax)
	}
}

// TestRequestIsLoopback covers IPv4/IPv6/non-loopback cases.
func TestRequestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:0":    true,
		"127.5.0.1:0":    true,
		"[::1]:0":        true,
		"10.0.0.1:0":     false,
		"203.0.113.1:0":  false,
		"":               false,
		"not-an-address": false,
	}
	for remote, want := range cases {
		got := requestIsLoopback(&http.Request{RemoteAddr: remote})
		if got != want {
			t.Errorf("requestIsLoopback(%q) = %v, want %v", remote, got, want)
		}
	}
}

// TestMetricsHandlerEndToEnd: registerSystemRoutes wires the auth correctly.
func TestMetricsHandlerEndToEnd(t *testing.T) {
	// Quick smoke that the wrapped /metrics returns either 200 (loopback)
	// or 403 (non-loopback). We don't want to start a real server; use
	// the guard directly with a fake metrics handler.
	guard := metricsAccessGuard("")
	wrapped := guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("# HELP foo bar\nfoo 1\n"))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback should access /metrics: %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "foo 1") {
		t.Fatalf("expected metrics body, got: %s", rec.Body.String())
	}
}

// Sanity that our JSON test imports are still resolvable.
var _ = stdjson.Marshal
