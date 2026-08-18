package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

type remuxRewriteTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t remuxRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "cloud.mail.ru" && t.target != nil {
		cloned := req.Clone(req.Context())
		u := *cloned.URL
		u.Scheme = t.target.Scheme
		u.Host = t.target.Host
		cloned.URL = &u
		return t.base.RoundTrip(cloned)
	}
	return t.base.RoundTrip(req)
}

func TestRemuxSimilarRjson(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.php" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`
<div>Поиск по сайту</div>
<div class="item__title"><a href="` + upstream.URL + `/movie/a">Inception (2010/WEB-DL)</a></div>
<div class="item__title"><a href="` + upstream.URL + `/movie/b">Inception: Alt (2010/WEB-DL)</a></div>
`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Remux: config.RemuxSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/remux?rjson=true&title=Inception&original_title=Inception&year=2010", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v body=%s", err, rec.Body.String())
	}
	if toString(payload["type"]) != "similar" {
		t.Fatalf("unexpected type: %#v body=%s", payload["type"], rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected similar rows count: %d", len(data))
	}
}

// TestRemuxWeblinkViewParsing is a network-free guard for the 404 fix: the
// stream URL must be built from cloud.mail.ru's weblink_view base (not the
// weblink_get download base with a per-session token, which produced a nonsense
// URL that 404'd on every video). Snippet mirrors a real cloud.mail.ru page.

func TestRemuxMovieRowsAndMovieEndpoint(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie/inception":
			_, _ = w.Write([]byte(`
<div class="page__desc">
	<div class="quote"><a href="https://cloud.mail.ru/public/abc123">HDR 2160p</a></div>
	<div class="quote"><a href="https://cloud.mail.ru/public/def456">Default 1400</a></div>
</div>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/public/abc123" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// weblink_view is the streaming base; weblink_get (download, tokenised) is a
		// decoy that must be ignored — see the weblink() fix.
		_, _ = w.Write([]byte(`{"weblink_get":{"url":"https://cdn.mail.example/public/TOKEN/g/no"},"weblink_view":{"url":"https://cdn.mail.example/weblink/view/"}}`))
	}))
	defer cloud.Close()

	cloudURL, err := url.Parse(cloud.URL)
	if err != nil {
		t.Fatalf("parse cloud url: %v", err)
	}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = remuxRewriteTransport{base: oldTransport, target: cloudURL}
	defer func() { http.DefaultTransport = oldTransport }()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Remux: config.RemuxSource{Host: upstream.URL},
		},
	}

	mainRec := httptest.NewRecorder()
	mainReq := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/remux?rjson=true&title=Inception&original_title=Inception&year=2010&href="+url.QueryEscape(upstream.URL+"/movie/inception"),
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(mainRec, mainReq)

	if mainRec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", mainRec.Code, mainRec.Body.String())
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(mainRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v body=%s", err, mainRec.Body.String())
	}
	if toString(payload["type"]) != "movie" {
		t.Fatalf("unexpected type: %#v body=%s", payload["type"], mainRec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected movie rows count: %d", len(data))
	}
	row0, _ := data[0].(map[string]any)
	if !strings.Contains(toString(row0["url"]), "/lite/remux/movie?") {
		t.Fatalf("unexpected movie row url: %#v", row0["url"])
	}

	videoRec := httptest.NewRecorder()
	videoReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/remux/movie?linkid=abc123&quality=2160p&title=Inception", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(videoRec, videoReq)

	if videoRec.Code != http.StatusOK {
		t.Fatalf("unexpected video status: %d body=%s", videoRec.Code, videoRec.Body.String())
	}
	if !strings.Contains(videoRec.Body.String(), `"method":"play"`) {
		t.Fatalf("unexpected video body: %s", videoRec.Body.String())
	}
	if !strings.Contains(videoRec.Body.String(), `https://cdn.mail.example/weblink/view/abc123`) {
		t.Fatalf("unexpected video url in body: %s", videoRec.Body.String())
	}
}
