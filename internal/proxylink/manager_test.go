package proxylink

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAESRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m, err := New(Options{
		CacheDir:   dir,
		VerifyIP:   true,
		EncryptAES: true,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	hash := m.EncryptURI("https://example.com/video.m3u8", "1.1.1.1", "test", true, false, false)
	if hash == "" {
		t.Fatalf("empty hash")
	}
	if !IsAes(hash) {
		t.Fatalf("expected aes hash")
	}

	model := m.Decrypt(hash, "1.1.1.1")
	if model == nil {
		t.Fatalf("decrypt returned nil")
	}
	if model.URI != "https://example.com/video.m3u8" {
		t.Fatalf("uri mismatch: %s", model.URI)
	}
	if model.Plugin != "test" {
		t.Fatalf("plugin mismatch: %s", model.Plugin)
	}
}

func TestMD5RoundTripWithVerifyIP(t *testing.T) {
	dir := t.TempDir()
	m, err := New(Options{
		CacheDir:   dir,
		VerifyIP:   true,
		EncryptAES: false,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	hash := m.EncryptURI("https://example.com/seg.ts", "2.2.2.2", "x", true, false, false)
	if IsAes(hash) {
		t.Fatalf("expected md5 hash")
	}
	if m.Decrypt(hash, "1.1.1.1") != nil {
		t.Fatalf("expected ip mismatch")
	}
	if m.Decrypt(hash, "2.2.2.2") == nil {
		t.Fatalf("expected decrypt success")
	}
}

// TestDebugDecryptReportsRealReason locks in the diagnostic fix: when a
// verify-IP AES link is fetched from a different IP than it was minted for,
// DebugDecrypt must report the IP mismatch (naming both IPs) rather than the
// old misleading "ok (aes)" — that blind spot is exactly what masked the cause
// of /proxy 404s in the cluster. It must also agree with Decrypt's verdict.
func TestDebugDecryptReportsRealReason(t *testing.T) {
	dir := t.TempDir()
	m, err := New(Options{CacheDir: dir, VerifyIP: true, EncryptAES: true})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	// AES verify-ip link minted for 1.1.1.1.
	hash := m.EncryptURI("https://example.com/v.m3u8", "1.1.1.1", "test", true, false, false)
	if !IsAes(hash) {
		t.Fatalf("expected aes hash")
	}

	// Same IP → ok, and Decrypt agrees.
	if got := m.DebugDecrypt(hash, "1.1.1.1"); got != "ok (aes)" {
		t.Fatalf("matching IP: got %q, want %q", got, "ok (aes)")
	}

	// Different IP → must NOT say "ok (aes)"; must name both IPs.
	got := m.DebugDecrypt(hash, "9.9.9.9")
	if !strings.Contains(got, "ip mismatch") || !strings.Contains(got, "1.1.1.1") || !strings.Contains(got, "9.9.9.9") {
		t.Fatalf("mismatched IP: got %q, want ip-mismatch naming both IPs", got)
	}
	if m.Decrypt(hash, "9.9.9.9") != nil {
		t.Fatalf("Decrypt must reject the IP mismatch that DebugDecrypt reported")
	}

	// MD5 expired link → reason must mention expiry (mirrors Decrypt).
	md5m, err := New(Options{CacheDir: dir, VerifyIP: false, EncryptAES: false})
	if err != nil {
		t.Fatalf("new md5 manager: %v", err)
	}
	mh := md5m.EncryptURI("https://example.com/seg.ts", "", "", false, false, false)
	md5m.mu.Lock()
	v := md5m.links[mh]
	v.Expires = time.Now().Add(-time.Minute)
	md5m.links[mh] = v
	md5m.mu.Unlock()
	if got := md5m.DebugDecrypt(mh, ""); !strings.Contains(got, "expired") {
		t.Fatalf("expired md5: got %q, want an expiry reason", got)
	}
}

func TestLoadExistingAESKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aeskey")
	if err := os.WriteFile(path, []byte("1234567890abcdef/abcdef1234567890"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	m, err := New(Options{CacheDir: dir, EncryptAES: true})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	hash := m.EncryptURI("https://example.com/a.mp4", "", "", false, false, false)
	if m.Decrypt(hash, "") == nil {
		t.Fatalf("decrypt nil")
	}
}

func TestExpiredMD5IsRejected(t *testing.T) {
	dir := t.TempDir()
	m, err := New(Options{
		CacheDir:   dir,
		VerifyIP:   false,
		EncryptAES: false,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	hash := m.EncryptURI("https://example.com/a.m3u8", "", "", false, false, false)
	m.mu.Lock()
	v := m.links[hash]
	v.Expires = time.Now().Add(-time.Minute)
	m.links[hash] = v
	m.mu.Unlock()

	if m.Decrypt(hash, "") != nil {
		t.Fatalf("expected expired hash to fail")
	}
}

// TestSharedSecretDeterministic verifies two managers built with the same
// shared_secret produce identical AES tokens (cross-node interop).
func TestSharedSecretDeterministic(t *testing.T) {
	secret := "test-shared-secret-deterministic"
	m1, err := New(Options{VerifyIP: false, EncryptAES: true, SharedSecret: secret})
	if err != nil {
		t.Fatalf("m1 new: %v", err)
	}
	m2, err := New(Options{VerifyIP: false, EncryptAES: true, SharedSecret: secret})
	if err != nil {
		t.Fatalf("m2 new: %v", err)
	}
	hash := m1.EncryptURI("https://example.com/stream.m3u8", "", "ka", false, false, false)
	if hash == "" {
		t.Fatalf("empty hash")
	}
	// m2 must decode token minted by m1.
	got := m2.Decrypt(hash, "")
	if got == nil {
		t.Fatalf("m2 failed to decrypt m1's token — shared_secret not deterministic")
	}
	if got.URI != "https://example.com/stream.m3u8" {
		t.Fatalf("uri mismatch: %s", got.URI)
	}
}

// TestReloadRotatesKey verifies that Manager.Reload swaps AES material
// — tokens minted before reload should NOT decode after reload to a
// different shared_secret, and a fresh token must decode.
func TestReloadRotatesKey(t *testing.T) {
	dir := t.TempDir()
	m, err := New(Options{CacheDir: dir, EncryptAES: true, SharedSecret: "secret-A"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	oldHash := m.EncryptURI("https://example.com/old.m3u8", "", "p", false, false, false)
	if m.Decrypt(oldHash, "") == nil {
		t.Fatalf("baseline decrypt failed")
	}

	// Rotate to a different secret.
	if err := m.Reload(Options{CacheDir: dir, EncryptAES: true, SharedSecret: "secret-B"}); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// Old token must no longer decrypt as the AES payload (link map may still
	// have it cached, but the AES path will fail to recover the URI).
	if got := m.Decrypt(oldHash, ""); got != nil && got.URI == "https://example.com/old.m3u8" {
		// If it still works it's because the link is in the in-memory cache.
		// Drop the cache and retry — pure AES decode must fail with new key.
		m.mu.Lock()
		m.links = make(map[string]Model)
		m.mu.Unlock()
		if m.Decrypt(oldHash, "") != nil {
			t.Fatalf("old token decoded under new key — rotation broken")
		}
	}

	// Fresh token under new key must work.
	newHash := m.EncryptURI("https://example.com/new.m3u8", "", "p", false, false, false)
	if got := m.Decrypt(newHash, ""); got == nil || got.URI != "https://example.com/new.m3u8" {
		t.Fatalf("post-reload encrypt/decrypt broken")
	}
}

// TTLHours ставит срок токенам БЕЗ привязки к IP — тем самым, что раньше жили
// вечно и утекли в чужой плейлист (кража экспорта 2026-08-31). Проверяем оба
// пути выдачи: с заголовками (EncryptURIWithHeaders) и без них.
func TestTTLStampsExpiryOnNonIPBoundTokens(t *testing.T) {
	m, err := New(Options{CacheDir: t.TempDir(), EncryptAES: true, TTLHours: 36})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	plain := m.EncryptURI("https://example.com/live.m3u8", "1.1.1.1", "iptv", false, false, false)
	withHdr := m.EncryptURIWithHeaders("https://example.com/live.m3u8", "1.1.1.1", "iptv",
		map[string]string{"User-Agent": "VLC"})

	for name, hash := range map[string]string{"plain": plain, "headers": withHdr} {
		model := m.Decrypt(hash, "1.1.1.1")
		if model == nil {
			t.Fatalf("%s: decrypt returned nil", name)
		}
		if model.Expires.IsZero() {
			t.Fatalf("%s: token has no expiry — leaked link would live forever", name)
		}
		if d := time.Until(model.Expires); d < 35*time.Hour || d > 37*time.Hour {
			t.Fatalf("%s: expiry %v is not ~36h out", name, d)
		}
	}
}

// Протухший токен не должен играть — иначе TTL бесполезен.
func TestExpiredAESTokenIsRejected(t *testing.T) {
	m, err := New(Options{CacheDir: t.TempDir(), EncryptAES: true, TTLHours: 1})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	hash := m.EncryptURIWithHeaders("https://example.com/live.m3u8", "1.1.1.1", "iptv",
		map[string]string{"User-Agent": "VLC"})

	// Выдаём токен с микро-сроком и даём ему истечь. (Отрицательный TTL не
	// подошёл бы: expiry() считает ttl<=0 запросом «без срока».)
	m.ttlNanos.Store(int64(time.Nanosecond))
	stale := m.EncryptURIWithHeaders("https://example.com/live.m3u8", "1.1.1.1", "iptv",
		map[string]string{"User-Agent": "VLC"})
	time.Sleep(2 * time.Millisecond)

	if m.Decrypt(hash, "1.1.1.1") == nil {
		t.Fatalf("свежий токен должен играть")
	}
	if m.Decrypt(stale, "1.1.1.1") != nil {
		t.Fatalf("истёкший токен принят — утёкшая ссылка продолжит играть")
	}
}

// TTLHours=0 — прежнее поведение: срок не ставится (нужно для постеров и
// совместимости с инсталляциями, где ротация ссылок нежелательна).
func TestZeroTTLKeepsTokensEternal(t *testing.T) {
	m, err := New(Options{CacheDir: t.TempDir(), EncryptAES: true})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	model := m.Decrypt(m.EncryptURI("https://example.com/a.m3u8", "1.1.1.1", "iptv", false, false, false), "1.1.1.1")
	if model == nil {
		t.Fatalf("decrypt returned nil")
	}
	if !model.Expires.IsZero() {
		t.Fatalf("TTLHours=0 не должен ставить срок, получили %v", model.Expires)
	}
}

// Добивание украденных ссылок: у них поля E нет вовсе (выданы до TTL), а пустое
// E = вечное. Плагины из RequireTTLPlugins такие токены не принимают — при этом
// ссылки прочих источников (фильмы) продолжают играть.
func TestEternalTokenRejectedOnlyForListedPlugins(t *testing.T) {
	// Менеджер БЕЗ TTL — так выдавались токены до фикса.
	old, err := New(Options{CacheDir: t.TempDir(), EncryptAES: true, SharedSecret: "s3cret"})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	stolenIPTV := old.EncryptURIWithHeaders("https://cdn/live.m3u8", "1.1.1.1", "iptv",
		map[string]string{"User-Agent": "VLC"})
	movie := old.EncryptURIWithHeaders("https://cdn/movie.mp4", "1.1.1.1", "kinopub",
		map[string]string{"User-Agent": "VLC"})

	if old.Decrypt(stolenIPTV, "1.1.1.1") == nil {
		t.Fatalf("до включения списка бессрочный токен должен играть")
	}

	// Тот же ключ (shared_secret), но теперь iptv требует срок.
	guarded, err := New(Options{CacheDir: t.TempDir(), EncryptAES: true, SharedSecret: "s3cret",
		TTLHours: 36, RequireTTLPlugins: []string{"iptv", "iptv-ru"}})
	if err != nil {
		t.Fatalf("new guarded: %v", err)
	}

	if guarded.Decrypt(stolenIPTV, "1.1.1.1") != nil {
		t.Fatalf("украденный бессрочный iptv-токен принят")
	}
	if guarded.Decrypt(movie, "1.1.1.1") == nil {
		t.Fatalf("ссылка непрофильного плагина не должна пострадать")
	}
	// Свежий iptv-токен уже со сроком — играет.
	fresh := guarded.EncryptURIWithHeaders("https://cdn/live.m3u8", "1.1.1.1", "iptv",
		map[string]string{"User-Agent": "VLC"})
	if guarded.Decrypt(fresh, "1.1.1.1") == nil {
		t.Fatalf("свежий токен со сроком должен играть")
	}
	if r := guarded.DebugDecrypt(stolenIPTV, "1.1.1.1"); !strings.Contains(r, "eternal") {
		t.Fatalf("диагностика должна называть причину, получили: %s", r)
	}
}
