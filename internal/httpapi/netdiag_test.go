package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lampac-go/internal/config"
)

func TestNetDiagFailSummary(t *testing.T) {
	rows := []NetDiagRow{
		{Host: "https://tv.example.com", DNS: 12, TCP: 40, TLS: 90, HTTP: 200, Code: 200},
		{Host: "https://edge-a.example.org", DNS: 10, TCP: -1, TLS: -2, HTTP: -2},
		{Host: "https://edge-b.example.net:2053", DNS: 9, TCP: 30, TLS: -1},
		{Host: "https://cdn.example", DNS: 9, TCP: 30, TLS: 80, HTTP: 300, Code: 502},
	}
	got := netDiagFailSummary(rows)
	want := "tcp:edge-a.example.org tls:edge-b.example.net:2053 http502:cdn.example"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if s := netDiagFailSummary(rows[:1]); s != "ok" {
		t.Fatalf("all-ok summary = %q", s)
	}
}

func TestNetDiagASNKey(t *testing.T) {
	if k := asnKey("95.24.13.7"); k != "95.24.13.0" {
		t.Fatalf("v4 key = %q", k)
	}
	if k := asnKey("2a00:1370:81ab:1234::5"); k != "2a00:1370:81ab::" {
		t.Fatalf("v6 key = %q", k)
	}
	if k := asnKey("garbage"); k != "" {
		t.Fatalf("bad ip key = %q", k)
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.5", "192.168.1.1", "::1"} {
		if isPublicIP(ip) {
			t.Fatalf("%s reported public", ip)
		}
	}
	if !isPublicIP("95.24.13.7") {
		t.Fatal("public v4 reported private")
	}
}

// Приём → кольцо → диск → перечитывание: то, на чём держится страница netdiag.
func TestNetDiagReceiveAndReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", dir)
	// свежее состояние пакета для этого теста
	netDiagMu.Lock()
	netDiagBuf = nil
	netDiagPrunedDay = ""
	netDiagMu.Unlock()
	netDiagOnce.Do(func() {}) // загрузка с диска уже «выполнена» — диск пуст

	cfg := config.Config{}
	cfg.Web.WeblogCollect = true
	h := netDiagReceiveHandler(cfg)

	body := `{"sid":"abc","ua":"ALPAC/1.0.61 (Android)","platform":"androidtv","reason":"stall","net":"wifi",
	  "source":"balancer","context":"«Фильм» · #1",
	  "rows":[{"host":"https://tv.example.com","role":"api","dns":12,"tcp":40,"tls":90,"http":210,"code":200,"kbps":8400},
	          {"host":"https://edge-a.example.org","role":"edge","dns":10,"tcp":-1,"tls":-2,"http":-2,"err":"ConnectException: ECONNREFUSED"}],
	  "stream":{"summary":"просадок 2","events":[{"t":1,"kind":"rebuffer","cause":"network","detail":"x"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/lite/netdiag", bytes.NewBufferString(body))
	req.RemoteAddr = "127.0.0.1:1234" // приватный → без похода в ip-api
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got := netDiagList(0, 10)
	if len(got) != 1 {
		t.Fatalf("ring has %d reports, want 1", len(got))
	}
	r := got[0]
	if r.Reason != "stall" || r.Net != "wifi" || len(r.Rows) != 2 || r.Rows[1].TCP != -1 || len(r.Stream) == 0 {
		t.Fatalf("stored report mangled: %+v", r)
	}
	if r.Platform != "androidtv" || r.Source != "balancer" {
		t.Fatalf("platform/source lost: %+v", r)
	}

	// файл дня появился и перечитывается в пустое кольцо
	day := time.UnixMilli(r.RT).UTC().Format("2006-01-02")
	if _, err := os.Stat(filepath.Join(dir, "database", "netdiag", day+".jsonl")); err != nil {
		t.Fatalf("day file missing: %v", err)
	}
	netDiagMu.Lock()
	netDiagBuf = nil
	netDiagMu.Unlock()
	netDiagLoad()
	if again := netDiagList(0, 10); len(again) != 1 || again[0].ID != r.ID {
		t.Fatalf("reload from disk failed: %+v", again)
	}

	// окно: отчёт старше since не отдаётся
	if old := netDiagList(time.Now().Add(time.Hour).UnixMilli(), 10); len(old) != 0 {
		t.Fatalf("since filter ignored: %d", len(old))
	}

	// пустые rows → 400; выключенный сбор → 404
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/lite/netdiag", bytes.NewBufferString(`{"rows":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty rows status = %d", rec.Code)
	}
	off := config.Config{}
	rec = httptest.NewRecorder()
	netDiagReceiveHandler(off)(rec, httptest.NewRequest(http.MethodPost, "/lite/netdiag", bytes.NewBufferString(body)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled collect status = %d", rec.Code)
	}
}
