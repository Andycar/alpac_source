package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCorseuRejectsInvalidToken(t *testing.T) {
	writeCorseuInitConf(t, `{"corseu":{"tokens":["tok1"]}}`)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/corseu?auth_token=bad&url=https://example.com", nil)
	rec := httptest.NewRecorder()

	corseuGetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestCorseuHTTPProxyFlow(t *testing.T) {
	writeCorseuInitConf(t, `{"corseu":{"tokens":["tok1"]}}`)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Test"); got != "ok" {
			t.Fatalf("unexpected forwarded header: %q", got)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok-body"))
	}))
	defer upstream.Close()

	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/corseu?auth_token=tok1&url="+upstream.URL+"/probe&headers=%7B%22X-Test%22%3A%22ok%22%7D",
		nil,
	)
	rec := httptest.NewRecorder()

	corseuGetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "ok-body" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestCorseuHeadersOnlyAndRules(t *testing.T) {
	writeCorseuInitConf(t, `{
		"corseu":{
			"tokens":["tok1"],
			"rules":[
				{"method":"GET","url":"needs","replace":false,"headers":{"x-from-rule":"1"}}
			]
		}
	}`)

	seenRuleHeader := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenRuleHeader = r.Header.Get("X-From-Rule")
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "should-not-be-forwarded")
	}))
	defer upstream.Close()

	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/corseu?auth_token=tok1&headersOnly=1&url="+upstream.URL+"/needs",
		nil,
	)
	rec := httptest.NewRecorder()

	corseuGetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	if seenRuleHeader != "1" {
		t.Fatalf("rule header was not applied, got %q", seenRuleHeader)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "" {
		t.Fatalf("expected empty body for headersOnly, got %q", got)
	}
	if rec.Header().Get("X-Upstream") != "" {
		t.Fatalf("x-* headers must be stripped in corseu response")
	}
}

func writeCorseuInitConf(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	path := filepath.Join(home, "init.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}
}
