package iptvhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func prefsEnv(t *testing.T) (*iptvPrefStore, http.HandlerFunc) {
	t.Helper()
	st := newIptvPrefStore(t.TempDir())
	h := iptvPrefsHandler(st, func(*http.Request) int64 { return 7 })
	return st, h
}

// Настройки должны переживать перезапуск: раньше список скрытых стран жил в
// localStorage браузера и пропадал вместе с кэшем, а на втором устройстве его
// не было вовсе.
func TestPrefsRoundTrip(t *testing.T) {
	st, h := prefsEnv(t)
	body := `{"hide_builtin":true,"hidden_countries":["ru","ua"],"hidden_genres":["news"],"channel_view":"list"}`
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/iptv/prefs", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("запись: %d", rec.Code)
	}
	got := st.get(7)
	if !got.HideBuiltin || got.ChannelView != "list" ||
		len(got.HiddenCountries) != 2 || len(got.HiddenGenres) != 1 {
		t.Fatalf("не сохранилось: %+v", got)
	}
	// Перечитываем с диска новым стором — именно это и есть «переживает перезапуск».
	again := newIptvPrefStore(strings.TrimSuffix(st.path, "/database/iptv_prefs.json"))
	if again.get(7).ChannelView != "list" {
		t.Fatalf("после перечтения настройка потеряна: %+v", again.get(7))
	}
}

// Мусор в списках не храним: пустые строки и повторы потом сравниваются на
// клиенте и дают «скрыто, но всё равно видно».
func TestPrefsNormalisesLists(t *testing.T) {
	st, h := prefsEnv(t)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/iptv/prefs",
		strings.NewReader(`{"hidden_countries":["ru","","ru"," ua ","ua"],"channel_view":"ерунда"}`)))
	got := st.get(7)
	if len(got.HiddenCountries) != 2 || got.HiddenCountries[0] != "ru" || got.HiddenCountries[1] != "ua" {
		t.Fatalf("список не нормализован: %+v", got.HiddenCountries)
	}
	if got.ChannelView != "" {
		t.Fatalf("принят выдуманный вид списка: %q", got.ChannelView)
	}
}

// Без авторизации настройки не отдаём и не пишем: иначе один аноним затирал бы
// их всем остальным (ключ 0 общий).
func TestPrefsRequireAuth(t *testing.T) {
	st := newIptvPrefStore(t.TempDir())
	h := iptvPrefsHandler(st, func(*http.Request) int64 { return 0 })
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/iptv/prefs", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("аноним получил настройки: %d", rec.Code)
	}
}

// Папки избранного: сохраняются, чистятся от мусора и не теряют каналы при
// совпавших именах.
func TestFavFoldersRoundTripAndCleanup(t *testing.T) {
	st, h := prefsEnv(t)
	body := `{"fav_folders":[
		{"name":" Спорт ","ids":["a","b","a"]},
		{"name":"Спорт","ids":["c"]},
		{"name":"   ","ids":["x"]},
		{"name":"Пустая","ids":[]}
	]}`
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/iptv/prefs", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("запись: %d", rec.Code)
	}
	f := st.get(7).FavFolders
	if len(f) != 2 {
		t.Fatalf("ожидали «Спорт» и «Пустая», получили %d: %+v", len(f), f)
	}
	if f[0].Name != "Спорт" {
		t.Fatalf("имя не обрезано от пробелов: %q", f[0].Name)
	}
	// Две «Спорт» слиты, а НЕ отброшены: терять каналы из-за совпавшего имени нельзя.
	if len(f[0].IDs) != 3 {
		t.Fatalf("каналы слитых папок потеряны: %+v", f[0].IDs)
	}
	// Папка без имени выброшена целиком.
	for _, x := range f {
		if strings.TrimSpace(x.Name) == "" {
			t.Fatal("безымянная папка сохранена")
		}
	}
}

// Слишком длинное имя режем, а не отвергаем: человек не должен терять папку
// из-за того, что промахнулся с длиной.
func TestFavFolderNameTrimmed(t *testing.T) {
	st, h := prefsEnv(t)
	long := strings.Repeat("я", 100)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/iptv/prefs",
		strings.NewReader(`{"fav_folders":[{"name":"`+long+`","ids":["a"]}]}`)))
	f := st.get(7).FavFolders
	if len(f) != 1 || len([]rune(f[0].Name)) != maxFolderNameRune {
		t.Fatalf("имя не обрезано: %+v", f)
	}
}
