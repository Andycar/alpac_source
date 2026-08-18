package iptv

import "testing"

// TestSetGlobalPlaylistsAppliesAtRuntime reproduces the "IPTV не настроен despite
// global_playlists being set" bug: the store is built once at startup, so a
// config-reload must push new global playlists in via SetGlobalPlaylists for them
// to appear in ListPlaylists without a full restart.
//
// A connection-refused URL (port 1) is used so RefreshGlobal fails fast without
// touching the network — ListPlaylists (mergeGlobal=false) synthesizes one entry
// per global URL regardless of fetch success.
func TestSetGlobalPlaylistsAppliesAtRuntime(t *testing.T) {
	s := NewStore(t.TempDir(), StoreConfig{GlobalPlaylists: nil, MergeGlobal: false})

	if got := s.ListPlaylists(0); len(got) != 0 {
		t.Fatalf("expected 0 playlists before config, got %d", len(got))
	}

	// Simulate an admin adding [iptv] global_playlists and reloading.
	s.SetGlobalPlaylists([]string{"http://127.0.0.1:1/a.m3u"}, false)
	got := s.ListPlaylists(0)
	if len(got) != 1 || !got[0].IsGlobal {
		t.Fatalf("expected 1 global playlist after reload, got %+v", got)
	}

	// Replacing the list prunes the old URL and applies the new one.
	s.SetGlobalPlaylists([]string{"http://127.0.0.1:1/b.m3u", "http://127.0.0.1:1/c.m3u"}, false)
	if got := s.ListPlaylists(0); len(got) != 2 {
		t.Fatalf("expected 2 global playlists after replace, got %d", len(got))
	}

	// Clearing global_playlists removes them — back to "not configured".
	s.SetGlobalPlaylists(nil, false)
	if got := s.ListPlaylists(0); len(got) != 0 {
		t.Fatalf("expected 0 playlists after clear, got %d", len(got))
	}
}

// TestSetGlobalPlaylistsMergeMode verifies the merged "Все каналы" entry appears
// once when merge_global is on and any global URL is configured.
func TestSetGlobalPlaylistsMergeMode(t *testing.T) {
	s := NewStore(t.TempDir(), StoreConfig{GlobalPlaylists: nil, MergeGlobal: false})
	s.SetGlobalPlaylists([]string{"http://127.0.0.1:1/a.m3u", "http://127.0.0.1:1/b.m3u"}, true)
	got := s.ListPlaylists(0)
	if len(got) != 1 || got[0].ID != MergedGlobalID {
		t.Fatalf("expected single merged playlist, got %+v", got)
	}
}
