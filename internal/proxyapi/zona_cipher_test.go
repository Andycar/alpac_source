package proxyapi

import (
	"testing"
)

// TestZonaParseCipherParams verifies that the E alphabet and baked
// timestamp P are pulled out of a real urlTransform JS string.
func TestZonaParseCipherParams(t *testing.T) {
	js := `"use strict";var E="DlChEXitLONYRkFjAsnBbymWzSHMqKPgQZpvwerofJTVdIuUcxaG";` +
		`function T(n){var r=L.indexOf(n);return r>-1?E[r]:n}` +
		`var L="ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",` +
		`O=self[L[27]+L[45]+L[40]+L[26]],w=function(n){return n.split("").map(T).join("")},` +
		`y="/x-en-x/",v=function(n){return n.includes(y)},` +
		`m=Date.now(),P="1776262615",g=1e3*P-m;` +
		`function b(n){if(null==g||v(n))return n;var r=Date.now()+g,e=new URL(n);` +
		`return e.origin+y+w(O(Math.round(r/1e3/60/60)+"/"+e.pathname+e.search))}return b;`

	p := ZonaParseCipherParams(js)
	if p.Alphabet != "DlChEXitLONYRkFjAsnBbymWzSHMqKPgQZpvwerofJTVdIuUcxaG" {
		t.Errorf("alphabet: got %q", p.Alphabet)
	}
	// round(1776262615 / 3600) = 493406  (actually 493406.28 → nearest 493406)
	if p.Hours != 493406 {
		t.Errorf("hours: got %d want 493406", p.Hours)
	}
}

// TestZonaParseCipherParamsInvalid handles missing fields gracefully.
func TestZonaParseCipherParamsInvalid(t *testing.T) {
	// No P variable.
	if p := ZonaParseCipherParams(`var E="DlChEXitLONYRkFjAsnBbymWzSHMqKPgQZpvwerofJTVdIuUcxaG"; return b;`); p.Hours != 0 {
		t.Errorf("missing P: got hours %d", p.Hours)
	}
	// No E variable (short string doesn't match 40+ char requirement).
	if p := ZonaParseCipherParams(`var E="Short"; var P="1776262615";`); p.Alphabet != "" {
		t.Errorf("short E should not parse, got %q", p.Alphabet)
	}
}

// TestZonaTransformURLShape verifies the structure of a transformed URL:
// same origin, "/x-en-x/" path prefix, and a cipher that's deterministic.
func TestZonaTransformURLShape(t *testing.T) {
	p := ZonaCipherParams{
		Hours:    493406,
		Alphabet: "DlChEXitLONYRkFjAsnBbymWzSHMqKPgQZpvwerofJTVdIuUcxaG",
	}
	in := "https://x-bcr.interkh.com/01_15_24/01/15/19/W2WG55XX/HIISKTUE.mp4/seg-1-v1.ts?hs=abc&t=1777126615"
	got := ZonaTransformURL(in, p)
	if got == in {
		t.Fatal("URL was not transformed")
	}
	const prefix = "https://x-bcr.interkh.com/x-en-x/"
	if !startsWith(got, prefix) {
		t.Errorf("prefix mismatch: %q", got)
	}
	// Determinism: same input must yield identical output.
	if got2 := ZonaTransformURL(in, p); got2 != got {
		t.Error("not deterministic")
	}
	// Already-ciphered URL passes through unchanged.
	already := "https://x-bcr.interkh.com/x-en-x/abc123=="
	if ZonaTransformURL(already, p) != already {
		t.Error("already-ciphered URL was double-transformed")
	}
}

// TestZonaTransformURLExactCipher nails the EXACT output for a known input
// so any regression in base64 / substitution is caught. This is the same
// pair we verified against the real CDN via the Python probe.
func TestZonaTransformURLExactCipher(t *testing.T) {
	p := ZonaCipherParams{
		Hours:    493406,
		Alphabet: "DlChEXitLONYRkFjAsnBbymWzSHMqKPgQZpvwerofJTVdIuUcxaG",
	}
	in := "https://x-bcr.interkh.com/01_15_24/01/15/19/W2WG55XX/HIISKTUE.mp4/index-v1.m3u8?hi=b131da54ebcd554&hu=1b931a22572eb62&ha=0ccb7ed47e4a516&hc=5950b6c9af1b9b5&hs=1vr0xPfN4j&ht=2f1d709401a0d79&hui=ec142639a969239&t=1777126615&x-cdn=10551403"
	want := "https://x-bcr.interkh.com/x-en-x/khwGkhD2Ya8cRy8xky8akC8cRn8xkn8xFn9WReKtkByzmC9LnbeBn1sysn5IqhAUHm5wSWQIKvEuMBk1Fh9fHB1pRBRxSiE1kiypz2A1kBArHtb9RmL5RGXZRvL1kGOezvzaOrZZjBlvz2L3SmA0k2b0zBbxkpSfzG01FBbczvSvFmXrRmL5zvbrHtR9RWSaRtZASw40HpSfKh0aSvXwkGD5khDxzBlwkGwrHtyJjmyvRBAakvR5zBw2FBLGFnS0jBE3kGqxRvz2RBbrPC1vSi49RBD1kBE0RhR="
	got := ZonaTransformURL(in, p)
	if got != want {
		t.Errorf("cipher mismatch\n  got:  %s\n  want: %s", got, want)
	}
}

