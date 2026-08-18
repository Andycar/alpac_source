package sisihttp

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestSisiPorntrexListParsesItems(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<div class="video-preview-screen">
				<a href="https://porntrex.example/video/abc-123" title="PTX Title"></a>
				<div data-src="//ptx.cdntrex.com/contents/videos_screenshots/1/2/preview.jpg"></div>
				<span class="quality">720p</span>
				<i class="fa fa-clock-o"></i>08:44</div>
			</div>`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_PORNTREX_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiPorntrexSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/ptx", nil)
	rec := httptest.NewRecorder()
	source.listHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	rawList, _ := payload["list"].([]any)
	if len(rawList) != 1 {
		t.Fatalf("expected 1 list item, got %d: %s", len(rawList), rec.Body.String())
	}
	item, _ := rawList[0].(map[string]any)
	if !strings.Contains(toString(item["video"]), "/ptx/vidosik?uri=video%2Fabc-123") {
		t.Fatalf("unexpected video: %v", item["video"])
	}
	bookmark, _ := item["bookmark"].(map[string]any)
	if toString(bookmark["site"]) != "ptx" {
		t.Fatalf("unexpected bookmark site: %v", bookmark["site"])
	}
}

func TestSisiPorntrexViewWrapsLinksToStrem(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			https:\/\/cdn.example\/get_file\/abc_720p.mp4?st=tok\u0026e=123
			https:\/\/cdn.example\/get_file\/abc_480p.mp4?st=tok2\u0026e=123`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_PORNTREX_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiPorntrexSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/ptx/vidosik?uri=video/abc-123", nil)
	rec := httptest.NewRecorder()
	source.viewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(toString(payload["720p"]), "/ptx/strem?link=") {
		t.Fatalf("720p link not wrapped: %v", payload["720p"])
	}
	decoded, _ := url.QueryUnescape(strings.TrimPrefix(toString(payload["720p"]), "http://lampac.local/ptx/strem?link="))
	if !strings.Contains(decoded, "st=tok&e=123") {
		t.Fatalf("720p query token lost: %q", decoded)
	}
}

func TestSisiPorntrexStremRedirects(t *testing.T) {
	resetHTTPAPIGlobals(t)
	var target string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		target = r.URL.Path
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	cfg := config.Config{}
	source := newSisiPorntrexSource(cfg)

	link := upstream.URL + "/first"
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/ptx/strem?link="+url.QueryEscape(link), nil)
	req.Header.Set("Range", "bytes=0-")
	rec := httptest.NewRecorder()
	source.streamHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if target != "/final" {
		t.Fatalf("expected upstream /final, got %q", target)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestParsePorntrexStreamLinksPreservesQuery(t *testing.T) {
	raw := `https:\/\/cdn.example\/get_file\/abc_1080p.mp4?st=abc\u0026e=170000`
	qualitys := parsePorntrexStreamLinks(raw)
	link, ok := qualitys["1080p"]
	if !ok {
		t.Fatalf("1080p link not found: %#v", qualitys)
	}
	if !strings.Contains(link, "st=abc&e=170000") {
		t.Fatalf("query token lost: %q", link)
	}
}

func TestSisiSpankbangListAndView(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if strings.Contains(r.URL.Path, "/video/") {
			_, _ = w.Write([]byte(`
				'1080p': ['https://cdn.example/sbg-1080.m3u8']
				'720p': ['https://cdn.example/sbg-720.m3u8']
				<div data-testid="video-item">
				  <a href="/video/rel-1" title="Rel SBG"></a>
				  <img src="https://img.cdn/sbg-rel.jpg"/>
				</div>`))
			return
		}
		_, _ = w.Write([]byte(`
			<div class="main-container"></div>
			<div data-testid="video-item">
			  <a href="/video/sbg-1" title="SBG Title"></a>
			  <img src="https://img.cdn/w:500/sbg.jpg"/>
			  <span class="video-item-resolution">1080p</span>
			  <span class="video-item-length">10:10</span>
			</div>`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_SPANKBANG_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiSpankbangSource(cfg)

	listReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/sbg", nil)
	listRec := httptest.NewRecorder()
	source.listHandler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRec.Code)
	}
	var listPayload map[string]any
	if err := stdjson.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	list, _ := listPayload["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected list item, got %d", len(list))
	}

	viewReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/sbg/vidosik?uri=video/sbg-1", nil)
	viewRec := httptest.NewRecorder()
	source.viewHandler().ServeHTTP(viewRec, viewReq)
	if viewRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", viewRec.Code)
	}
	var viewPayload map[string]any
	if err := stdjson.Unmarshal(viewRec.Body.Bytes(), &viewPayload); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	qualitys, _ := viewPayload["qualitys"].(map[string]any)
	if toString(qualitys["1080p"]) != "https://cdn.example/sbg-1080.m3u8" {
		t.Fatalf("unexpected 1080p: %v", qualitys["1080p"])
	}
}

func TestParseSpankbangPlaylistFallbackWithoutDataTestID(t *testing.T) {
	html := `
		<div class="video-item card">
		  <a href="/video/sbg-new-1" aria-label="SBG New Title"></a>
		  <img data-src="https://img.cdn/w:500/sbg-new.jpg"/>
		  <span class="video-item-resolution">720p</span>
		  <span class="video-item-length">08:08</span>
		</div>`
	out := parseSpankbangPlaylist("http://lampac.local", html)
	if len(out) != 1 {
		t.Fatalf("expected 1 item, got %d", len(out))
	}
	item := out[0]
	if !strings.Contains(toString(item["video"]), "/sbg/vidosik?uri=video%2Fsbg-new-1") {
		t.Fatalf("unexpected video url: %v", item["video"])
	}
	if toString(item["quality"]) != "720p" {
		t.Fatalf("unexpected quality: %v", item["quality"])
	}
}

func TestSisiRunetkiListParsesItemsAndPages(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{
			"total_count":145,
			"items":[
				{"gender":"f","username":"anna","esid":"esid123","thumb_image":"\\/img\\/a.{ext}","display_name":"Anna","vq":"HD"}
			]
		}`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_RUNETKI_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiRunetkiSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/runetki?sort=new&pg=2", nil)
	rec := httptest.NewRecorder()
	source.listHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	list, _ := payload["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 list item, got %d", len(list))
	}
	item, _ := list[0].(map[string]any)
	if !strings.Contains(toString(item["video"]), "stream_anna/playlist.m3u8") {
		t.Fatalf("unexpected video: %v", item["video"])
	}
	if intFromAny(payload["total_pages"]) != 3 {
		t.Fatalf("expected total_pages=3, got %v", payload["total_pages"])
	}
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	default:
		return 0
	}
}
