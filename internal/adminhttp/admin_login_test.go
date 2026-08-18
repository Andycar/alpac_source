package adminhttp

import (
	"strings"
	"testing"
)

// Tests for the admin session-cookie machinery moved from httpapi. These are the
// security core of password-auth: a signed HMAC token whose tampering / wrong-key
// / malformed variants must all be rejected. No host deps needed (pure funcs).

func TestAdminSession_RoundTrip(t *testing.T) {
	secret := []byte("test-secret-key-0123456789")

	for _, partial := range []bool{false, true} {
		tok := createAdminSession(secret, partial)
		gotPartial, ok := ValidateAdminSession(tok, secret)
		if !ok {
			t.Fatalf("partial=%v: valid token rejected", partial)
		}
		if gotPartial != partial {
			t.Errorf("partial flag not preserved: created %v, got %v", partial, gotPartial)
		}
	}
}

func TestAdminSession_WrongSecretRejected(t *testing.T) {
	tok := createAdminSession([]byte("secret-A-original"), false)
	if _, ok := ValidateAdminSession(tok, []byte("secret-B-different")); ok {
		t.Fatal("SECURITY: token validated under a different secret — HMAC key not enforced")
	}
}

func TestAdminSession_TamperRejected(t *testing.T) {
	secret := []byte("test-secret-key-0123456789")
	tok := createAdminSession(secret, false)

	dot := strings.IndexByte(tok, '.')
	if dot <= 0 || dot >= len(tok)-1 {
		t.Fatalf("unexpected token shape: %q", tok)
	}

	// Tamper the FIRST char of each segment — the leading base64 chars carry
	// high-order (always-meaningful) bits, unlike the last char whose low bits
	// are RawURLEncoding padding that can decode identically. Swap to a
	// different valid base64url char so decoding still succeeds but the bytes
	// (and thus the HMAC) differ.
	swap := func(c byte) byte {
		if c == 'A' {
			return 'B'
		}
		return 'A'
	}

	// Tampered payload (first char before the dot).
	bp := []byte(tok)
	bp[0] = swap(bp[0])
	if _, ok := ValidateAdminSession(string(bp), secret); ok {
		t.Fatal("SECURITY: token with tampered payload accepted")
	}

	// Tampered signature (first char after the dot).
	bs := []byte(tok)
	bs[dot+1] = swap(bs[dot+1])
	if _, ok := ValidateAdminSession(string(bs), secret); ok {
		t.Fatal("SECURITY: token with tampered signature accepted")
	}
}

func TestAdminSession_MalformedRejected(t *testing.T) {
	secret := []byte("test-secret-key-0123456789")
	for _, bad := range []string{"", "garbage", "no-dot-here", "a.b.c", ".", "onlyleft.", ".onlyright"} {
		if _, ok := ValidateAdminSession(bad, secret); ok {
			t.Errorf("SECURITY: malformed token %q accepted", bad)
		}
	}
}
