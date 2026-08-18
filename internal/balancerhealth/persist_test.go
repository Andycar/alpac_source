package balancerhealth

import (
	stdjson "encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAtomicGlobalHealthCheckerHelpers(t *testing.T) {
	prev := GetGlobalHealthChecker()
	t.Cleanup(func() { SetGlobalHealthChecker(prev) })

	hc := NewHealthChecker()
	SetGlobalHealthChecker(hc)
	if GetGlobalHealthChecker() != hc {
		t.Fatal("SetGlobalHealthChecker/Get round-trip failed")
	}
	SetGlobalHealthChecker(nil)
	if GetGlobalHealthChecker() != nil {
		t.Fatal("clearing global health checker should yield nil")
	}
}

// TestHealthCheckerPersistRoundTrip confirms LoadFromDisk hydrates the
// statuses map from a JSON file produced by saveToDisk, including the
// AutoDisabled flag — the whole point of the persistence layer.
func TestHealthCheckerPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// Pre-populate the persistence file.
	want := map[string]HealthStatus{
		"baddybalancer": {
			Healthy:          false,
			AutoDisabled:     true,
			ConsecutiveFails: 7,
			LastCheckTime:    time.Now().UTC().Truncate(time.Second),
		},
		"healthy": {Healthy: true},
	}
	data, err := stdjson.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, "database", "healthcheck.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	hc := NewHealthChecker()
	hc.LoadFromDisk(dir)

	got := hc.Snapshot()
	if !got["baddybalancer"].AutoDisabled {
		t.Fatalf("expected baddybalancer auto-disabled, got %+v", got["baddybalancer"])
	}
	if got["baddybalancer"].ConsecutiveFails != 7 {
		t.Fatalf("ConsecutiveFails not restored: %d", got["baddybalancer"].ConsecutiveFails)
	}

	// Now mutate and saveToDisk; check the file content survives a fresh load.
	hc.mu.Lock()
	hc.statuses["baddybalancer"].ConsecutiveFails = 99
	hc.mu.Unlock()
	hc.saveToDisk()

	hc2 := NewHealthChecker()
	hc2.LoadFromDisk(dir)
	if hc2.Snapshot()["baddybalancer"].ConsecutiveFails != 99 {
		t.Fatalf("saveToDisk round-trip failed; got %d", hc2.Snapshot()["baddybalancer"].ConsecutiveFails)
	}
}
