package iptv

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// When the playlist's tvg-ids don't match the EPG source's channel ids, the per-known-channel
// filter strands every programme. The engine must self-heal by reloading unfiltered so EPG isn't
// blank on every channel («теперь вообще нет EPG»).
func TestEPGEngine_SelfHealUnfiltered(t *testing.T) {
	now := time.Now().UTC()
	fmtT := func(tm time.Time) string { return tm.Format("20060102150405 -0700") }
	xml := `<?xml version="1.0" encoding="UTF-8"?><tv>
  <channel id="real.tv"><display-name>Real TV</display-name></channel>
  <programme start="` + fmtT(now.Add(-1*time.Hour)) + `" stop="` + fmtT(now.Add(1*time.Hour)) + `" channel="real.tv"><title>On Air</title></programme>
  <programme start="` + fmtT(now.Add(1*time.Hour)) + `" stop="` + fmtT(now.Add(2*time.Hour)) + `" channel="real.tv"><title>Next</title></programme>
</tv>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	defer srv.Close()

	// Playlist carries a tvg-id that does NOT match the source's channel id ("real.tv").
	store := &Store{cache: map[string]*PlaylistCache{
		"global": {Channels: []Channel{{ID: "c1", TvgID: "mismatch.tv"}}},
	}}
	engine := NewEPGEngine(store, EPGConfig{URLs: []string{srv.URL}})
	engine.refresh()

	st := engine.Status()
	if !st.SelfHealed {
		t.Fatalf("expected self-heal to trigger (filtered load stranded everything), status=%+v", st)
	}
	if st.Programs == 0 {
		t.Fatalf("expected programmes after self-heal reload, got 0")
	}
	if got := engine.Timeline("real.tv", now.Add(-2*time.Hour), now.Add(3*time.Hour)); len(got) == 0 {
		t.Fatalf("expected real.tv timeline non-empty after self-heal")
	}
}

// When the playlist tvg-ids DO match, the filter stays on (no self-heal) and programmes load.
func TestEPGEngine_FilterMatchNoSelfHeal(t *testing.T) {
	now := time.Now().UTC()
	fmtT := func(tm time.Time) string { return tm.Format("20060102150405 -0700") }
	xml := `<?xml version="1.0" encoding="UTF-8"?><tv>
  <channel id="match.tv"><display-name>Match TV</display-name></channel>
  <programme start="` + fmtT(now.Add(-1*time.Hour)) + `" stop="` + fmtT(now.Add(1*time.Hour)) + `" channel="match.tv"><title>On Air</title></programme>
</tv>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	defer srv.Close()

	store := &Store{cache: map[string]*PlaylistCache{
		"global": {Channels: []Channel{{ID: "c1", TvgID: "match.tv"}}},
	}}
	engine := NewEPGEngine(store, EPGConfig{URLs: []string{srv.URL}})
	engine.refresh()

	st := engine.Status()
	if st.SelfHealed {
		t.Fatalf("did not expect self-heal when tvg-id matches, status=%+v", st)
	}
	if !st.Filtered {
		t.Fatalf("expected filtered load when known tvg-ids present, status=%+v", st)
	}
	if st.Programs == 0 || st.MatchedTvgIDs == 0 {
		t.Fatalf("expected matched programmes, status=%+v", st)
	}
}
