package proxyapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

func TestDecodeDirectTarget(t *testing.T) {
	enc := url.PathEscape("http://example.com/live/master.m3u8")
	got, ok := decodeDirectTarget(enc, "v=1")
	if !ok {
		t.Fatalf("expected decodeDirectTarget to succeed")
	}
	if got != "http://example.com/live/master.m3u8?v=1" {
		t.Fatalf("unexpected target: %s", got)
	}
}

func TestResolveTargetStripsClientOnlyQueryParams(t *testing.T) {
	h := New(config.Config{}, nil)
	enc := url.PathEscape("https://cdn.example/master.m3u8?h=abc&e=1")
	got, meta, ok := h.resolveTarget(enc, "box_mac=xyz&uid=u1", "127.0.0.1")
	if !ok {
		t.Fatalf("expected resolveTarget to succeed")
	}
	want := "https://cdn.example/master.m3u8?h=abc&e=1"
	if got != want {
		t.Fatalf("unexpected target:\nwant: %s\n got: %s", want, got)
	}
	if meta.verifyIP {
		t.Fatalf("expected direct target to disable verifyIP")
	}
}

func TestResolveTargetKeepsUsefulQueryParams(t *testing.T) {
	h := New(config.Config{}, nil)
	enc := url.PathEscape("https://cdn.example/master.m3u8")
	got, _, ok := h.resolveTarget(enc, "v=1&token=abc", "127.0.0.1")
	if !ok {
		t.Fatalf("expected resolveTarget to succeed")
	}
	want := "https://cdn.example/master.m3u8?token=abc&v=1"
	if got != want {
		t.Fatalf("unexpected target:\nwant: %s\n got: %s", want, got)
	}
}

func TestRewriteM3U(t *testing.T) {
	h := New(config.Config{}, nil)
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/http://example.com/master.m3u8", nil)
	meta := linkMeta{
		reqIP:    "127.0.0.1",
		verifyIP: true,
		baseURI:  "http://cdn.example.com/path/master.m3u8",
	}

	src := "#EXTM3U\nhttp://abs.example.com/a.ts\nsegment1.ts\n../segment2.ts\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.key\"\n"
	got := h.rewriteM3U(src, req, meta)

	wantAbs := "http://lampac.local/proxy/http%3A%2F%2Fabs.example.com%2Fa.ts"
	if !strings.Contains(got, wantAbs) {
		t.Fatalf("missing absolute rewrite: %s", got)
	}

	wantRel := "http://lampac.local/proxy/http%3A%2F%2Fcdn.example.com%2Fpath%2Fsegment1.ts"
	if !strings.Contains(got, wantRel) {
		t.Fatalf("missing relative rewrite: %s", got)
	}
	wantRelUp := "http://lampac.local/proxy/http%3A%2F%2Fcdn.example.com%2Fsegment2.ts"
	if !strings.Contains(got, wantRelUp) {
		t.Fatalf("missing ../ relative rewrite: %s", got)
	}

	wantKey := "URI=\"http://lampac.local/proxy/http%3A%2F%2Fcdn.example.com%2Fpath%2Fkey.key\""
	if !strings.Contains(got, wantKey) {
		t.Fatalf("missing quoted URI rewrite: %s", got)
	}
}

// filmix /hls playlists put ?hash= on segment lines but NOT on the EXT-X-MAP
// init URI (fMP4/HDR rips) — the CDN 403s a hashless init fetch, so the rewrite
// must inherit the playlist's own hash. Other plugins stay untouched.
func TestRewriteM3UFilmixInheritsHashIntoInit(t *testing.T) {
	h := New(config.Config{}, nil)
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/x.m3u8", nil)

	src := "#EXTM3U\n" +
		"#EXT-X-MAP:URI=\"init-v1-a1.mp4\"\n" +
		"seg-1-v1-a1.ts?hash=FHASH\n"

	meta := linkMeta{
		reqIP:    "127.0.0.1",
		verifyIP: true,
		plugin:   "filmix",
		baseURI:  "https://nl104.cdnsqu.com/hls/hdr_018/film_2160.mp4/index.m3u8?hash=FHASH",
	}
	got := h.rewriteM3U(src, req, meta)

	wantInit := url.QueryEscape("https://nl104.cdnsqu.com/hls/hdr_018/film_2160.mp4/init-v1-a1.mp4?hash=FHASH")
	if !strings.Contains(got, wantInit) {
		t.Fatalf("EXT-X-MAP init must inherit the playlist hash:\n%s", got)
	}
	// Segment already had the hash — must not be duplicated.
	if strings.Contains(got, url.QueryEscape("hash=FHASH&hash=FHASH")) || strings.Contains(got, url.QueryEscape("hash=FHASH?hash=FHASH")) {
		t.Fatalf("hash duplicated on segment line:\n%s", got)
	}

	// Non-filmix plugin: init URI stays hashless.
	meta.plugin = "kodik"
	got = h.rewriteM3U(src, req, meta)
	if !strings.Contains(got, url.QueryEscape("https://nl104.cdnsqu.com/hls/hdr_018/film_2160.mp4/init-v1-a1.mp4")) ||
		strings.Contains(got, wantInit) {
		t.Fatalf("non-filmix plugin must not inherit hash:\n%s", got)
	}
}

