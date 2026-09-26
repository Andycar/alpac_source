package httpapi

import (
	stdjson "encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lampac-go/internal/tmdbcache"
)

func writeGroups(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "database", "tmdb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "groups.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root
}

func TestGroupStoreLoadsAndSkipsSingles(t *testing.T) {
	root := writeGroups(t, `[
	  {"id": 9000001, "title": "Монстр", "parts": [113988, 225634, 286801, 299939]},
	  {"id": 9000002, "title": "Огрызок", "parts": [1]},
	  {"id": 0, "title": "Без id", "parts": [2, 3]}
	]`)
	s := newTmdbGroupStore(root)

	g, ok := s.forPart(286801)
	if !ok {
		t.Fatal("часть антологии не нашлась по своему tmdb id")
	}
	if g.Title != "Монстр" || len(g.Parts) != 4 {
		t.Fatalf("неверная группа: %+v", g)
	}
	if _, ok := s.byCollectionID(9000001); !ok {
		t.Error("группа не нашлась по синтетическому id коллекции")
	}
	// Группа из одной части бессмысленна — ряд «все части» показывать нечему.
	if _, ok := s.forPart(1); ok {
		t.Error("группа из одной части должна отбрасываться")
	}
	// Без id коллекцию не отдать.
	if _, ok := s.forPart(2); ok {
		t.Error("группа без id должна отбрасываться")
	}
}

func TestFillGroupInjectsOnlyWhereItShould(t *testing.T) {
	root := writeGroups(t, `[{"id": 9000001, "title": "Монстр", "parts": [113988, 286801]}]`)
	h := &tmdbProxy{groups: newTmdbGroupStore(root)}

	collectionOf := func(body []byte) map[string]any {
		var doc map[string]any
		if err := stdjson.Unmarshal(body, &doc); err != nil {
			t.Fatalf("ответ не разобран: %v", err)
		}
		c, _ := doc["belongs_to_collection"].(map[string]any)
		return c
	}

	t.Run("часть антологии получает коллекцию", func(t *testing.T) {
		fr := &tmdbcache.FetchResult{Status: 200, Body: []byte(`{"id":286801,"name":"Монстр: История Эда Гина"}`)}
		h.fillGroup("3/tv/286801", fr)
		c := collectionOf(fr.Body)
		if c == nil || c["name"] != "Монстр" {
			t.Fatalf("коллекция не подставлена: %s", fr.Body)
		}
	})

	t.Run("посторонний сериал не трогаем", func(t *testing.T) {
		fr := &tmdbcache.FetchResult{Status: 200, Body: []byte(`{"id":99894,"name":"Невский"}`)}
		h.fillGroup("3/tv/99894", fr)
		if c := collectionOf(fr.Body); c != nil {
			t.Fatalf("чужой карточке подставили коллекцию: %s", fr.Body)
		}
	})

	t.Run("своя коллекция важнее нашей", func(t *testing.T) {
		fr := &tmdbcache.FetchResult{Status: 200,
			Body: []byte(`{"id":113988,"belongs_to_collection":{"id":42,"name":"Настоящая"}}`)}
		h.fillGroup("3/tv/113988", fr)
		if c := collectionOf(fr.Body); c == nil || c["name"] != "Настоящая" {
			t.Fatalf("затёрли настоящую коллекцию TMDB: %s", fr.Body)
		}
	})

	t.Run("подстраницы карточки не трогаем", func(t *testing.T) {
		// /tv/286801/videos и /season/1 — другие ответы, коллекции там быть не должно.
		for _, path := range []string{"3/tv/286801/videos", "3/tv/286801/season/1"} {
			fr := &tmdbcache.FetchResult{Status: 200, Body: []byte(`{"id":1}`)}
			h.fillGroup(path, fr)
			if c := collectionOf(fr.Body); c != nil {
				t.Errorf("%s: подставили коллекцию туда, где её быть не должно", path)
			}
		}
	})

	t.Run("неуспешный ответ не трогаем", func(t *testing.T) {
		fr := &tmdbcache.FetchResult{Status: 404, Body: []byte(`{"status_code":34}`)}
		h.fillGroup("3/tv/286801", fr)
		if c := collectionOf(fr.Body); c != nil {
			t.Error("подставили коллекцию в ошибочный ответ")
		}
	})
}

// Настоящие коллекции TMDB (id намного меньше 9 млн) должны уходить наверх, а не обслуживаться нами.
func TestServeGroupCollectionIgnoresRealCollections(t *testing.T) {
	root := writeGroups(t, `[{"id": 9000001, "title": "Монстр", "parts": [113988, 286801]}]`)
	h := &tmdbProxy{groups: newTmdbGroupStore(root)}
	for _, path := range []string{"3/collection/1241", "3/collection/86311", "3/movie/550"} {
		if _, ok := h.serveGroupCollection(t.Context(), path, ""); ok {
			t.Errorf("%s: перехватили чужой запрос", path)
		}
	}
}
