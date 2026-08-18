package tgauth

import (
	"testing"
	"time"
)

func seedCubStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s := NewStore(dir)
	t.Cleanup(s.Close)
	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	if err := s.Add(ApprovedToken{Token: "tok-A", TelegramID: 1, ExpiresAt: exp}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(ApprovedToken{Token: "tok-B", TelegramID: 2, ExpiresAt: exp}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return s, dir
}

func TestSetCubUser_LinkAndLookup(t *testing.T) {
	s, _ := seedCubStore(t)

	if !s.SetCubUser("tok-A", "cub-77", "user@example.com") {
		t.Fatal("SetCubUser returned false on first link")
	}
	if s.SetCubUser("tok-A", "cub-77", "user@example.com") {
		t.Error("SetCubUser returned true on identical re-link (should be no-op)")
	}

	uid, email := s.GetCubUser("tok-A")
	if uid != "cub-77" || email != "user@example.com" {
		t.Errorf("GetCubUser = (%q, %q), want (cub-77, user@example.com)", uid, email)
	}

	tok, ok := s.FindTokenByCubUser("cub-77")
	if !ok || tok != "tok-A" {
		t.Errorf("FindTokenByCubUser = (%q, %v), want (tok-A, true)", tok, ok)
	}

	if _, ok := s.FindTokenByCubUser("cub-unknown"); ok {
		t.Error("FindTokenByCubUser found a token for an unknown cub id")
	}
	if _, ok := s.FindTokenByCubUser(""); ok {
		t.Error("FindTokenByCubUser accepted an empty cub id")
	}
}

func TestFindTokenByCubUser_AmbiguousRefuses(t *testing.T) {
	s, _ := seedCubStore(t)
	// Ambiguity arises via the passive path (an explicit link steals the
	// anchor instead — see TestSetCubUser_ExplicitLinkStealsFromOtherTokens).
	s.SetCubUser("tok-A", "cub-77", "a@example.com")
	s.SetCubUserAuto("tok-B", "cub-77", "b@example.com")

	if tok, ok := s.FindTokenByCubUser("cub-77"); ok {
		t.Errorf("ambiguous cub id restored token %q, want refusal", tok)
	}
}

func TestFindTokenByCubUser_ExpiredIgnored(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	t.Cleanup(s.Close)
	if err := s.Add(ApprovedToken{Token: "tok-old", TelegramID: 3, ExpiresAt: time.Now().UTC().Add(-time.Hour)}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	s.SetCubUser("tok-old", "cub-9", "")

	if tok, ok := s.FindTokenByCubUser("cub-9"); ok {
		t.Errorf("expired token %q restored via cub anchor", tok)
	}
}

func TestClearCubUser_OptsOutOfAutoRelink(t *testing.T) {
	s, _ := seedCubStore(t)
	s.SetCubUser("tok-A", "cub-77", "a@example.com")

	if !s.ClearCubUser("tok-A") {
		t.Fatal("ClearCubUser returned false on a linked token")
	}
	if uid, _ := s.GetCubUser("tok-A"); uid != "" {
		t.Errorf("link survived ClearCubUser: %q", uid)
	}
	if _, ok := s.FindTokenByCubUser("cub-77"); ok {
		t.Error("FindTokenByCubUser still resolves after unlink")
	}
	if s.ClearCubUser("tok-A") {
		t.Error("ClearCubUser returned true on an already-unlinked token")
	}

	// Passive learner must NOT undo the unlink...
	if s.SetCubUserAuto("tok-A", "cub-77", "a@example.com") {
		t.Error("SetCubUserAuto re-linked despite opt-out")
	}
	if uid, _ := s.GetCubUser("tok-A"); uid != "" {
		t.Errorf("auto re-link happened: %q", uid)
	}

	// ...but an explicit link clears the opt-out and re-enables auto
	// refreshes of the SAME anchor (a different id still can't displace it —
	// see TestSetCubUserAuto_NeverReplacesDifferentLink).
	if !s.SetCubUser("tok-A", "cub-88", "b@example.com") {
		t.Fatal("explicit SetCubUser failed after opt-out")
	}
	if !s.SetCubUserAuto("tok-A", "cub-88", "c@example.com") {
		t.Error("SetCubUserAuto same-id refresh refused after explicit re-link cleared opt-out")
	}
}

func TestSetCubUser_ExplicitLinkStealsFromOtherTokens(t *testing.T) {
	s, _ := seedCubStore(t)
	// Ambiguous state: both tokens carry the same cub id (e.g. a stale link
	// on an old test account) — the collision guard refuses both.
	s.SetCubUser("tok-A", "cub-77", "old@example.com")
	s.SetCubUserAuto("tok-B", "cub-77", "old@example.com")
	if _, ok := s.FindTokenByCubUser("cub-77"); ok {
		t.Fatal("expected ambiguous state before the explicit re-link")
	}

	// Explicit link on tok-A claims the anchor and strips it from tok-B.
	if !s.SetCubUser("tok-A", "cub-77", "old@example.com") {
		t.Fatal("explicit SetCubUser reported no change while resolving ambiguity")
	}
	tok, ok := s.FindTokenByCubUser("cub-77")
	if !ok || tok != "tok-A" {
		t.Errorf("after explicit claim FindTokenByCubUser = (%q, %v), want (tok-A, true)", tok, ok)
	}
	if uid, _ := s.GetCubUser("tok-B"); uid != "" {
		t.Errorf("tok-B kept the stolen anchor: %q", uid)
	}

	// Passive learning must NOT steal: re-link tok-B via the auto path and
	// make sure tok-A keeps its anchor (state becomes ambiguous again, not
	// transferred).
	s.SetCubUserAuto("tok-B", "cub-77", "old@example.com")
	if uid, _ := s.GetCubUser("tok-A"); uid != "cub-77" {
		t.Errorf("auto link stole the anchor from tok-A: %q", uid)
	}

	active, expired := s.CubLinkStats("cub-77")
	if active != 2 || expired != 0 {
		t.Errorf("CubLinkStats = (%d, %d), want (2, 0)", active, expired)
	}
}

func TestSetCubUserAuto_NeverReplacesDifferentLink(t *testing.T) {
	s, _ := seedCubStore(t)
	s.SetCubUser("tok-A", "cub-77", "mine@example.com")

	// A different CUB account seen on the same user's device (second CUB
	// profile, guest login on the TV) must NOT displace the anchor.
	if s.SetCubUserAuto("tok-A", "cub-99", "guest@example.com") {
		t.Error("passive learn replaced an existing different link")
	}
	if uid, _ := s.GetCubUser("tok-A"); uid != "cub-77" {
		t.Errorf("anchor changed to %q, want cub-77", uid)
	}

	// Same id refresh (e.g. email updated) is still allowed.
	if !s.SetCubUserAuto("tok-A", "cub-77", "renamed@example.com") {
		t.Error("passive refresh of the same link was refused")
	}

	// Explicit re-link CAN switch to the new account.
	if !s.SetCubUser("tok-A", "cub-99", "guest@example.com") {
		t.Error("explicit re-link to a different account failed")
	}
	if uid, _ := s.GetCubUser("tok-A"); uid != "cub-99" {
		t.Errorf("explicit re-link didn't apply: %q", uid)
	}
}

func TestClearCubUser_KeepsOtherTokensIndexed(t *testing.T) {
	s, _ := seedCubStore(t)
	// Previously-ambiguous pair: both tokens carry the same cub id.
	s.SetCubUser("tok-A", "cub-77", "")
	s.SetCubUser("tok-B", "cub-77", "")

	// A unlinks — B must become resolvable (no longer ambiguous).
	s.ClearCubUser("tok-A")
	tok, ok := s.FindTokenByCubUser("cub-77")
	if !ok || tok != "tok-B" {
		t.Errorf("after unlinking tok-A, FindTokenByCubUser = (%q, %v), want (tok-B, true)", tok, ok)
	}
}

func TestCubLink_PersistsAcrossReload(t *testing.T) {
	s, dir := seedCubStore(t)
	s.SetCubUser("tok-A", "cub-42", "p@example.com")
	s.Close()

	s2 := NewStore(dir)
	t.Cleanup(s2.Close)
	tok, ok := s2.FindTokenByCubUser("cub-42")
	if !ok || tok != "tok-A" {
		t.Errorf("after reload FindTokenByCubUser = (%q, %v), want (tok-A, true)", tok, ok)
	}
	uid, email := s2.GetCubUser("tok-A")
	if uid != "cub-42" || email != "p@example.com" {
		t.Errorf("after reload GetCubUser = (%q, %q)", uid, email)
	}
}
