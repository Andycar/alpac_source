package sisihttp

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestSisiXvideosListParsesItems(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<div id="video_1">
			  <a href="/video.abcd123" title="First Video">
			    <img data-src="https://img.cdn/videos/thumbs169ll/abc-2.jpg"/>
			  </a>
			  <span class="duration">10:01</span>
			  <span class="video-hd-mark">HD</span>
			  <a href="/pornstars/jane"><span class="name">Jane Doe</span></a>
			</div>`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_XVIDEOS_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiXvideosSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/xds", nil)
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
	video := toString(item["video"])
	if !strings.Contains(video, "/xds/vidosik?uri=video.abcd123") {
		t.Fatalf("unexpected video url: %q", video)
	}
	bookmark, _ := item["bookmark"].(map[string]any)
	if toString(bookmark["site"]) != "xds" {
		t.Fatalf("unexpected bookmark site: %v", bookmark["site"])
	}
	model, _ := item["model"].(map[string]any)
	if toString(model["name"]) != "Jane Doe" {
		t.Fatalf("unexpected model name: %v", model["name"])
	}
}

func TestSisiXvideosListUsesPluginRoute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<div id="video_1">
			  <a href="/video.red123" title="Red Video">
			    <img data-src="https://img.cdn/videos/thumbs169ll/red-2.jpg"/>
			  </a>
			</div>`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_XVIDEOSRED_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiXvideosSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/xdsred", nil)
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
	video := toString(item["video"])
	if !strings.Contains(video, "/xdsred/vidosik?uri=video.red123") {
		t.Fatalf("unexpected xdsred video url: %q", video)
	}
	bookmark, _ := item["bookmark"].(map[string]any)
	if toString(bookmark["site"]) != "xdsred" {
		t.Fatalf("unexpected bookmark site: %v", bookmark["site"])
	}
}

func TestSisiXvideosViewParsesHLS(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<script>
				html5player.setVideoHLS('https://cdn.example/hls.m3u8');
				video_related=[{"tf":"Rel","u":"/video.rel1","if":"https://img.cdn/videos/thumbs169ll/rel-2.jpg","d":"05:00"}];window
			</script>`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_XVIDEOS_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiXvideosSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/xds/vidosik?uri=video.abcd123", nil)
	rec := httptest.NewRecorder()
	source.viewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	qualitys, _ := payload["qualitys"].(map[string]any)
	if toString(qualitys["auto"]) != "https://cdn.example/hls.m3u8" {
		t.Fatalf("unexpected auto quality: %v", qualitys["auto"])
	}
}

func TestSisiPornhubViewParsesQualities(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			{"videoUrl":"https:\\/\\/cdn.example\\/1080.m3u8","quality":"1080"}
			{"videoUrl":"https:\\/\\/cdn.example\\/720.m3u8","quality":"720"}`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_PORNHUB_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiPornHubSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/phub/vidosik?vkey=abc", nil)
	rec := httptest.NewRecorder()
	source.viewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	qualitys, _ := payload["qualitys"].(map[string]any)
	if toString(qualitys["1080p"]) != "https://cdn.example/1080.m3u8" {
		t.Fatalf("unexpected 1080 quality: %v", qualitys["1080p"])
	}
	if toString(qualitys["720p"]) != "https://cdn.example/720.m3u8" {
		t.Fatalf("unexpected 720 quality: %v", qualitys["720p"])
	}
}

func TestSisiPornhubViewUsesProxyQualitiesForBrowser(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			{"videoUrl":"https:\\/\\/cdn.example\\/1080.m3u8","quality":"1080"}`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_PORNHUB_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiPornHubSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/phub/vidosik?vkey=abc", nil)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	rec := httptest.NewRecorder()
	source.viewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	qualitys, _ := payload["qualitys"].(map[string]any)
	proxyQualitys, _ := payload["qualitys_proxy"].(map[string]any)
	q1080 := toString(qualitys["1080p"])
	if !strings.HasPrefix(q1080, "http://lampac.local/proxy/") {
		t.Fatalf("expected proxied quality for browser, got: %q", q1080)
	}
	if toString(proxyQualitys["1080p"]) != q1080 {
		t.Fatalf("qualitys and qualitys_proxy mismatch: %q != %q", q1080, toString(proxyQualitys["1080p"]))
	}
}

func TestSisiPornhubPremiumUsesPremiumHost(t *testing.T) {
	mainUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`{"videoUrl":"https:\\/\\/main.example\\/1080.m3u8","quality":"1080"}`))
	}))
	defer mainUpstream.Close()

	premUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`{"videoUrl":"https:\\/\\/prem.example\\/1080.m3u8","quality":"1080"}`))
	}))
	defer premUpstream.Close()

	t.Setenv("LAMPAC_GO_SISI_PORNHUB_HOST", mainUpstream.URL)
	t.Setenv("LAMPAC_GO_SISI_PORNHUBPREMIUM_HOST", premUpstream.URL)

	cfg := config.Config{}
	source := newSisiPornHubSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/phubprem/vidosik?vkey=abc", nil)
	rec := httptest.NewRecorder()
	source.viewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	qualitys, _ := payload["qualitys"].(map[string]any)
	if toString(qualitys["1080p"]) != "https://prem.example/1080.m3u8" {
		t.Fatalf("unexpected premium quality url: %v", qualitys["1080p"])
	}
}
