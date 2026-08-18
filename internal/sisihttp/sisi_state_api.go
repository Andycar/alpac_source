package sisihttp

import (
	"crypto/md5"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/auth"
	"lampac-go/internal/config"
)

type sisiEntry struct {
	UID     string         `json:"uid"`
	Created int64          `json:"created"`
	Payload map[string]any `json:"payload"`
}

var (
	sisiBookmarksMu sync.Mutex
	sisiHistoryMu   sync.Mutex
)

func sisiBookmarksHandler(_ config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := sisiUserHash(r)
		if user == "" {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}

		entries, err := sisiLoadEntries(filepath.Join("database", "sisi", "bookmarks", user+".json"))
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		host := hostFromRequest(r)
		pageSize := sisiIntOrDefault(r.URL.Query().Get("pageSize"), 36)
		if pageSize <= 0 {
			pageSize = 36
		}
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)
		if pg <= 0 {
			pg = 1
		}
		search := strings.TrimSpace(r.URL.Query().Get("search"))
		model := strings.TrimSpace(r.URL.Query().Get("model"))

		modelSet := map[string]struct{}{}
		filtered := make([]sisiEntry, 0, len(entries))
		for _, e := range entries {
			if e.Payload == nil {
				continue
			}

			name := strings.TrimSpace(toString(e.Payload["name"]))
			if search != "" && !strings.Contains(name, search) {
				continue
			}

			modelName := sisiModelName(e.Payload)
			if modelName != "" {
				modelSet[modelName] = struct{}{}
			}
			if model != "" && modelName != model {
				continue
			}

			filtered = append(filtered, e)
		}

		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Created > filtered[j].Created
		})

		totalPages := max((len(filtered)/pageSize)+1, 1)
		start := min((pg-1)*pageSize, len(filtered))
		end := min(start+pageSize, len(filtered))

		items := make([]map[string]any, 0, end-start)
		for _, e := range filtered[start:end] {
			items = append(items, sisiBookmarkItem(host, e))
		}

		menu := []map[string]any{
			{
				"title":        "Поиск",
				"search_on":    "search_on",
				"playlist_url": host + "/sisi/bookmarks",
			},
		}
		if len(modelSet) > 0 {
			models := make([]string, 0, len(modelSet))
			for m := range modelSet {
				models = append(models, m)
			}
			sort.Strings(models)

			submenu := make([]map[string]any, 0, len(models))
			for _, m := range models {
				submenu = append(submenu, map[string]any{
					"title":        m,
					"playlist_url": host + "/sisi/bookmarks?model=" + url.QueryEscape(m),
				})
			}
			menu = append(menu, map[string]any{
				"title":        "Модель: " + sisiModelTitle(model),
				"playlist_url": "submenu",
				"submenu":      submenu,
			})
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"menu":        menu,
			"list":        items,
			"total_pages": totalPages,
		})
	}
}

func sisiBookmarkAddHandler(_ config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := sisiUserHash(r)
		if user == "" {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}

		payload := sisiReadBodyObject(r)
		site := strings.TrimSpace(toString(sisiNested(payload, "bookmark", "site")))
		href := strings.TrimSpace(toString(sisiNested(payload, "bookmark", "href")))
		if site == "" || href == "" {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}

		uid := sisiMD5(site + ":" + href)
		sisiEnsureBookmarkUID(payload, uid)

		path := filepath.Join("database", "sisi", "bookmarks", user+".json")
		sisiBookmarksMu.Lock()
		defer sisiBookmarksMu.Unlock()

		entries, err := sisiLoadEntries(path)
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		for _, e := range entries {
			if strings.EqualFold(e.UID, uid) {
				writeJSON(w, http.StatusOK, map[string]any{"result": true})
				return
			}
		}

		entries = append(entries, sisiEntry{
			UID:     uid,
			Created: time.Now().UTC().Unix(),
			Payload: payload,
		})
		if err := sisiSaveEntries(path, entries); err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"result": true})
	}
}

func sisiBookmarkRemoveHandler(_ config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := sisiUserHash(r)
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			if body := sisiReadBodyObject(r); body != nil {
				id = strings.TrimSpace(toString(body["id"]))
			}
		}
		if user == "" || id == "" {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}

		path := filepath.Join("database", "sisi", "bookmarks", user+".json")
		sisiBookmarksMu.Lock()
		defer sisiBookmarksMu.Unlock()

		entries, err := sisiLoadEntries(path)
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		filtered := make([]sisiEntry, 0, len(entries))
		for _, e := range entries {
			if !strings.EqualFold(strings.TrimSpace(e.UID), id) {
				filtered = append(filtered, e)
			}
		}
		if err := sisiSaveEntries(path, filtered); err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"result": true})
	}
}

