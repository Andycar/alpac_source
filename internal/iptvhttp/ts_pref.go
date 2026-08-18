package iptvhttp

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ── настройка «Свой TorrServer» (аккаунтная, задаётся в мини-аппе бота) ──
//
// Вводить адрес на ТВ-клавиатуре — мучение (репорт: «выдаёт вписать адрес, но не пишет» на
// Samsung), поэтому адрес задаётся в Telegram-мини-аппе и хранится per-аккаунт; клиенты читают
// его по cookie тем же резолвером, что IPTV. Локальный ввод на устройстве остаётся и ГЛАВНЕЕ
// аккаунтного (клиент сам решает приоритет — сервер только хранит).

type tsPrefStore struct {
	path string
	mu   sync.Mutex
	m    map[string]string // md5("tspref:"+tgID) → url
}

func newTsPrefStore(repoRoot string) *tsPrefStore {
	s := &tsPrefStore{
		path: filepath.Join(repoRoot, "database", "torrserver_prefs.json"),
		m:    map[string]string{},
	}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.m)
	}
	return s
}

func tsPrefHash(tgID int64) string {
	h := md5.Sum(fmt.Appendf(nil, "tspref:%d", tgID))
	return hex.EncodeToString(h[:])
}

func (s *tsPrefStore) get(tgID int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[tsPrefHash(tgID)]
}

func (s *tsPrefStore) set(tgID int64, u string) {
	s.mu.Lock()
	if u == "" {
		delete(s.m, tsPrefHash(tgID))
	} else {
		s.m[tsPrefHash(tgID)] = u
	}
	b, _ := json.MarshalIndent(s.m, "", " ")
	path := s.path
	s.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	_ = os.WriteFile(path, b, 0600) // адреса — приватные (LAN-топология юзера)
}

// normalizeTsPref повторяет клиентскую нормализацию: «192.168.1.50:8090» с телефона — валидный
// ввод, схему дописываем сами. Пустая строка = «сбросить». false → мусор, сохранять нельзя.
func normalizeTsPref(raw string) (string, bool) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", true
	}
	low := strings.ToLower(t)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		t = "http://" + strings.TrimLeft(t, "/")
	}
	p, err := url.Parse(t)
	if err != nil || p.Host == "" {
		return "", false
	}
	return strings.TrimRight(t, "/"), true
}

// GET → {"url": "..."} ('' = не задан). Кука/kit — по переданному резолверу.
func tsPrefGetHandler(store *tsPrefStore, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"url": store.get(tgID)})
	}
}

// POST {"url": "..."} — пусто = сбросить. Отвечает нормализованным значением.
func tsPrefSetHandler(store *tsPrefStore, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2048))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		norm, ok := normalizeTsPref(req.URL)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad url"})
			return
		}
		store.set(tgID, norm)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": norm})
	}
}
