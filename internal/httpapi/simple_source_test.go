package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestSimpleSourcesChecksearch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Ashdi:       config.HostSource{Host: upstream.URL},
			Eneyida:     config.HostSource{Host: upstream.URL},
			Kinogo:      config.KinogoSource{Host: upstream.URL},
			FanCDN:      config.FanCDNSource{Host: upstream.URL},
			VideoDB:     config.VideoDBSource{APIHost: upstream.URL},
			Videoseed:   config.HostTokenSource{Host: upstream.URL},
			Zetflix:     config.ZetflixSource{Host: upstream.URL},
			CDNmovies:   config.HostSource{Host: upstream.URL},
			CDNvideohub: config.HostSource{Host: upstream.URL},
			Vibix: config.VibixSource{Host: upstream.URL},
			IframeVideo: config.IframeVideoSource{APIHost: upstream.URL},
			GetsTV:      config.HostTokenSource{Host: upstream.URL},
			Mirage:      config.MirageSource{APIHost: upstream.URL},
			MoonAnime:   config.HostTokenSource{Host: upstream.URL},
			IptvOnline:  config.HostTokenSource{Host: upstream.URL},
		},
	}

	sources := []string{
		"ashdi",
		"eneyida",
		"kinogo",
		"fancdn",
		"videodb",
		"zetflix",
		"cdnmovies",
		"cdnvideohub",
		"vibix",
		"iframevideo",
		"getstv",
		"mirage",
		"moonanime",
		"iptvonline",
	}

	for _, src := range sources {
		t.Run(src, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/"+src+"?checksearch=true&title=Inception", nil)
			authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("unexpected status: %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `"type":"movie"`) {
				t.Fatalf("expected show=true payload, got: %s", body)
			}
			if !strings.Contains(body, `"rch":false`) {
				t.Fatalf("expected rch=false payload, got: %s", body)
			}
		})
	}
}

func TestVokinoChecksearchParity(t *testing.T) {
	cfg := config.Config{}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/vokino?checksearch=true&id=550&serial=0", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "data-json=") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
