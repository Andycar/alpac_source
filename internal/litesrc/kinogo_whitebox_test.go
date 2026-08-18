package litesrc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
)

const kinogoSampleSearchHTML = `
<!doctype html><html><body>
<div class="fullsearch-msg">По Вашему запросу найдено <b>2 фильмов</b>:</div>
<div class="movie" id="26146">
  <div class="shortstorytitle">
    <h2 class="zagolovki"><a href="%[1]s/26146-movie-one.html">Movie One (2014)</a></h2>
  </div><!--shortstorytitle-->
  <div class="movie__card">
    <div class="movie__info-img">
      <img src="/templates/Kinogo/images/lazy-poster.png" data-src="/uploads/posts/2025/movie-one.webp" alt="">
    </div>
    <div class="movie__info-item"><b>Год выпуска:</b> <a href="/year/2014/">2014</a></div>
  </div>
</div>
<!--shortstory--><div class="movie" id="35185">
  <div class="shortstorytitle">
    <h2 class="zagolovki"><a href="%[1]s/35185-movie-two.html">Movie Two (2016)</a></h2>
  </div><!--shortstorytitle-->
  <div class="movie__card">
    <div class="movie__info-img">
      <img src="/templates/Kinogo/images/lazy-poster.png" data-src="/uploads/posts/2025/movie-two.webp" alt="">
    </div>
    <div class="movie__info-item"><b>Год выпуска:</b> <a href="/year/2016/">2016</a></div>
  </div>
</div>
<!--shortstory--></body></html>`

const kinogoSampleMovieEmbed = `
<html><body>
<script>
makePlayer({
  blocked: false,
  title: "Movie One",
  source: {
    hls: "https://cdn.example/movie/master.m3u8?t=1",
    dash: "https://cdn.example/movie/master.mpd?t=1",
    audio: {"names":["Дубляж","Eng.Original"],"order":[0,1]},
    cc: [{"url":"https://subs.example/ru.vtt","name":"Рус. полные - 1"}]
  }
});
</script>
</body></html>`

const kinogoSampleSerialEmbed = `
<html><body>
<script>
makePlayer({
  playlist: {
    seasons:[{"season":1,"episodes":[{"episode":"1","hls":"https://cdn.example/s1e1.m3u8","audio":{"names":["Дубляж"],"order":[0]}},{"episode":"2","hls":"https://cdn.example/s1e2.m3u8","audio":{"names":["Дубляж"],"order":[0]}}]},{"season":2,"episodes":[{"episode":"1","hls":"https://cdn.example/s2e1.m3u8","audio":{"names":["Дубляж"],"order":[0]}}]}],
  }
});
</script>
</body></html>`

const kinogoSampleMovieHTML = `
<html><body>
<iframe data-src="%[1]s/embed/movie/180"></iframe>
<iframe data-src="//walking-as.allarknow.online/?token_movie=abc&amp;token=def"></iframe>
<iframe data-src="%[1]s/embed/trailer/180?number=1"></iframe>
</body></html>`

func newKinogoTestUpstream(t *testing.T, movieEmbed, serialEmbed string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/"):
			_, _ = w.Write([]byte(strings.ReplaceAll(kinogoSampleSearchHTML, "%[1]s", srv.URL)))
		case strings.HasPrefix(r.URL.Path, "/26146-"):
			_, _ = w.Write([]byte(strings.ReplaceAll(kinogoSampleMovieHTML, "%[1]s", srv.URL)))
		case strings.HasPrefix(r.URL.Path, "/57363-"):
			_, _ = w.Write([]byte(strings.ReplaceAll(kinogoSampleMovieHTML, "%[1]s", srv.URL)))
		case r.URL.Path == "/embed/movie/180":
			_, _ = w.Write([]byte(movieEmbed))
		case r.URL.Path == "/embed/movie/255":
			_, _ = w.Write([]byte(serialEmbed))
		case r.URL.Path == "/":
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestKinogoSearchParseNewFormat(t *testing.T) {
	upstream := newKinogoTestUpstream(t, kinogoSampleMovieEmbed, kinogoSampleSerialEmbed)
	checker := NewKinogoChecker(config.Config{
		Online: config.OnlineConfig{Kinogo: config.KinogoSource{Host: upstream.URL}},
	})
	htmlBody := strings.ReplaceAll(kinogoSampleSearchHTML, "%[1]s", upstream.URL)
	results := checker.parseSearch(htmlBody, []string{"Movie One"}, 2014)
	if results.ExactHref == "" {
		t.Fatalf("expected exact href match for Movie One 2014, got: %#v", results)
	}
	if len(results.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(results.Items))
	}
	if !strings.HasSuffix(results.Items[0].Title, "Movie One") {
		t.Fatalf("expected 'Movie One' title (without year), got %q", results.Items[0].Title)
	}
	if results.Items[0].Year != "2014" {
		t.Fatalf("expected year=2014, got %q", results.Items[0].Year)
	}
}

