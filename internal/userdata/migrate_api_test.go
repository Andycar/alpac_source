package userdata

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/auth"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func tgContext(r *http.Request, tgID string) *http.Request {
	return r.WithContext(auth.WithUser(r.Context(), &auth.User{
		ID:      tgID,
		Expires: time.Now().Add(24 * time.Hour),
	}))
}

func parseMigrateResp(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := stdjson.Unmarshal(body, &m); err != nil {
		t.Fatalf("invalid response JSON: %v body=%s", err, string(body))
	}
	return m
}

// allowPrivateMigrate пускает migrateClient на 127.0.0.1 — там слушает httptest.
func allowPrivateMigrate(t *testing.T) {
	t.Helper()
	migrateAllowPrivate = true
	t.Cleanup(func() { migrateAllowPrivate = false })
}

func setupMigrateEnv(t *testing.T) string {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)
	writeInitConf(t, root, `{"sync_user":{"enable":true,"fullset":true}}`)
	return root
}

func seedBookmarks(t *testing.T, root, userID string, data map[string]any) {
	t.Helper()
	p := bookmarkUserPath(userID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir bookmark: %v", err)
	}
	raw, _ := stdjson.Marshal(data)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatalf("write bookmark: %v", err)
	}
}

func seedTimecodes(t *testing.T, root, userID string, data timecodeUserData) {
	t.Helper()
	p := timecodeUserPath(userID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir timecode: %v", err)
	}
	raw, _ := stdjson.Marshal(data)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatalf("write timecode: %v", err)
	}
}

