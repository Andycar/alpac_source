package httpapi

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"lampac-go/internal/jsmodules"
)

// TestRadioMusicSourceLive loads the music_sources/radio module and runs
// its handle() for the default "charts" action. Hits radio-browser.info,
// verifies that we get back at least one track with a resolvable stream
// URL. Skipped with -short.
func TestRadioMusicSourceLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	// Resolve repo root from this test file's location.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	musicDir := filepath.Join(repoRoot, "music_sources")

	mgr, err := jsmodules.NewManager(musicDir, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Scan(); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	mod, ok := mgr.Get("radio")
	if !ok {
		t.Fatal("radio module not found in music_sources/")
	}
	if mod.Error != "" {
		t.Fatalf("radio module parse error: %s", mod.Error)
	}

	resp, err := mgr.Invoke(context.Background(), "radio", jsmodules.Invocation{
		Query:     map[string]string{"action": "charts"},
		Host:      "http://test",
		RequestIP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("Invoke charts: %v", err)
	}
	body := string(resp.Body)
	if !strings.Contains(body, `"tracks"`) {
		t.Fatalf("expected tracks field in response, got: %.200s", body)
	}
	if !strings.Contains(body, `"audio":"http`) {
		t.Fatalf("expected at least one track with stream URL, got: %.300s", body)
	}
	t.Logf("radio module returned %d bytes", len(body))
}
