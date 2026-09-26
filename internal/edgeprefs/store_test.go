package edgeprefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToggleAndPersist(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	if got := s.Get(42); got != nil {
		t.Fatalf("новый зритель должен быть без запретов, а пришло %v", got)
	}
	if !s.Toggle(42, "https://edge-b.example.net:2053") {
		t.Fatal("первое нажатие обязано отключить ноду")
	}
	if !s.Has(42, "https://edge-b.example.net:2053") {
		t.Fatal("нода не отметилась отключённой")
	}
	// Регистр и хвостовой слэш — то же самое: в боте и в ссылке адрес приходит по-разному.
	if !s.Has(42, "HTTPS://EDGE-B.EXAMPLE.NET:2053/") {
		t.Fatal("сравнение адресов должно игнорировать регистр и слэш")
	}
	if s.Toggle(42, "https://edge-b.example.net:2053") {
		t.Fatal("повторное нажатие обязано включить ноду обратно")
	}
	if s.Has(42, "https://edge-b.example.net:2053") {
		t.Fatal("нода осталась отключённой после возврата")
	}

	// Настройка одного зрителя не должна протекать к другому.
	s.Toggle(42, "https://torr.example.com")
	if s.Has(7, "https://torr.example.com") {
		t.Fatal("запрет протёк на чужой аккаунт")
	}

	// Переживает перезапуск: файл — единственное, что связывает бот и телевизор.
	s2 := New(dir)
	if !s2.Has(42, "https://torr.example.com") {
		t.Fatal("после перезапуска запрет потерялся")
	}
	if _, err := os.Stat(filepath.Join(dir, "database", "cluster", "user_edge_skip.json")); err != nil {
		t.Fatalf("файл настроек не создан: %v", err)
	}
}

func TestSetDeduplicatesAndClears(t *testing.T) {
	s := New(t.TempDir())
	s.Set(1, []string{"https://a", "HTTPS://A/", " ", "https://b"})
	got := s.Get(1)
	if len(got) != 2 {
		t.Fatalf("повторы и пустые адреса должны отсеиваться, получено %v", got)
	}
	if s.Join(1) != "https://a,https://b" {
		t.Fatalf("Join собрал не то: %q", s.Join(1))
	}
	// Пустой список стирает запись целиком, иначе файл копил бы зрителей, включивших всё обратно.
	s.Set(1, nil)
	if got := s.Get(1); got != nil {
		t.Fatalf("после очистки должно быть пусто, а там %v", got)
	}
}

func TestZeroIDIgnored(t *testing.T) {
	s := New(t.TempDir())
	// Неавторизованный запрос — tgID = 0. Такие настройки некуда записывать, и молчаливая
	// запись под нулём смешала бы всех гостей в одну кучу.
	s.Set(0, []string{"https://a"})
	if s.Toggle(0, "https://a") || s.Has(0, "https://a") || s.Get(0) != nil {
		t.Fatal("нулевой аккаунт не должен ничего хранить")
	}
}

func TestPinBasics(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if s.Pin(42) != "" {
		t.Fatal("у нового зрителя закрепления быть не должно")
	}
	s.SetPin(42, "HTTPS://TORR.EXAMPLE.COM/")
	if got := s.Pin(42); got != "https://torr.example.com" {
		t.Fatalf("адрес не нормализован: %q", got)
	}
	// Переживает перезапуск — иначе выбор терялся бы при каждом обновлении.
	if s2 := New(dir); s2.Pin(42) != "https://torr.example.com" {
		t.Fatalf("после перезапуска закрепление потерялось: %q", s2.Pin(42))
	}
	s.SetPin(42, "")
	if s.Pin(42) != "" {
		t.Fatal("пустая строка обязана снимать закрепление")
	}
}

// Закрепить отключённую ноду — прямое противоречие. Человек явно сказал, что хочет её:
// закрепление побеждает, отключение снимается.
func TestPinClearsSkipOfSameNode(t *testing.T) {
	s := New(t.TempDir())
	s.Set(1, []string{"https://a", "https://b"})
	s.SetPin(1, "https://a")
	if s.Has(1, "https://a") {
		t.Fatal("закреплённая нода осталась отключённой")
	}
	if !s.Has(1, "https://b") {
		t.Fatal("чужое отключение задето")
	}
}

// И наоборот: отключил закреплённую — закрепление снимается, иначе сервер предпочитал бы ноду,
// на которую сам же запретил ходить.
func TestSkipDropsPinOfSameNode(t *testing.T) {
	s := New(t.TempDir())
	s.SetPin(1, "https://a")
	s.Set(1, []string{"https://a"})
	if s.Pin(1) != "" {
		t.Fatalf("закрепление пережило отключение той же ноды: %q", s.Pin(1))
	}
}

// Старый формат файла (просто список) обязан читаться: иначе обновление стёрло бы настройки.
func TestLegacyFormatStillReadable(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "database", "cluster"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "database", "cluster", "user_edge_skip.json")
	if err := os.WriteFile(f, []byte(`{"77":["https://old.example"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(dir)
	if !s.Has(77, "https://old.example") {
		t.Fatal("старый формат не прочитался")
	}
	// И дописывается уже в новом, не теряя прежнего.
	s.SetPin(77, "https://new.example")
	s2 := New(dir)
	if !s2.Has(77, "https://old.example") || s2.Pin(77) != "https://new.example" {
		t.Fatalf("после перезаписи потерялось: skip=%v pin=%q", s2.Get(77), s2.Pin(77))
	}
}

func TestPinIsolatedPerAccount(t *testing.T) {
	s := New(t.TempDir())
	s.SetPin(1, "https://a")
	if s.Pin(2) != "" {
		t.Fatal("закрепление протекло на чужой аккаунт")
	}
	s.SetPin(0, "https://a")
	if s.Pin(0) != "" {
		t.Fatal("неавторизованному хранить закрепление негде")
	}
}
