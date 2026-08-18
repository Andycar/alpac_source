package litesrc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lampac-go/internal/config"
)

func TestSakhTVPickMovie(t *testing.T) {
	items := []sakhtvSearchItem{
		{ID: 1, IDAlpha: "x-2020", OriginTitle: "X Movie", ReleaseDate: "2020-05-01"},
		{ID: 2, IDAlpha: "interstellar-2014", OriginTitle: "Interstellar", ReleaseDate: "2014-11-05"},
		{ID: 3, IDAlpha: "y-2021", OriginTitle: "Y Film", ReleaseDate: "2021-01-01"},
	}

	// Year match wins.
	m := pickSakhtvMovie(items, 2014, "", "", "")
	if m == nil || m.IDAlpha != "interstellar-2014" {
		t.Fatalf("expected interstellar-2014 by year, got %+v", m)
	}

	// Origin-title match when year missing.
	m = pickSakhtvMovie(items, 0, "", "", "Y Film")
	if m == nil || m.IDAlpha != "y-2021" {
		t.Fatalf("expected y-2021 by origin title, got %+v", m)
	}

	// Fallback to first item with id_alpha.
	m = pickSakhtvMovie(items, 1999, "", "", "")
	if m == nil || m.IDAlpha != "x-2020" {
		t.Fatalf("expected fallback to first, got %+v", m)
	}

	if pickSakhtvMovie(nil, 0, "", "", "") != nil {
		t.Fatalf("expected nil on empty input")
	}
}

func TestSakhTVPickSerial(t *testing.T) {
	items := []sakhtvSearchItem{
		{ID: 1, Tvshow: "the.breaks", Year: 2017, KPID: 1009766},
		{ID: 7, Tvshow: "breaking_bad", Year: 2008, KPID: 404900, ImdbURL: "http://www.imdb.com/title/tt903747/"},
		{ID: 9, Tvshow: "other_show", Year: 2010, KPID: 11111},
	}

	// kp_id match wins over year/imdb.
	s := pickSakhtvSerial(items, 1990, "", "404900")
	if s == nil || s.Tvshow != "breaking_bad" {
		t.Fatalf("expected breaking_bad by kp_id, got %+v", s)
	}

	// imdb match.
	s = pickSakhtvSerial(items, 0, "tt903747", "")
	if s == nil || s.Tvshow != "breaking_bad" {
		t.Fatalf("expected breaking_bad by imdb, got %+v", s)
	}

	// Year match.
	s = pickSakhtvSerial(items, 2017, "", "")
	if s == nil || s.Tvshow != "the.breaks" {
		t.Fatalf("expected the.breaks by year, got %+v", s)
	}
}

func TestSakhTVPickStream(t *testing.T) {
	movie := &sakhtvMovieDetail{}
	movie.Sources.Default = "https://cdn/default.m3u8"
	movie.Sources.Variants = []sakhtvVariant{
		{Type: "hlsmov01", URL: "https://cdn/v1.m3u8"},
		{Type: "hls", URL: "https://cdn/v2.m3u8"},
		{Type: "hls2", URL: "https://cdn/v3.m3u8"},
	}
	got := sakhtvPickStream(movie)
	if got != "https://cdn/v2.m3u8" {
		t.Fatalf("expected hls variant first, got %s", got)
	}

	// No variants → default.
	movie.Sources.Variants = nil
	if sakhtvPickStream(movie) != "https://cdn/default.m3u8" {
		t.Fatalf("expected default fallback")
	}

	// Empty everything.
	movie.Sources.Default = ""
	if sakhtvPickStream(movie) != "" {
		t.Fatalf("expected empty stream")
	}
}