func TestKinogoParseRealFixtures(t *testing.T) {
	checker := &kinogoChecker{host: "https://kinogo.la"}

	// Search results page.
	searchBytes, err := os.ReadFile("testdata/kinogo_la_search.html")
	if err != nil {
		t.Skipf("missing testdata/kinogo_la_search.html: %v", err)
	}
	results := checker.parseSearch(string(searchBytes), []string{"Интерстеллар"}, 2014)
	if len(results.Items) < 2 {
		t.Fatalf("expected at least 2 search items, got %d", len(results.Items))
	}
	if results.ExactHref == "" {
		t.Fatalf("expected an exact match for 'Интерстеллар' (2014), items=%+v", results.Items)
	}
	if !strings.Contains(results.ExactHref, "26146-interstellar") {
		t.Fatalf("unexpected exact href: %q", results.ExactHref)
	}
	if results.Items[0].Year != "2014" {
		t.Fatalf("first item year=%q, want 2014", results.Items[0].Year)
	}

	// Movie iframe (VenomPlayer with hls/dash/audio/cc).
	movieIframe, err := os.ReadFile("testdata/kinogo_la_movie_iframe.html")
	if err != nil {
		t.Skipf("missing testdata/kinogo_la_movie_iframe.html: %v", err)
	}
	loc := collapsMakePlayerRe.FindStringIndex(string(movieIframe))
	if len(loc) != 2 {
		t.Fatal("makePlayer( not found in movie iframe fixture")
	}
	movieContent := string(movieIframe[loc[1]:])
	hls := collapsNormalizeURL(submatch1(collapsHLSRe, movieContent))
	if hls == "" || !strings.Contains(hls, ".m3u8") {
		t.Fatalf("hls not extracted from movie iframe: %q", hls)
	}

	// Serial iframe (VenomPlayer with playlist.seasons[]).
	serialIframe, err := os.ReadFile("testdata/kinogo_la_serial_iframe.html")
	if err != nil {
		t.Skipf("missing testdata/kinogo_la_serial_iframe.html: %v", err)
	}
	raw := strings.TrimSpace(submatch1(collapsSeasonsRe, string(serialIframe)))
	if raw == "" {
		t.Fatal("seasons: regex did not match serial fixture")
	}
	serial, ok := collapsParseSeasons(raw)
	if !ok || len(serial) == 0 {
		t.Fatalf("collapsParseSeasons returned no seasons (ok=%v)", ok)
	}
	if serial[0].Season != 1 || len(serial[0].Episodes) == 0 {
		t.Fatalf("season[0]=%+v, want season=1 with episodes", serial[0])
	}
	if !strings.Contains(serial[0].Episodes[0].HLS, ".m3u8") {
		t.Fatalf("first episode HLS not extracted: %q", serial[0].Episodes[0].HLS)
	}

	// Movie page iframe selection (filters out trailers, picks api.ortified.ws).
	moviePage, err := os.ReadFile("testdata/kinogo_la_movie.html")
	if err != nil {
		t.Skipf("missing testdata/kinogo_la_movie.html: %v", err)
	}
	iframe := checker.pickIframe(string(moviePage))
	if iframe == "" {
		t.Fatal("pickIframe returned empty for real movie page")
	}
	if !strings.Contains(iframe, "/embed/movie/") {
		t.Fatalf("pickIframe did not pick the VenomPlayer embed: %q", iframe)
	}
	if strings.Contains(iframe, "/trailer/") {
		t.Fatalf("pickIframe selected a trailer iframe: %q", iframe)
	}
}

