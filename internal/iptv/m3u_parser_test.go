package iptv

import (
	"strings"
	"testing"
)

const testM3U = `#EXTM3U x-tvg-url="http://epg.example.com/xmltv.xml"
#EXTINF:-1 tvg-id="ch1" tvg-name="Channel One" tvg-logo="http://logo.com/1.png" group-title="News",Channel One HD
http://stream.example.com/ch1/index.m3u8
#EXTINF:-1 tvg-id="ch2" tvg-logo="http://logo.com/2.png" group-title="Sports",ESPN 4K
http://stream.example.com/ch2/index.m3u8
#EXTINF:-1 group-title="Movies",Movie Channel
http://stream.example.com/ch3/index.m3u8
#EXTINF:-1 tvg-id="ch4" group-title="News" catchup="default" catchup-days="7" catchup-source="?utc={utc}&lutc={lutc}",BBC World News
http://stream.example.com/ch4/index.m3u8
`

func TestParseM3U_Basic(t *testing.T) {
	var channels []Channel
	header, err := ParseM3U(strings.NewReader(testM3U), func(ch Channel) {
		channels = append(channels, ch)
	})
	if err != nil {
		t.Fatalf("ParseM3U error: %v", err)
	}

	// Header.
	if len(header.EPGUrls) != 1 || header.EPGUrls[0] != "http://epg.example.com/xmltv.xml" {
		t.Errorf("expected 1 EPG URL, got %v", header.EPGUrls)
	}

	// Channels count.
	if len(channels) != 4 {
		t.Fatalf("expected 4 channels, got %d", len(channels))
	}

	// Channel 1.
	ch := channels[0]
	if ch.Name != "Channel One HD" {
		t.Errorf("ch1 name = %q, want %q", ch.Name, "Channel One HD")
	}
	if ch.TvgID != "ch1" {
		t.Errorf("ch1 tvg-id = %q, want %q", ch.TvgID, "ch1")
	}
	if ch.TvgName != "Channel One" {
		t.Errorf("ch1 tvg-name = %q, want %q", ch.TvgName, "Channel One")
	}
	if ch.Logo != "http://logo.com/1.png" {
		t.Errorf("ch1 logo = %q", ch.Logo)
	}
	if ch.Group != "News" {
		t.Errorf("ch1 group = %q, want %q", ch.Group, "News")
	}
	if ch.Quality != "HD" {
		t.Errorf("ch1 quality = %q, want %q", ch.Quality, "HD")
	}
	if ch.CleanName != "Channel One" {
		t.Errorf("ch1 clean name = %q, want %q", ch.CleanName, "Channel One")
	}

	// Channel 2 — 4K quality.
	ch = channels[1]
	if ch.Quality != "4K" {
		t.Errorf("ch2 quality = %q, want %q", ch.Quality, "4K")
	}

	// Channel 3 — no quality tag.
	ch = channels[2]
	if ch.Quality != "" {
		t.Errorf("ch3 quality = %q, want empty", ch.Quality)
	}

	// Channel 4 — catchup.
	ch = channels[3]
	if ch.Catchup == nil {
		t.Fatal("ch4 catchup is nil")
	}
	if ch.Catchup.Type != "default" {
		t.Errorf("ch4 catchup type = %q, want %q", ch.Catchup.Type, "default")
	}
	if ch.Catchup.Days != 7 {
		t.Errorf("ch4 catchup days = %d, want 7", ch.Catchup.Days)
	}
	if ch.Catchup.Source != "?utc={utc}&lutc={lutc}" {
		t.Errorf("ch4 catchup source = %q", ch.Catchup.Source)
	}
}

