package litesrc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFilmixFilePath(t *testing.T) {
	cases := map[string]string{
		"https://nl221.werkecdn.me/s/FH3EyKY1ASlJwzDqMBNy/911.lostfilm.2018-nf20/s06e16_1080.mp4": "911.lostfilm.2018-nf20/s06e16_1080.mp4",
		"https://nl105.cdnsqu.com/s/FHq3StICLF/UHD_1313/Coyote.vs.Acme_1080.mp4?x=1":              "UHD_1313/Coyote.vs.Acme_1080.mp4",
		"https://host/hls/abc/index.m3u8?hash=zzz":                                                "",
		"https://nl1.cdnsqu.com/s/HASH/":                                                          "",
		"":                                                                                        "",
	}
	for in, want := range cases {
		if got := filmixFilePath(in); got != want {
			t.Errorf("filmixFilePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// Сброс общего состояния пакета между тестами.
func resetFilmixRemintState(t *testing.T, health map[string]filmixTokenState) {
	t.Helper()
	filmixMintMu.Lock()
	filmixMints = map[string]filmixMint{}
	filmixRemints = map[string]filmixRemintResult{}
	filmixMintMu.Unlock()
	filmixTokenHealthMu.Lock()
	prev := filmixTokenHealth
	filmixTokenHealth = health
	filmixTokenHealthMu.Unlock()
	t.Cleanup(func() {
		filmixTokenHealthMu.Lock()
		filmixTokenHealth = prev
		filmixTokenHealthMu.Unlock()
	})
}

// 429 на ссылке учётки A → пост запрашивается токеном учётки B, и в ответе находится тот же файл.
func TestFilmixRemintOtherAccount(t *testing.T) {
	var gotToken atomic.Value
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v2/post/777" {
			http.NotFound(w, r)
			return
		}
		gotToken.Store(r.URL.Query().Get("user_dev_token"))
		_, _ = w.Write([]byte(`{"player_links":{"movie":[
			{"link":"https://nl2.cdnsqu.com/s/HASHB/UHD_77/Some.Movie_[1080,720,].mp4","translation":"Дубляж"}],
			"playlist":[]}}`))
	}))
	defer srv.Close()

	resetFilmixRemintState(t, map[string]filmixTokenState{
		"tokA": {Alive: true, Pro: true, Account: "aaaaaa", Checked: time.Now()},
		"tokB": {Alive: true, Pro: true, Account: "bbbbbb", Checked: time.Now()},
	})
	f := &filmixChecker{client: srv.Client(), hosts: []string{srv.URL}, tokens: []string{"tokA"},
		reserveTokens: []string{"tokB"}, pro: true}

	stale := "https://nl1.cdnsqu.com/s/HASHA/UHD_77/Some.Movie_1080.mp4"
	filmixRememberMint([]map[string]string{{"url": stale + " or https://nl9.cdnsqu.com/s/HASHA/UHD_77/Some.Movie_1080.mp4"}}, 777, "tokA")

	fresh, ok := f.remint(context.Background(), stale)
	if !ok || fresh != "https://nl2.cdnsqu.com/s/HASHB/UHD_77/Some.Movie_1080.mp4" {
		t.Fatalf("remint = %q, %v", fresh, ok)
	}
	if tok, _ := gotToken.Load().(string); tok != "tokB" {
		t.Fatalf("пост запрошен токеном %q, ждали токен другой учётки", tok)
	}
	// Повтор (следующий кусок mp4) — из кэша, без API.
	if again, ok := f.remint(context.Background(), stale); !ok || again != fresh || calls.Load() != 1 {
		t.Fatalf("повтор: %q %v, обращений к API %d", again, ok, calls.Load())
	}
}

// Другой учётки нет (у ноды одна) — перевыпуска нет, API не трогаем.
func TestFilmixRemintNoOtherAccount(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	resetFilmixRemintState(t, map[string]filmixTokenState{
		"tokA":  {Alive: true, Pro: true, Account: "aaaaaa"},
		"tokA2": {Alive: true, Pro: true, Account: "aaaaaa"}, // второй токен той же учётки — не спасёт
	})
	f := &filmixChecker{client: srv.Client(), hosts: []string{srv.URL}, tokens: []string{"tokA", "tokA2"}, pro: true}
	stale := "https://nl1.cdnsqu.com/s/HASHA/Show/s01e01_1080.mp4"
	filmixRememberMint([]map[string]string{{"url": stale}}, 5, "tokA")
	if fresh, ok := f.remint(context.Background(), stale); ok || fresh != "" {
		t.Fatalf("перевыпуск без другой учётки: %q", fresh)
	}
	if calls.Load() != 0 {
		t.Fatalf("API звали %d раз без другой учётки", calls.Load())
	}
	// Незнакомая ссылка (выдана до рестарта) — тоже мимо.
	if _, ok := f.remint(context.Background(), "https://nl1.cdnsqu.com/s/X/Other/file_1080.mp4"); ok {
		t.Fatal("перевыпуск незнакомой ссылки")
	}
}

// Сериал: файл ищется по сезонам и озвучкам.
func TestFilmixRemintFindsEpisode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"player_links":{"movie":[],"playlist":{"1":{"LostFilm":{
			"1":{"link":"https://nl3.werkecdn.me/s/HASHB/Show-LF/s01e01_%s.mp4","qualities":[1080,720]},
			"2":{"link":"https://nl3.werkecdn.me/s/HASHB/Show-LF/s01e02_%s.mp4","qualities":[1080,720]}}}}}}`))
	}))
	defer srv.Close()
	resetFilmixRemintState(t, map[string]filmixTokenState{
		"tokA": {Alive: true, Pro: true, Account: "aaaaaa"},
		"tokB": {Alive: true, Pro: true, Account: "bbbbbb"},
	})
	// у main обе учётки — обычными токенами
	f := &filmixChecker{client: srv.Client(), hosts: []string{srv.URL}, tokens: []string{"tokA", "tokB"}, pro: true}
	stale := "https://nl1.werkecdn.me/s/HASHA/Show-LF/s01e02_720.mp4"
	filmixRememberMint([]map[string]string{{"url": stale}}, 9, "tokA")
	fresh, ok := f.remint(context.Background(), stale)
	if !ok || !strings.HasSuffix(fresh, "/s/HASHB/Show-LF/s01e02_720.mp4") {
		t.Fatalf("remint = %q, %v", fresh, ok)
	}
}