// A mirror that answers 403 with a Cloudflare interstitial (kinogo.luxury /
// kinogo.biz state since 2026-08) must be skipped in favour of the next one.
func TestKinogoFallsBackOverCloudflareChallenge(t *testing.T) {
	const challengeBody = `<!DOCTYPE html><html><head><title>Just a moment...</title>` +
		`<script src="https://challenges.cloudflare.com/turnstile/v0/api.js"></script></head><body></body></html>`

	var challengeHits int
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		challengeHits++
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(challengeBody))
	}))
	t.Cleanup(dead.Close)

	// Same shape, but serving the challenge with status 200 — CF does that too.
	deadSoft := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		challengeHits++
		_, _ = w.Write([]byte(challengeBody))
	}))
	t.Cleanup(deadSoft.Close)

	live := newKinogoTestUpstream(t, kinogoSampleMovieEmbed, kinogoSampleSerialEmbed)

	checker := &kinogoChecker{
		client: &http.Client{Timeout: 5 * time.Second},
		host:   dead.URL,
		hosts:  []string{dead.URL, deadSoft.URL, live.URL},
	}

	body, ok := checker.fetchSearch(context.Background(), "Movie One")
	if !ok {
		t.Fatal("fetchSearch gave up instead of falling back to the live mirror")
	}
	if challengeHits != 2 {
		t.Fatalf("challenge mirrors hit %d times, want 2", challengeHits)
	}
	results := checker.parseSearch(body, []string{"Movie One"}, 2014)
	if results.ExactHref == "" {
		t.Fatalf("no exact match parsed from the fallback mirror: %+v", results)
	}

	// The working mirror is remembered, so the dead ones are not retried.
	if got := checker.currentHost(); got != live.URL {
		t.Fatalf("currentHost=%q, want %q", got, live.URL)
	}
	if _, ok := checker.fetchSearch(context.Background(), "Movie One"); !ok {
		t.Fatal("second fetchSearch failed")
	}
	if challengeHits != 2 {
		t.Fatalf("dead mirrors retried: %d hits", challengeHits)
	}
}

// hrefs stored while another mirror was live must be re-pointed at the current
// one instead of being fetched from the dead domain.
func TestKinogoSplitHrefReroutesMirrorDomains(t *testing.T) {
	checker := &kinogoChecker{host: "https://kinogo.la"}
	cases := []struct{ in, wantPath, wantExternal string }{
		{"https://kinogo.luxury/26146-movie-one.html", "/26146-movie-one.html", ""},
		{"//kinogo.biz/35185-movie-two.html?x=1", "/35185-movie-two.html?x=1", ""},
		{"/26146-movie-one.html#comments", "/26146-movie-one.html", ""},
		{"26146-movie-one.html", "/26146-movie-one.html", ""},
		{"https://other.example/page.html", "", "https://other.example/page.html"},
	}
	for _, tc := range cases {
		gotPath, gotExternal := checker.splitHref(tc.in)
		if gotPath != tc.wantPath || gotExternal != tc.wantExternal {
			t.Fatalf("splitHref(%q) = (%q, %q), want (%q, %q)", tc.in, gotPath, gotExternal, tc.wantPath, tc.wantExternal)
		}
	}
}

// `cc` is the last key of `source: {…}` in ortified/kinogo movie payloads, so it
// has no trailing comma — requiring one silently dropped every subtitle track.
func TestKinogoSubtitlesWithoutTrailingComma(t *testing.T) {
	loc := collapsMakePlayerRe.FindStringIndex(kinogoSampleMovieEmbed)
	if len(loc) != 2 {
		t.Fatal("makePlayer( not found in sample embed")
	}
	subs := collapsParseSubtitlesRaw(submatch1(collapsCCRe, kinogoSampleMovieEmbed[loc[1]:]))
	if len(subs) != 1 {
		t.Fatalf("parsed %d subtitle tracks, want 1", len(subs))
	}
}

