package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCubProxySpecialRoutes(t *testing.T) {
	resetHTTPAPIGlobals(t)
	t.Setenv("LAMPAC_GO_REPO_ROOT", t.TempDir())

	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	post := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://lampac.local/cub/cub.rip/api/checker", strings.NewReader("data=test-value"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.httpServer.Handler.ServeHTTP(post, req)
	if post.Code != http.StatusOK {
		t.Fatalf("checker status: %d body=%s", post.Code, post.Body.String())
	}
	if body := strings.TrimSpace(post.Body.String()); body != "test-value" {
		t.Fatalf("checker body mismatch: %q", body)
	}

	bl := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://lampac.local/cub/cub.rip/api/plugins/blacklist", nil)
	srv.httpServer.Handler.ServeHTTP(bl, req)
	if bl.Code != http.StatusOK {
		t.Fatalf("blacklist status: %d body=%s", bl.Code, bl.Body.String())
	}
	if !strings.Contains(bl.Body.String(), "[]") {
		t.Fatalf("blacklist unexpected body: %s", bl.Body.String())
	}

	shots := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://lampac.local/cub/cub.rip/api/shots/list/favorite", nil)
	srv.httpServer.Handler.ServeHTTP(shots, req)
	if shots.Code != http.StatusOK {
		t.Fatalf("shots status: %d body=%s", shots.Code, shots.Body.String())
	}
	if !strings.Contains(shots.Body.String(), `"results":[]`) {
		t.Fatalf("shots unexpected body: %s", shots.Body.String())
	}

	shotsMap := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://lampac.local/cub/cub.rip/api/shots/list/map", nil)
	srv.httpServer.Handler.ServeHTTP(shotsMap, req)
	if shotsMap.Code != http.StatusOK {
		t.Fatalf("shots map status: %d body=%s", shotsMap.Code, shotsMap.Body.String())
	}
	if !strings.Contains(shotsMap.Body.String(), `"results":[]`) {
		t.Fatalf("shots map unexpected body: %s", shotsMap.Body.String())
	}
}

func TestCubProxyUpstreamPassThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/test" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	if host == upstream.URL {
		t.Fatalf("unexpected upstream URL: %s", upstream.URL)
	}

	resetHTTPAPIGlobals(t)
	t.Setenv("LAMPAC_GO_REPO_ROOT", t.TempDir())

	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/cub/"+host+"/api/test", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status: %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(strings.ToLower(ct), "application/json") {
		t.Fatalf("proxy content-type mismatch: %s", ct)
	}
	if strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("proxy body mismatch: %s", rec.Body.String())
	}
}

func TestCubProxyAliasRoute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")

	resetHTTPAPIGlobals(t)
	t.Setenv("LAMPAC_GO_REPO_ROOT", t.TempDir())

	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/cubproxy/cub/"+host+"/x", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("alias proxy status: %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCubProxyShotsListWithAuthPassThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/shots/list/favorite" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")

	resetHTTPAPIGlobals(t)
	t.Setenv("LAMPAC_GO_REPO_ROOT", t.TempDir())

	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/cub/"+host+"/api/shots/list/favorite", nil)
	req.Header.Set("token", "test")
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("shots auth status: %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("shots auth body mismatch: %s", rec.Body.String())
	}
}