func sisiHistoryListHandler(_ config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := sisiUserHash(r)
		state := loadSisiRuntimeCfg()
		if user == "" || !state.HistoryEnable {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}

		entries, err := sisiLoadEntries(filepath.Join("database", "sisi", "history", user+".json"))
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		pageSize := sisiIntOrDefault(r.URL.Query().Get("pageSize"), 36)
		if pageSize <= 0 {
			pageSize = 36
		}
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)
		if pg <= 0 {
			pg = 1
		}

		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Created > entries[j].Created
		})

		maxCap := pageSize * 20
		if maxCap > 0 && len(entries) > maxCap {
			entries = entries[:maxCap]
		}

		totalPages := max((len(entries)/pageSize)+1, 1)

		start := min((pg-1)*pageSize, len(entries))
		end := min(start+pageSize, len(entries))

		host := hostFromRequest(r)
		out := make([]map[string]any, 0, end-start)
		for _, e := range entries[start:end] {
			out = append(out, sisiHistoryItem(host, e))
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"list":        out,
			"total_pages": totalPages,
		})
	}
}

func sisiHistoryAddHandler(_ config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := sisiUserHash(r)
		state := loadSisiRuntimeCfg()
		payload := sisiReadBodyObject(r)
		site := strings.TrimSpace(toString(sisiNested(payload, "bookmark", "site")))
		href := strings.TrimSpace(toString(sisiNested(payload, "bookmark", "href")))
		if user == "" || !state.HistoryEnable || site == "" || href == "" {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}

		uid := sisiMD5(site + ":" + href)
		payload["history_uid"] = uid

		path := filepath.Join("database", "sisi", "history", user+".json")
		sisiHistoryMu.Lock()
		defer sisiHistoryMu.Unlock()

		entries, err := sisiLoadEntries(path)
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}
		for _, e := range entries {
			if strings.EqualFold(e.UID, uid) {
				writeJSON(w, http.StatusOK, map[string]any{"result": true})
				return
			}
		}

		entries = append(entries, sisiEntry{
			UID:     uid,
			Created: time.Now().UTC().Unix(),
			Payload: payload,
		})
		if err := sisiSaveEntries(path, entries); err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
	}
}

func sisiHistoryRemoveHandler(_ config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := sisiUserHash(r)
		state := loadSisiRuntimeCfg()
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if user == "" || !state.HistoryEnable || id == "" {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}

		path := filepath.Join("database", "sisi", "history", user+".json")
		sisiHistoryMu.Lock()
		defer sisiHistoryMu.Unlock()

		entries, err := sisiLoadEntries(path)
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		filtered := make([]sisiEntry, 0, len(entries))
		for _, e := range entries {
			if !strings.EqualFold(strings.TrimSpace(e.UID), id) {
				filtered = append(filtered, e)
			}
		}

		if err := sisiSaveEntries(path, filtered); err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
	}
}

func sisiBookmarkItem(host string, entry sisiEntry) map[string]any {
	p := entry.Payload
	site := strings.TrimSpace(toString(sisiNested(p, "bookmark", "site")))
	href := strings.TrimSpace(toString(sisiNested(p, "bookmark", "href")))
	image := strings.TrimSpace(toString(sisiNested(p, "bookmark", "image")))
	preview := strings.TrimSpace(toString(p["preview"]))

	return map[string]any{
		"name":    toString(p["name"]),
		"video":   sisiVideoLink(host, site, href),
		"picture": imageOrNil(image),
		"time":    p["time"],
		"json":    p["json"],
		"related": toBool(p["related"]) || sisiRelatedBySite(site),
		"quality": p["quality"],
		"preview": imageOrNil(preview),
		"model":   p["model"],
		"bookmark": map[string]any{
			"uid": entry.UID,
		},
	}
}