func seedStorage(t *testing.T, root, userID, pathName, content string) {
	t.Helper()
	opts := loadStorageOptions()
	out, ok := resolveStoragePath(pathName, "", true, userID, opts)
	if !ok {
		t.Fatalf("resolveStoragePath failed for %s/%s", userID, pathName)
	}
	if err := os.WriteFile(out.fs, []byte(content), 0o644); err != nil {
		t.Fatalf("write storage: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestMigrateRequiresTGAuth(t *testing.T) {
	_ = setupMigrateEnv(t)

	// No auth context → 401
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/migrate/check?old_uid=abc12345", nil)
	MigrateCheckHandler().ServeHTTP(rec, req)

	resp := parseMigrateResp(t, rec.Body.Bytes())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if resp["error"] != "tg_auth_required" {
		t.Fatalf("expected tg_auth_required, got %v", resp["error"])
	}
}

func TestMigrateCheckLocalAvailable(t *testing.T) {
	root := setupMigrateEnv(t)

	oldUID := "abc12345"
	seedBookmarks(t, root, oldUID, map[string]any{
		"card":    []any{map[string]any{"id": "100", "title": "Film"}},
		"history": []any{"100"},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/migrate/check?old_uid="+oldUID, nil)
	req = tgContext(req, "tg:999")
	MigrateCheckHandler().ServeHTTP(rec, req)

	resp := parseMigrateResp(t, rec.Body.Bytes())
	if resp["available"] != true {
		t.Fatalf("expected available=true, got %v", resp)
	}
	if resp["mode"] != "local" {
		t.Fatalf("expected mode=local, got %v", resp["mode"])
	}
}

func TestMigrateCheckNotAvailable(t *testing.T) {
	_ = setupMigrateEnv(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/migrate/check?old_uid=noexist123", nil)
	req = tgContext(req, "tg:999")
	MigrateCheckHandler().ServeHTTP(rec, req)

	resp := parseMigrateResp(t, rec.Body.Bytes())
	if resp["available"] != false {
		t.Fatalf("expected available=false, got %v", resp)
	}
}

func TestMergeBookmarks_EmptyNew(t *testing.T) {
	root := setupMigrateEnv(t)

	oldUID := "olduser01"
	newUID := "tg:123"
	seedBookmarks(t, root, oldUID, map[string]any{
		"card":    []any{map[string]any{"id": "100", "title": "Film A"}, map[string]any{"id": "200", "title": "Film B"}},
		"history": []any{"100"},
		"watch":   []any{"200"},
	})

	body := `{"old_uid":"` + oldUID + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/migrate/from-uid", strings.NewReader(body))
	req = tgContext(req, newUID)
	MigrateFromUIDHandler().ServeHTTP(rec, req)

	resp := parseMigrateResp(t, rec.Body.Bytes())
	if resp["success"] != true {
		t.Fatalf("expected success=true, got %v", resp)
	}
	if resp["bookmarks"] != true {
		t.Fatalf("expected bookmarks=true, got %v", resp)
	}

	// Verify data was copied
	data, exists, _ := loadBookmarkUser(newUID)
	if !exists {
		t.Fatal("new bookmark file should exist")
	}
	cards, _ := data["card"].([]any)
	if len(cards) != 2 {
		t.Fatalf("expected 2 cards, got %d", len(cards))
	}
}

func TestMergeBookmarks_Overlap(t *testing.T) {
	root := setupMigrateEnv(t)

	oldUID := "olduser02"
	newUID := "tg:456"

	// Old has cards 100 and 200
	seedBookmarks(t, root, oldUID, map[string]any{
		"card":    []any{map[string]any{"id": "100", "title": "Old A"}, map[string]any{"id": "200", "title": "Old B"}},
		"history": []any{"100", "200"},
	})

	// New already has card 100
	seedBookmarks(t, root, newUID, map[string]any{
		"card":    []any{map[string]any{"id": "100", "title": "New A"}},
		"history": []any{"100"},
	})

	body := `{"old_uid":"` + oldUID + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/migrate/from-uid", strings.NewReader(body))
	req = tgContext(req, newUID)
	MigrateFromUIDHandler().ServeHTTP(rec, req)

	resp := parseMigrateResp(t, rec.Body.Bytes())
	if resp["success"] != true {
		t.Fatalf("expected success=true, got %v", resp)
	}

	data, _, _ := loadBookmarkUser(newUID)
	cards, _ := data["card"].([]any)
	// Should have 2 cards (100 deduplicated, 200 added)
	if len(cards) != 2 {
		t.Fatalf("expected 2 cards after dedup, got %d: %+v", len(cards), cards)
	}
}

func TestMergeTimecodes_FillGaps(t *testing.T) {
	root := setupMigrateEnv(t)

	oldUID := "tcold0001"
	newUID := "tg:789"

	// Old timecodes
	seedTimecodes(t, root, oldUID, timecodeUserData{
		"100": {"s1e1": "120", "s1e2": "300"},
		"200": {"s1e1": "60"},
	})

	// New timecodes (100/s1e1 already exists)
	seedTimecodes(t, root, newUID, timecodeUserData{
		"100": {"s1e1": "999"},
	})

	body := `{"old_uid":"` + oldUID + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/migrate/from-uid", strings.NewReader(body))
	req = tgContext(req, newUID)
	MigrateFromUIDHandler().ServeHTTP(rec, req)

	resp := parseMigrateResp(t, rec.Body.Bytes())
	if resp["success"] != true {
		t.Fatalf("expected success=true, got %v", resp)
	}
	if resp["timecodes"] != true {
		t.Fatalf("expected timecodes=true, got %v", resp)
	}

	store, err := loadTimecodeUser(newUID)
	if err != nil {
		t.Fatal(err)
	}

	// 100/s1e1 should be "999" (new wins)
	if store["100"]["s1e1"] != "999" {
		t.Fatalf("expected 100/s1e1=999, got %s", store["100"]["s1e1"])
	}
	// 100/s1e2 should be "300" (gap filled from old)
	if store["100"]["s1e2"] != "300" {
		t.Fatalf("expected 100/s1e2=300, got %s", store["100"]["s1e2"])
	}
	// 200/s1e1 should be "60" (fully from old)
	if store["200"]["s1e1"] != "60" {
		t.Fatalf("expected 200/s1e1=60, got %s", store["200"]["s1e1"])
	}
}

func TestMigrateStorage(t *testing.T) {
	root := setupMigrateEnv(t)

	oldUID := "stold0001"
	newUID := "tg:321"

	seedStorage(t, root, oldUID, "sync_favorite", `{"data":"old_favorites"}`)
	seedStorage(t, root, oldUID, "sync_view", `{"data":"old_views"}`)

	body := `{"old_uid":"` + oldUID + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/migrate/from-uid", strings.NewReader(body))
	req = tgContext(req, newUID)
	MigrateFromUIDHandler().ServeHTTP(rec, req)

	resp := parseMigrateResp(t, rec.Body.Bytes())
	if resp["success"] != true {
		t.Fatalf("expected success=true, got %v", resp)
	}
	if resp["storage"] != true {
		t.Fatalf("expected storage=true, got %v", resp)
	}

	// Verify new storage files exist
	opts := loadStorageOptions()
	for _, pathName := range []string{"sync_favorite", "sync_view"} {
		out, ok := resolveStoragePath(pathName, "", false, newUID, opts)
		if !ok || !fileExists(out.fs) {
			t.Fatalf("expected storage %s to exist for new user", pathName)
		}
	}
}

func TestAlreadyMigrated(t *testing.T) {
	root := setupMigrateEnv(t)

	oldUID := "migold001"
	newUID := "tg:555"

	seedBookmarks(t, root, oldUID, map[string]any{
		"card":    []any{map[string]any{"id": "100", "title": "X"}},
		"history": []any{"100"},
	})

	// First migration
	body := `{"old_uid":"` + oldUID + `"}`
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/migrate/from-uid", strings.NewReader(body))
	req1 = tgContext(req1, newUID)
	MigrateFromUIDHandler().ServeHTTP(rec1, req1)

	resp1 := parseMigrateResp(t, rec1.Body.Bytes())
	if resp1["success"] != true {
		t.Fatalf("first migration should succeed: %v", resp1)
	}

	// Second migration — should be blocked
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/migrate/from-uid", strings.NewReader(body))
	req2 = tgContext(req2, newUID)
	MigrateFromUIDHandler().ServeHTTP(rec2, req2)

	resp2 := parseMigrateResp(t, rec2.Body.Bytes())
	if resp2["success"] != false || resp2["error"] != "already_migrated" {
		t.Fatalf("second migration should return already_migrated: %v", resp2)
	}
}

func TestMigrateFromServer_Bookmarks(t *testing.T) {
	root := setupMigrateEnv(t)
	newUID := "tg:777"
	oldUID := "rmt12345"

	// Disable SSRF protection for test (httptest listens on 127.0.0.1)
	origValidator := validateServerURL
	validateServerURL = func(raw string) error { return nil }
	t.Cleanup(func() { validateServerURL = origValidator })
	allowPrivateMigrate(t)

	// Start a mock "old lampac" server
	mux := http.NewServeMux()
	mux.HandleFunc("/bookmark/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"card": [
				{"id": "500", "title": "Remote Film"},
				{"id": "600", "title": "Remote Serial"}
			],
			"history": ["500"],
			"watch": ["600"]
		}`))
	})
	mux.HandleFunc("/timecode/all", func(w http.ResponseWriter, r *http.Request) {
		cardID := r.URL.Query().Get("card_id")
		w.Header().Set("Content-Type", "application/json")
		if cardID == "500" {
			w.Write([]byte(`{"s1e1": "42"}`))
		} else {
			w.Write([]byte(`{}`))
		}
	})
	mux.HandleFunc("/storage/get", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		w.Header().Set("Content-Type", "application/json")
		if path == "sync_view" {
			w.Write([]byte(`{"success":true,"data":"{\"online_view\":{\"100\":true}}"}`))
		} else {
			w.Write([]byte(`{"success":false}`))
		}
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	_ = root
	body := `{"old_server":"` + ts.URL + `","old_uid":"` + oldUID + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/migrate/from-server", strings.NewReader(body))
	req = tgContext(req, newUID)
	MigrateFromServerHandler().ServeHTTP(rec, req)

	resp := parseMigrateResp(t, rec.Body.Bytes())
	if resp["success"] != true {
		t.Fatalf("expected success=true, got %v", resp)
	}
	if resp["bookmarks"] != true {
		t.Fatalf("expected bookmarks=true, got %v", resp)
	}
	if resp["timecodes"] != true {
		t.Fatalf("expected timecodes=true, got %v", resp)
	}
	if resp["storage"] != true {
		t.Fatalf("expected storage=true, got %v", resp)
	}

	// Verify bookmarks
	data, exists, _ := loadBookmarkUser(newUID)
	if !exists {
		t.Fatal("bookmarks should exist")
	}
	cards, _ := data["card"].([]any)
	if len(cards) != 2 {
		t.Fatalf("expected 2 cards, got %d", len(cards))
	}

	// Verify timecodes
	store, _ := loadTimecodeUser(newUID)
	if store["500"]["s1e1"] != "42" {
		t.Fatalf("expected timecode 500/s1e1=42, got %v", store["500"])
	}
}

func TestSSRFProtection(t *testing.T) {
	_ = setupMigrateEnv(t)

	tests := []struct {
		name string
		url  string
	}{
		{"localhost", "http://localhost:8080"},
		{"loopback_ip", "http://127.0.0.1:8080"},
		{"private_10", "http://10.0.0.1:8080"},
		{"private_192", "http://192.168.1.1:8080"},
		{"private_172", "http://172.16.0.1:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"old_server":"` + tt.url + `","old_uid":"abc12345"}`
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/migrate/from-server", strings.NewReader(body))
			req = tgContext(req, "tg:999")
			MigrateFromServerHandler().ServeHTTP(rec, req)

			resp := parseMigrateResp(t, rec.Body.Bytes())
			if resp["success"] != false {
				t.Fatalf("expected success=false for %s, got %v", tt.url, resp)
			}
		})
	}
}

func TestMigrateInvalidOldUID(t *testing.T) {
	_ = setupMigrateEnv(t)

	tests := []struct {
		name string
		uid  string
	}{
		{"too_short", "ab"},
		{"with_upper", "ABC12345"},
		{"with_special", "abc!@#12"},
		{"too_long", "abcdefghijklmnopqrstuvwxyz"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/migrate/check?old_uid="+tt.uid, nil)
			req = tgContext(req, "tg:999")
			MigrateCheckHandler().ServeHTTP(rec, req)

			resp := parseMigrateResp(t, rec.Body.Bytes())
			if resp["available"] != false {
				t.Fatalf("expected available=false for uid=%q, got %v", tt.uid, resp)
			}
		})
	}
}
