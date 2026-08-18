package userdata

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBookmarkListDisabled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)
	writeInitConf(t, root, `{"sync_user":{"enable":false}}`)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/bookmark/list?uid=u1", nil)
	BookmarkListHandler().ServeHTTP(rec, req)

	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestBookmarkAddListSetRemoveFlow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)
	writeInitConf(t, root, `{"sync_user":{"enable":true,"fullset":true}}`)

	recInit := httptest.NewRecorder()
	reqInit := httptest.NewRequest(http.MethodGet, "http://lampac.local/bookmark/list?uid=u1", nil)
	BookmarkListHandler().ServeHTTP(recInit, reqInit)
	var initResp map[string]any
	if err := stdjson.Unmarshal(recInit.Body.Bytes(), &initResp); err != nil {
		t.Fatalf("invalid init response: %v", err)
	}
	if v, _ := initResp["dbInNotInitialization"].(bool); !v {
		t.Fatalf("expected dbInNotInitialization response, got: %+v", initResp)
	}

	addPayload := `{"where":"history","card":{"id":"100","title":"A"}}`
	recAdd := httptest.NewRecorder()
	reqAdd := httptest.NewRequest(http.MethodPost, "http://lampac.local/bookmark/add?uid=u1", strings.NewReader(addPayload))
	BookmarkAddHandler(false).ServeHTTP(recAdd, reqAdd)
	assertBookmarkSuccess(t, recAdd.Body.Bytes(), true)

	addPayload2 := `{"where":"watch","card":{"id":"200","title":"B"}}`
	recAdd2 := httptest.NewRecorder()
	reqAdd2 := httptest.NewRequest(http.MethodPost, "http://lampac.local/bookmark/add?uid=u1", strings.NewReader(addPayload2))
	BookmarkAddHandler(false).ServeHTTP(recAdd2, reqAdd2)
	assertBookmarkSuccess(t, recAdd2.Body.Bytes(), true)

	addedPayload := `{"id":"200"}`
	recAdded := httptest.NewRecorder()
	reqAdded := httptest.NewRequest(http.MethodPost, "http://lampac.local/bookmark/added?uid=u1", strings.NewReader(addedPayload))
	BookmarkAddHandler(true).ServeHTTP(recAdded, reqAdded)
	assertBookmarkSuccess(t, recAdded.Body.Bytes(), true)

	recList := httptest.NewRecorder()
	reqList := httptest.NewRequest(http.MethodGet, "http://lampac.local/bookmark/list?uid=u1", nil)
	BookmarkListHandler().ServeHTTP(recList, reqList)

	var listResp map[string]any
	if err := stdjson.Unmarshal(recList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("invalid list response: %v", err)
	}
	cardArr := asAnySlice(listResp["card"])
	if len(cardArr) != 2 {
		t.Fatalf("expected 2 cards, got: %+v", listResp["card"])
	}
	if topCard, _ := cardArr[0].(map[string]any); toString(topCard["id"]) != "200" {
		t.Fatalf("expected card 200 at front, got: %+v", cardArr[0])
	}
	watch := asAnySlice(listResp["watch"])
	if len(watch) == 0 || toString(watch[0]) != "200" {
		t.Fatalf("expected watch[0]=200, got: %+v", watch)
	}

	setPayload := `{"where":"custom","data":{"x":1}}`
	recSet := httptest.NewRecorder()
	reqSet := httptest.NewRequest(http.MethodPost, "http://lampac.local/bookmark/set?uid=u1", strings.NewReader(setPayload))
	BookmarkSetHandler().ServeHTTP(recSet, reqSet)
	assertBookmarkSuccess(t, recSet.Body.Bytes(), true)

	recField := httptest.NewRecorder()
	reqField := httptest.NewRequest(http.MethodGet, "http://lampac.local/bookmark/list?uid=u1&filed=custom", nil)
	BookmarkListHandler().ServeHTTP(recField, reqField)
	if !strings.Contains(recField.Body.String(), `"x":1`) {
		t.Fatalf("unexpected field response: %s", recField.Body.String())
	}

	removePayload := `{"method":"card","id":"100","where":"history"}`
	recRemove := httptest.NewRecorder()
	reqRemove := httptest.NewRequest(http.MethodPost, "http://lampac.local/bookmark/remove?uid=u1", strings.NewReader(removePayload))
	BookmarkRemoveHandler().ServeHTTP(recRemove, reqRemove)
	assertBookmarkSuccess(t, recRemove.Body.Bytes(), true)

	recAfterRemove := httptest.NewRecorder()
	reqAfterRemove := httptest.NewRequest(http.MethodGet, "http://lampac.local/bookmark/list?uid=u1", nil)
	BookmarkListHandler().ServeHTTP(recAfterRemove, reqAfterRemove)
	var afterRemove map[string]any
	if err := stdjson.Unmarshal(recAfterRemove.Body.Bytes(), &afterRemove); err != nil {
		t.Fatalf("invalid response after remove: %v", err)
	}
	history := asAnySlice(afterRemove["history"])
	for _, v := range history {
		if toString(v) == "100" {
			t.Fatalf("card id 100 must be removed from history: %+v", history)
		}
	}
}

func TestBookmarkSetValidationFullset(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)
	writeInitConf(t, root, `{"sync_user":{"enable":true,"fullset":false}}`)

	recAdd := httptest.NewRecorder()
	reqAdd := httptest.NewRequest(http.MethodPost, "http://lampac.local/bookmark/add?uid=u1", strings.NewReader(`{"where":"history","card":{"id":"10"}}`))
	BookmarkAddHandler(false).ServeHTTP(recAdd, reqAdd)
	assertBookmarkSuccess(t, recAdd.Body.Bytes(), true)

	recSet := httptest.NewRecorder()
	reqSet := httptest.NewRequest(http.MethodPost, "http://lampac.local/bookmark/set?uid=u1", strings.NewReader(`{"where":"history","data":[1]}`))
	BookmarkSetHandler().ServeHTTP(recSet, reqSet)

	var resp map[string]any
	if err := stdjson.Unmarshal(recSet.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid response: %v", err)
	}
	if success, _ := resp["success"].(bool); success {
		t.Fatalf("expected failure for fullset restriction, got: %+v", resp)
	}
	if !strings.Contains(strings.ToLower(toString(resp["message"])), "fullset") {
		t.Fatalf("expected fullset error message, got: %+v", resp)
	}
}

func writeInitConf(t *testing.T, root, json string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(json), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}
}

func assertBookmarkSuccess(t *testing.T, raw []byte, expected bool) {
	t.Helper()
	var payload map[string]any
	if err := stdjson.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("invalid bookmark response: %v body=%s", err, string(raw))
	}
	if got, _ := payload["success"].(bool); got != expected {
		t.Fatalf("unexpected bookmark success=%v expected=%v payload=%+v", got, expected, payload)
	}
}

func asAnySlice(v any) []any {
	out, _ := v.([]any)
	if out == nil {
		return []any{}
	}
	return out
}
