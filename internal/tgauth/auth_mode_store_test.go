package tgauth

import "testing"

func TestAuthModeStoreFallback(t *testing.T) {
	s := NewAuthModeStore(t.TempDir(), "tg")
	if got := s.Get(); got != "tg" {
		t.Fatalf("Get=%q want tg", got)
	}
}

func TestAuthModeStoreSetPersists(t *testing.T) {
	dir := t.TempDir()
	s := NewAuthModeStore(dir, "tg")
	if err := s.Set("password"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := s.Get(); got != "password" {
		t.Fatalf("Get after Set=%q want password", got)
	}
	// Reload — file should restore mode.
	s2 := NewAuthModeStore(dir, "tg")
	if got := s2.Get(); got != "password" {
		t.Fatalf("Get after reload=%q want password", got)
	}
}

func TestAuthModeStoreSetInvalid(t *testing.T) {
	s := NewAuthModeStore(t.TempDir(), "tg")
	if err := s.Set("bogus"); err != ErrInvalidAuthMode {
		t.Fatalf("Set(bogus) want ErrInvalidAuthMode, got %v", err)
	}
	if got := s.Get(); got != "tg" {
		t.Fatalf("mode mutated by invalid set: %q", got)
	}
}

func TestAuthModeStoreEmptyResetsToFallback(t *testing.T) {
	dir := t.TempDir()
	s := NewAuthModeStore(dir, "tg")
	_ = s.Set("none")
	if got := s.Get(); got != "none" {
		t.Fatalf("Get=%q want none", got)
	}
	if err := s.Set(""); err != nil {
		t.Fatalf("Set empty: %v", err)
	}
	if got := s.Get(); got != "tg" {
		t.Fatalf("Get after empty=%q want fallback tg", got)
	}
	// Subsequent reload should also fall back.
	s2 := NewAuthModeStore(dir, "tg")
	if got := s2.Get(); got != "tg" {
		t.Fatalf("Get after empty + reload=%q want tg", got)
	}
}

func TestAuthModeStoreSetFallback(t *testing.T) {
	// SetFallback only affects the value Get returns when no override is
	// stored. With an override in place (s.mode != "") SetFallback is
	// inert — that's the whole point of the override.
	s := NewAuthModeStore(t.TempDir(), "tg")
	_ = s.Set("") // clear any override
	s.SetFallback("none")
	if got := s.Get(); got != "none" {
		t.Fatalf("Get after SetFallback with empty override=%q want none", got)
	}

	// Setting an explicit mode pins the value regardless of fallback.
	_ = s.Set("password")
	s.SetFallback("tg")
	if got := s.Get(); got != "password" {
		t.Fatalf("explicit override should beat fallback, got %q", got)
	}
}
