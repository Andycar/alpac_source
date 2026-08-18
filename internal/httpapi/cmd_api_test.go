package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestCmdHandlerProcessStart(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)

	outFile := filepath.Join(root, "cmd.out")
	scriptPath := filepath.Join(root, "cmd.sh")
	script := "#!/bin/sh\n" +
		"echo \"$1\" > " + outFile + "\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	initConf := `{"cmd":{"run":{"path":"` + scriptPath + `","arguments":["{value}"]}}}`
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(initConf), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	router := chi.NewRouter()
	router.Get("/cmd/{key}/*", cmdHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/cmd/run/abc?x=1", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var got string
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(outFile)
		if err == nil {
			candidate := strings.TrimSpace(string(b))
			if candidate != "" {
				got = candidate
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got != "abc?x=1" {
		t.Fatalf("unexpected command argument output: %q", got)
	}
}

func TestCmdHandlerUnknownOrEmpty(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(`{"cmd":{}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	router := chi.NewRouter()
	router.Get("/cmd/{key}/*", cmdHandler())

	recUnknown := httptest.NewRecorder()
	reqUnknown := httptest.NewRequest(http.MethodGet, "http://lampac.local/cmd/missing/any", nil)
	router.ServeHTTP(recUnknown, reqUnknown)
	if recUnknown.Code != http.StatusOK {
		t.Fatalf("unexpected unknown status: %d", recUnknown.Code)
	}
}