func TestSakhTVParseYear(t *testing.T) {
	cases := map[string]int{
		"2014-11-05": 2014,
		"":           0,
		"abc":        0,
		"1999":       1999,
	}
	for in, want := range cases {
		if got := sakhtvParseYear(in); got != want {
			t.Errorf("sakhtvParseYear(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSakhTVSearchQueries(t *testing.T) {
	qs := sakhtvSearchQueries("Интерстеллар", "Interstellar")
	if len(qs) != 2 || qs[0] != "Интерстеллар" || qs[1] != "Interstellar" {
		t.Fatalf("unexpected queries: %v", qs)
	}
	// Same value → dedup.
	qs = sakhtvSearchQueries("Foo", "foo")
	if len(qs) != 1 {
		t.Fatalf("expected dedup, got %v", qs)
	}
	// Empty inputs.
	if len(sakhtvSearchQueries("", "")) != 0 {
		t.Fatalf("expected empty list for blank input")
	}
}

func TestSakhTVUserAgentMimicsAPK(t *testing.T) {
	// Matches the APK template "SakhTVAndroid/<verName>/<MAKE MODEL>/Android <RELEASE>"
	// from BitmapFactoryDecoder$$ExternalSyntheticLambda0 case 28.
	for i := 0; i < 50; i++ {
		ua := sakhtvUserAgent()
		if !strings.HasPrefix(ua, "SakhTVAndroid/"+sakhtvAppVersion+"/") {
			t.Fatalf("UA prefix mismatch: %q", ua)
		}
		if !strings.Contains(ua, "/Android ") {
			t.Fatalf("UA missing Android release: %q", ua)
		}
	}
}

func TestSakhTVSubtitles(t *testing.T) {
	tracks := []sakhtvTrack{
		{Label: "Русский [rus]", Language: "rus", Src: "https://x/1.vtt"},
		{Label: "", Language: "eng", Src: "https://x/2.vtt"},
		{Label: "skip", Language: "rus", Src: ""},
	}
	subs := sakhtvSubtitles(tracks)
	if len(subs) != 2 {
		t.Fatalf("expected 2 subs, got %d", len(subs))
	}
	if subs[0]["label"] != "Русский [rus]" || subs[0]["url"] != "https://x/1.vtt" {
		t.Errorf("unexpected sub 0: %+v", subs[0])
	}
	if subs[1]["label"] != "ENG" {
		t.Errorf("expected upper-case lang fallback, got %s", subs[1]["label"])
	}
}

// --- Session handling ------------------------------------------------------
//
// The SakhTV account is single-session upstream: every login retires the
// previous token and kills the stream links other viewers are playing. These
// tests pin the three rules that keep logins rare.

type sakhtvFakeAPI struct {
	srv    *httptest.Server
	logins int32
}

// newSakhtvFakeAPI serves login (counted) plus two probe endpoints:
// /forbidden always 403, /expired always 401.
func newSakhtvFakeAPI(t *testing.T) *sakhtvFakeAPI {
	t.Helper()
	api := &sakhtvFakeAPI{}
	api.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/users/login":
			n := atomic.AddInt32(&api.logins, 1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"auth":true,"token":"tok%d"}`, n)
		case strings.HasPrefix(r.URL.Path, "/forbidden"):
			w.WriteHeader(http.StatusForbidden)
		case strings.HasPrefix(r.URL.Path, "/expired"):
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(api.srv.Close)
	return api
}

func (a *sakhtvFakeAPI) count() int { return int(atomic.LoadInt32(&a.logins)) }

func newSakhtvTestChecker(t *testing.T, host, root string) *sakhtvChecker {
	t.Helper()
	var cfg config.Config
	cfg.Compat.RepoRoot = root
	cfg.Online.SakhTV.Host = host
	cfg.Online.SakhTV.Login = "user"
	cfg.Online.SakhTV.Passwd = "pass"
	s := NewSakhTVChecker(cfg)
	s.client = host2Client()
	return s
}

func host2Client() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

func TestSakhTV403DoesNotRelogin(t *testing.T) {
	api := newSakhtvFakeAPI(t)
	s := newSakhtvTestChecker(t, api.srv.URL, t.TempDir())

	var out map[string]any
	if err := s.doJSON(context.Background(), "/forbidden", &out); err == nil {
		t.Fatal("expected error on 403")
	}
	// Exactly one login: the initial one. A 403 is a content/plan gate, not a
	// dead session — re-logging in on it is what used to evict other viewers.
	if got := api.count(); got != 1 {
		t.Fatalf("expected 1 login (initial), got %d", got)
	}
}

func TestSakhTVLoginCooldownSuppressesSecondLogin(t *testing.T) {
	api := newSakhtvFakeAPI(t)
	s := newSakhtvTestChecker(t, api.srv.URL, t.TempDir())

	var out map[string]any
	// First 401 → the initial login happened moments ago, so the refresh is
	// suppressed rather than minting a session that kills current playback.
	err := s.doJSON(context.Background(), "/expired", &out)
	if !errors.Is(err, errSakhTVLoginCooldown) {
		t.Fatalf("expected cooldown error, got %v", err)
	}
	if got := api.count(); got != 1 {
		t.Fatalf("expected login suppressed by cooldown, got %d logins", got)
	}

	// Past the window a genuine 401 does refresh — once — then gives up.
	s.tokenMu.Lock()
	s.lastLogin = time.Now().Add(-2 * sakhtvLoginMinInterval)
	s.tokenMu.Unlock()
	err = s.doJSON(context.Background(), "/expired", &out)
	if !errors.Is(err, errSakhTVUnauthorized) {
		t.Fatalf("expected unauthorized after retry, got %v", err)
	}
	if got := api.count(); got != 2 {
		t.Fatalf("expected exactly one re-login, got %d logins total", got)
	}
}

func TestSakhTVTokenSurvivesRestart(t *testing.T) {
	api := newSakhtvFakeAPI(t)
	root := t.TempDir()

	s := newSakhtvTestChecker(t, api.srv.URL, root)
	tok, err := s.authToken(context.Background())
	if err != nil || tok != "tok1" {
		t.Fatalf("authToken = %q, %v", tok, err)
	}

	// A rebuilt checker (hot-reload, restart) must reuse the persisted session
	// instead of logging in again and retiring it.
	s2 := newSakhtvTestChecker(t, api.srv.URL, root)
	tok2, err := s2.authToken(context.Background())
	if err != nil || tok2 != "tok1" {
		t.Fatalf("second checker: authToken = %q, %v", tok2, err)
	}
	if got := api.count(); got != 1 {
		t.Fatalf("expected the persisted token to be reused, got %d logins", got)
	}

	// Different credentials → the stored session belongs to someone else.
	var other config.Config
	other.Compat.RepoRoot = root
	other.Online.SakhTV.Host = api.srv.URL
	other.Online.SakhTV.Login = "other"
	other.Online.SakhTV.Passwd = "pass"
	s3 := NewSakhTVChecker(other)
	s3.tokenMu.Lock()
	stored := s3.token
	s3.tokenMu.Unlock()
	if stored != "" {
		t.Fatalf("expected no token reuse across accounts, got %q", stored)
	}
}

func TestSakhTVApplyConfigDropsTokenOnAccountChange(t *testing.T) {
	api := newSakhtvFakeAPI(t)
	root := t.TempDir()
	s := newSakhtvTestChecker(t, api.srv.URL, root)
	if _, err := s.authToken(context.Background()); err != nil {
		t.Fatalf("authToken: %v", err)
	}

	// Same config re-applied (every liteSourceHandler rebuild does this): token kept.
	var same config.Config
	same.Compat.RepoRoot = root
	same.Online.SakhTV.Host = api.srv.URL
	same.Online.SakhTV.Login = "user"
	same.Online.SakhTV.Passwd = "pass"
	s.applyConfig(same)
	s.tokenMu.Lock()
	kept := s.token
	s.tokenMu.Unlock()
	if kept != "tok1" {
		t.Fatalf("rebuild must not drop the session, got %q", kept)
	}

	// Credentials edited in the admin panel: drop it.
	same.Online.SakhTV.Passwd = "new-pass"
	s.applyConfig(same)
	s.tokenMu.Lock()
	dropped := s.token
	s.tokenMu.Unlock()
	if dropped != "" {
		t.Fatalf("expected token dropped on credentials change, got %q", dropped)
	}
}
