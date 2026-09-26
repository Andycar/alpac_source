package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"lampac-go/internal/edgeprefs"
)

func withEdgePrefs(t *testing.T, tgID int64, skip []string) {
	t.Helper()
	prev, prevFn := edgePrefsRef, edgePrefsTGIDFn
	store := edgeprefs.New(t.TempDir())
	store.Set(tgID, skip)
	edgePrefsRef = store
	edgePrefsTGIDFn = func(*http.Request) int64 { return tgID }
	t.Cleanup(func() { edgePrefsRef, edgePrefsTGIDFn = prev, prevFn })
}

// Настройка из бота обязана доехать до ноды: ссылку собирает она, а хранилища у неё нет.
func TestStampEdgeSkipAddsStoredList(t *testing.T) {
	withEdgePrefs(t, 42, []string{"https://edge-b.example.net:2053"})
	r := httptest.NewRequest(http.MethodGet, "/lite/rezka?title=%D0%A2%D0%B5%D1%81%D1%82&year=2020", nil)
	before := r.URL.RawQuery
	stampEdgeSkip(r)
	if r.URL.Query().Get("edge_skip") != "https://edge-b.example.net:2053" {
		t.Fatalf("запрет не дописан: %q", r.URL.RawQuery)
	}
	// Остальные параметры должны остаться байт в байт: пересборка запроса перекодировала бы
	// кириллицу и прочее там, где этого никто не просил.
	if got := r.URL.RawQuery[:len(before)]; got != before {
		t.Fatalf("хвост запроса переписан: было %q, стало %q", before, got)
	}
	if r.URL.Query().Get("title") != "Тест" {
		t.Fatalf("параметр title испортился: %q", r.URL.Query().Get("title"))
	}
}

// Список, присланный клиентом, главнее: дублировать его сохранённым нельзя.
func TestStampEdgeSkipKeepsClientList(t *testing.T) {
	withEdgePrefs(t, 42, []string{"https://edge-b.example.net:2053"})
	r := httptest.NewRequest(http.MethodGet, "/lite/rezka?edge_skip=https%3A%2F%2Ftorr.example.com", nil)
	stampEdgeSkip(r)
	if got := r.URL.Query()["edge_skip"]; len(got) != 1 || got[0] != "https://torr.example.com" {
		t.Fatalf("клиентский список подменён или задвоен: %v", got)
	}
}

// Неопознанный зритель и пустой список ничего не меняют.
func TestStampEdgeSkipNoopCases(t *testing.T) {
	withEdgePrefs(t, 0, nil)
	r := httptest.NewRequest(http.MethodGet, "/lite/rezka?a=1", nil)
	stampEdgeSkip(r)
	if r.URL.RawQuery != "a=1" {
		t.Fatalf("запрос изменили для неопознанного зрителя: %q", r.URL.RawQuery)
	}

	withEdgePrefs(t, 42, nil)
	r2 := httptest.NewRequest(http.MethodGet, "/lite/rezka?a=1", nil)
	stampEdgeSkip(r2)
	if r2.URL.RawQuery != "a=1" {
		t.Fatalf("пустой список не должен ничего дописывать: %q", r2.URL.RawQuery)
	}
}

// Запрос вообще без параметров не должен получить лишний «&» в начале.
func TestStampEdgeSkipEmptyQuery(t *testing.T) {
	withEdgePrefs(t, 42, []string{"https://torr.example.com"})
	r := httptest.NewRequest(http.MethodGet, "/lite/rezka", nil)
	stampEdgeSkip(r)
	if r.URL.RawQuery != "edge_skip=https%3A%2F%2Ftorr.example.com" {
		t.Fatalf("испорченный запрос: %q", r.URL.RawQuery)
	}
}

func withEdgePin(t *testing.T, tgID int64, pin string) {
	t.Helper()
	prev, prevFn := edgePrefsRef, edgePrefsTGIDFn
	store := edgeprefs.New(t.TempDir())
	store.SetPin(tgID, pin)
	edgePrefsRef = store
	edgePrefsTGIDFn = func(*http.Request) int64 { return tgID }
	t.Cleanup(func() { edgePrefsRef, edgePrefsTGIDFn = prev, prevFn })
}

// Закрепление должно доехать до ноды: выбор добытчика и выдача сегментов читают один и тот же
// параметр `edge=`, а нода своего хранилища настроек не имеет.
func TestStampEdgePinAddsPinned(t *testing.T) {
	withEdgePin(t, 42, "https://torr.example.com")
	r := httptest.NewRequest(http.MethodGet, "/lite/rezka?title=%D0%A2%D0%B5%D1%81%D1%82", nil)
	before := r.URL.RawQuery
	stampEdgePin(r)
	if got := r.URL.Query().Get("edge"); got != "https://torr.example.com" {
		t.Fatalf("закрепление не дописано: %q", r.URL.RawQuery)
	}
	if r.URL.RawQuery[:len(before)] != before {
		t.Fatal("хвост запроса переписан")
	}
	if r.URL.Query().Get("title") != "Тест" {
		t.Fatalf("параметр title испортился: %q", r.URL.Query().Get("title"))
	}
}

// Клиент прислал свой выбор — он только что нажал кнопку на этом устройстве, перебивать нельзя.
func TestStampEdgePinKeepsClientChoice(t *testing.T) {
	withEdgePin(t, 42, "https://torr.example.com")
	r := httptest.NewRequest(http.MethodGet, "/lite/rezka?edge=https%3A%2F%2Ftorr.example.com", nil)
	stampEdgePin(r)
	if got := r.URL.Query()["edge"]; len(got) != 1 || got[0] != "https://torr.example.com" {
		t.Fatalf("клиентский выбор подменён или задвоен: %v", got)
	}
}

// Без закрепления и без опознанного зрителя запрос не меняется.
func TestStampEdgePinNoop(t *testing.T) {
	withEdgePin(t, 42, "")
	r := httptest.NewRequest(http.MethodGet, "/lite/rezka?a=1", nil)
	stampEdgePin(r)
	if r.URL.RawQuery != "a=1" {
		t.Fatalf("запрос изменён без закрепления: %q", r.URL.RawQuery)
	}
	withEdgePin(t, 0, "https://torr.example.com")
	r2 := httptest.NewRequest(http.MethodGet, "/lite/rezka?a=1", nil)
	stampEdgePin(r2)
	if r2.URL.RawQuery != "a=1" {
		t.Fatalf("запрос изменён для неопознанного зрителя: %q", r2.URL.RawQuery)
	}
}