// Manifest-only mode keeps the playlist rewrite while handing segment bytes
// straight to the client, so a 4K stream does not transit this server. Nested
// playlists must stay proxied — they need the same rewrite one level down.
// (fMP4 playlists are the exception; see TestRewriteM3UManifestOnlyKeepsFMP4Proxied.)
func TestRewriteM3UManifestOnlyKeepsPlaylistsProxied(t *testing.T) {
	h := New(config.Config{
		Online: config.OnlineConfig{StreamProxyManifestOnly: []string{"SomeCDN"}},
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/x.m3u8", nil)
	meta := linkMeta{
		reqIP:    "127.0.0.1",
		verifyIP: true,
		plugin:   "somecdn",
		baseURI:  "https://nl104.cdnsqu.com/hls/uhd_018/film_2160.mp4/index.m3u8?hash=FHASH",
	}

	src := "#EXTM3U\n" +
		"seg-1-v1-a1.ts?hash=FHASH\n" +
		"https://nl104.cdnsqu.com/hls/uhd_018/film_2160.mp4/seg-2-v1-a1.ts?hash=FHASH\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=6324707\n" +
		"variant-v1.m3u8?hash=FHASH\n"
	got := h.rewriteM3U(src, req, meta)

	for _, want := range []string{
		"https://nl104.cdnsqu.com/hls/uhd_018/film_2160.mp4/seg-1-v1-a1.ts?hash=FHASH",
		"https://nl104.cdnsqu.com/hls/uhd_018/film_2160.mp4/seg-2-v1-a1.ts?hash=FHASH",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected direct CDN url %q in:\n%s", want, got)
		}
	}
	// Exactly one /proxy token — the nested playlist. Segments are direct.
	if n := strings.Count(got, "/proxy/"); n != 1 {
		t.Fatalf("expected exactly 1 proxied uri (the nested playlist), got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "/proxy/"+url.QueryEscape("https://nl104.cdnsqu.com/hls/uhd_018/film_2160.mp4/variant-v1.m3u8?hash=FHASH")) {
		t.Fatalf("nested playlist must stay proxied:\n%s", got)
	}

	// A plugin NOT in the list keeps everything proxied.
	meta.plugin = "kodik"
	got = h.rewriteM3U(src, req, meta)
	if strings.Contains(got, "\nhttps://nl104.cdnsqu.com/hls/uhd_018/film_2160.mp4/seg-2") {
		t.Fatalf("non-listed plugin must keep segments proxied:\n%s", got)
	}
}

func TestRewriteM3UDirectModeUsesKeylessSegmentLinks(t *testing.T) {
	links, err := proxylink.New(proxylink.Options{
		CacheDir:   t.TempDir(),
		VerifyIP:   true,
		EncryptAES: true,
	})
	if err != nil {
		t.Fatalf("proxylink init: %v", err)
	}

	h := New(config.Config{}, links)
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/"+url.PathEscape("https://cdn.example/path/master.m3u8"), nil)
	meta := linkMeta{
		reqIP:    "127.0.0.1",
		verifyIP: false, // direct /proxy/<urlencoded-url> mode
		baseURI:  "https://cdn.example/path/master.m3u8",
	}

	src := "#EXTM3U\nsegment1.ts\n"
	got := h.rewriteM3U(src, req, meta)
	want := "http://lampac.local/proxy/https%3A%2F%2Fcdn.example%2Fpath%2Fsegment1.ts"
	if !strings.Contains(got, want) {
		t.Fatalf("expected keyless urlencoded segment link:\nwant contains: %s\n got: %s", want, got)
	}

	meta.plugin = "pornhub"
	gotPH := h.rewriteM3U(src, req, meta)
	if !strings.Contains(gotPH, want) {
		t.Fatalf("expected keyless urlencoded segment link for pornhub:\nwant contains: %s\n got: %s", want, gotPH)
	}
}

func TestHandleMPDRewritesBaseURL(t *testing.T) {
	cfg := config.Config{
		ServerProxy: config.ServerProxyConfig{
			ResponseContentLength: true,
		},
	}
	h := New(cfg, nil)
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/http://origin.local/manifest.mpd", nil)
	rec := httptest.NewRecorder()

	in := `<?xml version="1.0"?><MPD><BaseURL>https://cdn.example.com/video/</BaseURL></MPD>`
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"application/dash+xml"}},
		Body:          io.NopCloser(strings.NewReader(in)),
		ContentLength: int64(len(in)),
	}

	h.handleMPD(rec, req, resp, linkMeta{reqIP: "127.0.0.1", verifyIP: true, baseURI: "http://origin.local/manifest.mpd"})

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	got := rec.Body.String()
	want := "<BaseURL>http://lampac.local/proxy-dash/https%3A%2F%2Fcdn.example.com%2Fvideo%2F/</BaseURL>"
	if !strings.Contains(got, want) {
		t.Fatalf("missing BaseURL rewrite: %s", got)
	}
}

