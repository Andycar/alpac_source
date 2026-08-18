package litesrc

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type remuxRewriteTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func TestRemuxWeblinkViewParsing(t *testing.T) {
	html := `x,"weblink_get":{"count":"1","url":"https://cloclo54.cloud.mail.ru/public/26qdR1pzJsarGsTYX7gZ/g/no"},` +
		`"weblink_thumbnails":{"count":"50","url":"https://thumb.cloud.mail.ru/weblink/thumb/"},` +
		`"weblink_view":{"count":"50","url":"https://cloclo51.cloud.mail.ru/weblink/view/"}},x`
	m := remuxWeblinkViewRe.FindStringSubmatch(html)
	if len(m) < 2 {
		t.Fatal("weblink_view regex did not match the real page shape")
	}
	base := strings.TrimRight(strings.ReplaceAll(m[1], `\/`, "/"), "/")
	got := base + "/" + "LbZT/a7wCwHSRv"
	want := "https://cloclo51.cloud.mail.ru/weblink/view/LbZT/a7wCwHSRv"
	if got != want {
		t.Fatalf("stream url = %q, want %q", got, want)
	}
	// Regression guard: must not fall back to the weblink_get /public/<token>/g/no base.
	if strings.Contains(got, "/public/") || strings.Contains(got, "/g/no") {
		t.Fatalf("regressed to weblink_get download base: %s", got)
	}
}
