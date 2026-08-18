package torrbalancer

import (
	"testing"

	"lampac-go/internal/config"
)

func TestSeedFromConfig(t *testing.T) {
	t.Run("url", func(t *testing.T) {
		s, err := NewStore(t.TempDir(), config.TorrServerConfig{URL: "http://1.2.3.4:666/", Login: "u", Password: "p"})
		if err != nil {
			t.Fatal(err)
		}
		got := s.Snapshot()
		if len(got) != 1 {
			t.Fatalf("want 1 seeded backend, got %d", len(got))
		}
		if got[0].Host != "http://1.2.3.4:666" {
			t.Fatalf("host = %q, want trimmed http://1.2.3.4:666", got[0].Host)
		}
		if got[0].Login != "u" || got[0].Password != "p" || !got[0].Enabled {
			t.Fatalf("seeded creds/enabled wrong: %+v", got[0])
		}
	})

	t.Run("port only", func(t *testing.T) {
		s, err := NewStore(t.TempDir(), config.TorrServerConfig{Port: 9080})
		if err != nil {
			t.Fatal(err)
		}
		got := s.Snapshot()
		if len(got) != 1 || got[0].Host != "http://127.0.0.1:9080" {
			t.Fatalf("port-only seed wrong: %+v", got)
		}
	})

	t.Run("empty config seeds nothing", func(t *testing.T) {
		s, err := NewStore(t.TempDir(), config.TorrServerConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Snapshot()) != 0 {
			t.Fatalf("want no seed, got %d", len(s.Snapshot()))
		}
	})
}

func TestStoreCRUDAndPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir, config.TorrServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Add("node-a", "10.0.0.1:9080", "ts", "secret", 2, true, "primary")
	if err != nil {
		t.Fatal(err)
	}
	if b.Host != "http://10.0.0.1:9080" {
		t.Fatalf("host not normalized: %q", b.Host)
	}
	if _, err := s.Add("dup", "http://10.0.0.1:9080", "", "", 1, true, ""); err == nil {
		t.Fatal("expected duplicate-host error")
	}

	w := 5
	en := false
	if _, err := s.Update(b.ID, UpdatePatch{Weight: &w, Enabled: &en}); err != nil {
		t.Fatal(err)
	}
	if f, ok := s.Find(b.ID); !ok || f.Weight != 5 || f.Enabled {
		t.Fatalf("update not reflected: %+v ok=%v", f, ok)
	}

	// Reload from disk → persisted state survives.
	s2, err := NewStore(dir, config.TorrServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Snapshot(); len(got) != 1 || got[0].Weight != 5 || got[0].Password != "secret" {
		t.Fatalf("persisted state wrong: %+v", got)
	}

	if err := s.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot()) != 0 {
		t.Fatalf("delete failed, %d remain", len(s.Snapshot()))
	}
}