// kinogo bakes a qualifier into every entry title — "(2018)" for films,
// "(1-10 сезон)" for series — and dates a series by its first season. Demanding
// a literal title+year match meant almost nothing produced an exact hit, so
// every lookup degraded to a `similar` list that /capi drops.
func TestKinogoExactMatchTitleQualifiersAndYearDrift(t *testing.T) {
	const searchHTML = `
<div class="movie" id="1">
  <h2 class="zagolovki"><a href="/1-realnye-pacany.html">Реальные пацаны (1-10 сезон)</a></h2>
  <img data-src="/uploads/a.webp">
  <div class="movie__info-item"><b>Год выпуска:</b> <a href="/y/2010/">2010</a></div>
</div>
<div class="movie" id="2">
  <h2 class="zagolovki"><a href="/2-obsessija-2025.html">Обсессия (2025)</a></h2>
  <img data-src="/uploads/b.webp">
  <div class="movie__info-item"><b>Год выпуска:</b> <a href="/y/2025/">2025</a></div>
</div>
<div class="movie" id="3">
  <h2 class="zagolovki"><a href="/3-ukrytie-2011.html">Укрытие (2011)</a></h2>
  <img data-src="/uploads/c.webp">
  <div class="movie__info-item"><b>Год выпуска:</b> <a href="/y/2011/">2011</a></div>
</div>
<div class="movie" id="4">
  <h2 class="zagolovki"><a href="/4-ukrytie-3-sezon.html">Укрытие (3 сезон)</a></h2>
  <img data-src="/uploads/d.webp">
  <div class="movie__info-item"><b>Год выпуска:</b> <a href="/y/2024/">2024</a></div>
</div>`

	checker := &kinogoChecker{host: "https://kinogo.la"}
	cases := []struct {
		name   string
		titles []string
		year   int
		want   string
	}{
		// Season suffix stripped, and 2010 (first season) vs a 2011 card is close enough.
		{"series with season range", []string{"Реальные пацаны"}, 2011, "/1-realnye-pacany.html"},
		// Card year drifted a year ahead of the site's.
		{"year off by one", []string{"Обсессия"}, 2026, "/2-obsessija-2025.html"},
		// Two same-titled entries: the closest year wins, not the first in page order.
		{"closest year wins", []string{"Укрытие"}, 2024, "/4-ukrytie-3-sezon.html"},
		{"closest year wins the other way", []string{"Укрытие"}, 2011, "/3-ukrytie-2011.html"},
		// Original title matches when the Russian one does not.
		{"matches original title", []string{"Silo", "Укрытие"}, 2024, "/4-ukrytie-3-sezon.html"},
		// A genuinely different release is still rejected.
		{"far year rejected", []string{"Обсессия"}, 2015, ""},
		{"unrelated title rejected", []string{"Мандалорец"}, 2024, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checker.parseSearch(searchHTML, tc.titles, tc.year).ExactHref
			if got != tc.want {
				t.Fatalf("ExactHref=%q, want %q", got, tc.want)
			}
		})
	}
}

// api.ortified.ws answers 422 for part of the catalogue; the page carries more
// than one embed, so a failed first iframe must not end the lookup.
func TestKinogoEmbedFallsBackToSecondIframe(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/99-movie.html":
			_, _ = w.Write([]byte(`
<iframe data-src="` + srv.URL + `/embed/trailer/1?number=1"></iframe>
<iframe data-src="` + srv.URL + `/embed/movie/422"></iframe>
<iframe data-src="` + srv.URL + `/embed/movie/180"></iframe>`))
		case "/embed/movie/422":
			w.WriteHeader(http.StatusUnprocessableEntity)
		case "/embed/movie/180":
			_, _ = w.Write([]byte(kinogoSampleMovieEmbed))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	checker := &kinogoChecker{
		client: &http.Client{Timeout: 5 * time.Second},
		host:   srv.URL,
		hosts:  []string{srv.URL},
	}
	embed, ok := checker.fetchEmbed(context.Background(), "/99-movie.html")
	if !ok {
		t.Fatal("fetchEmbed gave up after the 422 iframe instead of trying the next one")
	}
	if hls := collapsNormalizeURL(submatch1(collapsHLSRe, embed.Content)); hls == "" {
		t.Fatalf("no hls parsed from the fallback iframe: %q", embed.Content)
	}
}

