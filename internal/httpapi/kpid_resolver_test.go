package httpapi

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTmdbKey(t *testing.T) {
	if got := tmdbKey("movie", 302401); got != "movie:302401" {
		t.Errorf("movie key = %q", got)
	}
	if got := tmdbKey("tv", 1399); got != "tv:1399" {
		t.Errorf("tv key = %q", got)
	}
	// movie and tv with the same numeric id must NOT collide
	if tmdbKey("movie", 100) == tmdbKey("tv", 100) {
		t.Error("movie/tv keys collide")
	}
}

func TestSparqlEsc(t *testing.T) {
	if got := sparqlEsc(`a"b\c`); got != `a\"b\\c` {
		t.Errorf("sparqlEsc = %q", got)
	}
}

func newTestStore(path string) *kpIDStore {
	return &kpIDStore{
		path:   path,
		byTmdb: map[string]int{},
		byImdb: map[string]int{},
		neg:    map[string]time.Time{},
	}
}

func TestKpIDStoreGetPut(t *testing.T) {
	s := newTestStore(filepath.Join(t.TempDir(), "kp.json"))
	if _, ok := s.get("movie", 302401, "tt3774114"); ok {
		t.Fatal("empty store returned a hit")
	}
	s.put("movie", 302401, "tt3774114", 843831)
	// hit by tmdb
	if kp, ok := s.get("movie", 302401, ""); !ok || kp != 843831 {
		t.Errorf("by tmdb = %d,%v", kp, ok)
	}
	// hit by imdb (different tmdb id supplied)
	if kp, ok := s.get("movie", 0, "tt3774114"); !ok || kp != 843831 {
		t.Errorf("by imdb = %d,%v", kp, ok)
	}
	// wrong type must miss on the tmdb key (no imdb given)
	if _, ok := s.get("tv", 302401, ""); ok {
		t.Error("tv type matched a movie tmdb id")
	}
}

func TestKpIDStorePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kp.json")
	s := newTestStore(path)
	s.put("movie", 302401, "tt3774114", 843831)
	s.put("tv", 1399, "", 464963)
	s.flush()

	// reload from disk
	s2 := newTestStore(path)
	s2.load()
	if kp, ok := s2.get("movie", 302401, ""); !ok || kp != 843831 {
		t.Errorf("reloaded movie = %d,%v", kp, ok)
	}
	if kp, ok := s2.get("tv", 1399, ""); !ok || kp != 464963 {
		t.Errorf("reloaded tv = %d,%v", kp, ok)
	}
}

func TestKpIDStoreNegCache(t *testing.T) {
	s := newTestStore(filepath.Join(t.TempDir(), "kp.json"))
	const k = "movie:999|"
	if s.negCached(k) {
		t.Fatal("unset neg reported cached")
	}
	s.markNeg(k)
	if !s.negCached(k) {
		t.Error("marked neg not cached")
	}
	// expired
	s.neg[k] = time.Now().Add(-2 * kpStoreNegTTL)
	if s.negCached(k) {
		t.Error("expired neg still cached")
	}
}
