package httpapi

import (
	"net/http/httptest"
	"testing"
)

// When every upstream fails we still answer 200 with a card-shaped body. Lampa renders
// that body without checking for failure, and its helpers call .join/.length on the
// array fields — a missing production_countries crashed the whole app with
// "countries.join is not a function". Guard the shape.
func TestEmptyDetailCardFallbackHasArrayFields(t *testing.T) {
	h := &tmdbProxy{}
	r := httptest.NewRequest("GET", "/tmdb/api/3/movie/123?append_to_response=translations,credits,videos", nil)
	w := httptest.NewRecorder()

	h.writeEmptyFallback(w, r)

	var obj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatalf("fallback body is not JSON: %v", err)
	}

	for _, field := range []string{"genres", "production_countries", "production_companies", "spoken_languages", "seasons", "episode_run_time"} {
		if _, ok := obj[field].([]any); !ok {
			t.Errorf("field %q = %#v, want [] (Lampa calls .join/.length on it)", field, obj[field])
		}
	}

	// append_to_response sub-objects must survive alongside the card fields.
	tr, ok := obj["translations"].(map[string]any)
	if !ok || tr["translations"] == nil {
		t.Errorf("translations = %#v, want {translations: []}", obj["translations"])
	}
}
