package iptvhttp

import (
	"strings"
	"testing"

	"lampac-go/internal/iptv"
	"lampac-go/internal/proxylink"
)

// TestIPTVResolveStreamURL pins the direct-vs-proxy decision and the
// fail-closed guarantee: when a stream MUST be proxied (header-gated, browser
// cleartext, or proxy=all playlist) and the proxy is unavailable, the raw
// upstream URL must never be returned.
func TestIPTVResolveStreamURL(t *testing.T) {
	httpsCh := &iptv.Channel{URL: "https://cdn.example/stream.m3u8"}

	// Браузерный клиент (без direct=1) НЕ получает сырой URL даже для https:
	// IPTV-CDN не отдают CORS-заголовки, fetch манифеста падает до видео.
	// nil proxy → fail closed.
	if u, errSlug := iptvResolveStreamURL(httpsCh.URL, httpsCh, nil, nil, "1.2.3.4", "https://app.host", false, ""); errSlug == "" || u != "" {
		t.Fatalf("browser https fail-closed: got (%q,%q)", u, errSlug)
	}

	// Safe direct play: https, direct=1 (нативный плеер), no credential headers.
	if u, errSlug := iptvResolveStreamURL(httpsCh.URL, httpsCh, nil, nil, "1.2.3.4", "https://app.host", true, ""); errSlug != "" || u != httpsCh.URL {
		t.Fatalf("direct https: got (%q,%q)", u, errSlug)
	}

	// Cleartext http:// in the BROWSER must be proxied; nil proxy → fail closed.
	httpCh := &iptv.Channel{URL: "http://cdn.example/stream.m3u8"}
	if u, errSlug := iptvResolveStreamURL(httpCh.URL, httpCh, nil, nil, "1.2.3.4", "https://app.host", false, ""); errSlug == "" || u != "" {
		t.Fatalf("http fail-closed: got (%q,%q)", u, errSlug)
	}

	// Cleartext http:// with direct=1 (native app, usesCleartextTraffic) plays raw —
	// это гео-кейс: стрим уходит с IP устройства, а не сервера.
	if u, errSlug := iptvResolveStreamURL(httpCh.URL, httpCh, nil, nil, "1.2.3.4", "https://app.host", true, ""); errSlug != "" || u != httpCh.URL {
		t.Fatalf("http direct native: got (%q,%q)", u, errSlug)
	}

	// Header-gated https must be proxied EVEN with direct=1 (headers live server-side);
	// nil proxy → fail closed.
	uaCh := &iptv.Channel{URL: "https://cdn.example/stream.m3u8", UserAgent: "SpecialUA"}
	if u, errSlug := iptvResolveStreamURL(uaCh.URL, uaCh, nil, nil, "1.2.3.4", "https://app.host", true, ""); errSlug == "" || u != "" {
		t.Fatalf("ua fail-closed: got (%q,%q)", u, errSlug)
	}

	// Playlist-level UA is a header requirement too — proxied, fail closed without proxy.
	uaPl := &iptv.Playlist{UserAgent: "PanelUA"}
	if u, errSlug := iptvResolveStreamURL(httpsCh.URL, httpsCh, uaPl, nil, "1.2.3.4", "https://app.host", true, ""); errSlug == "" || u != "" {
		t.Fatalf("playlist-ua fail-closed: got (%q,%q)", u, errSlug)
	}

	// proxy=all playlist (sharing protection) → fail closed without proxy, direct or not.
	pl := &iptv.Playlist{ProxyMode: "all"}
	if u, errSlug := iptvResolveStreamURL(httpsCh.URL, httpsCh, pl, nil, "1.2.3.4", "https://app.host", true, ""); errSlug == "" || u != "" {
		t.Fatalf("proxy-all fail-closed: got (%q,%q)", u, errSlug)
	}
}

// Резолвер НЕ должен заводить сессию сам. Он зовётся по разу на канал, и
// первая версия этого механизма плодила по сессии на каждый — две тысячи штук
// за одно открытие списка каналов. Сессию заводит ручка, одну на запрос.
func TestResolverDoesNotMintSessions(t *testing.T) {
	links, err := proxylink.New(proxylink.Options{
		CacheDir: t.TempDir(), EncryptAES: true,
		SharedSecret:   "0123456789abcdef0123456789abcdef",
		SessionPlugins: []string{"iptv"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ch := &iptv.Channel{URL: "http://cdn.example/stream.m3u8"}
	sid, _ := links.OpenSession("1.2.3.4", 42, "dev1", 0)
	if sid == "" {
		t.Fatal("сессия не заведена")
	}
	for i := 0; i < 50; i++ {
		u, errSlug := iptvResolveStreamURL(ch.URL, ch, nil, links, "1.2.3.4", "https://app.host", false, sid)
		if errSlug != "" {
			t.Fatalf("резолв не удался: %s", errSlug)
		}
		if !strings.Contains(u, "sid="+sid) {
			t.Fatalf("ссылка без sid потока: %s", u)
		}
	}
	if live, revoked := links.SessionStats(); live != 1 || revoked != 0 {
		t.Fatalf("резолвер наплодил сессий: живых %d, погашенных %d (ожидалась одна)", live, revoked)
	}
}
