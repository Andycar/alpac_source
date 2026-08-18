package litesrc

import (
	"net/http"
	"os"
	"testing"
	"time"
)

// The obrut embed id is base64(kp) with the '=' padding stripped BEFORE the
// reverse. Stripping after the reverse is a no-op (the padding ends up at the
// front), which sent ids like "==wM2AjNzITM" upstream and got a 404 for every
// kinopoisk id whose digit count is not a multiple of 3.
func TestZetflixEncodeObrutKPIDStripsPaddingBeforeReverse(t *testing.T) {
	cases := []struct {
		kp   int64
		want string
	}{
		{301, "xAzM"},           // 3 digits — no padding, unaffected
		{435, "1MDN"},           // 3 digits
		{464963, "zYTO0YDN"},    // 6 digits — no padding
		{1236063, "wM2AjNzITM"}, // 7 digits — two '=' before the fix
		{12345678, "gzN2UDNzITM"},
		{1234, "ANzITM"}, // 4 digits — two '='
	}
	for _, tc := range cases {
		if got := zetflixEncodeObrutKPID(tc.kp); got != tc.want {
			t.Errorf("zetflixEncodeObrutKPID(%d) = %q, want %q", tc.kp, got, tc.want)
		}
	}
}

// ZETFLIX_LIVE=1 go test ./internal/litesrc/ -run TestZetflixObrutLive -v
//
// Confirms the encoded id is what obrut actually accepts: the fixed encoding
// returns the player page, the old (padding-after-reverse) form 404s.
func TestZetflixObrutLive(t *testing.T) {
	if os.Getenv("ZETFLIX_LIVE") == "" {
		t.Skip("set ZETFLIX_LIVE=1 to hit obrut.show")
	}
	client := &http.Client{Timeout: 25 * time.Second}
	if proxied, ok := liveProxyClient(t); ok {
		client = proxied
	}

	get := func(encoded string) int {
		req, err := http.NewRequest(http.MethodGet, "https://"+zetflixObrutHost+"/embed/AO/kinopoisk/"+encoded+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
		req.Header.Set("Referer", "https://kinogo.media/") // obrut 404s without it
		req.Header.Set("sec-fetch-dest", "iframe")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := get(zetflixEncodeObrutKPID(1236063)); code != http.StatusOK {
		t.Fatalf("fixed encoding got %d, want 200", code)
	}
	if code := get("==wM2AjNzITM"); code == http.StatusOK {
		t.Fatal("the old padded encoding unexpectedly works — re-check the fix's premise")
	}
}
