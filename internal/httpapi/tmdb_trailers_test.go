package httpapi

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/litesrc"
)

func TestParseTMDBVideoPath(t *testing.T) {
	cases := []struct {
		path         string
		kind         string
		id           int
		direct, want bool
	}{
		{"3/movie/123/videos", "movie", 123, true, true},
		{"3/tv/45/videos", "tv", 45, true, true},
		{"3/movie/123", "movie", 123, false, true},
		{"3/tv/45/", "tv", 45, false, true},
		{"3/movie/123/credits", "", 0, false, false},
		{"3/search/movie", "", 0, false, false},
		{"3/person/9", "", 0, false, false},
	}
	for _, c := range cases {
		k, id, d, ok := parseTMDBVideoPath(c.path)
		if ok != c.want || k != c.kind || id != c.id || d != c.direct {
			t.Errorf("%q → kind=%q id=%d direct=%v ok=%v", c.path, k, id, d, ok)
		}
	}
}

func TestWithVideoLanguage(t *testing.T) {
	q := url.Values{"language": {"ru-RU"}}
	if !withVideoLanguage("3/movie/1/videos", q) || q.Get("include_video_language") != "ru,en,null" {
		t.Fatal("прямой /videos должен получить include_video_language")
	}
	q = url.Values{"language": {"ru-RU"}, "append_to_response": {"credits,videos,similar"}}
	if !withVideoLanguage("3/tv/2", q) {
		t.Fatal("карточка с append videos должна получить параметр")
	}
	q = url.Values{"append_to_response": {"credits"}}
	if withVideoLanguage("3/tv/2", q) {
		t.Fatal("карточка без videos — не трогаем")
	}
	q = url.Values{"include_video_language": {"en"}}
	if withVideoLanguage("3/movie/1/videos", q) || q.Get("include_video_language") != "en" {
		t.Fatal("клиентский параметр не перезаписываем")
	}
	if withVideoLanguage("3/search/movie", url.Values{}) {
		t.Fatal("посторонний путь не трогаем")
	}
}

func newTestFinder(t *testing.T) *litesrc.TrailerFinder {
	t.Helper()
	cfg := config.Config{}
	cfg.Compat.RepoRoot = t.TempDir()
	return litesrc.NewTrailerFinder(cfg, nil) // без yt-dlp: Schedule всегда false
}

func TestInjectTrailerAppendCase(t *testing.T) {
	f := newTestFinder(t)
	f.Remember("movie", 5, litesrc.TrailerHit{Key: "zz", Title: "Трейлер", Lang: "ru", Source: "search", At: time.Now()})
	body := []byte(`{"id":5,"title":"Фильм","original_title":"Movie","release_date":"2020-01-01","videos":{"results":[]}}`)
	out, changed, noCache := injectTrailer(body, "3/movie/5", f, nil)
	if !changed || noCache {
		t.Fatalf("changed=%v noCache=%v", changed, noCache)
	}
	s := string(out)
	for _, want := range []string{`"key":"zz"`, `"site":"YouTube"`, `"type":"Trailer"`, `"iso_639_1":"ru"`} {
		if !strings.Contains(s, want) {
			t.Errorf("в теле нет %s:\n%s", want, s)
		}
	}
}

func TestInjectTrailerLeavesRealVideosAlone(t *testing.T) {
	f := newTestFinder(t)
	f.Remember("tv", 6, litesrc.TrailerHit{Key: "zz", At: time.Now()})
	body := []byte(`{"results":[{"site":"YouTube","key":"real","type":"Trailer"}]}`)
	if _, changed, noCache := injectTrailer(body, "3/tv/6/videos", f, nil); changed || noCache {
		t.Fatal("при живом YouTube-видео ничего не подкладываем")
	}
}

func TestInjectTrailerMissIsCacheable(t *testing.T) {
	f := newTestFinder(t)
	f.Remember("movie", 7, litesrc.TrailerHit{Miss: true, At: time.Now()})
	body := []byte(`{"id":7,"title":"X","videos":{"results":[]}}`)
	if _, changed, noCache := injectTrailer(body, "3/movie/7", f, nil); changed || noCache {
		t.Fatal("известный промах: тело как есть и кэшировать можно")
	}
}

// Неизвестный тайтл без yt-dlp: планировать нечего → тело без изменений и
// noCache=false (не блокируем кэш ради поиска, которого не будет).
func TestInjectTrailerUnknownWithoutSearch(t *testing.T) {
	f := newTestFinder(t)
	body := []byte(`{"id":8,"title":"Y","original_title":"Y","release_date":"2019-02-02","videos":{"results":[]}}`)
	if _, changed, noCache := injectTrailer(body, "3/movie/8", f, nil); changed || noCache {
		t.Fatalf("changed=%v noCache=%v", changed, noCache)
	}
}

func TestInjectTrailerDirectNeedsDetail(t *testing.T) {
	f := newTestFinder(t)
	// detail=nil → нечего искать, тело нетронуто.
	if _, changed, noCache := injectTrailer([]byte(`{"results":[]}`), "3/movie/9/videos", f, nil); changed || noCache {
		t.Fatal("без карточки прямой /videos не меняется")
	}
}
