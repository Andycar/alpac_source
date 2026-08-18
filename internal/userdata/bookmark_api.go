package userdata

import (
	"crypto/md5"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"lampac-go/internal/auth"
)

var bookmarkMu sync.Mutex

var bookmarkCategories = []string{
	"history",
	"like",
	"watch",
	"wath",
	"book",
	"look",
	"viewed",
	"scheduled",
	"continued",
	"thrown",
}

func BookmarkListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !bookmarkSyncEnabled() {
			writeRawJSON(w, []byte("{}"))
			return
		}

		userID := BookmarkUserID(r)
		data, exists, _ := loadBookmarkUser(userID)
		if !exists {
			writeJSON(w, http.StatusOK, map[string]any{"dbInNotInitialization": true})
			return
		}

		filed := strings.TrimSpace(r.URL.Query().Get("filed"))
		if filed != "" {
			val, ok := data[filed]
			if !ok {
				writeRawJSON(w, []byte("null"))
				return
			}
			raw, err := stdjson.Marshal(val)
			if err != nil {
				writeRawJSON(w, []byte("null"))
				return
			}
			writeRawJSON(w, raw)
			return
		}

		writeJSON(w, http.StatusOK, data)
	}
}

func BookmarkSetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := BookmarkUserID(r)
		if userID == "" || !bookmarkSyncEnabled() {
			writeBookmarkFailure(w, "")
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil || len(strings.TrimSpace(string(body))) == 0 {
			writeBookmarkFailure(w, "")
			return
		}

		var token any
		if err := stdjson.Unmarshal(body, &token); err != nil {
			writeBookmarkFailure(w, "")
			return
		}

		jobs := extractObjectList(token)
		if len(jobs) == 0 {
			writeBookmarkFailure(w, "")
			return
		}

		bookmarkMu.Lock()
		defer bookmarkMu.Unlock()

		data, exists, _ := loadBookmarkUser(userID)
		fullset := bookmarkFullsetEnabled()

		for _, job := range jobs {
			where := strings.ToLower(strings.TrimSpace(toString(job["where"])))
			if where == "" {
				writeBookmarkFailure(w, "")
				return
			}

			if exists && !fullset && (where == "card" || isBookmarkCategory(where)) {
				writeBookmarkFailure(w, "enable sync_user.fullset in init.conf")
				return
			}

			val, ok := job["data"]
			if !ok {
				writeBookmarkFailure(w, "")
				return
			}
			data[where] = val
		}

		ensureBookmarkDefaults(data)
		if err := saveBookmarkUser(userID, data); err != nil {
			writeBookmarkFailure(w, "")
			return
		}

		// Broadcast event to other devices of the same user via NWS. Owner uid
		// (no profile suffix) — NWS resolves device connections by the bare
		// identity, so a suffixed bucket id would silently reach nobody.
		connID := r.URL.Query().Get("connectionId")
		deps.NwsBroadcast(connID, bookmarkOwnerOnly(r), "bookmark", map[string]any{
			"type":       "set",
			"data":       data,
			"profile_id": requestProfileID(r),
		})

		writeBookmarkSuccess(w)
	}
}

func BookmarkAddHandler(isAdded bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := BookmarkUserID(r)
		if userID == "" || !bookmarkSyncEnabled() {
			writeBookmarkFailure(w, "")
			return
		}

		token, payloads := readBookmarkPayloads(r)
		_ = token
		if len(payloads) == 0 {
			writeBookmarkFailure(w, "")
			return
		}

		bookmarkMu.Lock()
		defer bookmarkMu.Unlock()

		data, _, _ := loadBookmarkUser(userID)
		changed := false

		for _, payload := range payloads {
			cardID := payload.resolveCardID()
			if cardID == "" {
				continue
			}

			changed = ensureBookmarkCard(data, payload.Card, cardID) || changed
			if payload.Where != "" {
				changed = addBookmarkCategoryID(data, payload.Where, cardID) || changed
			}
			if isAdded {
				changed = moveBookmarkIDToFrontAll(data, cardID) || changed
			}
		}

		if changed {
			_ = saveBookmarkUser(userID, data)

			// Broadcast add event to other devices of the same user via NWS.
			// profile_id lets receivers apply it only when the same
			// sub-profile is active (syncpro filters on it).
			connID := r.URL.Query().Get("connectionId")
			for _, payload := range payloads {
				deps.NwsBroadcast(connID, bookmarkOwnerOnly(r), "bookmark", map[string]any{
					"type": "add",
					"data": map[string]any{
						"where": payload.Where,
						"card":  payload.Card,
					},
					"profile_id": requestProfileID(r),
				})
			}
		}

		writeBookmarkSuccess(w)
	}
}

func BookmarkRemoveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := BookmarkUserID(r)
		if userID == "" || !bookmarkSyncEnabled() {
			writeBookmarkFailure(w, "")
			return
		}

		token, payloads := readBookmarkPayloads(r)
		_ = token
		if len(payloads) == 0 {
			writeBookmarkFailure(w, "")
			return
		}

		bookmarkMu.Lock()
		defer bookmarkMu.Unlock()

		data, exists, _ := loadBookmarkUser(userID)
		if !exists {
			writeBookmarkSuccess(w)
			return
		}

		changed := false
		for _, payload := range payloads {
			cardID := payload.resolveCardID()
			if cardID == "" {
				continue
			}
			if payload.Where != "" {
				changed = removeBookmarkCategoryID(data, payload.Where, cardID) || changed
			}
			if strings.EqualFold(payload.Method, "card") {
				changed = removeBookmarkIDFromAll(data, cardID) || changed
				changed = removeBookmarkCard(data, cardID) || changed
			}
		}

		if changed {
			_ = saveBookmarkUser(userID, data)

			// Broadcast remove event to other devices of the same user via NWS.
			// Owner uid (no profile suffix) — NWS resolves devices by the bare
			// identity; profile_id rides the payload for client-side filtering.
			connID := r.URL.Query().Get("connectionId")
			for _, payload := range payloads {
				deps.NwsBroadcast(connID, bookmarkOwnerOnly(r), "bookmark", map[string]any{
					"type": "remove",
					"data": map[string]any{
						"where": payload.Where,
						"card":  payload.Card,
					},
					"profile_id": requestProfileID(r),
				})
			}
		}

		writeBookmarkSuccess(w)
	}
}

type bookmarkPayload struct {
	Method string
	Where  string
	Card   map[string]any
	CardID string
}

func readBookmarkPayloads(r *http.Request) (any, []bookmarkPayload) {
	body, err := io.ReadAll(r.Body)
	if err != nil || len(strings.TrimSpace(string(body))) == 0 {
		return nil, nil
	}

	var token any
	if err := stdjson.Unmarshal(body, &token); err != nil {
		return nil, nil
	}

	objs := extractObjectList(token)
	payloads := make([]bookmarkPayload, 0, len(objs))
	for _, job := range objs {
		payload := bookmarkPayload{
			Method: strings.TrimSpace(toString(job["method"])),
			CardID: strings.TrimSpace(strings.ToLower(toString(nonNil(job["id"], job["card_id"])))),
		}
		where := strings.TrimSpace(strings.ToLower(toString(nonNil(job["where"], job["list"]))))
		if where != "" && where != "card" {
			payload.Where = where
		}
		if card, ok := job["card"].(map[string]any); ok {
			payload.Card = card
		}
		payloads = append(payloads, payload)
	}
	return token, payloads
}

func (p bookmarkPayload) resolveCardID() string {
	if p.CardID != "" {
		return p.CardID
	}
	if p.Card != nil {
		id := strings.TrimSpace(strings.ToLower(toString(p.Card["id"])))
		return id
	}
	return ""
}

func extractObjectList(token any) []map[string]any {
	switch v := token.(type) {
	case map[string]any:
		return []map[string]any{v}
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if obj, ok := item.(map[string]any); ok {
				out = append(out, obj)
			}
		}
		return out
	default:
		return nil
	}
}

func loadBookmarkUser(userID string) (map[string]any, bool, error) {
	path := bookmarkUserPath(userID)
	data, err := os.ReadFile(path)
	if err != nil {
		root := createDefaultBookmarks()
		return root, false, err
	}
	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		root = createDefaultBookmarks()
		return root, true, err
	}
	ensureBookmarkDefaults(root)
	return root, true, nil
}

func saveBookmarkUser(userID string, root map[string]any) error {
	path := bookmarkUserPath(userID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := stdjson.Marshal(root)
	if err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o644)
}

func bookmarkUserPath(userID string) string {
	sum := md5.Sum([]byte(userID))
	name := hex.EncodeToString(sum[:]) + ".json"
	return relToRuntime(filepath.Join("database", "bookmark", name))
}

func createDefaultBookmarks() map[string]any {
	root := map[string]any{
		"card": []any{},
	}
	for _, c := range bookmarkCategories {
		root[c] = []any{}
	}
	return root
}

func ensureBookmarkDefaults(root map[string]any) {
	if root == nil {
		return
	}
	if _, ok := root["card"].([]any); !ok {
		root["card"] = []any{}
	}
	for _, c := range bookmarkCategories {
		if _, ok := root[c].([]any); !ok {
			root[c] = []any{}
		}
	}
}

func ensureBookmarkCard(root map[string]any, card map[string]any, id string) bool {
	if root == nil || card == nil || id == "" {
		return false
	}
	ensureBookmarkDefaults(root)
	arr, _ := root["card"].([]any)
	if arr == nil {
		arr = []any{}
	}

	cardCopy := deepCopyObject(card)
	for i := range arr {
		obj, ok := arr[i].(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(toString(obj["id"])), id) {
			if deepEqualJSON(obj, cardCopy) {
				return false
			}
			arr[i] = cardCopy
			root["card"] = arr
			return true
		}
	}
	root["card"] = append([]any{cardCopy}, arr...)
	return true
}