func TestHandleProxyM3UFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\nsegment.ts\n"))
	}))
	defer upstream.Close()

	h := New(config.Config{}, nil)
	enc := url.PathEscape(upstream.URL + "/master.m3u8")
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/"+enc, nil)
	rec := httptest.NewRecorder()

	h.HandleProxy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "http://lampac.local/proxy/") {
		t.Fatalf("expected rewritten proxy links, got: %s", rec.Body.String())
	}
}

type captureRoundTripper struct {
	req *http.Request
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.req = req.Clone(context.Background())
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/octet-stream"}},
		Body:       io.NopCloser(strings.NewReader("ok")),
		Request:    req,
	}, nil
}

func TestFetchWithMetaSetsPornhubCDNHeaders(t *testing.T) {
	h := New(config.Config{}, nil)
	rt := &captureRoundTripper{}
	h.client = &http.Client{
		Transport: rt,
	}

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/x", nil)
	_, _, err := h.fetchWithMeta("https://ev-h.phncdn.com/hls/x/seg-1-v1-a1.ts?a=1", req, linkMeta{})
	if err != nil {
		t.Fatalf("fetchWithMeta error: %v", err)
	}
	if rt.req == nil {
		t.Fatalf("upstream request not captured")
	}
	if got := rt.req.Header.Get("Referer"); got != "https://www.pornhub.com/" {
		t.Fatalf("unexpected referer: %q", got)
	}
	if got := rt.req.Header.Get("Origin"); got != "https://www.pornhub.com" {
		t.Fatalf("unexpected origin: %q", got)
	}
	if got := rt.req.Header.Get("Cookie"); !strings.Contains(got, "platform=pc") {
		t.Fatalf("unexpected cookie: %q", got)
	}
}

func TestDecodeZetflixObrutStreamURL(t *testing.T) {
	raw := "https://cdn-54243ba5.obrut.show/stream/AO/MHc0RHa/QbvNmLuR2YyVGc1RmclBXdz5ie/AO1NTbuQ3clZWauFWb6MHbopDNw1mLwgDMx8SMyQTMyAjNyAjM6EGOxMzN4UTYzgjMhFDM2MmNxM2MxMmZzMmNzYmZjVWOvYDOykjMlVDZ3ImM5UjYxEGMkhTN3IGO2gDNwAjZmlDNlJzNkFTOilzLzVWayV2c2R3L"
	got, ok := decodeZetflixObrutStreamURL(raw)
	if !ok {
		t.Fatalf("expected decode success")
	}
	want := "https://z.superdupercdn.com/tvseries/9b91d72e49ff004868b758d0a1b592b7d5e29286/9ecff36c3fc13c16c601a283a587318a:2026021421/1080.mp4:hls:manifest.m3u8"
	if got != want {
		t.Fatalf("unexpected decoded url:\nwant: %s\n got: %s", want, got)
	}
}

