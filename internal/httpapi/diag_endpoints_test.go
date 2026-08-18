package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHeadersEchoHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/headers", nil)
	req.Header.Set("X-Test-Header", "abc123")
	headersEchoHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var payload map[string]map[string][]string
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got := payload["headers"]["X-Test-Header"]; len(got) != 1 || got[0] != "abc123" {
		t.Fatalf("unexpected header echo: %+v", payload)
	}
}

func TestMyIPAndGeoHandlers(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/myip", nil)
	req.RemoteAddr = "10.1.2.3:4567"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.1")
	req.Header.Set("CF-IPCountry", "DE")

	recIP := httptest.NewRecorder()
	myIPHandler(recIP, req)
	if recIP.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recIP.Code)
	}
	if strings.TrimSpace(recIP.Body.String()) != "203.0.113.10" {
		t.Fatalf("unexpected ip body: %s", recIP.Body.String())
	}

	recGeo := httptest.NewRecorder()
	geoHandler(recGeo, req)
	if recGeo.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recGeo.Code)
	}
	var geo map[string]string
	if err := stdjson.Unmarshal(recGeo.Body.Bytes(), &geo); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if geo["ip"] != "203.0.113.10" || geo["country"] != "DE" {
		t.Fatalf("unexpected geo payload: %+v", geo)
	}
}

func TestReqInfoAndChromiumPing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/reqinfo?a=1", nil)
	req.RemoteAddr = "192.0.2.10:3456"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("User-Agent", "LampacTestAgent/1.0")

	recInfo := httptest.NewRecorder()
	reqInfoHandler(recInfo, req)
	if recInfo.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recInfo.Code)
	}
	var info map[string]any
	if err := stdjson.Unmarshal(recInfo.Body.Bytes(), &info); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if info["path"] != "/reqinfo" {
		t.Fatalf("unexpected reqinfo path: %+v", info)
	}
	if info["scheme"] != "https" {
		t.Fatalf("unexpected reqinfo scheme: %+v", info)
	}

	recPing := httptest.NewRecorder()
	chromiumPingHandler(recPing, httptest.NewRequest(http.MethodGet, "http://lampac.local/api/chromium/ping", nil))
	if recPing.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recPing.Code)
	}
	if !strings.Contains(recPing.Body.String(), `"status":"ok"`) {
		t.Fatalf("unexpected ping body: %s", recPing.Body.String())
	}
}
