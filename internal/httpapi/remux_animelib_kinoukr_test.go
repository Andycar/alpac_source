package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestRemuxChecksearchNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.php" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`
<div>Поиск по сайту</div>
<div class="item__title"><a href="https://megaoblako.com/movie">Inception (2010/WEB-DL)</a></div>
`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Remux: config.RemuxSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/remux?checksearch=true&title=Inception&year=2010", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestAnimelibChecksearchNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/anime" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer lib-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"rus_name":"Начало","eng_name":"Inception","slug_url":"inception","releaseDate":"2010-01-01"}]}`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			AnimeLib: config.HostTokenSource{Host: upstream.URL, Token: "lib-token"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/animelib?checksearch=true&title=Inception&year=2010", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestKinoukrChecksearchNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/xfsearch/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`<div id="dle-content"><div class="short">
			<a class="short-title" href="https://kinoukr.tv/100-inception.html">Початок</a>
		</div></div>`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Kinoukr: config.KinoukrSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinoukr?checksearch=true&title=Inception&original_title=Inception&year=2010", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"rch":true`) {
		t.Fatalf("expected rch:true, got: %s", rec.Body.String())
	}
}
