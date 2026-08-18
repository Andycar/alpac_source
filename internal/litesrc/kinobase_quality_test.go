package litesrc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// Kinobase publishes the release quality in the card's year span
// ("2021, 4K"). The badge used to be a hardcoded FHD, which understated the
// large 4K part of the catalogue.
func TestKinobaseQualityBadgeFromLabel(t *testing.T) {
	cases := map[string]string{
		"4K":       "4K",
		"2160p":    "4K",
		"UHD":      "4K",
		"BDRip":    "FHD",
		"WEB-DL":   "FHD",
		"1080p":    "FHD",
		"HDRip":    "HD",
		"720p":     "HD",
		"TS":       "SD",
		"CAMRip":   "SD",
		"":         "",
		"Лицензия": "",
	}
	for label, want := range cases {
		if got := kinobaseQualityBadge(label); got != want {
			t.Errorf("kinobaseQualityBadge(%q) = %q, want %q", label, got, want)
		}
	}
}

const kinobaseSearchCards = `<ul class="items">
<li class="x1 item"><div class="poster"><a href="/film/191009-venom-2"></a></div>
<div class="title"><a href="/film/191009-venom-2">Веном 2</a></div>
<span class="rating">7.1</span><span class="year">2021, 4K</span></li>
<li class="x1 item"><div class="poster"><a href="/film/999-other"></a></div>
<div class="title"><a href="/film/999-other">Другой фильм</a></div>
<span class="rating">6.0</span><span class="year">2021, TS</span></li>
</ul>`

func TestKinobaseChecksearchReportsRealQuality(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(kinobaseSearchCards))
	}))
	t.Cleanup(upstream.Close)

	checker := NewKinobaseChecker(config.Config{
		Online: config.OnlineConfig{Kinobase: config.KinobaseSource{Host: upstream.URL}},
	})

	show, quality := checker.checkSearchQuality(context.Background(), "Веном 2", "Venom 2", "2021")
	if !show {
		t.Fatal("source reported as unavailable")
	}
	if quality != "4K" {
		t.Fatalf("quality=%q, want 4K (card says «2021, 4K»)", quality)
	}

	// The badge must come from the MATCHED card, not the best one on the page.
	show, quality = checker.checkSearchQuality(context.Background(), "Другой фильм", "", "2021")
	if !show || quality != "SD" {
		t.Fatalf("show=%v quality=%q, want show=true quality=SD", show, quality)
	}
}

func TestKinobaseChecksearchResponseCarriesQuality(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(kinobaseSearchCards))
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Config{Online: config.OnlineConfig{Kinobase: config.KinobaseSource{Host: upstream.URL}}}
	handler := NewKinobaseChecker(cfg).Handle(cfg, nil)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet,
		"http://x/lite/kinobase?checksearch=true&title="+url.QueryEscape("Веном 2")+"&year=2021", nil))

	if body := rec.Body.String(); !strings.Contains(body, `"quality":"4K"`) {
		t.Fatalf("checksearch body = %s, want quality 4K", body)
	}
}
