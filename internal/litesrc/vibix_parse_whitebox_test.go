package litesrc

import "testing"

func TestVibixParseVoiceGroups_MultiVoice(t *testing.T) {
	file := "[1080p]{Дубляж}https://a.example/1080.m3u8{Оригинал}https://b.example/1080.m3u8," +
		"[720p]{Дубляж}https://a.example/720.m3u8{Оригинал}https://b.example/720.m3u8"

	groups := vibixParseVoiceGroups(file)
	if len(groups) != 2 {
		t.Fatalf("expected 2 voice groups, got %d: %+v", len(groups), groups)
	}
	if groups[0].voice != "Дубляж" || groups[1].voice != "Оригинал" {
		t.Fatalf("unexpected voice order: %q, %q", groups[0].voice, groups[1].voice)
	}
	// Each voice must expose exactly its own two qualities, highest first.
	for _, g := range groups {
		if len(g.streams) != 2 {
			t.Fatalf("voice %q: expected 2 streams, got %d", g.voice, len(g.streams))
		}
		if g.streams[0]["quality"] != "1080p" || g.streams[1]["quality"] != "720p" {
			t.Fatalf("voice %q: unexpected quality order: %+v", g.voice, g.streams)
		}
	}
	if groups[0].streams[0]["url"] != "https://a.example/1080.m3u8" {
		t.Fatalf("Дубляж 1080 url wrong: %q", groups[0].streams[0]["url"])
	}
	if groups[1].streams[0]["url"] != "https://b.example/1080.m3u8" {
		t.Fatalf("Оригинал 1080 url wrong: %q", groups[1].streams[0]["url"])
	}
}

func TestVibixParseVoiceGroups_NoVoiceFallback(t *testing.T) {
	// Legacy/no-voice format (as in the API unit tests): a single default group.
	groups := vibixParseVoiceGroups("[1080]https://x.example/x.m3u8,[720]https://y.example/y.m3u8")
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d: %+v", len(groups), groups)
	}
	if groups[0].voice != "" {
		t.Fatalf("expected empty default voice, got %q", groups[0].voice)
	}
	if len(groups[0].streams) != 2 || groups[0].streams[0]["quality"] != "1080p" {
		t.Fatalf("unexpected default streams: %+v", groups[0].streams)
	}
}

func TestVibixParsePlayerFile_SerialJSON(t *testing.T) {
	js := `[{"title":"Сезон 1","folder":[{"title":"1 серия","file":"[1080p]{Voice}https://a/1.m3u8"}]}]`
	pl, ok := vibixParsePlayerFile(js)
	if !ok || len(pl) != 1 {
		t.Fatalf("expected 1 season, ok=%v pl=%+v", ok, pl)
	}
	if pl[0].Title != "Сезон 1" || len(pl[0].Folder) != 1 {
		t.Fatalf("unexpected season: %+v", pl[0])
	}
	if pl[0].Folder[0].Title != "1 серия" {
		t.Fatalf("unexpected episode title: %q", pl[0].Folder[0].Title)
	}
}

func TestVibixParsePlayerFile_BareMovie(t *testing.T) {
	pl, ok := vibixParsePlayerFile("[1080p]{Voice}https://a/movie.m3u8")
	if !ok || len(pl) != 1 {
		t.Fatalf("expected 1 movie item, ok=%v pl=%+v", ok, pl)
	}
	if pl[0].File == "" {
		t.Fatalf("expected movie file to be set, got: %+v", pl[0])
	}
}

func TestVibixParseStreams_DedupByQuality(t *testing.T) {
	// Two voices at the same quality must collapse to one entry per quality.
	streams := vibixParseStreams("[1080p]{A}https://a/1080.m3u8{B}https://b/1080.m3u8,[720p]{A}https://a/720.m3u8")
	if len(streams) != 2 {
		t.Fatalf("expected 2 qualities, got %d: %+v", len(streams), streams)
	}
	if streams[0]["quality"] != "1080p" || streams[1]["quality"] != "720p" {
		t.Fatalf("unexpected quality order: %+v", streams)
	}
	if streams[0]["url"] != "https://a/1080.m3u8" {
		t.Fatalf("expected first url per quality to win, got: %q", streams[0]["url"])
	}
}
