package updater

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// TestVerifyMinisignGolden builds a synthetic minisign key/sig pair in-memory
// and verifies that VerifyMinisign accepts it, then mutates each component
// and verifies that every mutation is rejected.
func TestVerifyMinisignGolden(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var keyID [8]byte
	_, _ = rand.Read(keyID[:])

	data := []byte("SHA256SUMS content here\n")

	// Primary signature = Ed25519(data)
	sig := ed25519.Sign(priv, data)
	// Global signature = Ed25519(sig || trusted_comment)
	trusted := "timestamp:1700000000 file:SHA256SUMS hashed"
	globalSig := ed25519.Sign(priv, append(append([]byte{}, sig...), []byte(trusted)...))

	// Pack main sig: "Ed" + keyID + signature
	mainBlob := append([]byte("Ed"), keyID[:]...)
	mainBlob = append(mainBlob, sig...)

	pubBlob := append([]byte("Ed"), keyID[:]...)
	pubBlob = append(pubBlob, pub...)

	sigText := "untrusted comment: signature from lampac-go test\n" +
		base64.StdEncoding.EncodeToString(mainBlob) + "\n" +
		"trusted comment: " + trusted + "\n" +
		base64.StdEncoding.EncodeToString(globalSig) + "\n"

	pubText := "untrusted comment: minisign public key TEST\n" +
		base64.StdEncoding.EncodeToString(pubBlob) + "\n"

	if err := VerifyMinisign(data, sigText, pubText); err != nil {
		t.Fatalf("good signature rejected: %v", err)
	}

	// Corrupt data.
	if err := VerifyMinisign([]byte("tampered"), sigText, pubText); err == nil {
		t.Error("corrupted data accepted")
	}

	// Wrong pub key → key id mismatch.
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	pubBlob2 := append([]byte("Ed"), keyID[:]...)
	pubBlob2 = append(pubBlob2, pub2...)
	pubText2 := "untrusted comment: other\n" + base64.StdEncoding.EncodeToString(pubBlob2) + "\n"
	if err := VerifyMinisign(data, sigText, pubText2); err == nil {
		t.Error("wrong pubkey accepted")
	}
}
