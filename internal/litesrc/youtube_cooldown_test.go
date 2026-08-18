package litesrc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The 2026-08-17 outage in one sentence: YouTube challenged the server's IP, every client failed
// within seconds, each was parked for the full cooldown, and from then on requests returned
// «all extraction strategies failed» instantly — while yt-dlp by hand still extracted 27 formats.
// These tests pin the two behaviours that prevent a repeat.

func TestAntiBotStderrRecognisesIPLevelRefusals(t *testing.T) {
	blocked := []string{
		"WARNING: [youtube] Sign in to confirm you’re not a bot. Use --cookies-from-browser",
		"ERROR: [youtube] Sign in to confirm you're not a bot",
		"ERROR: unable to download API page: HTTP Error 429: Too Many Requests",
		"ERROR: Too Many Requests",
	}
	for _, s := range blocked {
		if !ytAntiBotStderr(s) {
			t.Errorf("missed an IP-level refusal: %q", s)
		}
	}

	// Client-specific failures MUST stay outside this bucket — they are exactly what the cooldown
	// is for, and mislabelling them would keep a genuinely dead client in rotation forever.
	notBlocked := []string{
		"WARNING: [youtube] Some tv client https formats have been skipped as they are DRM protected",
		"WARNING: [youtube] mweb client https formats require a GVS PO Token which was not provided",
		"WARNING: [youtube] SABR-only streaming experiment for the current session",
		"ERROR: Requested format is not available",
		"",
	}
	for _, s := range notBlocked {
		if ytAntiBotStderr(s) {
			t.Errorf("client-specific failure wrongly treated as an IP block: %q", s)
		}
	}
}

// A checker whose every strategy is cooling down must still attempt extraction.
func TestCooldownFailsOpenWhenEveryClientIsParked(t *testing.T) {
	y := &YoutubeChecker{stratCooldown: map[string]time.Time{}}
	labels := []string{"default", "mweb", "tv", "android"}
	until := time.Now().Add(stratCooldownTTL)
	for _, l := range labels {
		y.stratCooldown[l] = until
	}

	if !allStrategiesCooling(y, labels) {
		t.Fatal("expected every strategy to read as cooling")
	}

	// One client recovering is enough to leave fail-open mode — the healthy one must be preferred
	// over blindly retrying the parked ones.
	delete(y.stratCooldown, "default")
	if allStrategiesCooling(y, labels) {
		t.Fatal("a recovered client must end the all-cooling state")
	}

	// An expired entry counts as recovered, not as still-parked.
	y.stratCooldown["default"] = time.Now().Add(-time.Minute)
	if allStrategiesCooling(y, labels) {
		t.Fatal("an expired cooldown must not count as cooling")
	}
}

// allStrategiesCooling mirrors the check inside extractFormats.
func allStrategiesCooling(y *YoutubeChecker, labels []string) bool {
	for _, label := range labels {
		y.mu.RLock()
		coolUntil, cooling := y.stratCooldown[label]
		y.mu.RUnlock()
		if !cooling || !time.Now().Before(coolUntil) {
			return false
		}
	}
	return true
}

// A client that succeeds must lose its cooldown, so one bad minute doesn't sideline it for the
// whole TTL once the upstream recovers.
func TestSuccessClearsCooldown(t *testing.T) {
	y := &YoutubeChecker{stratCooldown: map[string]time.Time{
		"default": time.Now().Add(stratCooldownTTL),
	}}
	y.mu.Lock()
	delete(y.stratCooldown, "default")
	y.mu.Unlock()
	if _, still := y.stratCooldown["default"]; still {
		t.Fatal("cooldown survived a successful extraction")
	}
}

// yt-dlp rewrites whatever file --cookies points at, so it must never point at the master.
// Two prod incidents came from this: a 0-byte master on 2026-08-14, and — after «fixing» that with
// chattr +i — an empty home feed on 2026-08-17, because yt-dlp then exits non-zero and the search
// path treats that as failure.
func TestCookieWorkCopyProtectsTheMaster(t *testing.T) {
	dir := t.TempDir()
	master := filepath.Join(dir, "cookies.txt")
	const content = "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1820008323\tSID\tsecret\n"
	if err := os.WriteFile(master, []byte(content), 0o600); err != nil {
		t.Fatalf("seed master: %v", err)
	}

	y := &YoutubeChecker{cookiePath: master}
	work := y.ensureCookieWork()
	if work == master {
		t.Fatal("yt-dlp was handed the master file")
	}
	if b, _ := os.ReadFile(work); string(b) != content {
		t.Fatal("work copy does not match the master")
	}

	// Simulate yt-dlp truncating the work copy (a run killed mid-write) — the next call must heal it
	// from the master instead of authenticating with nothing.
	if err := os.WriteFile(work, nil, 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if again := y.ensureCookieWork(); again != work {
		t.Fatalf("expected the same work path, got %q", again)
	}
	if b, _ := os.ReadFile(work); string(b) != content {
		t.Fatal("truncated work copy was not restored from the master")
	}

	// The master must be byte-identical throughout.
	if b, _ := os.ReadFile(master); string(b) != content {
		t.Fatal("master was modified")
	}

	// The decisive case: yt-dlp's rewrite is LOSSY — it strips the login cookies (prod saw SID,
	// SAPISID and LOGIN_INFO disappear). A degraded copy must NOT survive into the next run, or
	// authentication quietly rots away and YouTube starts demanding «confirm you're not a bot».
	degraded := "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1820008323\tPREF\tf4=1\n"
	if err := os.WriteFile(work, []byte(degraded), 0o600); err != nil {
		t.Fatalf("degrade: %v", err)
	}
	y.ensureCookieWork()
	if b, _ := os.ReadFile(work); string(b) != content {
		t.Fatal("a work copy stripped of login cookies was not refreshed from the master")
	}
}

// No cookies configured → no --cookies, and no crash.
func TestCookieWorkCopyWithoutMaster(t *testing.T) {
	y := &YoutubeChecker{}
	if got := y.ensureCookieWork(); got != "" {
		t.Fatalf("expected no cookie path, got %q", got)
	}
}
