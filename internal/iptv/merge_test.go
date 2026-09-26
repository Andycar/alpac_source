package iptv

import "testing"

func TestNormChannelName(t *testing.T) {
	cases := map[string]string{
		"Первый канал HD":  "первый канал",
		"Первый Канал":     "первый канал",
		"Первый канал FHD": "первый канал",
		"СТС 🇷🇺":           "стс",
		"ТНТ (1080p)":      "тнт",
		"Кинопоказ 4K UHD": "кинопоказ",
	}
	for in, want := range cases {
		if got := normChannelName(in); got != want {
			t.Errorf("normChannelName(%q) = %q, want %q", in, got, want)
		}
	}
}

func newMergeStore(global ...string) *Store {
	return &Store{
		globalURLs:  global,
		mergeGlobal: true,
		cache:       make(map[string]*PlaylistCache),
		health:      &healthState{dead: make(map[string]struct{})},
	}
}

// TestMergedLocked_Memoized pins the perf-audit fix: mergedLocked() memoizes its
// result (no rebuild per call) within the TTL, and invalidateMerged() forces a
// rebuild that reflects underlying changes.
func TestMergedLocked_Memoized(t *testing.T) {
	uA := "http://a/list.m3u"
	s := newMergeStore(uA)
	key := "global_" + playlistIDFromURL(uA)
	s.cache[key] = &PlaylistCache{Channels: []Channel{
		{ID: "1", Name: "Первый", URL: "http://a/1"},
	}}

	s.mu.RLock()
	a := s.mergedLocked()
	s.mu.RUnlock()
	if len(a) != 1 {
		t.Fatalf("first build: len=%d want 1", len(a))
	}

	// Mutate the underlying cache WITHOUT invalidating — the memoized result must
	// NOT rebuild (same backing array, still len 1).
	s.cache[key].Channels = append(s.cache[key].Channels, Channel{ID: "2", Name: "СТС", URL: "http://a/2"})
	s.mu.RLock()
	b := s.mergedLocked()
	s.mu.RUnlock()
	if len(b) != 1 || &a[0] != &b[0] {
		t.Fatalf("expected memoized slice (len=1, same array), got len=%d shared=%v", len(b), len(b) > 0 && &a[0] == &b[0])
	}

	// After invalidation the next call rebuilds and reflects the mutation.
	s.invalidateMerged()
	s.mu.RLock()
	c := s.mergedLocked()
	s.mu.RUnlock()
	if len(c) != 2 {
		t.Fatalf("after invalidate: len=%d want 2 (rebuild should see the added channel)", len(c))
	}
}

func TestMergedLocked_DedupAcrossSources(t *testing.T) {
	uA, uB := "http://a/list.m3u", "http://b/list.m3u"
	s := newMergeStore(uA, uB)
	s.cache["global_"+playlistIDFromURL(uA)] = &PlaylistCache{Channels: []Channel{
		{ID: "1", Name: "Первый канал HD", URL: "http://a/1", Quality: "HD"},
		{ID: "2", Name: "СТС", URL: "http://a/sts"},
	}}
	s.cache["global_"+playlistIDFromURL(uB)] = &PlaylistCache{Channels: []Channel{
		{ID: "3", Name: "Первый Канал FHD", URL: "http://b/1", Quality: "FHD"}, // dup of #1, higher quality
		{ID: "4", Name: "ТНТ", URL: "http://b/tnt"},
	}}

	s.mu.RLock()
	merged := s.mergedLocked()
	s.mu.RUnlock()

	if len(merged) != 3 {
		t.Fatalf("expected 3 deduped channels, got %d: %+v", len(merged), names(merged))
	}
	// the kept "Первый" entry must be the higher-quality (FHD) one
	for _, ch := range merged {
		if normChannelName(ch.Name) == "первый канал" && ch.Quality != "FHD" {
			t.Errorf("dedup kept %q (%s), expected the FHD duplicate", ch.Name, ch.Quality)
		}
	}
}

func TestMergedLocked_DropsDeadPrefersLiveDuplicate(t *testing.T) {
	uA, uB := "http://a/list.m3u", "http://b/list.m3u"
	s := newMergeStore(uA, uB)
	s.healthOn = true
	s.cache["global_"+playlistIDFromURL(uA)] = &PlaylistCache{Channels: []Channel{
		{ID: "1", Name: "Первый канал FHD", URL: "http://a/1", Quality: "FHD"}, // higher quality but DEAD
	}}
	s.cache["global_"+playlistIDFromURL(uB)] = &PlaylistCache{Channels: []Channel{
		{ID: "2", Name: "Первый Канал HD", URL: "http://b/1", Quality: "HD"}, // live
	}}
	s.health.dead["http://a/1"] = struct{}{} // A's higher-quality stream is down

	s.mu.RLock()
	merged := s.mergedLocked()
	s.mu.RUnlock()

	if len(merged) != 1 {
		t.Fatalf("expected 1 channel (live duplicate), got %d", len(merged))
	}
	if merged[0].URL != "http://b/1" {
		t.Errorf("expected the LIVE duplicate (B), got %q", merged[0].URL)
	}
}

func names(chs []Channel) []string {
	out := make([]string, len(chs))
	for i, c := range chs {
		out[i] = c.Name
	}
	return out
}
