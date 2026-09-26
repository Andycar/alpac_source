package httpapi

import (
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestSamsungWGTServesPrebuiltWidget(t *testing.T) {
	root := t.TempDir()
	host := "http://lampac.local"
	key := fmt.Sprintf("%x", md5.Sum([]byte(host+"v3")))
	widgetsDir := filepath.Join(root, "data", "widgets")
	if err := os.MkdirAll(widgetsDir, 0o755); err != nil {
		t.Fatalf("mkdir widgets: %v", err)
	}

	expected := []byte("widget-binary-content")
	if err := os.WriteFile(filepath.Join(widgetsDir, key+".wgt"), expected, 0o644); err != nil {
		t.Fatalf("write widget: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	req := httptest.NewRequest(http.MethodGet, host+"/samsung.wgt", nil)
	rec := httptest.NewRecorder()

	samsungWGTHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("unexpected content-type: %q", ct)
	}
	// Имя при скачивании — наше, а не унаследованное lampac.wgt (переименовано 2026-09-10).
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="alpac.wgt"` {
		t.Fatalf("unexpected content-disposition: %q", cd)
	}
	if body := rec.Body.Bytes(); string(body) != string(expected) {
		t.Fatalf("unexpected body: %q", string(body))
	}
}

func TestSamsungWGTReturns503WithoutLegacyFallback(t *testing.T) {
	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: t.TempDir()}}
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/samsung.wgt", nil)
	rec := httptest.NewRecorder()

	samsungWGTHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "samsung widget templates not installed") {
		t.Fatalf("unexpected response body: %q", rec.Body.String())
	}
}

// Оболочки для телевизоров — за входом, обе. /webos.ipk и так был закрыт гейтом tgauth,
// а /samsung.wgt проскакивал по расширению .wgt в списке публичных ассетов и раздавался
// анонимам. Проверяем сам аллоулист: он общий для основного гейта и зеркал (mirror.go).
func TestWidgetDownloadsRequireAuth(t *testing.T) {
	for _, p := range []string{"/samsung.wgt", "/webos.ipk"} {
		if gatePreAuthAllowed(httptest.NewRequest(http.MethodGet, "http://lampac.local"+p, nil)) {
			t.Fatalf("%s пропущен до входа", p)
		}
	}
	// Контроль: страница входа и её статика по-прежнему доступны без токена.
	for _, p := range []string{"/tg/auth", "/assets/app.js", "/plugin.lampa"} {
		if !gatePreAuthAllowed(httptest.NewRequest(http.MethodGet, "http://lampac.local"+p, nil)) {
			t.Fatalf("%s неожиданно закрыт", p)
		}
	}
}
