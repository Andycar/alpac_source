package iptvhttp

import (
	ejson "encoding/json"
	"strings"
	"testing"

	"lampac-go/internal/iptv"
)

// The /api/iptv/playlists and /channels responses must NEVER carry the raw upstream
// source — the paid playlist URL, EPG URLs, per-channel CDN stream URL, or the
// User-Agent/Referer credentials. Leaking them dumped the paid source straight into
// the browser Network tab (owner: «отображаем наш платный плейлист»).

func TestIPTVPublicPlaylistHidesSource(t *testing.T) {
	p := iptv.Playlist{
		ID: "abc", Name: "Global", ChannelCount: 1692, IsGlobal: true,
		URL:     "https://pl.vivamax.pro/24e10f28fd6e/playlist.m3u8",
		EPGUrls: []string{"https://epg.vivamax.pro/epg.xml.gz"},
	}
	b, err := ejson.Marshal(toPublicPlaylist(p))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, secret := range []string{"vivamax", "playlist.m3u8", "epg.xml", "url", "epg_urls"} {
		if strings.Contains(strings.ToLower(s), secret) {
			t.Fatalf("public playlist leaks %q: %s", secret, s)
		}
	}
	// Sanity: the safe fields survive.
	for _, want := range []string{"Global", "1692", "is_global"} {
		if !strings.Contains(s, want) {
			t.Fatalf("public playlist dropped a safe field %q: %s", want, s)
		}
	}
}

func TestIPTVPublicChannelHidesSource(t *testing.T) {
	c := iptv.Channel{
		ID: "ch1", Name: "ТНТ HD", CleanName: "ТНТ", PlaylistID: "abc",
		URL:       "https://pl.vivamax.pro/24e10f28fd6e/tnt/index.m3u8?token=secret",
		UserAgent: "VivaSecretUA/1.0",
		Referer:   "https://vivamax.pro/",
		Logo:      "https://cdn.example/tnt.png",
		Catchup:   &iptv.Catchup{Type: "flussonic", Days: 7, Source: "https://pl.vivamax.pro/archive/{utc}"},
	}
	b, err := ejson.Marshal(toPublicChannel(c))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, secret := range []string{"vivamax", "index.m3u8", "token=secret", "VivaSecretUA", "user_agent", "referer", "archive", "\"source\""} {
		if strings.Contains(s, secret) {
			t.Fatalf("public channel leaks %q: %s", secret, s)
		}
	}
	// Sanity: the UI still gets what it needs (name, logo, catchup days).
	for _, want := range []string{"ТНТ", "tnt.png", "\"days\":7", "flussonic"} {
		if !strings.Contains(s, want) {
			t.Fatalf("public channel dropped a needed field %q: %s", want, s)
		}
	}
}