func TestParseM3U_VLCOpt(t *testing.T) {
	m3u := `#EXTM3U
#EXTINF:-1,Protected Channel
#EXTVLCOPT:http-user-agent=CustomUA/1.0
#EXTVLCOPT:http-referrer=http://example.com/
http://stream.example.com/protected/index.m3u8
`
	var channels []Channel
	_, err := ParseM3U(strings.NewReader(m3u), func(ch Channel) {
		channels = append(channels, ch)
	})
	if err != nil {
		t.Fatalf("ParseM3U error: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	if channels[0].UserAgent != "CustomUA/1.0" {
		t.Errorf("user agent = %q, want %q", channels[0].UserAgent, "CustomUA/1.0")
	}
	if channels[0].Referer != "http://example.com/" {
		t.Errorf("referer = %q, want %q", channels[0].Referer, "http://example.com/")
	}
}

func TestParseM3U_MultipleEPGUrls(t *testing.T) {
	m3u := `#EXTM3U x-tvg-url="http://epg1.com/xmltv.xml,http://epg2.com/xmltv.xml"
#EXTINF:-1,Test Channel
http://stream.example.com/test.m3u8
`
	var channels []Channel
	header, err := ParseM3U(strings.NewReader(m3u), func(ch Channel) {
		channels = append(channels, ch)
	})
	if err != nil {
		t.Fatalf("ParseM3U error: %v", err)
	}
	if len(header.EPGUrls) != 2 {
		t.Errorf("expected 2 EPG URLs, got %d: %v", len(header.EPGUrls), header.EPGUrls)
	}
}

func TestParseM3U_SkipsInvalidURLs(t *testing.T) {
	m3u := `#EXTM3U
#EXTINF:-1,Good Channel
http://stream.example.com/good.m3u8
#EXTINF:-1,Bad Channel
not_a_url
#EXTINF:-1,Another Good
https://stream.example.com/good2.m3u8
`
	var channels []Channel
	_, err := ParseM3U(strings.NewReader(m3u), func(ch Channel) {
		channels = append(channels, ch)
	})
	if err != nil {
		t.Fatalf("ParseM3U error: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels (skipping invalid URL), got %d", len(channels))
	}
}

func TestParseM3U_EmptyInput(t *testing.T) {
	var channels []Channel
	header, err := ParseM3U(strings.NewReader(""), func(ch Channel) {
		channels = append(channels, ch)
	})
	if err != nil {
		t.Fatalf("ParseM3U error: %v", err)
	}
	if len(channels) != 0 {
		t.Errorf("expected 0 channels, got %d", len(channels))
	}
	if len(header.EPGUrls) != 0 {
		t.Errorf("expected 0 EPG URLs, got %d", len(header.EPGUrls))
	}
}

func TestDetectQuality(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"CNN HD", "HD"},
		{"ESPN FHD", "FHD"},
		{"Discovery 4K", "4K"},
		{"Fox SD", "SD"},
		{"BBC UHD", "4K"},
		{"Channel 1080p", "FHD"},
		{"Channel 720p", "HD"},
		{"Regular Channel", ""},
	}
	for _, tc := range tests {
		if got := detectQuality(tc.name); got != tc.want {
			t.Errorf("detectQuality(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCleanChannelName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"CNN HD", "CNN"},
		{"ESPN (FHD)", "ESPN"},
		{"Discovery 4K", "Discovery"},
		{"Regular Channel", "Regular Channel"},
		{"BBC [HD]", "BBC"},
	}
	for _, tc := range tests {
		if got := cleanChannelName(tc.name); got != tc.want {
			t.Errorf("cleanChannelName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestExtractAttr(t *testing.T) {
	line := `#EXTINF:-1 tvg-id="abc" tvg-name="Test" group-title="News",Test Channel`
	if v := extractAttr(line, "tvg-id"); v != "abc" {
		t.Errorf("tvg-id = %q, want %q", v, "abc")
	}
	if v := extractAttr(line, "tvg-name"); v != "Test" {
		t.Errorf("tvg-name = %q, want %q", v, "Test")
	}
	if v := extractAttr(line, "group-title"); v != "News" {
		t.Errorf("group-title = %q, want %q", v, "News")
	}
	if v := extractAttr(line, "nonexistent"); v != "" {
		t.Errorf("nonexistent = %q, want empty", v)
	}
}

func TestChannelID_Deterministic(t *testing.T) {
	id1 := channelID("http://example.com/stream.m3u8", "Test Channel")
	id2 := channelID("http://example.com/stream.m3u8", "Test Channel")
	if id1 != id2 {
		t.Errorf("channelID not deterministic: %q != %q", id1, id2)
	}

	// Different URL → different ID.
	id3 := channelID("http://example.com/other.m3u8", "Test Channel")
	if id1 == id3 {
		t.Error("different URLs should produce different IDs")
	}
}

// Some providers specify the group via a separate #EXTGRP: line instead of the inline
// group-title="..." attribute (real playlist: fluxeras uplist, 0 group-title, 2308 #EXTGRP).
// Without #EXTGRP support every channel had an empty group → no category split.
func TestParseM3U_ExtGrpLine(t *testing.T) {
	m3u := "#EXTM3U\n" +
		"#EXTINF:0 tvg-rec=\"7\",.black\n" +
		"#EXTGRP:кино\n" +
		"http://h/iptv/708/index.m3u8\n" +
		"#EXTINF:0 tvg-rec=\"0\",Матч ТВ\n" +
		"#EXTGRP:спорт\n" +
		"http://h/iptv/100/index.m3u8\n" +
		// group-title takes precedence over #EXTGRP when both present
		"#EXTINF:-1 group-title=\"новости\",Россия 24\n" +
		"#EXTGRP:другие\n" +
		"http://h/iptv/200/index.m3u8\n"
	var chans []Channel
	if _, err := ParseM3U(strings.NewReader(m3u), func(ch Channel) { chans = append(chans, ch) }); err != nil {
		t.Fatalf("ParseM3U error: %v", err)
	}
	if len(chans) != 3 {
		t.Fatalf("got %d channels, want 3", len(chans))
	}
	if chans[0].Group != "кино" {
		t.Errorf("ch0 group = %q, want кино (from #EXTGRP)", chans[0].Group)
	}
	if chans[1].Group != "спорт" {
		t.Errorf("ch1 group = %q, want спорт (from #EXTGRP)", chans[1].Group)
	}
	if chans[2].Group != "новости" {
		t.Errorf("ch2 group = %q, want новости (group-title wins over #EXTGRP)", chans[2].Group)
	}
}
