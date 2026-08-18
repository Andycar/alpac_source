package sisihttp

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestSisiBookmarksCRUD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"sisi":{"history":{"enable":true}}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	cfg := config.Config{}

	denyReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/sisi/bookmarks", nil)
	denyRec := httptest.NewRecorder()
	sisiBookmarksHandler(cfg).ServeHTTP(denyRec, denyReq)
	if denyRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for anonymous bookmarks list, got %d", denyRec.Code)
	}

	addReq := httptest.NewRequest(http.MethodPost, "http://lampac.local/sisi/bookmark/add?uid=user1", strings.NewReader(`{
		"name":"X item",
		"bookmark":{"site":"xds","href":"https://xvideos.com/abc","image":"https://img.example/a.jpg"},
		"related":false
	}`))
	addRec := httptest.NewRecorder()
	sisiBookmarkAddHandler(cfg).ServeHTTP(addRec, addReq)
	if addRec.Code != http.StatusOK {
		t.Fatalf("unexpected add status: %d", addRec.Code)
	}
	if !strings.Contains(addRec.Body.String(), `"result":true`) {
		t.Fatalf("unexpected add body: %s", addRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/sisi/bookmarks?uid=user1", nil)
	listRec := httptest.NewRecorder()
	sisiBookmarksHandler(cfg).ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("unexpected list status: %d", listRec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	rawList, _ := payload["list"].([]any)
	if len(rawList) != 1 {
		t.Fatalf("expected 1 bookmark item, got %d: %s", len(rawList), listRec.Body.String())
	}
	item, _ := rawList[0].(map[string]any)
	video := toString(item["video"])
	if !strings.Contains(video, "/xds/vidosik?uri=") {
		t.Fatalf("unexpected bookmark video link: %q", video)
	}
	bookmark, _ := item["bookmark"].(map[string]any)
	uid := toString(bookmark["uid"])
	if uid == "" {
		t.Fatalf("bookmark uid is empty")
	}

	removeReq := httptest.NewRequest(http.MethodPost, "http://lampac.local/sisi/bookmark/remove?uid=user1&id="+uid, nil)
	removeRec := httptest.NewRecorder()
	sisiBookmarkRemoveHandler(cfg).ServeHTTP(removeRec, removeReq)
	if removeRec.Code != http.StatusOK {
		t.Fatalf("unexpected remove status: %d", removeRec.Code)
	}

	emptyReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/sisi/bookmarks?uid=user1", nil)
	emptyRec := httptest.NewRecorder()
	sisiBookmarksHandler(cfg).ServeHTTP(emptyRec, emptyReq)
	var emptyPayload map[string]any
	if err := stdjson.Unmarshal(emptyRec.Body.Bytes(), &emptyPayload); err != nil {
		t.Fatalf("decode empty list response: %v", err)
	}
	rawEmpty, _ := emptyPayload["list"].([]any)
	if len(rawEmpty) != 0 {
		t.Fatalf("expected empty bookmarks list, got %d", len(rawEmpty))
	}
}

func TestSisiHistoryRespectsInitAndCRUD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	cfg := config.Config{}

	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"sisi":{"history":{"enable":false}}}`), 0o644); err != nil {
		t.Fatalf("write init.conf disabled: %v", err)
	}
	denyReq := httptest.NewRequest(http.MethodPost, "http://lampac.local/sisi/history/add?uid=user1", strings.NewReader(`{
		"name":"PH item",
		"bookmark":{"site":"phub","href":"video_key"}
	}`))
	denyRec := httptest.NewRecorder()
	sisiHistoryAddHandler(cfg).ServeHTTP(denyRec, denyReq)
	if denyRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when history disabled, got %d", denyRec.Code)
	}

	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"sisi":{"history":{"enable":true}}}`), 0o644); err != nil {
		t.Fatalf("write init.conf enabled: %v", err)
	}

	addReq := httptest.NewRequest(http.MethodPost, "http://lampac.local/sisi/history/add?uid=user1", strings.NewReader(`{
		"name":"PH item",
		"bookmark":{"site":"phub","href":"video_key","image":"x"},
		"related":false
	}`))
	addRec := httptest.NewRecorder()
	sisiHistoryAddHandler(cfg).ServeHTTP(addRec, addReq)
	if addRec.Code != http.StatusOK {
		t.Fatalf("unexpected history add status: %d", addRec.Code)
	}

	listReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/sisi/historys?uid=user1", nil)
	listRec := httptest.NewRecorder()
	sisiHistoryListHandler(cfg).ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("unexpected history list status: %d", listRec.Code)
	}
	var listPayload map[string]any
	if err := stdjson.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode history list: %v", err)
	}
	rawList, _ := listPayload["list"].([]any)
	if len(rawList) != 1 {
		t.Fatalf("expected 1 history item, got %d: %s", len(rawList), listRec.Body.String())
	}
	item, _ := rawList[0].(map[string]any)
	video := toString(item["video"])
	if !strings.Contains(video, "/phub/vidosik?vkey=") {
		t.Fatalf("unexpected phub history video link: %q", video)
	}
	historyUID := toString(item["history_uid"])
	if historyUID == "" {
		t.Fatalf("history_uid is empty")
	}

	removeReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/sisi/history/remove?uid=user1&id="+historyUID, nil)
	removeRec := httptest.NewRecorder()
	sisiHistoryRemoveHandler(cfg).ServeHTTP(removeRec, removeReq)
	if removeRec.Code != http.StatusOK {
		t.Fatalf("unexpected history remove status: %d", removeRec.Code)
	}
}
