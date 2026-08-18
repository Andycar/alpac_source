package sisihttp

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestSisiXnxxListParsesItems(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<div id="video_1">
			  <a href="/video-abc123/x/test" title="XNXX Title">
			    <img data-src="https://img.cdn/thumbs/ab/cd/item.THUMBNUM.jpg"/>
			  </a>
			  </span>07:10<span class="video-hd">
			  <span class="superfluous"> - </span>1080p</span>
			</div>`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_XNXX_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiXnxxSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/xnx", nil)
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
	if !strings.Contains(video, "/xnx/vidosik?uri=video-abc123%2Fx%2Ftest") {
		t.Fatalf("unexpected video url: %q", video)
	}
	bookmark, _ := item["bookmark"].(map[string]any)
	if toString(bookmark["site"]) != "xnx" {
		t.Fatalf("unexpected bookmark site: %v", bookmark["site"])
	}
	if !strings.HasSuffix(toString(item["preview"]), "_169.mp4") {
		t.Fatalf("unexpected preview url: %v", item["preview"])
	}
}

func TestSisiXnxxViewParsesHLSAndRelated(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<script>
			  html5player.setVideoHLS('https://cdn.example/xnxx/master.m3u8');
			  video_related=[{"tf":"Rel Item","u":"/video-rel1","i":"https://img.cdn/r1.jpg"}];window
			</script>`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_XNXX_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiXnxxSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/xnx/vidosik?uri=video-abc123", nil)
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
	if toString(qualitys["auto"]) != "https://cdn.example/xnxx/master.m3u8" {
		t.Fatalf("unexpected auto quality: %v", qualitys["auto"])
	}
	rawRelated, _ := payload["recomends"].([]any)
	if len(rawRelated) != 1 {
		t.Fatalf("expected 1 related item, got %d", len(rawRelated))
	}
	rel, _ := rawRelated[0].(map[string]any)
	if !strings.Contains(toString(rel["video"]), "/xnx/vidosik?uri=video-rel1") {
		t.Fatalf("unexpected related video url: %v", rel["video"])
	}
}

func TestSisiXhamsterListParsesItems(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<div class="mixed-section">
			  <div class="thumb-list__item video-thumb -hd">
			    <a class="thumb-image-container__name" href="https://xhamster.example/videos/item-123">Ham Title</a>
			    <img class="thumb-image-container__image" src="https://img.cdn/ham.jpg"/>
			    <div data-role="video-duration"><span>12:34</span></div>
			    <div data-previewvideo="https://cdn.cdn/preview.mp4"></div>
			  </div>
			</div>`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_XHAMSTER_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiXhamsterSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/xmrgay?sort=best", nil)
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
	if !strings.Contains(video, "/xmr/vidosik?uri=videos%2Fitem-123") {
		t.Fatalf("unexpected video url: %q", video)
	}
	bookmark, _ := item["bookmark"].(map[string]any)
	if toString(bookmark["site"]) != "xmr" {
		t.Fatalf("unexpected bookmark site: %v", bookmark["site"])
	}
	if toString(item["quality"]) != "HD" {
		t.Fatalf("unexpected quality: %v", item["quality"])
	}
}

func TestSisiXhamsterListParsesInitialsScriptFallback(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<script id='initials-script'>window.initials={
			  "layoutPage":{
			    "videoListProps":{
			      "videoThumbProps":[
			        {
			          "videoType":"video",
			          "title":"Ham Json Title",
			          "pageURL":"https://xhamster.example/videos/item-json",
			          "thumbURL":"https://img.cdn/json.jpg",
			          "trailerURL":"https://cdn.cdn/json.mp4",
			          "duration":3723,
			          "isUHD":true
			        }
			      ]
			    }
			  }
			};</script>`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_XHAMSTER_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiXhamsterSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/xmr", nil)
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
	if !strings.Contains(toString(item["video"]), "/xmr/vidosik?uri=videos%2Fitem-json") {
		t.Fatalf("unexpected video url: %v", item["video"])
	}
	if toString(item["quality"]) != "4K" {
		t.Fatalf("unexpected quality: %v", item["quality"])
	}
	if toString(item["time"]) != "1:02:03" {
		t.Fatalf("unexpected duration: %v", item["time"])
	}
	if toString(item["preview"]) != "https://cdn.cdn/json.mp4" {
		t.Fatalf("unexpected preview: %v", item["preview"])
	}
}

func TestSisiXhamsterViewParsesStream(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<link rel="preload" href="/hls/master.m3u8">
			<div class="thumb-list__item video-thumb">
			  <a class="thumb-image-container__name" href="https://xhamster.example/videos/rel-1">Related</a>
			  <img class="thumb-image-container__image" src="https://img.cdn/rel.jpg"/>
			</div>`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_XHAMSTER_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiXhamsterSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/xmr/vidosik?uri=videos/item-123", nil)
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
	if toString(qualitys["auto"]) != upstream.URL+"/hls/master.m3u8" {
		t.Fatalf("unexpected stream url: %v", qualitys["auto"])
	}
	rawRelated, _ := payload["recomends"].([]any)
	if len(rawRelated) != 1 {
		t.Fatalf("expected related item, got %d", len(rawRelated))
	}
}

func TestFirstSrcsetURLHandlesCommaInTransformPath(t *testing.T) {
	srcset := `https://ic-vt-nss.xhcdn.com/a/hash/s(w:526,h:296),jpeg/000/001.jpg 526w, https://ic-vt-nss.xhcdn.com/a/hash/s(w:320,h:180),jpeg/000/001.jpg 320w`
	got := firstSrcsetURL(srcset)
	want := "https://ic-vt-nss.xhcdn.com/a/hash/s(w:526,h:296),jpeg/000/001.jpg"
	if got != want {
		t.Fatalf("unexpected srcset url: got %q want %q", got, want)
	}
}

func TestFirstSrcsetURLHandlesSpacesInsideTransformPath(t *testing.T) {
	srcset := `https://ic-vt-nss.xhcdn.com/a/hash/s(w:526, h:296),jpeg/000/001.jpg 526w, https://ic-vt-nss.xhcdn.com/a/hash/s(w:320, h:180),jpeg/000/001.jpg 320w`
	got := firstSrcsetURL(srcset)
	want := "https://ic-vt-nss.xhcdn.com/a/hash/s(w:526, h:296),jpeg/000/001.jpg"
	if got != want {
		t.Fatalf("unexpected srcset url: got %q want %q", got, want)
	}
}
