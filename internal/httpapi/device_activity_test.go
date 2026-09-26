package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// Отметка активности должна срабатывать на том виде запроса, который РЕАЛЬНО шлёт клиент:
// подписанный /capi с uid в query и токеном в куке. Тест писался после того, как выкатка на
// прод не сдвинула ни одной отметки: нужно было отделить «код не работает» от «до кода не
// доходит».
func TestTouchDeviceActivityMarksBoundDevice(t *testing.T) {
	devActSeen = map[string]devActMark{} // троттлинг глобальный — изолируем тест

	st := tgauth.NewStore(t.TempDir())
	const tok, uid = "tok-abc", "dev-123"
	if err := st.Add(tgauth.ApprovedToken{
		Token:     tok,
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Devices:   []tgauth.DeviceInfo{{UID: uid, Label: "TV", BoundAt: time.Now()}},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/capi/profiles?uid="+uid, nil)
	r.AddCookie(&http.Cookie{Name: "lampac_token", Value: tok})
	touchDeviceActivity(st, r)

	at, ok := st.Lookup(tok)
	if !ok {
		t.Fatal("токен пропал из стора")
	}
	if len(at.Devices) != 1 || at.Devices[0].LastSeen.IsZero() {
		t.Fatalf("LastSeen не проставлен: %+v", at.Devices)
	}
}

// Второй запрос в ту же минуту не должен трогать стор — иначе десятки запросов клиента в
// минуту превращаются в десятки блокировок стора.
func TestTouchDeviceActivityThrottles(t *testing.T) {
	devActSeen = map[string]devActMark{}
	now := time.Now().UTC()
	if touch, _ := devActShould("u1", now); !touch {
		t.Fatal("первый вызов обязан пройти")
	}
	if touch, _ := devActShould("u1", now.Add(10*time.Second)); touch {
		t.Fatal("вызов через 10 секунд обязан быть отброшен")
	}
	if touch, _ := devActShould("u1", now.Add(2*time.Minute)); !touch {
		t.Fatal("через две минуты снова можно")
	}
}

// Пройти ИМЕННО через middleware: unit-тест функции ничего не говорит о том, доходит ли до неё
// запрос. Выкатка на прод не сдвинула ни одной отметки при 116 подходящих запросах за 10 минут —
// значит ломается либо резолв токена, либо сам путь до вызова.
func TestGateTouchesActivityOnCapi(t *testing.T) {
	devActSeen = map[string]devActMark{}
	st := tgauth.NewStore(t.TempDir())
	const tok, uid = "tok-gate", "dev-gate"
	if err := st.Add(tgauth.ApprovedToken{
		Token:     tok,
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Devices:   []tgauth.DeviceInfo{{UID: uid, BoundAt: time.Now()}},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	mw := tgAuthGateMiddleware(st, nil, nil, nil, nil, nil, nil, config.Config{})
	reached := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))

	r := httptest.NewRequest(http.MethodGet, "/capi/profiles?uid="+uid, nil)
	r.AddCookie(&http.Cookie{Name: "lampac_token", Value: tok})
	h.ServeHTTP(httptest.NewRecorder(), r)

	if !reached {
		t.Fatal("гейт не пропустил /capi дальше")
	}
	at, _ := st.Lookup(tok)
	if at == nil || len(at.Devices) == 0 || at.Devices[0].LastSeen.IsZero() {
		t.Fatalf("гейт не отметил активность: %+v", at)
	}
}
