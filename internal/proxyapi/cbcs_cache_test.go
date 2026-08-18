package proxyapi

import (
	"strings"
	"testing"
	"time"
)

// TestCBCSKeyCacheTTLExpires confirms entries past their TTL are not served.
func TestCBCSKeyCacheTTLExpires(t *testing.T) {
	const uri = "https://kinescope.example/lic/test-expire"

	if got, ok := cbcsKeyCache.Load("nonexistent-key"); ok || got != nil {
		t.Fatalf("nonexistent key returned (got=%v ok=%v)", got, ok)
	}

	keyBytes := []byte("1234567890123456")
	cbcsKeyCache.Store(uri, keyBytes)
	got, ok := cbcsKeyCache.Load(uri)
	if !ok || string(got) != string(keyBytes) {
		t.Fatalf("after Store: ok=%v got=%v", ok, got)
	}

	cbcsKeyCache.mu.Lock()
	e := cbcsKeyCache.entries[uri]
	e.expires = time.Now().Add(-time.Second)
	cbcsKeyCache.entries[uri] = e
	cbcsKeyCache.mu.Unlock()

	if got, ok := cbcsKeyCache.Load(uri); ok || got != nil {
		t.Fatalf("expired entry returned (got=%v ok=%v)", got, ok)
	}
}

// TestCBCSKeyCacheHardCap guards against unbounded growth.
func TestCBCSKeyCacheHardCap(t *testing.T) {
	cbcsKeyCache.mu.Lock()
	cbcsKeyCache.entries = make(map[string]cbcsKeyEntry, 256)
	cbcsKeyCache.mu.Unlock()

	for i := 0; i < cbcsKeyMax+200; i++ {
		cbcsKeyCache.Store(strings.Repeat("k", i+1), []byte("1234567890123456"))
	}
	cbcsKeyCache.mu.RLock()
	size := len(cbcsKeyCache.entries)
	cbcsKeyCache.mu.RUnlock()
	if size > cbcsKeyMax {
		t.Fatalf("hard cap not enforced on cbcsKeyCache: %d (max %d)", size, cbcsKeyMax)
	}
}
