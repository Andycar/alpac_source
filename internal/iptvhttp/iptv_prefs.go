package iptvhttp

import (
	"crypto/md5"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

// iptv_prefs.go — личные настройки IPTV пользователя.
//
// Пока настройка одна: «не показывать встроенные плейлисты». Человеку со своим
// списком каналов наши доноры и реестр только мешают — он их не выбирал, а они
// занимают половину экрана выбора. Настройка ЛИЧНАЯ: серверный registry_only
// выключил бы встроенные списки всем сразу.

type iptvPrefs struct {
	HideBuiltin bool `json:"hide_builtin"`
	// Скрытые категории выдачи. Раньше список скрытых стран жил в localStorage
	// БРАУЗЕРА: на телевизоре и на телефоне у одного человека получались разные
	// наборы, а после чистки кэша настройка пропадала совсем. Жанры не скрывались
	// вообще. Теперь и то и другое лежит на сервере и общее для всех устройств.
	HiddenCountries []string `json:"hidden_countries"`
	HiddenGenres    []string `json:"hidden_genres"`
	// ChannelView — «сетка» карточками или «таблица» строками. Таблица нужна там,
	// где каналов сотни: в сетке на экран влезает десяток плиток, а строкой —
	// втрое больше, и искать глазами проще.
	ChannelView string `json:"channel_view"` // "" | "grid" | "list"
	// SplitFavByGenre — разбивать «Избранное» и «Недавние» на жанры. У кого в
	// избранном три канала, тому это только лишние строки; у кого полторы сотни —
	// без разбивки список бесполезен. Поэтому настройка, а не поведение.
	SplitFavByGenre bool `json:"split_fav_by_genre"`
	// FavFolders — СВОИ папки внутри избранного. Список, а не карта: порядок задаёт
	// пользователь, и в карте он бы терялся при каждом чтении.
	FavFolders []favFolder `json:"fav_folders,omitempty"`
}

// favFolder — именованная папка избранного и id каналов в ней. Канал может лежать
// в нескольких папках сразу: это метки, а не перемещение, и «Спорт» с «Смотреть
// вечером» не должны отнимать канал друг у друга.
type favFolder struct {
	Name string   `json:"name"`
	IDs  []string `json:"ids"`
}

type iptvPrefStore struct {
	mu   sync.RWMutex
	path string
	m    map[string]iptvPrefs
}

func newIptvPrefStore(repoRoot string) *iptvPrefStore {
	s := &iptvPrefStore{
		path: filepath.Join(repoRoot, "database", "iptv_prefs.json"),
		m:    map[string]iptvPrefs{},
	}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = stdjson.Unmarshal(b, &s.m)
	}
	return s
}

// key хэширует id пользователя: в файле настроек незачем хранить телеграм-id
// открытым текстом, а для ключа достаточно стабильности.
func iptvPrefKey(tgID int64) string {
	h := md5.Sum(fmt.Appendf(nil, "iptvpref:%d", tgID))
	return fmt.Sprintf("%x", h[:8])
}

func (s *iptvPrefStore) get(tgID int64) iptvPrefs {
	if s == nil || tgID == 0 {
		return iptvPrefs{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[iptvPrefKey(tgID)]
}

func (s *iptvPrefStore) set(tgID int64, p iptvPrefs) {
	if s == nil || tgID == 0 {
		return
	}
	s.mu.Lock()
	s.m[iptvPrefKey(tgID)] = p
	data, err := stdjson.Marshal(s.m)
	s.mu.Unlock()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		log.Warn().Err(err).Str("path", s.path).Msg("iptv: не смог сохранить личные настройки")
	}
}

// iptvPrefsHandler — чтение и запись личных настроек IPTV.
func iptvPrefsHandler(prefs *iptvPrefStore, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, prefs.get(tgID))
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		var req iptvPrefs
		if err := stdjson.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
			return
		}
		// Нормализуем: пустые строки и дубликаты в списках — мусор, который
		// потом сравнивается на клиенте и даёт «скрыто, но видно».
		req.HiddenCountries = dedupNonEmpty(req.HiddenCountries)
		req.HiddenGenres = dedupNonEmpty(req.HiddenGenres)
		req.FavFolders = normalizeFolders(req.FavFolders)
		if req.ChannelView != "grid" && req.ChannelView != "list" {
			req.ChannelView = ""
		}
		prefs.set(tgID, req)
		writeJSON(w, http.StatusOK, req)
	}
}

// dedupNonEmpty убирает пустые строки и повторы, сохраняя порядок.
func dedupNonEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// Потолки на папки. Не защита от злого умысла (запись и так только своя), а страховка
// от клиента, который зациклился: файл настроек читается на каждый запрос списка, и
// раздувать его нечем.
const (
	maxFavFolders     = 50
	maxIDsPerFolder   = 500
	maxFolderNameRune = 40
)

// normalizeFolders чистит папки: пустые имена и пустые папки выбрасываются, дубликаты
// имён схлопываются (иначе в списке две «Спорт», и человек не понимает, в какую попал),
// id внутри папки дедуплицируются.
func normalizeFolders(in []favFolder) []favFolder {
	if len(in) == 0 {
		return nil
	}
	out := make([]favFolder, 0, len(in))
	byName := make(map[string]int, len(in))
	for _, f := range in {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			continue
		}
		if r := []rune(name); len(r) > maxFolderNameRune {
			name = string(r[:maxFolderNameRune])
		}
		ids := dedupNonEmpty(f.IDs)
		if len(ids) > maxIDsPerFolder {
			ids = ids[:maxIDsPerFolder]
		}
		if idx, dup := byName[name]; dup {
			// Слияние, а не отбрасывание: терять каналы из-за совпавшего имени нельзя.
			out[idx].IDs = dedupNonEmpty(append(out[idx].IDs, ids...))
			continue
		}
		if len(out) >= maxFavFolders {
			break
		}
		byName[name] = len(out)
		out = append(out, favFolder{Name: name, IDs: ids})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
