package tgauth

import (
	"testing"
	"time"
)

func proofStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	_ = s.Add(ApprovedToken{Token: "tok-A", TelegramID: 1, ExpiresAt: exp})
	_ = s.Add(ApprovedToken{Token: "tok-B", TelegramID: 2, ExpiresAt: exp})
	now := time.Now().UTC()
	if _, err := s.AddDevice("tok-A", DeviceInfo{UID: "a1", BoundAt: now, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDevice("tok-B", DeviceInfo{UID: "b1", BoundAt: now, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	return s
}

// Ключ выдаётся один раз и потом не меняется: иначе клиент, сохранивший его,
// начал бы получать отказы после каждого обращения к /tg/auth/status.
func TestProofKeyIsStable(t *testing.T) {
	s := proofStore(t)
	k1, ok := s.EnsureProofKey("tok-A", "a1")
	if !ok || len(k1) != 64 {
		t.Fatalf("ключ не выдан или неверной длины: %q ok=%v", k1, ok)
	}
	k2, _ := s.EnsureProofKey("tok-A", "a1")
	if k1 != k2 {
		t.Fatal("ключ сменился при повторном запросе")
	}
	if got, ok := s.ProofKeyOf("a1"); !ok || got != k1 {
		t.Fatalf("проверяющая сторона видит другой ключ: %q", got)
	}
}

// У разных устройств ключи разные — иначе кража одного вскрывала бы всех, то
// есть мы вернулись бы к общему секрету.
func TestProofKeysAreDistinctPerDevice(t *testing.T) {
	s := proofStore(t)
	a, _ := s.EnsureProofKey("tok-A", "a1")
	b, _ := s.EnsureProofKey("tok-B", "b1")
	if a == "" || b == "" || a == b {
		t.Fatalf("ключи совпали или пусты: %q / %q", a, b)
	}
}

// Ключ не выдаётся тому, кто назвал произвольный uid или чужой токен: иначе
// любой желающий получал бы валидную аттестацию, просто попросив её.
func TestProofKeyNotIssuedWithoutRealDevice(t *testing.T) {
	s := proofStore(t)
	if _, ok := s.EnsureProofKey("tok-A", "выдуманный"); ok {
		t.Fatal("ключ выдан несуществующему устройству")
	}
	if _, ok := s.EnsureProofKey("tok-A", "b1"); ok {
		t.Fatal("ключ выдан для устройства ЧУЖОГО токена")
	}
	if _, ok := s.EnsureProofKey("нет-такого-токена", "a1"); ok {
		t.Fatal("ключ выдан по несуществующему токену")
	}
}

// Проверяющая сторона ключей не создаёт: доверие не должно рождаться из
// самой проверки.
func TestVerifierDoesNotMintKeys(t *testing.T) {
	s := proofStore(t)
	if _, ok := s.ProofKeyOf("a1"); ok {
		t.Fatal("ключ существует до выдачи")
	}
	if _, ok := s.ProofKeyOf("вообще-неизвестный"); ok {
		t.Fatal("ключ найден у неизвестного устройства")
	}
}

// Отвязка устройства обязана убивать его ключ, иначе «отвязать» не значит
// ничего: снятая с устройства строка продолжала бы подписывать запросы.
func TestRevokingDeviceKillsItsKey(t *testing.T) {
	s := proofStore(t)
	key, _ := s.EnsureProofKey("tok-A", "a1")
	if key == "" {
		t.Fatal("ключ не выдан")
	}
	if err := s.RemoveDevice("tok-A", "a1"); err != nil {
		t.Fatalf("RemoveDevice: %v", err)
	}
	if got, ok := s.ProofKeyOf("a1"); ok {
		t.Fatalf("ключ отвязанного устройства всё ещё действует: %q", got)
	}
}
