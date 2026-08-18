package litesrc

import "testing"

func TestIsYouTubeImageHost(t *testing.T) {
	allowed := []string{
		"i.ytimg.com", "i9.ytimg.com", "ytimg.com",
		"yt3.ggpht.com", "ggpht.com", "lh3.googleusercontent.com",
		"yt3.googleusercontent.com", "I.YTIMG.COM", // case-insensitive
		"i.ytimg.com:443", // port stripped
	}
	for _, h := range allowed {
		if !isYouTubeImageHost(h) {
			t.Errorf("isYouTubeImageHost(%q) = false, want true", h)
		}
	}

	// SSRF guard: anything not a YouTube image host must be rejected, including look-alikes.
	denied := []string{
		"", "localhost", "127.0.0.1", "169.254.169.254", "example.com",
		"ytimg.com.evil.com", // suffix trick
		"evilytimg.com",      // not a dotted suffix
		"googleusercontent.com.evil.io",
		"ggpht.com.attacker.net",
	}
	for _, h := range denied {
		if isYouTubeImageHost(h) {
			t.Errorf("isYouTubeImageHost(%q) = true, want false", h)
		}
	}
}

func TestYtImgProxyMaybe(t *testing.T) {
	const host = "https://tv.example.com"

	// Valid YouTube avatar URL → proxied through our endpoint, raw URL escaped into ?url=.
	raw := "https://yt3.ggpht.com/abc=s88-c-k-c0x00ffffff-no-rj"
	got := ytImgProxyMaybe(host, raw)
	want := host + "/lite/youtube/img?url=https%3A%2F%2Fyt3.ggpht.com%2Fabc%3Ds88-c-k-c0x00ffffff-no-rj"
	if got != want {
		t.Errorf("ytImgProxyMaybe valid:\n got=%q\nwant=%q", got, want)
	}

	// Empty / non-YouTube / non-http → "" so the caller falls back instead of leaking/SSRF.
	for _, bad := range []string{"", "   ", "https://evil.com/x.jpg", "ftp://i.ytimg.com/x.jpg", "not a url"} {
		if out := ytImgProxyMaybe(host, bad); out != "" {
			t.Errorf("ytImgProxyMaybe(%q) = %q, want \"\"", bad, out)
		}
	}
}
