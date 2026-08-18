package litesrc

import (
	"context"
	"testing"
	"time"
)

// Инцидент 2026-08-05: hash переиспользовался бессрочно, протух — и весь filmix (HDR, 1080p, SDR)
// стал отвечать 403. Перевыпуск же отдавал АНОНИМНЫЙ hash, потому что access оставался от прежнего.
func TestAccessTokenIsBoundToItsHash(t *testing.T) {
	f := &filmixChecker{fxUser: "u", fxPasswd: "p"}
	if !f.fxStoreAuth(`{"accessToken":"A1","refreshToken":"R1"}`, "hash-1") {
		t.Fatal("токен должен сохраниться")
	}
	if f.fxAccessHash != "hash-1" {
		t.Fatalf("access обязан помнить свой hash, получено %q", f.fxAccessHash)
	}
	// Состояние переживает перезапись: refresh под новый hash перепривязывает токен.
	if !f.fxStoreAuth(`{"accessToken":"A2"}`, "hash-2") {
		t.Fatal("повторное сохранение должно проходить")
	}
	if f.fxAccessHash != "hash-2" || f.fxAccess != "A2" {
		t.Fatalf("привязка не обновилась: hash=%q access=%q", f.fxAccessHash, f.fxAccess)
	}
	// refreshToken не затирается пустым значением — иначе следующий цикл уйдёт в полный логин,
	// а тот стоит слота устройства (лимит 5).
	if f.fxRefresh != "R1" {
		t.Fatalf("refresh не должен теряться, получено %q", f.fxRefresh)
	}
}

// ★Ротация обязана быть РЕАКТИВНОЙ. Ежечасная (2026-08-05) дала 5 логинов за 3 часа: каждый занимает
// слот устройства из пяти, новая сессия вытесняет прежнюю, и ссылки у смотрящих отдают 403.
func TestHashReusedWhileSessionAlive(t *testing.T) {
	f := &filmixChecker{fxUser: "u", fxPasswd: "p"}
	f.fxStateLoaded = true
	f.fxHash = "live-hash"
	f.fxHashAt = time.Now().Add(-72 * time.Hour) // возраст сам по себе не повод к перевыпуску

	if got := f.fxEnsureHash(context.Background()); got != "live-hash" {
		t.Fatalf("живой hash обязан переиспользоваться независимо от возраста, получено %q", got)
	}

	// Провал проверки живости — единственное, что открывает дорогу перевыпуску.
	f.fxHashDead = true
	f.fxHashFetchedAt = time.Now() // кулдаун ещё держит: отдаём прежний, а не пустоту
	if got := f.fxEnsureHash(context.Background()); got != "live-hash" {
		t.Fatalf("при неудачном перевыпуске старый hash не должен теряться, получено %q", got)
	}
}

// Проверка живости не должна ходить в сеть чаще, чем раз в fxAliveProbeTTL, и не должна трогать
// уже помеченную мёртвой сессию (иначе перевыпуск зациклится).
func TestVerifyAliveThrottled(t *testing.T) {
	f := &filmixChecker{fxUser: "u", fxPasswd: "p", fxHost: "https://api.example.invalid"}
	f.fxAliveAt = time.Now()
	f.fxVerifyAlive(context.Background(), "h", "a") // недавняя проверка → выходит без запроса
	if f.fxHashDead {
		t.Fatal("троттлинг не сработал: сессия помечена мёртвой без проверки")
	}
	f.fxHashDead = true
	f.fxAliveAt = time.Time{}
	f.fxVerifyAlive(context.Background(), "h", "a") // уже мертва → повторно не проверяем
	if !f.fxHashDead {
		t.Fatal("флаг смерти не должен сбрасываться проверкой")
	}
	// Без hash или access проверять нечего — и падать тоже нельзя.
	f.fxVerifyAlive(context.Background(), "", "")
}
