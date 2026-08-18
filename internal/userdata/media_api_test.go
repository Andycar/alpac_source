package userdata

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lampac-go/internal/proxylink"

	"github.com/go-chi/chi/v5"
)

func TestMediaRedirectAndPost(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)
	initConf := `{"media":{"tokens":["tok1"]},"serverproxy":{"enable":true,"verifyip":true}}`
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(initConf), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	links, err := proxylink.New(proxylink.Options{CacheDir: t.TempDir(), VerifyIP: true, EncryptAES: true})
	if err != nil {
		t.Fatalf("proxylink.New failed: %v", err)
	}

	router := chi.NewRouter()
	router.Get("/media/rsize/{token}/{width}/{height}/*", MediaRsizeHandler(links))
	router.Get("/media/{type}/{token}/*", MediaTypeHandler(links))
	router.Get("/media", MediaGetHandler(links))
	router.Post("/media", MediaPostHandler(links))

	recImg := httptest.NewRecorder()
	reqImg := httptest.NewRequest(http.MethodGet, "http://lampac.local/media/rsize/tok1/210/0/https://example.com/a.jpg", nil)
	router.ServeHTTP(recImg, reqImg)
	if recImg.Code != http.StatusFound {
		t.Fatalf("unexpected rsize status: %d body=%s", recImg.Code, recImg.Body.String())
	}
	locImg := recImg.Header().Get("Location")
	if !strings.HasPrefix(locImg, "http://lampac.local/proxyimg:210:0/") {
		t.Fatalf("unexpected rsize location: %s", locImg)
	}

	recStream := httptest.NewRecorder()
	reqStream := httptest.NewRequest(http.MethodGet, "http://lampac.local/media/video/tok1/https://example.com/movie.m3u8", nil)
	router.ServeHTTP(recStream, reqStream)
	if recStream.Code != http.StatusFound {
		t.Fatalf("unexpected type status: %d body=%s", recStream.Code, recStream.Body.String())
	}
	locStream := recStream.Header().Get("Location")
	if !strings.HasPrefix(locStream, "http://lampac.local/proxy/") {
		t.Fatalf("unexpected stream location: %s", locStream)
	}

	recGet := httptest.NewRecorder()
	reqGet := httptest.NewRequest(http.MethodGet, "http://lampac.local/media?url=https://example.com/b.png&auth_token=tok1&type=img&width=50&height=0", nil)
	router.ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusFound {
		t.Fatalf("unexpected media get status: %d body=%s", recGet.Code, recGet.Body.String())
	}
	if !strings.HasPrefix(recGet.Header().Get("Location"), "http://lampac.local/proxyimg:50:0/") {
		t.Fatalf("unexpected media get location: %s", recGet.Header().Get("Location"))
	}

	postBody := `{"auth_token":"tok1","type":"img","width":25,"height":25,"urls":["https://example.com/a.jpg","https://example.com/b.jpg"]}`
	recPost := httptest.NewRecorder()
	reqPost := httptest.NewRequest(http.MethodPost, "http://lampac.local/media", strings.NewReader(postBody))
	router.ServeHTTP(recPost, reqPost)
	if recPost.Code != http.StatusOK {
		t.Fatalf("unexpected media post status: %d body=%s", recPost.Code, recPost.Body.String())
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(recPost.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid media post json: %v", err)
	}
	if ok, _ := payload["success"].(bool); !ok {
		t.Fatalf("unexpected post payload: %+v", payload)
	}
	urls, _ := payload["urls"].([]any)
	if len(urls) != 2 {
		t.Fatalf("unexpected urls list: %+v", payload["urls"])
	}
}

func TestMediaUnauthorizedAndNoProxy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)
	initConf := `{"media":{"tokens":["tok1"]},"serverproxy":{"enable":false}}`
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(initConf), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	links, err := proxylink.New(proxylink.Options{CacheDir: t.TempDir(), VerifyIP: true, EncryptAES: true})
	if err != nil {
		t.Fatalf("proxylink.New failed: %v", err)
	}

	recUnauth := httptest.NewRecorder()
	reqUnauth := httptest.NewRequest(http.MethodGet, "http://lampac.local/media?url=https://example.com/a.jpg&auth_token=bad&type=img", nil)
	MediaGetHandler(links).ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected unauthorized status: %d body=%s", recUnauth.Code, recUnauth.Body.String())
	}

	recDirect := httptest.NewRecorder()
	reqDirect := httptest.NewRequest(http.MethodGet, "http://lampac.local/media?url=https://example.com/a.jpg&auth_token=tok1&type=img", nil)
	MediaGetHandler(links).ServeHTTP(recDirect, reqDirect)
	if recDirect.Code != http.StatusFound {
		t.Fatalf("unexpected direct status: %d body=%s", recDirect.Code, recDirect.Body.String())
	}
	if recDirect.Header().Get("Location") != "https://example.com/a.jpg" {
		t.Fatalf("expected direct url redirect, got: %s", recDirect.Header().Get("Location"))
	}
}
