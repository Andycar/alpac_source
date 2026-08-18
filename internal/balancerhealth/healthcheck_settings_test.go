package balancerhealth

import (
	stdjson "encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHealthCheckerApplySettings clamps out-of-range values and updates the
// runtime tunables. The snapshot must round-trip what the caller actually
// gets applied (post-clamp), not the raw input.
func TestHealthCheckerApplySettings(t *testing.T) {
	hc := NewHealthChecker()

	// All in-range: applied verbatim.
	hc.ApplySettings(HealthSettings{
		Enabled:          true,
		IntervalSec:      30,
		TimeoutSec:       7,
		FailThreshold:    4,
		RecoverThreshold: 1,
	})
	got := hc.Settings()
	if got.IntervalSec != 30 || got.TimeoutSec != 7 || got.FailThreshold != 4 || got.RecoverThreshold != 1 || !got.Enabled {
		t.Fatalf("in-range round-trip failed: %+v", got)
	}

	// Out-of-range: clamped silently.
	hc.ApplySettings(HealthSettings{
		Enabled:          false,
		IntervalSec:      99999, // → 3600
		TimeoutSec:       0,     // → 1
		FailThreshold:    -5,    // → 1
		RecoverThreshold: 9999,  // → 50
	})
	got = hc.Settings()
	if got.Enabled {
		t.Errorf("Enabled should be false; got true")
	}
	if got.IntervalSec != 3600 {
		t.Errorf("IntervalSec not clamped to 3600: %d", got.IntervalSec)
	}
	if got.TimeoutSec != 1 {
		t.Errorf("TimeoutSec not clamped to 1: %d", got.TimeoutSec)
	}
	if got.FailThreshold != 1 {
		t.Errorf("FailThreshold not clamped to 1: %d", got.FailThreshold)
	}
	if got.RecoverThreshold != 50 {
		t.Errorf("RecoverThreshold not clamped to 50: %d", got.RecoverThreshold)
	}
}

// TestHealthCheckerResetStatus exercises both single-balancer and bulk
// reset paths.
func TestHealthCheckerResetStatus(t *testing.T) {
	hc := NewHealthChecker()
	hc.mu.Lock()
	hc.statuses["foo"] = &HealthStatus{Healthy: false, AutoDisabled: true, ConsecutiveFails: 5}
	hc.statuses["bar"] = &HealthStatus{Healthy: true, ConsecutiveOK: 3}
	hc.mu.Unlock()

	if affected := hc.ResetStatus("Foo"); affected != 1 {
		t.Fatalf("expected single reset, got %d", affected)
	}
	if _, ok := hc.Snapshot()["foo"]; ok {
		t.Fatalf("foo should be wiped after reset")
	}
	if _, ok := hc.Snapshot()["bar"]; !ok {
		t.Fatalf("bar should still exist after targeted reset")
	}

	if affected := hc.ResetStatus(""); affected != 1 {
		t.Fatalf("expected bulk reset to wipe 1, got %d", affected)
	}
	if got := hc.Snapshot(); len(got) != 0 {
		t.Fatalf("bulk reset should clear all, got %d entries", len(got))
	}

	// Reset on unknown name reports 0 affected and doesn't blow up.
	if affected := hc.ResetStatus("nothing"); affected != 0 {
		t.Fatalf("unknown reset should report 0 affected, got %d", affected)
	}
}

// TestHealthCheckerManualReEnable clears AutoDisabled and resets streak
// counters without removing the entry — the operator wants the next probe
// to record into the same row.
func TestHealthCheckerManualReEnable(t *testing.T) {
	hc := NewHealthChecker()
	hc.mu.Lock()
	hc.statuses["target"] = &HealthStatus{
		Healthy:          false,
		AutoDisabled:     true,
		ConsecutiveFails: 7,
		LastCheckError:   "boom",
		LastCheckTime:    time.Now(),
	}
	hc.mu.Unlock()

	if !hc.ManualReEnable("Target") {
		t.Fatal("ManualReEnable should report success when entry exists")
	}
	s := hc.Snapshot()["target"]
	if s.AutoDisabled || s.ConsecutiveFails != 0 || s.LastCheckError != "" {
		t.Fatalf("ManualReEnable did not clear flags: %+v", s)
	}
	// LastCheckTime should be preserved so the table still shows when the
	// last attempt happened — only the failure-related fields reset.
	if s.LastCheckTime.IsZero() {
		t.Fatalf("LastCheckTime should be preserved")
	}

	// No-op on unknown / empty name.
	if hc.ManualReEnable("nothing") {
		t.Error("ManualReEnable should return false for unknown name")
	}
	if hc.ManualReEnable("") {
		t.Error("ManualReEnable should return false for empty name")
	}
}

// TestHealthCheckerExcluded covers the per-balancer opt-out: SetExcluded
// flips, IsExcluded reads it back, ApplySettings normalises duplicates +
// whitespace, and Settings() returns a stable sorted slice.
func TestHealthCheckerExcluded(t *testing.T) {
	hc := NewHealthChecker()

	// Default: no exclusions.
	if hc.IsExcluded("kinotochka") {
		t.Fatal("fresh checker should have no exclusions")
	}
	if s := hc.Settings(); len(s.Excluded) != 0 {
		t.Fatalf("fresh Settings().Excluded should be empty, got %v", s.Excluded)
	}

	// SetExcluded round-trip — case-insensitive.
	hc.SetExcluded("KinoTochka", true)
	if !hc.IsExcluded("kinotochka") {
		t.Fatal("SetExcluded(true) should be reflected by IsExcluded")
	}
	if !hc.IsExcluded("KINOTOCHKA") {
		t.Fatal("IsExcluded should be case-insensitive")
	}

	// Toggle off.
	hc.SetExcluded("kinotochka", false)
	if hc.IsExcluded("kinotochka") {
		t.Fatal("SetExcluded(false) should clear the flag")
	}

	// Empty/whitespace balancer name is a no-op.
	if hc.SetExcluded("   ", true) {
		t.Fatal("blank balancer should not produce an exclusion")
	}

	// ApplySettings normalisation: lowercases, trims, dedupes.
	hc.ApplySettings(HealthSettings{
		Enabled:          true,
		IntervalSec:      60,
		TimeoutSec:       10,
		FailThreshold:    3,
		RecoverThreshold: 2,
		Excluded:         []string{"FILMIX", "  collaps  ", "filmix", "", "   "},
	})
	out := hc.Settings().Excluded
	if len(out) != 2 {
		t.Fatalf("expected 2 deduped entries, got %d: %v", len(out), out)
	}
	if out[0] != "collaps" || out[1] != "filmix" {
		t.Fatalf("expected sorted lowercase ['collaps','filmix'], got %v", out)
	}
	if !hc.IsExcluded("filmix") || !hc.IsExcluded("collaps") {
		t.Fatalf("normalised entries not recognised by IsExcluded")
	}
}

// TestHealthcheckSettingsOverrideRoundTrip verifies the admin-side override
// file is read and parsed correctly when present, and silently ignored
// when missing/corrupt — that's the contract the boot path depends on.
func TestHealthcheckSettingsOverrideRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Missing file → not ok, zero value.
	if _, ok := LoadHealthcheckSettingsOverride(dir); ok {
		t.Fatal("missing file should return ok=false")
	}

	// Write a valid override including a non-empty Excluded list.
	want := HealthSettings{
		Enabled:          false,
		IntervalSec:      120,
		TimeoutSec:       15,
		FailThreshold:    5,
		RecoverThreshold: 3,
		Excluded:         []string{"filmix", "kinotochka"},
	}
	if err := SaveHealthcheckSettingsOverride(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := LoadHealthcheckSettingsOverride(dir)
	if !ok {
		t.Fatal("expected ok=true after save")
	}
	// Slice field can't be compared with ==; check scalar fields and Excluded separately.
	if got.Enabled != want.Enabled || got.IntervalSec != want.IntervalSec ||
		got.TimeoutSec != want.TimeoutSec || got.FailThreshold != want.FailThreshold ||
		got.RecoverThreshold != want.RecoverThreshold {
		t.Fatalf("scalar round-trip mismatch:\n  got  %+v\n  want %+v", got, want)
	}
	if len(got.Excluded) != len(want.Excluded) {
		t.Fatalf("Excluded length mismatch: got %v want %v", got.Excluded, want.Excluded)
	}
	for i := range got.Excluded {
		if got.Excluded[i] != want.Excluded[i] {
			t.Fatalf("Excluded[%d] mismatch: got %q want %q", i, got.Excluded[i], want.Excluded[i])
		}
	}

	// Corrupt file → returns ok=false (logged warn, doesn't panic).
	path := filepath.Join(dir, healthcheckSettingsFile)
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	if _, ok := LoadHealthcheckSettingsOverride(dir); ok {
		t.Fatal("corrupt file should return ok=false")
	}

	// Make sure the file we wrote is JSON readable by a vanilla unmarshal —
	// this guards against accidental schema drift between the writer here
	// and the reader in LoadHealthcheckSettingsOverride.
	if err := SaveHealthcheckSettingsOverride(dir, want); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed HealthSettings
	if err := stdjson.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal saved override: %v", err)
	}
}
