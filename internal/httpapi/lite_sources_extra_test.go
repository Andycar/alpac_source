package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"lampac-go/internal/config"
)

func TestLiteMainNodeOnly(t *testing.T) {
	local := []string{
		"youtube", "youtube/img", "youtube/dash.mpd", "youtube/channel",
		"youtube/feed/subscriptions", "trailer", "trailer/foo",
	}
	for _, raw := range local {
		if !liteMainNodeOnly(raw) {
			t.Errorf("liteMainNodeOnly(%q) = false, want true (must stay on main node)", raw)
		}
	}
	forwardable := []string{
		"filmix", "filmix/play", "kinopub", "youtubex", "trailers", "collaps", "",
	}
	for _, raw := range forwardable {
		if liteMainNodeOnly(raw) {
			t.Errorf("liteMainNodeOnly(%q) = true, want false (must remain forwardable)", raw)
		}
	}
}

func TestLiteWithSearchEndpoint(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{WithSearch: []string{"kinobase", "filmix", "lumex"}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/withsearch", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var got []string
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("unexpected list length: %d", len(got))
	}
}
