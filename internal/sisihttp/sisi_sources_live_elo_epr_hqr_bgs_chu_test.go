package sisihttp

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestSisiEbalovoListAndView(t *testing.T) {
	resetHTTPAPIGlobals(t)
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.HasPrefix(r.URL.Path, "/video/"):
			_, _ = w.Write([]byte(`
				<div class="item">
				  <div class="item-info"></div>
				  <a href="https://elo.example/video/rel-1"></a>
					  <div class="item-title">Rel 1</div>
					  <img src="https://img.cdn/11/22/333.jpg" />
					  data-eb="11:22;"
					</div>
					<script>video_alt_url: "` + upstream.URL + `/redirect";</script>
				`))
		case r.URL.Path == "/redirect":
			http.Redirect(w, r, "/final.m3u8", http.StatusFound)
		default:
			_, _ = w.Write([]byte(`
				<div class="item">
				  <div class="item-info"></div>
				  <a href="https://elo.example/video/abc-1"></a>
				  <div class="item-title">ELO Title</div>
				  <img src="https://img.cdn/1/2/101.jpg" />
				  data-eb="08:01;"
				</div>
			`))
		}
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_EBALOVO_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiEbalovoSource(cfg)

	listReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/elo", nil)
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
		t.Fatalf("expected 1 list item, got %d", len(list))
	}

	viewReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/elo/vidosik?uri=video/abc-1", nil)
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
	if !strings.HasSuffix(toString(qualitys["auto"]), "/final.m3u8") {
		t.Fatalf("expected redirected auto quality link, got: %v", qualitys)
	}
}

func TestParseEbalovoPlaylistFallbackWithoutLegacyItemClasses(t *testing.T) {
	html := `
		<section class="cards">
		  <article class="card">
		    <a class="thumb" href="https://elo.example/video/new-1" title="ELO New Title"></a>
		    <img data-src="https://img.cdn/new-1.webp" />
		  </article>
		</section>`
	out := parseEbalovoPlaylist("http://lampac.local", html)
	if len(out) != 1 {
		t.Fatalf("expected 1 item, got %d", len(out))
	}
	item := out[0]
	if !strings.Contains(toString(item["video"]), "/elo/vidosik?uri=video%2Fnew-1") {
		t.Fatalf("unexpected video url: %v", item["video"])
	}
	if toString(item["picture"]) != "https://img.cdn/new-1.webp" {
		t.Fatalf("unexpected picture: %v", item["picture"])
	}
}

func TestParseEbalovoPlaylistFallbackWithSlugPath(t *testing.T) {
	html := `
		<section class="cards">
		  <article class="card">
		    <a class="thumb" href="/68335_sample-video-title.html" title="ELO Slug Title"></a>
		    <img data-src="https://img.cdn/new-2.webp" />
		  </article>
		</section>`
	out := parseEbalovoPlaylist("http://lampac.local", html)
	if len(out) != 1 {
		t.Fatalf("expected 1 item, got %d", len(out))
	}
	item := out[0]
	if !strings.Contains(toString(item["video"]), "/elo/vidosik?uri=68335_sample-video-title.html") {
		t.Fatalf("unexpected video url: %v", item["video"])
	}
}

func TestSisiEpornerListAndView(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.Contains(r.URL.Path, "/xhr/video/"):
			_, _ = w.Write([]byte(`{"src":"https:\/\/cdn.example\/abc-720p.mp4","extra":1}`))
		case strings.HasPrefix(r.URL.Path, "/video/"):
			_, _ = w.Write([]byte(`
				<div id="relateddiv">
				  <div class="mb">
				    <p class="mbtit"><a href="/video/rel-1">Rel EPR</a></p>
				    <img src="https://img.cdn/epr-rel.jpg" data-id="rel1" />
				  </div>
				</div>
				<script>
				  var vid = 'abc123';
				  var hash = '00000001000000020000000300000004';
				</script>
			`))
		default:
			_, _ = w.Write([]byte(`
				<div id="vidresults">
				  <div class="mb">
				    <p class="mbtit"><a href="/video/epr-1">EPR Title</a></p>
				    <img src="https://img.cdn/epr.jpg" data-id="epr1" />
				    <div class="mvhdico"><span>720p</span></div>
				    <span class="mbtim">09:11</span>
				  </div>
				</div>
			`))
		}
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_EPORNER_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiEpornerSource(cfg)

	listReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/epr", nil)
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
		t.Fatalf("expected 1 list item, got %d", len(list))
	}

	viewReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/epr/vidosik?uri=video/epr-1", nil)
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
	if toString(qualitys["720p"]) != "https://cdn.example/abc-720p.mp4" {
		t.Fatalf("unexpected 720p link: %v", qualitys["720p"])
	}
}