func TestZetflixCDNVariantsFallbacks(t *testing.T) {
	v1 := "https://z.superdupercdn.com/a/b/1080.mp4:hls:manifest-v1-a2.m3u8"
	got := zetflixCDNVariants(v1)
	if len(got) < 2 {
		t.Fatalf("expected at least two variants, got: %v", got)
	}
	if got[0] != v1 {
		t.Fatalf("expected first variant to keep original, got: %s", got[0])
	}
	if !strings.Contains(got[1], ":hls:manifest.m3u8") {
		t.Fatalf("expected fallback manifest.m3u8 variant, got: %v", got)
	}
}

// fMP4 у ЧУЖОГО плагина остаётся полностью проксированным: hash наследуется только для filmix,
// значит hashless EXT-X-MAP там починить нечем и прямой init встанет на 403.
// Обычные TS-плейлисты сохраняют выигрыш прямых байтов.
func TestRewriteM3UManifestOnlyKeepsFMP4Proxied(t *testing.T) {
	h := New(config.Config{
		Online: config.OnlineConfig{StreamProxyManifestOnly: []string{"somecdn"}},
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/x.m3u8", nil)
	meta := linkMeta{
		reqIP:    "127.0.0.1",
		verifyIP: true,
		plugin:   "somecdn",
		baseURI:  "https://nl104.cdnsqu.com/hls/hdr_018/film_2160.mp4/index.m3u8?hash=FHASH",
	}

	fmp4 := "#EXTM3U\n#EXT-X-MAP:URI=\"init-v1-a1.mp4\"\nseg-1-v1-a1.m4s?hash=FHASH\n"
	got := h.rewriteM3U(fmp4, req, meta)
	if strings.Contains(got, "https://nl104.cdnsqu.com/hls/hdr_018/film_2160.mp4/seg-1") {
		t.Fatalf("fMP4 segments must stay proxied:\n%s", got)
	}
	if !strings.Contains(got, "lampac.local/proxy/") {
		t.Fatalf("fMP4 segments must be rewritten through the proxy:\n%s", got)
	}

	ts := "#EXTM3U\nseg-1-v1-a1.ts?hash=FHASH\n"
	got = h.rewriteM3U(ts, req, meta)
	if !strings.Contains(got, "https://nl104.cdnsqu.com/hls/hdr_018/film_2160.mp4/seg-1-v1-a1.ts?hash=FHASH") {
		t.Fatalf("plain TS segments must go direct:\n%s", got)
	}
}

// filmix в manifest-only РАЗРЕШЁН (2026-08-05): привязки сегментов к IP нет — прежний вывод был
// сделан на протухшем hash. Отдаём медиа напрямую, а в манифесте чиним hashless EXT-X-MAP, иначе
// init-сегмент 403-ит всё воспроизведение fMP4 (HDR/HEVC).
func TestManifestOnlyFilmixDirectSegmentsAndRepairedInit(t *testing.T) {
	h := New(config.Config{
		Online: config.OnlineConfig{StreamProxyManifestOnly: []string{"Filmix", "somecdn"}},
	}, nil)
	if !h.manifestOnlyPlugins["filmix"] {
		t.Fatal("filmix больше не должен вычёркиваться из manifest-only")
	}

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/x.m3u8", nil)
	meta := linkMeta{
		reqIP:    "127.0.0.1",
		verifyIP: true,
		plugin:   "filmix",
		baseURI:  "https://nl221.werkecdn.me/hls/hdr_018_rus/film_2160.mp4/index.m3u8?hash=FHASH",
	}
	// fMP4-плейлист: init без hash (так его отдаёт CDN), сегмент — с hash.
	src := "#EXTM3U\n" +
		`#EXT-X-MAP:URI="https://nl221.werkecdn.me/hls/hdr_018_rus/film_2160.mp4/init-v1-a1.mp4"` + "\n" +
		"https://nl221.werkecdn.me/hls/hdr_018_rus/film_2160.mp4/seg-1-v1-a1.m4s?hash=FHASH\n"
	got := h.rewriteM3U(src, req, meta)

	// Сегменты — прямыми байтами мимо сервера (в этом весь смысл режима).
	if strings.Contains(got, "lampac.local/proxy/") {
		t.Fatalf("сегменты HDR должны идти напрямую:\n%s", got)
	}
	// А init обязан получить hash, иначе CDN отдаст 403 и плеер встанет на первом кадре.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "EXT-X-MAP") && !strings.Contains(line, "hash=FHASH") {
			t.Fatalf("в EXT-X-MAP не дописан hash:\n%s", line)
		}
	}
}
