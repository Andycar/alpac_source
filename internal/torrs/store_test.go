//go:build torrs

package torrs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// TestStoreRoundTrip writes torrents + settings to the JSON store, reopens
// it, and verifies the data round-trips intact.
func TestStoreRoundTrip(t *testing.T) {
	home := t.TempDir()
	s, err := NewStore(home)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	want := TorrentDB{
		Hash:       "AABBCCDDEEFF00112233445566778899AABBCCDD",
		Title:      "Test",
		Poster:     "https://example.com/p.jpg",
		Data:       `{"card":"x"}`,
		Magnet:     "magnet:?xt=urn:btih:aabbccddeeff00112233445566778899aabbccdd",
		Timestamp:  1234567890,
		LastAccess: 1234567899,
		Owner:      "u-tok",
	}
	if err := s.SaveTorrent(want); err != nil {
		t.Fatalf("SaveTorrent: %v", err)
	}

	st := Settings{CacheSize: 128 << 20, PreloadSize: 8 << 20, ReaderReadAHead: 80}
	if err := s.SaveSettings(st); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and verify.
	s2, err := NewStore(home)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	got, err := s2.GetTorrent(want.Hash)
	if err != nil {
		t.Fatalf("GetTorrent: %v", err)
	}
	// Hash is normalised to lowercase by SaveTorrent.
	want.Hash = "aabbccddeeff00112233445566778899aabbccdd"
	if *got != want {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", *got, want)
	}

	if gotSt := s2.GetSettings(); gotSt != st {
		t.Errorf("settings round-trip mismatch: got %+v, want %+v", gotSt, st)
	}

	// List + Remove.
	list, _ := s2.ListTorrents()
	if len(list) != 1 {
		t.Errorf("ListTorrents = %d entries, want 1", len(list))
	}
	if err := s2.RemoveTorrent(want.Hash); err != nil {
		t.Fatalf("RemoveTorrent: %v", err)
	}
	if _, err := s2.GetTorrent(want.Hash); err == nil {
		t.Errorf("GetTorrent after Remove returned no error, want not-found")
	}
}

// TestStoreMigrationFromBBolt seeds a legacy bbolt file, opens NewStore, and
// verifies the data was carried over + the old file renamed aside.
func TestStoreMigrationFromBBolt(t *testing.T) {
	home := t.TempDir()
	legacyPath := filepath.Join(home, "torrs.db")

	// Seed the legacy bbolt DB with the bucket layout the old code used.
	db, err := bolt.Open(legacyPath, 0o600, &bolt.Options{Timeout: 0})
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		bt, _ := tx.CreateBucketIfNotExists([]byte("torrents"))
		bs, _ := tx.CreateBucketIfNotExists([]byte("settings"))

		legacy := TorrentDB{
			Hash:      "deadbeef00000000000000000000000000000000",
			Title:     "Legacy",
			Magnet:    "magnet:?xt=urn:btih:deadbeef00000000000000000000000000000000",
			Timestamp: 1,
		}
		buf, _ := json.Marshal(legacy)
		if err := bt.Put([]byte(legacy.Hash), buf); err != nil {
			return err
		}

		st := Settings{CacheSize: 99 << 20, PreloadSize: 9 << 20, ReaderReadAHead: 90}
		buf, _ = json.Marshal(st)
		return bs.Put([]byte("main"), buf)
	})
	if err != nil {
		t.Fatalf("seed bbolt: %v", err)
	}
	_ = db.Close()

	// Open the new store — should migrate.
	s, err := NewStore(home)
	if err != nil {
		t.Fatalf("NewStore (migration): %v", err)
	}
	defer s.Close()

	got, err := s.GetTorrent("deadbeef00000000000000000000000000000000")
	if err != nil {
		t.Fatalf("legacy entry not migrated: %v", err)
	}
	if got.Title != "Legacy" {
		t.Errorf("migrated title = %q, want Legacy", got.Title)
	}
	if gotSt := s.GetSettings(); gotSt.PreloadSize != 9<<20 {
		t.Errorf("migrated settings.PreloadSize = %d, want %d", gotSt.PreloadSize, 9<<20)
	}

	// Legacy file should be renamed aside, JSON file should exist.
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("legacy bbolt file still present at %s", legacyPath)
	}
	if _, err := os.Stat(legacyPath + ".bbolt-bak"); err != nil {
		t.Errorf("expected .bbolt-bak rename, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "store", "torrents.json")); err != nil {
		t.Errorf("torrents.json missing after migration: %v", err)
	}
}
