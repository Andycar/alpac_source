package litesrc

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"
)

// TestEngBrowserSniffLive drives the real headless-browser sniffer against a
// reachable ENG embed (embed.su / TwoEmbed). Guarded by LAMPAC_LIVE_ENG=1
// because it launches Chrome and hits the network.
//
//	LAMPAC_LIVE_ENG=1 go test ./internal/httpapi -run EngBrowserSniffLive -v
func TestEngBrowserSniffLive(t *testing.T) {
	if os.Getenv("LAMPAC_LIVE_ENG") != "1" {
		t.Skip("set LAMPAC_LIVE_ENG=1 for the live browser test")
	}
	wantRe := regexp.MustCompile(`\.m3u8`)
	abortRe := regexp.MustCompile(`(fonts\.googleapis|pixel\.embed|rtmark|doubleclick|googletagmanager)\.`)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	res, ok := engBrowserSniff(ctx, "twoembed", "https://embed.su/embed/movie/550", wantRe, abortRe, "", 35*time.Second)
	t.Logf("ok=%v url=%q headers=%v", ok, res.URL, res.Headers)
	if !ok || res.URL == "" {
		t.Fatal("no m3u8 captured from embed.su")
	}
}
