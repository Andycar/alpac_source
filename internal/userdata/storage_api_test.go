package userdata

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestStorageSetGetAndTempFlow(t *testing.T) {
	resetHTTPAPIGlobals(t)
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)

	initConf := `{"storage":{"enable":true,"max_size":1000,"md5name":true}}`
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(initConf), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	recSet := httptest.NewRecorder()
	reqSet := httptest.NewRequest(http.MethodPost, "http://lampac.local/storage/set?path=favorites&pathfile=fileA&uid=user-1", strings.NewReader(`{"hello":"world"}`))
	StorageSetHandler().ServeHTTP(recSet, reqSet)
	if recSet.Code != http.StatusOK {
		t.Fatalf("unexpected set status: %d body=%s", recSet.Code, recSet.Body.String())
	}

	setBody := decodeJSONMap(t, recSet.Body.String())
	if ok, _ := setBody["success"].(bool); !ok {
		t.Fatalf("unexpected set body: %+v", setBody)
	}
	fileInfo, _ := setBody["fileInfo"].(map[string]any)
	if fileInfo == nil || fileInfo["path"] == "" {
		t.Fatalf("unexpected fileInfo in set body: %+v", setBody)
	}

	recGet := httptest.NewRecorder()
	reqGet := httptest.NewRequest(http.MethodGet, "http://lampac.local/storage/get?path=favorites&pathfile=fileA&uid=user-1", nil)
	StorageGetHandler().ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("unexpected get status: %d body=%s", recGet.Code, recGet.Body.String())
	}
	getBody := decodeJSONMap(t, recGet.Body.String())
	if getBody["data"] != `{"hello":"world"}` {
		t.Fatalf("unexpected get data: %+v", getBody)
	}

	recGetInfo := httptest.NewRecorder()
	reqGetInfo := httptest.NewRequest(http.MethodGet, "http://lampac.local/storage/get?path=favorites&pathfile=fileA&uid=user-1&responseInfo=true", nil)
	StorageGetHandler().ServeHTTP(recGetInfo, reqGetInfo)
	getInfoBody := decodeJSONMap(t, recGetInfo.Body.String())
	if _, hasData := getInfoBody["data"]; hasData {
		t.Fatalf("responseInfo should not include data: %+v", getInfoBody)
	}

	router := chi.NewRouter()
	router.Post("/storage/temp/{key}", StorageTempSetHandler())
	router.Get("/storage/temp/{key}", StorageTempGetHandler())

	recTempSet := httptest.NewRecorder()
	reqTempSet := httptest.NewRequest(http.MethodPost, "http://lampac.local/storage/temp/session-1", strings.NewReader(`temp-data`))
	router.ServeHTTP(recTempSet, reqTempSet)
	if recTempSet.Code != http.StatusOK {
		t.Fatalf("unexpected temp set status: %d body=%s", recTempSet.Code, recTempSet.Body.String())
	}

	recTempGet := httptest.NewRecorder()
	reqTempGet := httptest.NewRequest(http.MethodGet, "http://lampac.local/storage/temp/session-1", nil)
	router.ServeHTTP(recTempGet, reqTempGet)
	if recTempGet.Code != http.StatusOK {
		t.Fatalf("unexpected temp get status: %d body=%s", recTempGet.Code, recTempGet.Body.String())
	}
	tempGetBody := decodeJSONMap(t, recTempGet.Body.String())
	if tempGetBody["data"] != "temp-data" {
		t.Fatalf("unexpected temp get body: %+v", tempGetBody)
	}
}

func TestStorageDisabledAndMaxSize(t *testing.T) {
	resetHTTPAPIGlobals(t)
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)

	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(`{"storage":{"enable":false}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	recDisabled := httptest.NewRecorder()
	reqDisabled := httptest.NewRequest(http.MethodGet, "http://lampac.local/storage/get?path=x&pathfile=y&uid=z", nil)
	StorageGetHandler().ServeHTTP(recDisabled, reqDisabled)
	disabledBody := decodeJSONMap(t, recDisabled.Body.String())
	if disabledBody["msg"] != "disabled" {
		t.Fatalf("unexpected disabled body: %+v", disabledBody)
	}

	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(`{"storage":{"enable":true,"max_size":3}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	recMax := httptest.NewRecorder()
	reqMax := httptest.NewRequest(http.MethodPost, "http://lampac.local/storage/set?path=x&pathfile=y&uid=z", strings.NewReader("1234"))
	StorageSetHandler().ServeHTTP(recMax, reqMax)
	maxBody := decodeJSONMap(t, recMax.Body.String())
	if maxBody["msg"] != "max_size" {
		t.Fatalf("unexpected max_size body: %+v", maxBody)
	}
}

func decodeJSONMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := stdjson.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("invalid json %q: %v", raw, err)
	}
	return out
}