func TestSisiEpornerViewModernPlayerScript(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.Contains(r.URL.Path, "/xhr/video/"):
			_, _ = w.Write([]byte(`{
				"sources": {
					"mp4": {
						"480p": {"labelShort":"480p","src":"https://cdn.example/new-480p.mp4"},
						"360p": {"labelShort":"360p","src":"https://cdn.example/new-360p.mp4"}
					}
				}
			}`))
		case strings.HasPrefix(r.URL.Path, "/video/"):
			_, _ = w.Write([]byte(`
				<div id="relateddiv">
				  <div class="mb">
				    <p class="mbtit"><a href="/video/rel-modern-1">Rel EPR Modern</a></p>
				    <img src="https://img.cdn/epr-rel-modern.jpg" data-id="rel-modern-1" />
				  </div>
				</div>
				<script>
				  EP.video.player.vid = 'abc123';
				  EP.video.player.hash = '00000001000000020000000300000004';
				</script>
			`))
		default:
			_, _ = w.Write([]byte(`<div id="vidresults"></div>`))
		}
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_EPORNER_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiEpornerSource(cfg)

	viewReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/epr/vidosik?uri=video/epr-modern-1", nil)
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
	if toString(qualitys["480p"]) != "https://cdn.example/new-480p.mp4" {
		t.Fatalf("unexpected 480p link: %v", qualitys["480p"])
	}
	if toString(qualitys["360p"]) != "https://cdn.example/new-360p.mp4" {
		t.Fatalf("unexpected 360p link: %v", qualitys["360p"])
	}
}

func TestSisiEpornerViewUsesProxyQualitiesForBrowser(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.Contains(r.URL.Path, "/xhr/video/"):
			_, _ = w.Write([]byte(`{"src":"https:\/\/cdn.example\/abc-720p.mp4","extra":1}`))
		case strings.HasPrefix(r.URL.Path, "/video/"):
			_, _ = w.Write([]byte(`
				<script>
				  EP.video.player.vid = 'abc123';
				  EP.video.player.hash = '00000001000000020000000300000004';
				</script>
			`))
		default:
			_, _ = w.Write([]byte(`<div id="vidresults"></div>`))
		}
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_EPORNER_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiEpornerSource(cfg)

	viewReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/epr/vidosik?uri=video/epr-1", nil)
	viewReq.Header.Set("Sec-Fetch-Mode", "cors")
	viewReq.Header.Set("User-Agent", "Mozilla/5.0")
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
	proxyQualitys, _ := viewPayload["qualitys_proxy"].(map[string]any)
	q720 := toString(qualitys["720p"])
	if !strings.HasPrefix(q720, "http://lampac.local/proxy/") {
		t.Fatalf("expected proxied 720p for browser, got %q", q720)
	}
	if toString(proxyQualitys["720p"]) != q720 {
		t.Fatalf("qualitys and qualitys_proxy mismatch: %q != %q", q720, toString(proxyQualitys["720p"]))
	}
}

func TestSisiHQpornerListAndView(t *testing.T) {
	resetHTTPAPIGlobals(t)
	var upstream *httptest.Server
	upstream = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.HasPrefix(r.URL.Path, "/video/abc/"):
			_, _ = w.Write([]byte(`
				<video>
				  <source src="//cdn.example/hq-1080.mp4" title="1080p" />
				  <source src="//cdn.example/hq-default.mp4" title="Default" />
				</video>
			`))
		case strings.HasPrefix(r.URL.Path, "/video/"):
			_, _ = w.Write([]byte(`<iframe src="//` + r.Host + `/video/abc/"></iframe>`))
		default:
			_, _ = w.Write([]byte(`
					<div class="img-container">
					  <a href="/video/hq-1" class="atfi123"><img src="//img.cdn/hq.jpg" class="img" alt="HQ Title"></a>
					  <i class="fa fa-clock-o" ></i> 13:37<
					</div>
				`))
		}
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_HQPORNER_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiHQpornerSource(cfg)
	source.client = upstream.Client()

	listReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/hqr", nil)
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
		t.Fatalf("expected 1 list item, got %d, body: %s", len(list), listRec.Body.String())
	}

	viewReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/hqr/vidosik?uri=video/hq-1", nil)
	viewRec := httptest.NewRecorder()
	source.viewHandler().ServeHTTP(viewRec, viewReq)
	if viewRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", viewRec.Code)
	}
	var viewPayload map[string]any
	if err := stdjson.Unmarshal(viewRec.Body.Bytes(), &viewPayload); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if toString(viewPayload["1080p"]) != "https://cdn.example/hq-1080.mp4" {
		t.Fatalf("unexpected 1080p link: %v", viewPayload["1080p"])
	}
}

