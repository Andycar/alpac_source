package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFilmixToHLSLegacyForm(t *testing.T) {
	f := &filmixChecker{hls: true}

	// cdnsqu/werkecdn dropped HLS (the /hls/...index.m3u8?hash= form 403s/404s), so toHLS must
	// leave the direct /s/<token>/...mp4 form untouched — that's what streams 206.
	for _, dead := range []string{
		"https://nl105.cdnsqu.com/s/HASH123/sasha.tanya/s11e28_1080.mp4",
		"https://nl03.werkecdn.me/s/HASH123/abc/film_1080.mp4",
	} {
		if got := f.toHLS(dead); got != dead {
			t.Fatalf("dead-HLS CDN must pass through unchanged:\n got  %s\n want %s", got, dead)
		}
	}

	// Any other CDN that still serves HLS keeps the legacy rewrite.
	got := f.toHLS("https://nl105.othercdn.net/s/HASH123/sasha.tanya/s11e28_1080.mp4")
	want := "https://nl105.othercdn.net/hls/sasha.tanya/s11e28_1080.mp4/index.m3u8?hash=HASH123"
	if got != want {
		t.Fatalf("legacy:\n got  %s\n want %s", got, want)
	}
}

// Legacy /s/ hashes cap at 720p server-side since 2026-07 (higher qualities
// stream the premium stub) — allowQuality must never let >720p through, token
// or not. The old pro auto-detect by the post "quality" field is gone: that
// field now reports content quality ("2160" even anonymous).
func TestFilmixLegacyQualityCap(t *testing.T) {
	f := &filmixChecker{}
	for _, q := range []int{2160, 1440, 1080} {
		if f.allowQuality(q, "protoken") {
			t.Fatalf("legacy path must cap at 720p, allowed %d", q)
		}
	}
	if !f.allowQuality(720, "token") || !f.allowQuality(480, "token") {
		t.Fatalf("≤720p with token must pass")
	}
	if f.allowQuality(720, "") {
		t.Fatalf("anonymous must cap at 480p")
	}
	if !f.allowQuality(480, "") {
		t.Fatalf("anonymous 480p must pass")
	}
}

func TestDecodeFXVideoLinks(t *testing.T) {
	// Error message answers (blocked title / dead hash) are a decode failure.
	if _, _, ok := decodeFXVideoLinks([]byte(`{"message":"Видео заблокировано!"}`)); ok {
		t.Fatalf("message answer must not decode")
	}

	// Movie voiceovers with empty file lists are dropped; all-empty = failure.
	if _, _, ok := decodeFXVideoLinks([]byte(`[{"voiceover":"Dub","files":[]}]`)); ok {
		t.Fatalf("movie with no files must not decode")
	}
	movies, serial, ok := decodeFXVideoLinks([]byte(`[
		{"voiceover":"Dub","files":[{"url":"https://cdn/hls/f_2160.mp4/index.m3u8?hash=h","quality":2160}]},
		{"voiceover":"Empty","files":[]}
	]`))
	if !ok || serial != nil || len(movies) != 1 || movies[0].Voiceover != "Dub" {
		t.Fatalf("unexpected movie decode: ok=%v movies=%v serial=%v", ok, movies, serial)
	}

	// Serial: voice → "season-N" → {season, episodes{eN → files}}.
	movies, serial, ok = decodeFXVideoLinks([]byte(`{
		"Dub":{"season-1":{"season":1,"episodes":{"e1":{"episode":1,"files":[{"url":"https://cdn/hls/e1_720.mp4/index.m3u8?hash=h","quality":720}]}}}}
	}`))
	if !ok || movies != nil || len(serial) != 1 {
		t.Fatalf("unexpected serial decode: ok=%v movies=%v serial=%v", ok, movies, serial)
	}
	if serial["Dub"]["season-1"].Season.Int() != 1 {
		t.Fatalf("season number lost: %+v", serial["Dub"])
	}
}

func TestFXStreamsOrderAndEpisodeOrder(t *testing.T) {
	streams := fxStreams([]fxFile{
		{URL: "u480", Quality: 480},
		{URL: "u2160", Quality: 2160},
		{URL: " ", Quality: 1080},
		{URL: "u720", Quality: 720},
	})
	if len(streams) != 3 || streams[0]["quality"] != "2160p" || streams[2]["quality"] != "480p" {
		t.Fatalf("unexpected stream order: %v", streams)
	}

	if n := fxEpisodeOrder(fxEpisode{Episode: 7}, "e3", 1); n != 7 {
		t.Fatalf("payload episode number must win, got %d", n)
	}
	if n := fxEpisodeOrder(fxEpisode{}, "e12", 1); n != 12 {
		t.Fatalf("eN key must parse, got %d", n)
	}
	if n := fxEpisodeOrder(fxEpisode{}, "спецвыпуск", 5); n != 5 {
		t.Fatalf("fallback must apply, got %d", n)
	}
}