func sisiHistoryItem(host string, entry sisiEntry) map[string]any {
	p := entry.Payload
	site := strings.TrimSpace(toString(sisiNested(p, "bookmark", "site")))
	href := strings.TrimSpace(toString(sisiNested(p, "bookmark", "href")))
	image := strings.TrimSpace(toString(sisiNested(p, "bookmark", "image")))
	bookmark := sisiNestedObject(p, "bookmark")
	if bookmark == nil {
		bookmark = map[string]any{}
	}

	return map[string]any{
		"name":     toString(p["name"]),
		"video":    sisiVideoLink(host, site, href),
		"picture":  imageOrNil(image),
		"time":     p["time"],
		"json":     p["json"],
		"related":  toBool(p["related"]) || sisiRelatedBySite(site),
		"quality":  p["quality"],
		"preview":  imageOrNil(strings.TrimSpace(toString(p["preview"]))),
		"model":    p["model"],
		"bookmark": bookmark,
		"history_uid": func() any {
			if v := strings.TrimSpace(toString(p["history_uid"])); v != "" {
				return v
			}
			return entry.UID
		}(),
	}
}

func sisiVideoLink(host, site, href string) string {
	if host == "" || site == "" || href == "" {
		return ""
	}
	if site == "phub" || site == "phubprem" {
		return host + "/" + site + "/vidosik?vkey=" + url.QueryEscape(href)
	}
	return host + "/" + site + "/vidosik?uri=" + url.QueryEscape(href)
}

func sisiRelatedBySite(site string) bool {
	if site == "" {
		return false
	}
	return regexp.MustCompile(`^(elo|epr|fph|phub|sbg|xmr|xnx|xds)`).MatchString(site)
}

func sisiReadBodyObject(r *http.Request) map[string]any {
	if r == nil || r.Body == nil {
		return map[string]any{}
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || len(strings.TrimSpace(string(body))) == 0 {
		return map[string]any{}
	}
	var obj map[string]any
	if err := stdjson.Unmarshal(body, &obj); err != nil {
		return map[string]any{}
	}
	if obj == nil {
		return map[string]any{}
	}
	return obj
}

func sisiNested(m map[string]any, keys ...string) any {
	cur := any(m)
	for _, key := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[key]
	}
	return cur
}

func sisiNestedObject(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	obj, _ := m[key].(map[string]any)
	return obj
}

func sisiEnsureBookmarkUID(payload map[string]any, uid string) {
	if payload == nil || uid == "" {
		return
	}
	bookmark, ok := payload["bookmark"].(map[string]any)
	if !ok || bookmark == nil {
		bookmark = map[string]any{}
		payload["bookmark"] = bookmark
	}
	bookmark["uid"] = uid
}

func sisiModelName(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	model := payload["model"]
	if m, ok := model.(map[string]any); ok {
		name := strings.TrimSpace(toString(m["name"]))
		if name != "" {
			return name
		}
	}
	return strings.TrimSpace(toString(model))
}

func sisiModelTitle(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "выбрать"
	}
	return model
}

func imageOrNil(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func sisiUserHash(r *http.Request) string {
	if r == nil {
		return ""
	}
	base := ""
	if user, ok := auth.UserFromContext(r.Context()); ok && user != nil {
		base = strings.TrimSpace(user.ID)
	}
	if base == "" {
		for _, candidate := range []string{
			r.URL.Query().Get("uid"),
			r.URL.Query().Get("user_uid"),
			r.URL.Query().Get("account_email"),
			r.URL.Query().Get("token"),
			r.URL.Query().Get("auth_token"),
			r.Header.Get("X-User-Uid"),
			r.Header.Get("X-Account-Email"),
			cookieValue(r, "uid"),
			cookieValue(r, "account_email"),
		} {
			candidate = strings.TrimSpace(candidate)
			if candidate != "" {
				base = candidate
				break
			}
		}
	}
	if base == "" {
		return ""
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("profile_id")); pid != "" && pid != "0" {
		base = base + "_" + pid
	}
	return sisiMD5(base)
}

func sisiMD5(v string) string {
	sum := md5.Sum([]byte(v))
	return fmt.Sprintf("%x", sum)
}

func sisiIntOrDefault(v string, def int) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	var out int
	if _, err := fmt.Sscanf(v, "%d", &out); err != nil {
		return def
	}
	return out
}

func sisiLoadEntries(rel string) ([]sisiEntry, error) {
	path := relToRuntime(rel)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []sisiEntry{}, nil
		}
		return nil, err
	}
	var entries []sisiEntry
	if err := stdjson.Unmarshal(data, &entries); err != nil {
		return []sisiEntry{}, nil
	}
	if entries == nil {
		return []sisiEntry{}, nil
	}
	return entries, nil
}

func sisiSaveEntries(rel string, entries []sisiEntry) error {
	path := relToRuntime(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := stdjson.Marshal(entries)
	if err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o644)
}
