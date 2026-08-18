package userdata

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTimecodeEmptyAndAddFlow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)

	recEmpty := httptest.NewRecorder()
	reqEmpty := httptest.NewRequest(http.MethodGet, "http://lampac.local/timecode/all", nil)
	TimecodeAllHandler().ServeHTTP(recEmpty, reqEmpty)
	if strings.TrimSpace(recEmpty.Body.String()) != "{}" {
		t.Fatalf("expected empty object, got: %s", recEmpty.Body.String())
	}

	body := "id=ep1&data=120"
	recAdd := httptest.NewRecorder()
	reqAdd := httptest.NewRequest(http.MethodPost, "http://lampac.local/timecode/add?card_id=cardA&uid=user1", strings.NewReader(body))
	reqAdd.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	TimecodeAddHandler().ServeHTTP(recAdd, reqAdd)

	var addResp map[string]any
	if err := stdjson.Unmarshal(recAdd.Body.Bytes(), &addResp); err != nil {
		t.Fatalf("invalid add response: %v", err)
	}
	if ok, _ := addResp["success"].(bool); !ok {
		t.Fatalf("unexpected add response: %+v", addResp)
	}

	recGet := httptest.NewRecorder()
	reqGet := httptest.NewRequest(http.MethodGet, "http://lampac.local/timecode/all?card_id=cardA&uid=user1", nil)
	TimecodeAllHandler().ServeHTTP(recGet, reqGet)
	var getResp map[string]string
	if err := stdjson.Unmarshal(recGet.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("invalid get response: %v", err)
	}
	if getResp["ep1"] != "120" {
		t.Fatalf("unexpected timecode map: %+v", getResp)
	}
}

// TestTimecodeAllFlat covers ?all=1 — the flat, cross-card dump used to
// rebuild file_view on a profile switch. Entries from different cards must
// merge into one map keyed by fileId; without a card and without all=1 the
// endpoint returns empty.
func TestTimecodeAllFlat(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)

	add := func(card, id, data string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost,
			"http://lampac.local/timecode/add?card_id="+card+"&uid=u9", strings.NewReader("id="+id+"&data="+data))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		TimecodeAddHandler().ServeHTTP(rec, req)
	}
	add("cardA", "ep1", "120")
	add("cardA", "ep2", "300")
	add("cardB", "movie", "999")

	// all=1 → flat merge across cardA + cardB.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/timecode/all?all=1&uid=u9", nil)
	TimecodeAllHandler().ServeHTTP(rec, req)
	var flat map[string]string
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &flat); err != nil {
		t.Fatalf("invalid all=1 response: %v", err)
	}
	if flat["ep1"] != "120" || flat["ep2"] != "300" || flat["movie"] != "999" {
		t.Fatalf("all=1 must flatten every card: %+v", flat)
	}

	// No card_id and no all=1 → empty (unchanged legacy behaviour).
	recEmpty := httptest.NewRecorder()
	reqEmpty := httptest.NewRequest(http.MethodGet, "http://lampac.local/timecode/all?uid=u9", nil)
	TimecodeAllHandler().ServeHTTP(recEmpty, reqEmpty)
	if strings.TrimSpace(recEmpty.Body.String()) != "{}" {
		t.Fatalf("no card + no all → empty, got: %s", recEmpty.Body.String())
	}
}

func TestTimecodeProfileScope(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)

	post := func(url, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		TimecodeAddHandler().ServeHTTP(rec, req)
	}

	post("http://lampac.local/timecode/add?card_id=cardA&uid=user1", "id=ep1&data=100")
	post("http://lampac.local/timecode/add?card_id=cardA&uid=user1&profile_id=2", "id=ep1&data=200")

	get := func(url string) map[string]string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		TimecodeAllHandler().ServeHTTP(rec, req)
		var out map[string]string
		if err := stdjson.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("invalid get response: %v", err)
		}
		return out
	}

	base := get("http://lampac.local/timecode/all?card_id=cardA&uid=user1")
	profile := get("http://lampac.local/timecode/all?card_id=cardA&uid=user1&profile_id=2")

	if base["ep1"] != "100" {
		t.Fatalf("unexpected base profile data: %+v", base)
	}
	if profile["ep1"] != "200" {
		t.Fatalf("unexpected profile data: %+v", profile)
	}
}

func TestTimecodeAddValidation(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://lampac.local/timecode/add?card_id=", strings.NewReader("id=&data="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	TimecodeAddHandler().ServeHTTP(rec, req)

	var resp map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid response: %v", err)
	}
	if success, _ := resp["success"].(bool); success {
		t.Fatalf("expected failure response, got: %+v", resp)
	}
}
