package litesrc

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// Live integration tests that only run when a debug Chrome is reachable on
// $ZONA_CHROME_PORT (or :9333 by default). They exercise the real Zona
// player to validate end-to-end stream resolution. Run with:
//
//	ZONA_CHROME_PORT=9333 go test ./internal/httpapi/ -run ZonaLive -v
//
// These are intentionally skipped on CI — they depend on a running browser
// and an external service.

func zonaLiveSkipIfNoChrome(t *testing.T) int {
	t.Helper()
	port := 9333
	if v := os.Getenv("ZONA_CHROME_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			port = p
		}
	}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/json/version"
	resp, err := http.Get(url)
	if err != nil || resp.StatusCode != 200 {
		t.Skipf("no debug Chrome at %s (%v)", url, err)
	}
	if resp != nil {
		resp.Body.Close()
	}
	return port
}

// TestZonaLiveMovie exercises the full flow for a movie: open
// kinoserial.online, call getStreams, parse chunks. Uses Крик 7 (kp 5364826)
// which was known to have working sources at dev time.
func TestZonaLiveMovie(t *testing.T) {
	port := zonaLiveSkipIfNoChrome(t)
	zonaBrowser.mu.Lock()
	if zonaBrowser.allocStop != nil {
		zonaBrowser.allocStop()
	}
	zonaBrowser.allocCtx, zonaBrowser.allocStop, zonaBrowser.port = nil, nil, 0
	zonaBrowser.mu.Unlock()

	z := &zonaChecker{}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	chunks, ok := z.fetchStreamsViaChromePort(ctx, 5364826, "", port)
	if !ok {
		t.Fatal("fetchStreamsViaChromePort failed")
	}
	if len(chunks) == 0 {
		t.Fatal("no chunks returned")
	}
	streams := zonaParseStreams(chunks)
	if len(streams) == 0 {
		t.Fatalf("no usable streams after filter; raw chunks=%d", len(chunks))
	}
	// At least one MOBILINK or HDVB result expected.
	var mobilink, hdvb int
	for _, s := range streams {
		switch s.Extractor {
		case "MOBILINK":
			mobilink++
		case "HDVB":
			hdvb++
		}
	}
	t.Logf("zona live movie: chunks=%d streams=%d mobilink=%d hdvb=%d",
		len(chunks), len(streams), mobilink, hdvb)
	if mobilink == 0 && hdvb == 0 {
		t.Error("expected at least one MOBILINK or HDVB stream")
	}
}

// TestZonaLiveSerial runs the same flow for a serial episode.
// Uses Squid Game S01E01 (kp 1301710) as a known-working target.
func TestZonaLiveSerial(t *testing.T) {
	port := zonaLiveSkipIfNoChrome(t)
	zonaBrowser.mu.Lock()
	if zonaBrowser.allocStop != nil {
		zonaBrowser.allocStop()
	}
	zonaBrowser.allocCtx, zonaBrowser.allocStop, zonaBrowser.port = nil, nil, 0
	zonaBrowser.mu.Unlock()

	z := &zonaChecker{}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	chunks, ok := z.fetchStreamsViaChromePort(ctx, 1301710, "S01E01", port)
	if !ok {
		t.Fatal("fetchStreamsViaChromePort failed")
	}
	streams := zonaParseStreams(chunks)
	if len(streams) == 0 {
		t.Fatalf("no usable streams; raw chunks=%d", len(chunks))
	}
	var voices = map[string]struct{}{}
	for _, s := range streams {
		voices[s.Extractor+"|"+zonaVoiceName(s.Extractor, s.Translation)] = struct{}{}
	}
	t.Logf("zona live serial: chunks=%d streams=%d voices=%d",
		len(chunks), len(streams), len(voices))
}
