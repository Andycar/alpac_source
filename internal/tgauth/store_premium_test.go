package tgauth

import (
	"testing"
	"time"
)

// Tests for the premium-tier overlay timer that's independent from the
// base token expiry. The user's complaint was that paying for premium
// was adding days to ExpiresAt (the 365-day base timer) instead of
// tracking premium separately. These tests pin the new model.

func newPremiumTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s := NewStore(dir)
	tok := "tok-premium-test"
	err := s.Add(ApprovedToken{
		Token:      tok,
		TelegramID: 100,
		CreatedAt:  time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(365 * 24 * time.Hour), // typical /start auto-approve
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return s, tok
}

// TestExtendPremium_StartsFromNowWhenNotActive: user not currently in
// premium → buying 90 days sets PremiumUntil = now + 90d, regardless of
// any past PremiumUntil that might have already expired.
func TestExtendPremium_StartsFromNowWhenNotActive(t *testing.T) {
	s, tok := newPremiumTestStore(t)

	before := time.Now().UTC()
	until := s.ExtendPremium(tok, 90)
	want := before.Add(90 * 24 * time.Hour)
	if until.Before(want.Add(-time.Minute)) || until.After(want.Add(time.Minute)) {
		t.Errorf("ExtendPremium(90) → %v, want ≈ %v", until, want)
	}
	// ExpiresAt (base subscription) must NOT have moved — that was
	// the original bug.
	got := s.FindByTelegramID(100).ExpiresAt
	delta := got.Sub(before)
	if delta < 364*24*time.Hour || delta > 366*24*time.Hour {
		t.Errorf("ExpiresAt changed by ExtendPremium! delta=%v (must remain ~365 days)", delta)
	}
}

// TestExtendPremium_StacksOnActivePremium: user has 30 days of premium
// left, buys another 90 → premium ends in 120 days, not 90 (stacking
// like a magazine subscription).
func TestExtendPremium_StacksOnActivePremium(t *testing.T) {
	s, tok := newPremiumTestStore(t)
	// Pre-fill premium with 30 days remaining.
	first := s.ExtendPremium(tok, 30)
	if first.IsZero() {
		t.Fatal("first ExtendPremium returned zero time")
	}

	second := s.ExtendPremium(tok, 90)
	expected := first.Add(90 * 24 * time.Hour)
	if second.Before(expected.Add(-time.Minute)) || second.After(expected.Add(time.Minute)) {
		t.Errorf("stacked ExtendPremium → %v, want ≈ %v (first %v + 90d)",
			second, expected, first)
	}
}

// TestExtendPremium_StartsFromNowWhenExpired: stale PremiumUntil in the
// past doesn't poison a new purchase. Buying after lapse → counts from
// today, not from the old expiry.
func TestExtendPremium_StartsFromNowWhenExpired(t *testing.T) {
	s, tok := newPremiumTestStore(t)
	// Simulate an old expired premium.
	s.SetPremiumUntil(tok, time.Now().UTC().Add(-60*24*time.Hour))

	before := time.Now().UTC()
	until := s.ExtendPremium(tok, 30)
	want := before.Add(30 * 24 * time.Hour)
	if until.Before(want.Add(-time.Minute)) || until.After(want.Add(time.Minute)) {
		t.Errorf("ExtendPremium after lapse → %v, want ≈ %v (must NOT compound on the past)", until, want)
	}
}

// TestGetEffectiveGroup_PremiumOverlay tests the lazy-downgrade
// semantics: PremiumUntil > now → "premium"; else fall back to GroupID.
func TestGetEffectiveGroup_PremiumOverlay(t *testing.T) {
	s, tok := newPremiumTestStore(t)

	// Base case: no premium → effective is GroupID (empty = default)
	if got := s.GetEffectiveGroup(tok, "premium"); got != "" {
		t.Errorf("no premium: got %q, want \"\" (= default group)", got)
	}

	// Buy premium → effective is "premium"
	s.ExtendPremium(tok, 90)
	if got := s.GetEffectiveGroup(tok, "premium"); got != "premium" {
		t.Errorf("active premium: got %q, want \"premium\"", got)
	}

	// User is in "vip" base group, buys premium on top — premium overlay wins.
	s.SetGroupID(tok, "vip")
	if got := s.GetEffectiveGroup(tok, "premium"); got != "premium" {
		t.Errorf("vip + active premium: got %q, want \"premium\" (overlay)", got)
	}

	// Premium expires → falls back to base GroupID (vip)
	s.SetPremiumUntil(tok, time.Now().UTC().Add(-1*time.Hour))
	if got := s.GetEffectiveGroup(tok, "premium"); got != "vip" {
		t.Errorf("expired premium with vip base: got %q, want \"vip\"", got)
	}
}

// TestGetEffectiveGroup_LegacyGrandfathering: tokens written before
// PremiumUntil existed have GroupID="premium" but no PremiumUntil.
// They must keep behaving as premium until manual cleanup — never
// silently downgrade existing paying users.
func TestGetEffectiveGroup_LegacyGrandfathering(t *testing.T) {
	s, tok := newPremiumTestStore(t)
	s.SetGroupID(tok, "premium") // simulate legacy state
	// PremiumUntil is zero (default).

	got := s.GetEffectiveGroup(tok, "premium")
	if got != "premium" {
		t.Errorf("legacy GroupID=premium token: got %q, want \"premium\" (grandfathered)", got)
	}
}

// TestSetPremiumUntil_ClearsWithZero: passing zero time clears the
// premium overlay (user immediately drops back to base group).
func TestSetPremiumUntil_ClearsWithZero(t *testing.T) {
	s, tok := newPremiumTestStore(t)
	s.ExtendPremium(tok, 90)
	if got := s.GetEffectiveGroup(tok, "premium"); got != "premium" {
		t.Fatal("setup: should be in premium")
	}

	s.SetPremiumUntil(tok, time.Time{}) // zero
	if got := s.GetEffectiveGroup(tok, "premium"); got != "" {
		t.Errorf("after clearing PremiumUntil: got %q, want \"\" (base group)", got)
	}
}

// TestExtendPremium_DoesNotChangeBaseExpiry is the regression for the
// original user complaint. The 365-day base timer must remain
// untouched no matter how much premium the user buys.
func TestExtendPremium_DoesNotChangeBaseExpiry(t *testing.T) {
	s, tok := newPremiumTestStore(t)
	baseExpiry := s.FindByTelegramID(100).ExpiresAt

	// Stack a bunch of premium purchases.
	s.ExtendPremium(tok, 30)
	s.ExtendPremium(tok, 30)
	s.ExtendPremium(tok, 30)
	s.ExtendPremium(tok, 90)

	afterExpiry := s.FindByTelegramID(100).ExpiresAt
	if !afterExpiry.Equal(baseExpiry) {
		t.Errorf("base ExpiresAt was modified by ExtendPremium calls!\n  before: %v\n  after:  %v\n"+
			"This is the original bug the feature is meant to fix.",
			baseExpiry, afterExpiry)
	}
}

// TestExtendPremium_GoldStandardScenario walks through the exact
// scenario from the user's bug report:
//
//  1. User registers — base 365 days.
//  2. Buys 90 days premium → effective group = "premium"
//  3. Time passes 91 days.
//  4. After expiry — effective group = "" (= default), but base
//     subscription continues with 274 days remaining.
func TestExtendPremium_GoldStandardScenario(t *testing.T) {
	s, tok := newPremiumTestStore(t)

	// Step 1+2: buy 90 days of premium.
	until := s.ExtendPremium(tok, 90)
	if got := s.GetEffectiveGroup(tok, "premium"); got != "premium" {
		t.Errorf("immediately after purchase: group=%q, want premium", got)
	}

	// Step 3: simulate time travel — set PremiumUntil to the past.
	s.SetPremiumUntil(tok, until.Add(-91*24*time.Hour)) // hack: pretend 91 days passed

	// Step 4: effective group falls back to base.
	if got := s.GetEffectiveGroup(tok, "premium"); got != "" {
		t.Errorf("after premium expiry: group=%q, want \"\" (= default)", got)
	}
	// And base subscription still has time left.
	remaining := s.FindByTelegramID(100).ExpiresAt.Sub(time.Now().UTC())
	if remaining < 364*24*time.Hour {
		t.Errorf("base subscription lost time: remaining=%v, want ≥ 364d", remaining)
	}
}
