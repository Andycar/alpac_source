// Package edgeprefs хранит список нод, которые зритель отключил у себя.
//
// Зачем на сервере, а не в приложении: отключать ноды можно из двух мест — из бота и с экрана
// «Проверка связи» на телевизоре. Держи список только в приложении, и эти два места разъехались
// бы уже на второй правке. Поэтому источник истины один, на аккаунт, а параметр `edge_skip=` в
// ссылке остаётся транспортом и ручным переопределением.
//
// Список применяется в момент СБОРКИ ссылки (там запрос авторизован и зритель известен), после
// чего едет в сегменты сам — плеер за токеном в /proxy уже не ходит, и спросить его там не у кого.
package edgeprefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

// Store — отключённые ноды по телеграм-аккаунту. Пишется целиком: записей мало (по одной на
// зрителя, который вообще трогал настройку), а править их можно из бота и с телевизора разом.
type Store struct {
	mu       sync.RWMutex
	skip     map[int64][]string
	pin      map[int64]string
	filePath string
}

// prefsFile — формат на диске. Раньше значением был просто список отключённых; такие записи
// читаются по-прежнему (см. load), иначе настройки зрителей пропали бы при обновлении.
type prefsFile struct {
	Skip []string `json:"skip,omitempty"`
	Pin  string   `json:"pin,omitempty"`
}

// New поднимает хранилище из database/cluster/user_edge_skip.json внутри dbDir.
func New(dbDir string) *Store {
	s := &Store{
		skip:     make(map[int64][]string),
		pin:      make(map[int64]string),
		filePath: filepath.Join(dbDir, "database", "cluster", "user_edge_skip.json"),
	}
	s.load()
	return s
}

// Normalize приводит адрес к виду, в котором адреса сравниваются: без пробелов, в нижнем
// регистре, без хвостового слэша. Ровно то же делает parseEdgeSkip в proxyapi — иначе «выключил
// в боте» и «пришло в ссылке» перестали бы совпадать.
func Normalize(raw string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(raw)), "/")
}

// Get отдаёт отключённые ноды зрителя. Копия: вызывающий волен её сортировать и дописывать.
func (s *Store) Get(tgID int64) []string {
	if s == nil || tgID == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.skip[tgID]
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	copy(out, list)
	return out
}

// Has — отключена ли конкретная нода.
func (s *Store) Has(tgID int64, edge string) bool {
	if s == nil || tgID == 0 {
		return false
	}
	want := Normalize(edge)
	if want == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.skip[tgID] {
		if h == want {
			return true
		}
	}
	return false
}

// Set заменяет список целиком. Пустой список стирает запись, чтобы файл не копил зрителей,
// которые включили всё обратно.
func (s *Store) Set(tgID int64, edges []string) {
	if s == nil || tgID == 0 {
		return
	}
	clean := make([]string, 0, len(edges))
	seen := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		n := Normalize(e)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		clean = append(clean, n)
	}
	sort.Strings(clean)

	s.mu.Lock()
	if len(clean) == 0 {
		delete(s.skip, tgID)
	} else {
		s.skip[tgID] = clean
	}
	// Отключил ту, что была закреплена — закрепление снимается: держать его значило бы
	// предпочитать ноду, на которую сам же запретил ходить.
	if p := s.pin[tgID]; p != "" {
		for _, h := range clean {
			if h == p {
				delete(s.pin, tgID)
				break
			}
		}
	}
	s.mu.Unlock()
	s.save()
}

// Toggle переключает одну ноду и возвращает, отключена ли она теперь.
func (s *Store) Toggle(tgID int64, edge string) bool {
	if s == nil || tgID == 0 {
		return false
	}
	want := Normalize(edge)
	if want == "" {
		return false
	}
	cur := s.Get(tgID)
	next := make([]string, 0, len(cur)+1)
	found := false
	for _, h := range cur {
		if h == want {
			found = true
			continue
		}
		next = append(next, h)
	}
	if !found {
		next = append(next, want)
	}
	s.Set(tgID, next)
	return !found
}