// video-links is memoised per (identity, postid) with singleflight: capi drills
// filmix for every viewer of a card, and each quality/episode switch re-enters
// index(), so without this one card open becomes N upstream calls.
func TestFilmixFXVideoLinksCachedAndSingleflight(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api-fx/request-token":
			_, _ = w.Write([]byte(`{"token":"h1"}`))
		case "/api-fx/post/7430/video-links":
			atomic.AddInt32(&calls, 1)
			_, _ = w.Write([]byte(`[{"voiceover":"Dub","files":[{"url":"https://cdn/hls/f_2160.mp4/index.m3u8?hash=h1","quality":2160}]}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	f := &filmixChecker{client: srv.Client(), fxHost: srv.URL}

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			movies, _, ok := f.fxVideoLinks(context.Background(), 7430)
			if !ok || len(movies) != 1 {
				t.Errorf("unexpected result: ok=%v movies=%d", ok, len(movies))
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 upstream call for 12 concurrent viewers, got %d", got)
	}

	// A later call still reads the cache.
	if _, _, ok := f.fxVideoLinks(context.Background(), 7430); !ok {
		t.Fatalf("cached call must succeed")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("cache miss after warm entry: %d calls", got)
	}

	// Different identity must not share the entry.
	if f.fxLinksCacheKey(7430) == (&filmixChecker{fxUser: "u"}).fxLinksCacheKey(7430) {
		t.Fatalf("anonymous and account cache keys must differ")
	}
}

// THE HASH IS THE DEVICE: a login on a known hash is free, a login on a fresh one
// costs one of the account's five slots forever. So the hash must survive both a
// blocked title AND the `expire` field (which belongs to the pairing code, not to
// the hash — a 7h-old hash still serves video-links).
func TestFilmixFXBlockedTitleDoesNotReauth(t *testing.T) {
	var tokenCalls, authCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api-fx/request-token":
			atomic.AddInt32(&tokenCalls, 1)
			// expire deliberately in the PAST — honouring it minted a device every 3.5h.
			fmt.Fprintf(w, `{"token":"h%d","code":"ABCD","expire":%d}`, atomic.LoadInt32(&tokenCalls), time.Now().Add(-time.Hour).Unix())
		case r.URL.Path == "/api-fx/auth":
			atomic.AddInt32(&authCalls, 1)
			_, _ = w.Write([]byte(`{"accessToken":"AT","refreshToken":"RT"}`))
		case strings.HasSuffix(r.URL.Path, "/video-links"):
			_, _ = w.Write([]byte(`{"message":"Видео заблокировано!"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	f := &filmixChecker{
		client:      srv.Client(),
		fxHost:      srv.URL,
		fxUser:      "user",
		fxPasswd:    "pass",
		fxStatePath: filepath.Join(t.TempDir(), "database", "filmix_fx.json"),
	}

	// Several blocked titles, each viewed twice.
	for _, id := range []int{1, 2, 3, 4} {
		for i := 0; i < 2; i++ {
			if _, _, ok := f.fxVideoLinks(context.Background(), id); ok {
				t.Fatalf("blocked title must not decode")
			}
		}
	}

	if got := atomic.LoadInt32(&authCalls); got != 1 {
		t.Fatalf("expected exactly 1 login (1 device), got %d — that many device slots would be burned", got)
	}
	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Fatalf("expected the hash to be minted once and reused, got %d request-token calls", got)
	}

	// State must be on disk so a restart reuses it instead of logging in again.
	raw, err := os.ReadFile(f.fxStatePath)
	if err != nil {
		t.Fatalf("state file must be written: %v", err)
	}
	var st fxPersistedState
	if err := stdjson.Unmarshal(raw, &st); err != nil {
		t.Fatalf("state unmarshal: %v", err)
	}
	if st.Hash == "" || st.Refresh != "RT" {
		t.Fatalf("state must carry hash+refresh: %+v", st)
	}

	// A restart reads it back and performs NO new login.
	f2 := &filmixChecker{client: srv.Client(), fxHost: srv.URL, fxUser: "user", fxPasswd: "pass", fxStatePath: f.fxStatePath}
	_, _, _ = f2.fxVideoLinks(context.Background(), 9)
	if got := atomic.LoadInt32(&authCalls); got != 1 {
		t.Fatalf("restart must reuse persisted tokens, got %d logins", got)
	}
}