// api.ortified.ws rejects the server's datacenter IP with a bodyless 422 while
// the site itself is only reachable directly — so the embed leg must fall back
// to its own SOCKS-routed client instead of failing the whole lookup.
func TestKinogoEmbedFallsBackToProxyClient(t *testing.T) {
	var directHits, proxyHits int

	// Stands in for api.ortified.ws refusing this egress.
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits++
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	t.Cleanup(refusing.Close)

	// Stands in for the same host reached through the proxy.
	serving := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits++
		_, _ = w.Write([]byte(kinogoSampleMovieEmbed))
	}))
	t.Cleanup(serving.Close)

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<iframe data-src="` + refusing.URL + `/embed/movie/1"></iframe>`))
	}))
	t.Cleanup(site.Close)

	checker := &kinogoChecker{
		client: &http.Client{Timeout: 5 * time.Second},
		host:   site.URL,
		hosts:  []string{site.URL},
		// The "proxy" clients rewrite the request to another upstream, which is
		// what a working SOCKS egress amounts to from here. The first one is a
		// dead egress (ortified bans them one by one), the second works.
		embedClients: []kinogoEmbedRoute{
			{addr: "dead", client: &http.Client{Timeout: 5 * time.Second, Transport: rewriteTransport{to: refusing.URL}}},
			{addr: "live", client: &http.Client{Timeout: 5 * time.Second, Transport: rewriteTransport{to: serving.URL}}},
		},
	}

	embed, ok := checker.fetchEmbed(context.Background(), "/1-movie.html")
	if !ok {
		t.Fatal("fetchEmbed gave up instead of retrying the embed through the proxy client")
	}
	if hls := collapsNormalizeURL(submatch1(collapsHLSRe, embed.Content)); hls == "" {
		t.Fatalf("no hls parsed from the proxied embed: %q", embed.Content)
	}
	if directHits == 0 {
		t.Error("direct client was never tried — installs with a clean IP must not pay for a proxy hop")
	}
	if proxyHits == 0 {
		t.Error("no proxy route was used")
	}
	if directHits < 2 {
		t.Errorf("the banned proxy route was not tried before the live one (direct+dead hits=%d)", directHits)
	}
}

// Several proxies may be configured; a banned one must not take the source down.
func TestKinogoEmbedProxyListParsing(t *testing.T) {
	routes := kinogoEmbedClients("socks5://127.0.0.1:40009, 127.0.0.1:41000;127.0.0.1:41001  127.0.0.1:40009")
	if len(routes) != 3 {
		t.Fatalf("parsed %d routes, want 3 (duplicates collapsed): %+v", len(routes), routes)
	}
	if routes[0].addr != "socks5://127.0.0.1:40009" || routes[1].addr != "127.0.0.1:41000" {
		t.Fatalf("unexpected order/addrs: %+v", routes)
	}
	if got := kinogoEmbedClients("   "); len(got) != 0 {
		t.Fatalf("blank setting produced %d routes", len(got))
	}
}

// rewriteTransport sends every request to a fixed base URL, standing in for a
// proxy that reaches a host this process otherwise cannot.
type rewriteTransport struct{ to string }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(rt.to)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
	clone.Host = target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// With no embed proxy configured the behaviour must be exactly as before.
func TestKinogoEmbedWithoutProxyClientStaysDirect(t *testing.T) {
	checker := NewKinogoChecker(config.Config{})
	if len(checker.embedClients) != 0 {
		t.Fatal("embed clients built without embed_socks_proxy configured")
	}
	configured := NewKinogoChecker(config.Config{
		Online: config.OnlineConfig{Kinogo: config.KinogoSource{EmbedSocksProxy: "127.0.0.1:40000"}},
	})
	if len(configured.embedClients) != 1 {
		t.Fatalf("embed_socks_proxy configured but built %d clients", len(configured.embedClients))
	}
}

// A known-dead configured host must not win over the live default.
func TestKinogoDeadConfiguredHostIsDemoted(t *testing.T) {
	checker := NewKinogoChecker(config.Config{
		Online: config.OnlineConfig{Kinogo: config.KinogoSource{Host: "https://kinogo.luxury"}},
	})
	if checker.host != kinogoDefaultHost {
		t.Fatalf("primary host=%q, want %q", checker.host, kinogoDefaultHost)
	}
	if len(checker.hosts) != 2 || checker.hosts[1] != "https://kinogo.luxury" {
		t.Fatalf("hosts=%v, want the dead mirror kept as a trailing candidate", checker.hosts)
	}

	// A host that is not on the dead list stays primary.
	custom := NewKinogoChecker(config.Config{
		Online: config.OnlineConfig{Kinogo: config.KinogoSource{Host: "kinogo.example"}},
	})
	if custom.host != "https://kinogo.example" {
		t.Fatalf("custom host=%q, want https://kinogo.example", custom.host)
	}
}

func TestKinogoPickIframeSkipsTrailers(t *testing.T) {
	checker := &kinogoChecker{host: "https://kinogo.la"}
	html := `
<iframe data-src="//api.ortified.ws/embed/trailer/180?number=1"></iframe>
<iframe data-src="//api.ortified.ws/embed/movie/180"></iframe>
<iframe data-src="//walking-as.allarknow.online/?token_movie=abc"></iframe>
`
	got := checker.pickIframe(html)
	want := "https://api.ortified.ws/embed/movie/180"
	if got != want {
		t.Fatalf("pickIframe: got %q want %q", got, want)
	}
}
