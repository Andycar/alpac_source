package iptv

import "testing"

// TestOfficialHostMatch: первоисточником делает ХОСТ, а не совпадение строки
// где-нибудь в пути — иначе панель с «1tv.ru» в имени файла притворилась бы
// вещателем.
func TestOfficialHostMatch(t *testing.T) {
	m := newOfficialMatcher([]string{"my-broadcaster.tv"})
	official := []string{
		"https://live-vgtrksmotrim.cdnvideo.ru/vgtrksmotrim/x.m3u8",
		"https://cdn-dvr.ntv.ru/5_minut/index.m3u8",
		"https://edge1.1internet.tv/dash-live2/streams/1tv/1tvdash.mpd",
		"http://stream.my-broadcaster.tv/ch/1.m3u8",
	}
	for _, u := range official {
		if !m.isOfficial(u) {
			t.Errorf("должен считаться первоисточником: %s", u)
		}
	}
	notOfficial := []string{
		"http://wdqiofmj.klaipedalt.xyz/iptv/KEY/1924/index.m3u8",
		"http://31.148.48.15/Pervii_kanal_HD/index.m3u8",
		"http://panel.example/playlists/1tv.ru.m3u8", // домен вещателя в ПУТИ
		"",
	}
	for _, u := range notOfficial {
		if m.isOfficial(u) {
			t.Errorf("НЕ первоисточник, а совпало: %s", u)
		}
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://a.b.ru/x/y.m3u8":       "a.b.ru",
		"http://1.2.3.4:8080/s.m3u8":    "1.2.3.4",
		"http://u:p@host.tv/a":          "host.tv",
		"HTTPS://UP.CASE.RU/x":          "up.case.ru",
		"host-without-scheme.ru/a.m3u8": "host-without-scheme.ru",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPreferOfficialOverPanelQuality — ради чего всё: панельный 4K проигрывает
// потоку вещателя, потому что панель падает целиком (кончилась подписка, лимит
// сессий), а вещатель — нет. При выключенном preferOfficial выигрывает качество.
func TestPreferOfficialOverPanelQuality(t *testing.T) {
	donors := []Channel{
		{ID: "p", Name: "Россия 1", URL: "http://panel.xyz/iptv/KEY/100/index.m3u8", Quality: "4K", TvgID: "r1"},
		{ID: "o", Name: "Россия 1", URL: "https://live-vgtrksmotrim.cdnvideo.ru/x/playlist.m3u8", Quality: "SD", TvgID: "r1"},
	}
	s := newRegistryStore(t, donors)
	s.preferOfficial = true
	s.IngestRegistry(true)

	id := regChannelID(normChannelName("Россия 1"))
	ch, _ := s.GetChannel(0, id)
	if ch == nil || ch.URL != "https://live-vgtrksmotrim.cdnvideo.ru/x/playlist.m3u8" {
		t.Fatalf("первоисточник должен обгонять панельный 4K, got %v", ch)
	}

	// Первоисточник умер — играем с панели, а не молчим.
	s.healthOn = true
	s.health.mu.Lock()
	s.health.dead["https://live-vgtrksmotrim.cdnvideo.ru/x/playlist.m3u8"] = struct{}{}
	s.health.mu.Unlock()
	ch, _ = s.GetChannel(0, id)
	if ch.URL != "http://panel.xyz/iptv/KEY/100/index.m3u8" {
		t.Fatalf("мёртвый первоисточник обязан уступить живой панели, got %s", ch.URL)
	}

	// Выключенный preferOfficial возвращает приоритет качеству.
	s.health.mu.Lock()
	s.health.dead = map[string]struct{}{}
	s.health.mu.Unlock()
	s.preferOfficial = false
	ch, _ = s.GetChannel(0, id)
	if ch.URL != "http://panel.xyz/iptv/KEY/100/index.m3u8" {
		t.Fatalf("без preferOfficial должен побеждать 4K, got %s", ch.URL)
	}
}

// TestPinnedStillWinsOverOfficial: закреплённое руками главнее любых эвристик.
func TestPinnedStillWinsOverOfficial(t *testing.T) {
	donors := []Channel{
		{ID: "o", Name: "НТВ", URL: "https://cdn-dvr.ntv.ru/x/index.m3u8", Quality: "HD", TvgID: "ntv"},
	}
	s := newRegistryStore(t, donors)
	s.preferOfficial = true
	s.IngestRegistry(true)
	id := regChannelID(normChannelName("НТВ"))
	if _, err := s.registry.Upsert(RegChannel{ID: id, Pinned: []RegSource{{URL: "http://my/own.m3u8"}}}); err != nil {
		t.Fatal(err)
	}
	ch, _ := s.GetChannel(0, id)
	if ch.URL != "http://my/own.m3u8" {
		t.Fatalf("pinned обязан быть выше первоисточника, got %s", ch.URL)
	}
}
