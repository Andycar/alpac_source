package updater

// Minisign signature verification (pure-Go, no external tooling required).
//
// Minisign format docs: https://jedisct1.github.io/minisign/
//
// We support BOTH minisign signature algorithms: "Ed" (legacy Ed25519 over the
// raw data) and "ED" (Ed25519 over a BLAKE2b-512 prehash of the data). Modern
// minisign (>= ~0.10, incl. 0.12) produces "ED" by DEFAULT — `-H` is no longer
// required — so verifying "ED" is mandatory, otherwise real releases signed
// with a stock `minisign -Sm` would be rejected.
//
// A .minisig file has the shape:
//
//	untrusted comment: <anything>
//	<base64 of: signature_algorithm(2B) || key_id(8B) || signature(64B)>
//	trusted comment: <anything>
//	<base64 of: ed25519 signature over (data || trusted_comment_bytes)>
//
// A public key has the shape:
//
//	untrusted comment: minisign public key <key_id>
//	<base64 of: signature_algorithm(2B) || key_id(8B) || public_key(32B)>

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// ErrSignatureMismatch is returned when the signature does not match.
var ErrSignatureMismatch = errors.New("updater: minisign signature mismatch")

// VerifyMinisign verifies `data` against a raw .minisig payload and a
// raw minisign public key (the base64 body of the pubkey file OR the full
// pubkey file including comments). Returns nil on success.
func VerifyMinisign(data []byte, sigText, pubKeyText string) error {
	pub, pubKeyID, err := parseMinisignPubKey(pubKeyText)
	if err != nil {
		return fmt.Errorf("pubkey: %w", err)
	}

	sig, sigKeyID, trustedComment, globalSig, prehashed, err := parseMinisignSig(sigText)
	if err != nil {
		return fmt.Errorf("sig: %w", err)
	}

	if sigKeyID != pubKeyID {
		return fmt.Errorf("updater: key id mismatch (sig %x vs pub %x)", sigKeyID, pubKeyID)
	}

	// Primary signature is Ed25519 over the data — directly for "Ed" (legacy)
	// or over a BLAKE2b-512 prehash of the data for "ED" (minisign default).
	signedMsg := data
	if prehashed {
		h := blake2b.Sum512(data)
		signedMsg = h[:]
	}
	if !ed25519.Verify(pub, signedMsg, sig) {
		return ErrSignatureMismatch
	}

	// Global signature: Ed25519(sig || trusted_comment)
	globalMsg := append(append([]byte{}, sig...), []byte(trustedComment)...)
	if !ed25519.Verify(pub, globalMsg, globalSig) {
		return fmt.Errorf("updater: trusted-comment signature mismatch")
	}

	return nil
}

// ---------------------------------------------------------------------------
// parsers
// ---------------------------------------------------------------------------

func parseMinisignPubKey(s string) (ed25519.PublicKey, [8]byte, error) {
	var zeroID [8]byte
	body := stripComments(s)
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, zeroID, fmt.Errorf("base64: %w", err)
	}
	if len(raw) != 2+8+32 {
		return nil, zeroID, fmt.Errorf("unexpected length %d (want 42)", len(raw))
	}
	if string(raw[:2]) != "Ed" {
		return nil, zeroID, fmt.Errorf("unsupported algorithm %q (only legacy Ed is supported)", raw[:2])
	}
	var id [8]byte
	copy(id[:], raw[2:10])
	return ed25519.PublicKey(raw[10:]), id, nil
}

func parseMinisignSig(s string) (sig []byte, keyID [8]byte, trustedComment string, globalSig []byte, prehashed bool, err error) {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var sigB64, trusted, globalB64 string
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "untrusted comment:"):
			// next line is the main signature
			if i+1 < len(lines) {
				sigB64 = strings.TrimSpace(lines[i+1])
				i++
			}
		case strings.HasPrefix(l, "trusted comment:"):
			trusted = strings.TrimSpace(strings.TrimPrefix(l, "trusted comment:"))
			// trim the leading space
			trusted = strings.TrimPrefix(trusted, " ")
			if i+1 < len(lines) {
				globalB64 = strings.TrimSpace(lines[i+1])
				i++
			}
		}
	}
	if sigB64 == "" || globalB64 == "" {
		err = errors.New("malformed .minisig (missing signature lines)")
		return
	}

	raw, derr := base64.StdEncoding.DecodeString(sigB64)
	if derr != nil {
		err = fmt.Errorf("decode main sig: %w", derr)
		return
	}
	if len(raw) != 2+8+64 {
		err = fmt.Errorf("main sig length %d (want 74)", len(raw))
		return
	}
	switch string(raw[:2]) {
	case "Ed":
		prehashed = false
	case "ED":
		prehashed = true
	default:
		err = fmt.Errorf("unsupported sig algorithm %q (want Ed or ED)", raw[:2])
		return
	}
	copy(keyID[:], raw[2:10])
	sig = raw[10:]

	globalSig, derr = base64.StdEncoding.DecodeString(globalB64)
	if derr != nil {
		err = fmt.Errorf("decode global sig: %w", derr)
		return
	}
	if len(globalSig) != 64 {
		err = fmt.Errorf("global sig length %d (want 64)", len(globalSig))
		return
	}
	trustedComment = trusted
	return
}

// stripComments returns the first non-comment, non-empty line of s — for a
// minisign pubkey file that's the base64 body.
func stripComments(s string) string {
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "untrusted comment:") || strings.HasPrefix(l, "trusted comment:") {
			continue
		}
		return l
	}
	return strings.TrimSpace(s)
}