func addBookmarkCategoryID(root map[string]any, category, id string) bool {
	ensureBookmarkDefaults(root)
	arr, _ := root[category].([]any)
	if arr == nil {
		arr = []any{}
	}
	for _, v := range arr {
		if strings.EqualFold(toString(v), id) {
			return false
		}
	}
	if n, err := strconv.ParseInt(id, 10, 64); err == nil && n > 0 {
		root[category] = append([]any{n}, arr...)
	} else {
		root[category] = append([]any{id}, arr...)
	}
	return true
}

func moveBookmarkIDToFrontAll(root map[string]any, id string) bool {
	changed := false
	for k, v := range root {
		if k == "card" {
			continue
		}
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		newArr, moved := moveAnyIDToFront(arr, id)
		if moved {
			root[k] = newArr
			changed = true
		}
	}
	return changed
}

func removeBookmarkCategoryID(root map[string]any, category, id string) bool {
	arr, ok := root[category].([]any)
	if !ok {
		return false
	}
	newArr, changed := removeAnyID(arr, id)
	if changed {
		root[category] = newArr
	}
	return changed
}

func removeBookmarkIDFromAll(root map[string]any, id string) bool {
	changed := false
	for k, v := range root {
		if k == "card" {
			continue
		}
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		newArr, removed := removeAnyID(arr, id)
		if removed {
			root[k] = newArr
			changed = true
		}
	}
	return changed
}

func removeBookmarkCard(root map[string]any, id string) bool {
	arr, ok := root["card"].([]any)
	if !ok {
		return false
	}
	for i, v := range arr {
		obj, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(toString(obj["id"])), id) {
			root["card"] = append(arr[:i], arr[i+1:]...)
			return true
		}
	}
	return false
}

func moveAnyIDToFront(arr []any, id string) ([]any, bool) {
	for i, v := range arr {
		if strings.EqualFold(toString(v), id) {
			if i == 0 {
				return arr, false
			}
			item := arr[i]
			out := make([]any, 0, len(arr))
			out = append(out, item)
			out = append(out, arr[:i]...)
			out = append(out, arr[i+1:]...)
			return out, true
		}
	}
	return arr, false
}

func removeAnyID(arr []any, id string) ([]any, bool) {
	for i, v := range arr {
		if strings.EqualFold(toString(v), id) {
			return append(arr[:i], arr[i+1:]...), true
		}
	}
	return arr, false
}

func BookmarkUserID(r *http.Request) string {
	// Priority 1: authenticated user from auth middleware.
	var base string
	if user, ok := auth.UserFromContext(r.Context()); ok && user != nil && user.ID != "" {
		base = user.ID
	}

	// Priority 2: client-supplied identifiers (backward compat).
	if base == "" {
		base = strings.TrimSpace(toString(nonNil(
			r.URL.Query().Get("uid"),
			r.URL.Query().Get("user_uid"),
			r.Header.Get("X-User-Uid"),
			cookieValue(r, "uid"),
		)))
	}
	if base == "" {
		return ""
	}

	profileID := strings.TrimSpace(r.URL.Query().Get("profile_id"))
	if profileID != "" && profileID != "0" {
		return base + "_" + profileID
	}
	return base
}

// bookmarkOwnerOnly is BookmarkUserID without the profile suffix — the capi
// sync bridge keys accounts by the bare owner identity.
func bookmarkOwnerOnly(r *http.Request) string {
	if user, ok := auth.UserFromContext(r.Context()); ok && user != nil && user.ID != "" {
		return user.ID
	}
	return strings.TrimSpace(toString(nonNil(
		r.URL.Query().Get("uid"),
		r.URL.Query().Get("user_uid"),
		r.Header.Get("X-User-Uid"),
		cookieValue(r, "uid"),
	)))
}

func bookmarkSyncEnabled() bool {
	data, ok := deps.ReadFileAny("init.conf")
	if !ok {
		return true
	}
	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return true
	}
	node, ok := root["sync_user"].(map[string]any)
	if !ok {
		return true
	}
	enabled, ok := node["enable"].(bool)
	if !ok {
		return true
	}
	return enabled
}

func bookmarkFullsetEnabled() bool {
	data, ok := deps.ReadFileAny("init.conf")
	if !ok {
		return false
	}
	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return false
	}
	node, ok := root["sync_user"].(map[string]any)
	if !ok {
		return false
	}
	fullset, ok := node["fullset"].(bool)
	if !ok {
		return false
	}
	return fullset
}

func writeBookmarkSuccess(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func writeBookmarkFailure(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"success": false,
		"message": messageOrNil(message),
	})
}

func messageOrNil(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func isBookmarkCategory(v string) bool {
	return slices.Contains(bookmarkCategories, v)
}

func nonNil(values ...any) any {
	for _, v := range values {
		switch vv := v.(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(vv) == "" {
				continue
			}
			return vv
		default:
			return vv
		}
	}
	return nil
}

func deepCopyObject(src map[string]any) map[string]any {
	raw, _ := stdjson.Marshal(src)
	var out map[string]any
	_ = stdjson.Unmarshal(raw, &out)
	if out == nil {
		return map[string]any{}
	}
	return out
}

func deepEqualJSON(a, b any) bool {
	ra, _ := stdjson.Marshal(a)
	rb, _ := stdjson.Marshal(b)
	return string(ra) == string(rb)
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}
