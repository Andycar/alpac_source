package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestLiteSourceChecksearchWithoutLegacy(t *testing.T) {
	cfg := config.Config{}

	rec := httptest.NewRecorder()
	req := authedTestRequest(httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/rezka?checksearch=true", nil))
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"rch":false`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestLiteSourceNotImplementedWithoutLegacy(t *testing.T) {
	cfg := config.Config{}

	rec := httptest.NewRecorder()
	req := authedTestRequest(httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/jac", nil))
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}

// TestLiteSourceSourceDiscoveryGatedWithoutAuth covers the 2026-05-28
// auth-bypass fix: when an auth method IS configured but the request
// has no user, source-discovery endpoints return a safe empty response.
//
// TestLiteSourceSourceDiscoveryGatedWithoutAuth and
// TestLiteSourceCompatOpenWhenNoAuthConfig were removed 2026-05-28 along
// with the source-discovery gate. The gate caused regressions across
// Lampa forks (synthetic balancer → "Ошибка" instead of a banner). The
// per-handler auth check went away; only TestIsLiteStreamPath stays as
// classifier coverage for future gate work.

// Stream-path classification is covered by TestIsLiteStreamPath in
// lite_auth_gate_test.go. End-to-end "stream path passes through gate"
// can't be black-box tested from this handler — both the gate's
// early-return and the balancer-disabled short-circuit write the same
// `{}` body for the same input. The right indirect check is the
// classification test plus production /proxy/ playback monitoring.