// Pin — нода, закреплённая зрителем вручную («всегда через эту»), или пусто.
//
// Это ПРЕДПОЧТЕНИЕ, а не запрет: сервер уважает его, пока нода жива, и спокойно уходит на
// другую, когда она не отвечает. Иначе закрепление превращалось бы в обрыв просмотра при
// первой же неполадке — а зритель просил «лучше отсюда», а не «только отсюда или никак».
func (s *Store) Pin(tgID int64) string {
	if s == nil || tgID == 0 {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pin[tgID]
}

// SetPin закрепляет ноду. Пустая строка снимает закрепление.
//
// Закрепить отключённую ноду нельзя: это прямое противоречие, и молча предпочесть одно другому
// значило бы соврать зрителю в интерфейсе. Такой вызов СНИМАЕТ отключение — человек явно сказал,
// что хочет именно её.
func (s *Store) SetPin(tgID int64, edge string) {
	if s == nil || tgID == 0 {
		return
	}
	want := Normalize(edge)
	s.mu.Lock()
	if want == "" {
		delete(s.pin, tgID)
	} else {
		s.pin[tgID] = want
		if list := s.skip[tgID]; len(list) > 0 {
			next := list[:0:0]
			for _, h := range list {
				if h != want {
					next = append(next, h)
				}
			}
			if len(next) == 0 {
				delete(s.skip, tgID)
			} else {
				s.skip[tgID] = next
			}
		}
	}
	s.mu.Unlock()
	s.save()
}

// Join собирает список в вид параметра `edge_skip=`.
func (s *Store) Join(tgID int64) string {
	return strings.Join(s.Get(tgID), ",")
}

func (s *Store) load() {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return // файла ещё нет — нормальное первое состояние
	}
	// Текущий формат: {"12345":{"skip":["https://a"],"pin":"https://b"}}
	// Старый:          {"12345":["https://a","https://b"]}
	// Читаем оба — иначе обновление стёрло бы настройки всех, кто успел что-то отключить.
	raw := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Warn().Err(err).Msg("edgeprefs: не разобрал user_edge_skip.json")
		return
	}
	for k, v := range raw {
		id, err := strconv.ParseInt(k, 10, 64)
		if err != nil || id == 0 {
			continue
		}
		var f prefsFile
		if err := json.Unmarshal(v, &f); err != nil {
			var legacy []string
			if err2 := json.Unmarshal(v, &legacy); err2 != nil {
				continue
			}
			f.Skip = legacy
		}
		clean := make([]string, 0, len(f.Skip))
		for _, e := range f.Skip {
			if n := Normalize(e); n != "" {
				clean = append(clean, n)
			}
		}
		if len(clean) > 0 {
			s.skip[id] = clean
		}
		if pin := Normalize(f.Pin); pin != "" {
			s.pin[id] = pin
		}
	}
}

func (s *Store) save() {
	s.mu.RLock()
	raw := make(map[string]prefsFile, len(s.skip)+len(s.pin))
	for id, list := range s.skip {
		raw[strconv.FormatInt(id, 10)] = prefsFile{Skip: list}
	}
	for id, pin := range s.pin {
		k := strconv.FormatInt(id, 10)
		f := raw[k]
		f.Pin = pin
		raw[k] = f
	}
	s.mu.RUnlock()

	data, err := json.Marshal(raw)
	if err != nil {
		return
	}
	if dir := filepath.Dir(s.filePath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	// Через временный файл: оборванная запись оставила бы битый JSON, который молча подняли бы
	// при следующем старте и потеряли настройки всех зрителей разом.
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Warn().Err(err).Msg("edgeprefs: не смог записать user_edge_skip.json")
		return
	}
	if err := os.Rename(tmp, s.filePath); err != nil {
		_ = os.Remove(tmp)
	}
}