// TestZonaNeedsCipher: .m3u8 skips cipher, segments require it.
func TestZonaNeedsCipher(t *testing.T) {
	cases := map[string]bool{
		"https://x.y/a/b.m3u8?t=1": false,
		"https://x.y/a/b.ts?t=1":   true,
		"https://x.y/a/b.m4s":      true,
		"https://x.y/a/b.mpd":      true,
		"https://x.y/a/b.webm":     true,
		"https://x.y/a/b.aac?x":    true,
		"https://x.y/a/b.mp4":      true,
		"https://x.y/a/b":          false,
		"":                         false,
	}
	for u, want := range cases {
		if got := zonaNeedsCipher(u); got != want {
			t.Errorf("%q: got %v want %v", u, got, want)
		}
	}
}

// TestApplyZonaCipherPreservesHeaders verifies that applyZonaCipher does
// NOT mutate meta.headers — the cipher params must survive multiple calls
// because rewriteM3U re-encrypts sub-URLs using the same meta.
func TestApplyZonaCipherPreservesHeaders(t *testing.T) {
	meta := &linkMeta{
		plugin: "zona",
		headers: map[string]string{
			ZonaCipherHoursHeader:    "493406",
			ZonaCipherAlphabetHeader: "DlChEXitLONYRkFjAsnBbymWzSHMqKPgQZpvwerofJTVdIuUcxaG",
			"Referer":                "https://kinoserial.online/",
		},
	}

	// m3u8 URL: left untouched, cipher headers PRESERVED.
	master := "https://x.interkh.com/path/master.m3u8?t=1"
	if got := applyZonaCipher(master, meta); got != master {
		t.Errorf("m3u8 should not be transformed: %q", got)
	}
	if _, ok := meta.headers[ZonaCipherHoursHeader]; !ok {
		t.Error("cipher-hours header must be preserved for subsequent rewrites")
	}
	if _, ok := meta.headers[ZonaCipherAlphabetHeader]; !ok {
		t.Error("cipher-alphabet header must be preserved for subsequent rewrites")
	}

	// Subsequent .ts call is still ciphered because headers survived.
	seg := "https://x.interkh.com/path/seg-1.ts?t=1"
	got := applyZonaCipher(seg, meta)
	if got == seg {
		t.Errorf(".ts should be ciphered on second call: %q", got)
	}
}

// TestIsZonaCipherHeader covers the pseudo-header blacklist used by
// fetchWithMeta to skip forwarding X-Zona-Cipher-* upstream.
func TestIsZonaCipherHeader(t *testing.T) {
	cases := map[string]bool{
		ZonaCipherHoursHeader:    true,
		ZonaCipherAlphabetHeader: true,
		"x-zona-cipher-hours":    true, // case-insensitive
		"x-zona-cipher-e":        true,
		"Referer":                false,
		"User-Agent":             false,
		"":                       false,
	}
	for k, want := range cases {
		if got := isZonaCipherHeader(k); got != want {
			t.Errorf("%q: got %v want %v", k, got, want)
		}
	}
}

// TestApplyZonaCipherSegment re-ciphers a segment URL.
func TestApplyZonaCipherSegment(t *testing.T) {
	meta := &linkMeta{
		plugin: "zona",
		headers: map[string]string{
			ZonaCipherHoursHeader:    "493406",
			ZonaCipherAlphabetHeader: "DlChEXitLONYRkFjAsnBbymWzSHMqKPgQZpvwerofJTVdIuUcxaG",
		},
	}
	in := "https://x-bcr.interkh.com/01_15_24/01/15/19/W2WG55XX/HIISKTUE.mp4/seg-1-v1.ts?t=1"
	got := applyZonaCipher(in, meta)
	if got == in {
		t.Error(".ts should be ciphered")
	}
	if !startsWith(got, "https://x-bcr.interkh.com/x-en-x/") {
		t.Errorf("unexpected output: %s", got)
	}
	// Determinism.
	meta2 := &linkMeta{
		plugin: "zona",
		headers: map[string]string{
			ZonaCipherHoursHeader:    "493406",
			ZonaCipherAlphabetHeader: "DlChEXitLONYRkFjAsnBbymWzSHMqKPgQZpvwerofJTVdIuUcxaG",
		},
	}
	if applyZonaCipher(in, meta2) != got {
		t.Error("cipher not deterministic between calls")
	}
}

// TestApplyPreFetchRewriteOtherPlugin confirms non-zona plugins are no-ops.
func TestApplyPreFetchRewriteOtherPlugin(t *testing.T) {
	meta := linkMeta{
		plugin:  "hdvb",
		headers: map[string]string{"Referer": "https://x/"},
	}
	in := "https://cdn.example/seg-1.ts"
	if got := applyPreFetchRewrite(in, meta); got != in {
		t.Errorf("non-zona plugin must pass through, got %q", got)
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