func TestParseHQpornerPlaylistFallbackOnChangedCardMarkup(t *testing.T) {
	html := `
		<div class="img-container new">
		  <a href="/video/hq-new-1" class="thumb-link">
		    <img data-src="//img.cdn/hq-new.webp" title="HQ New Title" />
		  </a>
		</div>`
	out := parseHQpornerPlaylist("http://lampac.local", html)
	if len(out) != 1 {
		t.Fatalf("expected 1 item, got %d", len(out))
	}
	item := out[0]
	if !strings.Contains(toString(item["video"]), "/hqr/vidosik?uri=video%2Fhq-new-1") {
		t.Fatalf("unexpected video url: %v", item["video"])
	}
	if toString(item["picture"]) != "https://img.cdn/hq-new.webp" {
		t.Fatalf("unexpected picture: %v", item["picture"])
	}
}

func TestSisiBongaAndChaturbateListsAndView(t *testing.T) {
	resetHTTPAPIGlobals(t)
	bongaUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{
			"total_count":145,
			"items":[{"gender":"f","username":"anna","esid":"esid123","thumb_image":"\\/img\\/a.{ext}","display_name":"Anna","vq":"HD"}]
		}`))
	}))
	defer bongaUpstream.Close()

	chuUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if strings.HasPrefix(r.URL.Path, "/model1/") {
			_, _ = w.Write([]byte(`https://edge.example/hls/abc\u002Ddef/playlist.m3u8`))
			return
		}
		_, _ = w.Write([]byte(`display_age{"current_show":"public","username":"model1","img":"https:\/\/img.example\/m.jpg"}`))
	}))
	defer chuUpstream.Close()

	t.Setenv("LAMPAC_GO_SISI_BONGACAMS_HOST", bongaUpstream.URL)
	t.Setenv("LAMPAC_GO_SISI_CHATURBATE_HOST", chuUpstream.URL)

	cfg := config.Config{}
	bgs := newSisiBongaSource(cfg)
	chu := newSisiChaturbateSource(cfg)

	bgsReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/bgs?sort=new&pg=2", nil)
	bgsRec := httptest.NewRecorder()
	bgs.listHandler().ServeHTTP(bgsRec, bgsReq)
	if bgsRec.Code != http.StatusOK {
		t.Fatalf("expected bgs 200, got %d", bgsRec.Code)
	}
	var bgsPayload map[string]any
	if err := stdjson.Unmarshal(bgsRec.Body.Bytes(), &bgsPayload); err != nil {
		t.Fatalf("decode bgs list: %v", err)
	}
	bgsList, _ := bgsPayload["list"].([]any)
	if len(bgsList) != 1 {
		t.Fatalf("expected bgs list item, got %d", len(bgsList))
	}
	bgsItem, _ := bgsList[0].(map[string]any)
	if !strings.Contains(toString(bgsItem["video"]), "/public-aac/stream_anna/chunks.m3u8") {
		t.Fatalf("unexpected bgs stream path: %v", bgsItem["video"])
	}
	if intFromAny(bgsPayload["total_pages"]) != 3 {
		t.Fatalf("expected bgs total_pages=3, got %v", bgsPayload["total_pages"])
	}

	chuReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/chu", nil)
	chuRec := httptest.NewRecorder()
	chu.listHandler().ServeHTTP(chuRec, chuReq)
	if chuRec.Code != http.StatusOK {
		t.Fatalf("expected chu list 200, got %d", chuRec.Code)
	}
	var chuPayload map[string]any
	if err := stdjson.Unmarshal(chuRec.Body.Bytes(), &chuPayload); err != nil {
		t.Fatalf("decode chu list: %v", err)
	}
	chuList, _ := chuPayload["list"].([]any)
	if len(chuList) != 1 {
		t.Fatalf("expected chu list item, got %d", len(chuList))
	}

	chuViewReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/chu/potok?baba=model1", nil)
	chuViewRec := httptest.NewRecorder()
	chu.viewHandler().ServeHTTP(chuViewRec, chuViewReq)
	if chuViewRec.Code != http.StatusOK {
		t.Fatalf("expected chu view 200, got %d", chuViewRec.Code)
	}
	var chuViewPayload map[string]any
	if err := stdjson.Unmarshal(chuViewRec.Body.Bytes(), &chuViewPayload); err != nil {
		t.Fatalf("decode chu view: %v", err)
	}
	if toString(chuViewPayload["auto"]) != "https://edge.example/hls/abc-def/playlist.m3u8" {
		t.Fatalf("unexpected chu auto link: %v", chuViewPayload["auto"])
	}
}
