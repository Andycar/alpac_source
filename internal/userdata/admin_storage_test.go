package userdata

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Пишем через штатный StorageSetHandler (тот же md5-вывод пути, что у прода),
// затем атрибуцируем и удаляем через admin-хелперы.
func TestAdminListAndDeleteUserStorage(t *testing.T) {
	resetHTTPAPIGlobals(t)
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)

	write := func(path, uid, pathfile, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		u := "http://lampac.local/storage/set?path=" + path + "&uid=" + uid
		if pathfile != "" {
			u += "&pathfile=" + pathfile
		}
		req := httptest.NewRequest(http.MethodPost, u, strings.NewReader(body))
		StorageSetHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("set %s for %s: status %d body=%s", path, uid, rec.Code, rec.Body.String())
		}
	}

	// аккаунт-бэкап, бэкап sub-профиля, легаси девайс-бэкап, sync-файл, чужой файл
	write("backup", "tg:123", "", `{"a":"1"}`)
	write("backup", "tg:123", "p1", `{"b":"2"}`)
	write("backup", "devuid99", "", `{"c":"3"}`)
	write("sync_view", "tg:123", "", `{"d":"4"}`)
	write("backup", "tg:999", "", `{"other":"x"}`)

	owners := []string{"tg:123", "devuid99"}
	profiles := []string{"p1"}

	files := AdminListUserStorage(owners, profiles)
	if len(files) != 4 {
		t.Fatalf("want 4 attributed files, got %d: %+v", len(files), files)
	}
	byPath := map[string]int{}
	for _, f := range files {
		byPath[f.Path]++
		if f.Size == 0 || f.ModTime == 0 {
			t.Fatalf("file without size/mtime: %+v", f)
		}
	}
	if byPath["backup"] != 3 || byPath["sync_view"] != 1 {
		t.Fatalf("unexpected path split: %v", byPath)
	}

	// удаление только backup-пути не трогает sync_view и чужой файл
	deleted, freed := AdminDeleteUserStorage(owners, profiles, []string{"backup"})
	if deleted != 3 || freed == 0 {
		t.Fatalf("delete backup: deleted=%d freed=%d", deleted, freed)
	}
	if left := AdminListUserStorage(owners, profiles); len(left) != 1 || left[0].Path != "sync_view" {
		t.Fatalf("after delete want only sync_view, got %+v", left)
	}
	if other := AdminListUserStorage([]string{"tg:999"}, nil); len(other) != 1 {
		t.Fatalf("stranger's file must survive, got %+v", other)
	}

	// nil paths = всё
	deleted, _ = AdminDeleteUserStorage(owners, profiles, nil)
	if deleted != 1 {
		t.Fatalf("delete all: deleted=%d", deleted)
	}
	if left := AdminListUserStorage(owners, profiles); len(left) != 0 {
		t.Fatalf("expected empty, got %+v", left)
	}
}

// Self-service wipe: свои файлы (включая расширенные identity) сносятся, чужие
// живут, IP-фолбэк без явного uid отклоняется.
func TestStorageWipeHandler(t *testing.T) {
	resetHTTPAPIGlobals(t)
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)

	write := func(uid, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "http://lampac.local/storage/set?path=backup&uid="+uid, strings.NewReader(body))
		StorageSetHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("set for %s: %d", uid, rec.Code)
		}
	}
	write("user-1", `{"mine":"1"}`)
	write("dev-42", `{"legacy":"1"}`)
	write("user-2", `{"stranger":"1"}`)

	expand := func(uid string) ([]string, []string) {
		if uid == "user-1" {
			return []string{"dev-42"}, nil
		}
		return nil, nil
	}

	// IP-фолбэк (нет ни авторизации, ни ?uid=) → отказ
	rec := httptest.NewRecorder()
	StorageWipeHandler(expand, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://lampac.local/storage/wipe", nil))
	var refused struct {
		Success bool `json:"success"`
	}
	_ = stdjson.Unmarshal(rec.Body.Bytes(), &refused)
	if refused.Success {
		t.Fatalf("wipe via bare IP identity must be refused, got %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	StorageWipeHandler(expand, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://lampac.local/storage/wipe?uid=user-1", nil))
	var resp struct {
		Success bool `json:"success"`
		Deleted int  `json:"deleted"`
	}
	if stdjson.Unmarshal(rec.Body.Bytes(), &resp) != nil || !resp.Success {
		t.Fatalf("wipe failed: %s", rec.Body.String())
	}
	if resp.Deleted != 2 {
		t.Fatalf("want 2 deleted (свой + легаси девайс), got %d", resp.Deleted)
	}
	if left := AdminListUserStorage([]string{"user-2"}, nil); len(left) != 1 {
		t.Fatalf("stranger's backup must survive, got %+v", left)
	}
}
