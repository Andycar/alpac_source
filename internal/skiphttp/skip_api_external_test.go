package skiphttp

import (
	"net/http/httptest"
	"os"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/skipdb"
)

// End-to-end through the real handler: with nothing in our curated DB, the
// answer must come from the public aggregator — and must arrive in the legacy
// wire shape ({"segments":[{type,start,end}]}) that every existing client parses.
//
//	SKIPSRC_LIVE=1 go test ./internal/skiphttp/ -run External -v
func TestExternalFallbackServesAggregatedSegments(t *testing.T) {
	if os.Getenv("SKIPSRC_LIVE") == "" {
		t.Skip("set SKIPSRC_LIVE=1 to hit the real skip databases")
	}
	db := skipdb.New(t.TempDir(), skipdb.Defaults{}) // deliberately empty
	h := skipLookupHandler(db, config.Config{})

	// Attack on Titan S01E01 — known to Aniskip.
	req := httptest.NewRequest("GET", "/api/skip?imdb_id=tt2560140&s=1&e=1&duration=1540", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Segments []struct {
			Type  string  `json:"type"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v (%s)", err, rec.Body.String())
	}
	if len(body.Segments) == 0 {
		t.Fatal("no segments — the external fallback did not fire")
	}
	var intro bool
	for _, s := range body.Segments {
		t.Logf("%-6s %.1f–%.1f", s.Type, s.Start, s.End)
		if s.End <= s.Start {
			t.Errorf("degenerate segment %+v", s)
		}
		if s.Type == "intro" {
			intro = true
		}
	}
	if !intro {
		t.Error("expected an intro segment in the legacy vocabulary")
	}
}

// The curated DB must not be second-guessed by crowd data: when we have an
// entry, it is the answer.
func TestCuratedEntryWinsOverExternal(t *testing.T) {
	db := skipdb.New(t.TempDir(), skipdb.Defaults{})
	if err := db.Set("tt2560140", 1, 1, []skipdb.Segment{
		{Type: "intro", Start: 5, End: 15},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := skipLookupHandler(db, config.Config{})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/api/skip?imdb_id=tt2560140&s=1&e=1&duration=1540", nil))

	var body struct {
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(body.Segments) != 1 || body.Segments[0].Start != 5 || body.Segments[0].End != 15 {
		t.Fatalf("curated entry must be served verbatim, got %+v", body.Segments)
	}
}
