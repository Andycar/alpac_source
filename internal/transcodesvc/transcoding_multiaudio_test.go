package transcodesvc

import (
	"lampac-go/internal/transcode"
	"strings"
	"testing"
)

func maProbe() map[string]any {
	mkAud := func(abs int, codec, lang, title string) map[string]any {
		m := map[string]any{"index": float64(abs), "codec_type": "audio", "codec_name": codec}
		tags := map[string]any{}
		if lang != "" {
			tags["language"] = lang
		}
		if title != "" {
			tags["title"] = title
		}
		if len(tags) > 0 {
			m["tags"] = tags
		}
		return m
	}
	return map[string]any{"streams": []any{
		map[string]any{"index": float64(0), "codec_type": "video", "codec_name": "hevc"},
		mkAud(1, "eac3", "rus", "Дубляж"),
		mkAud(2, "aac", "eng", ""),
		mkAud(3, "truehd", "rus", "TrueHD"), // not shelvable → excluded
		mkAud(4, "dts", "ukr", ""),
		map[string]any{"index": float64(5), "codec_type": "subtitle", "codec_name": "subrip"},
	}}
}

func TestListShelfAudioTracks(t *testing.T) {
	tracks := listShelfAudioTracks(maProbe())
	if len(tracks) != 3 {
		t.Fatalf("expected 3 shelvable tracks (truehd excluded), got %d: %+v", len(tracks), tracks)
	}
	if tracks[0].AbsIndex != 1 || tracks[0].RelIndex != 0 || tracks[0].displayName() != "Дубляж" {
		t.Errorf("track0 mismatch: %+v name=%q", tracks[0], tracks[0].displayName())
	}
	// truehd is rel index 2 — dts after it must keep rel=3 (ctx.Audio.Index semantics).
	if tracks[2].AbsIndex != 4 || tracks[2].RelIndex != 3 || tracks[2].Codec != "dts" {
		t.Errorf("track2 mismatch: %+v", tracks[2])
	}
	if tracks[1].displayName() != "eng" {
		t.Errorf("lang fallback name: got %q", tracks[1].displayName())
	}
}

func TestAppendAudioShelfOutputs(t *testing.T) {
	ctx := transcodingContext{
		ShelfTracks: []shelfAudioTrack{{AbsIndex: 1}, {AbsIndex: 4}},
	}
	ctx.HLS.SegDur = 6
	args := appendAudioShelfOutputs(nil, ctx, 7)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-map 0:1", "-map 0:4",
		"-segment_time 6", "-segment_start_number 7",
		"-segment_format mpegts",
		"aud_shelf_1/s_%05d.ts", "aud_shelf_4/s_%05d.ts",
		"-c copy", "-copyts",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q in: %s", want, joined)
		}
	}
}

func TestAppendAudioShelfOutputsSkipsActive(t *testing.T) {
	// The active track's audio is already muxed into the video variant, so it
	// must NOT get its own shelf output (that copy would be wasted).
	ctx := transcodingContext{
		ShelfTracks:    []shelfAudioTrack{{AbsIndex: 1}, {AbsIndex: 4}},
		ActiveAudioAbs: 1,
	}
	ctx.HLS.SegDur = 6
	joined := strings.Join(appendAudioShelfOutputs(nil, ctx, 0), " ")
	if strings.Contains(joined, "aud_shelf_1/") {
		t.Errorf("active track (abs 1) shelf must be skipped:\n%s", joined)
	}
	if !strings.Contains(joined, "aud_shelf_4/") {
		t.Errorf("non-active track (abs 4) shelf missing:\n%s", joined)
	}
}

func TestMasterPlaylistAudioGroupNonShelvableActive(t *testing.T) {
	// Scenario B: the active/default track is lossless (TrueHD) — not shelvable,
	// so it's absent from ShelfTracks and gets re-encoded to AAC in the variant.
	// The group must still carry exactly one URI-less DEFAULT member, synthesized
	// from the probe, else players have no default audio.
	probe := map[string]any{
		"streams": []any{
			map[string]any{"index": float64(0), "codec_type": "video", "codec_name": "hevc"},
			map[string]any{"index": float64(1), "codec_type": "audio", "codec_name": "truehd",
				"tags": map[string]any{"language": "eng", "title": "TrueHD Atmos"}},
			map[string]any{"index": float64(2), "codec_type": "audio", "codec_name": "aac",
				"tags": map[string]any{"language": "rus"}},
			map[string]any{"index": float64(3), "codec_type": "audio", "codec_name": "ac3",
				"tags": map[string]any{"language": "eng"}},
		},
	}
	job := &TranscodingJob{Context: transcodingContext{
		Mode:    transcode.ModeRemux,
		FFProbe: probe,
		ShelfTracks: []shelfAudioTrack{
			{AbsIndex: 2, RelIndex: 1, Codec: "aac", Lang: "rus"},
			{AbsIndex: 3, RelIndex: 2, Codec: "ac3", Lang: "eng"},
		},
		ActiveAudioAbs: -1,
	}}
	job.Context.Audio.Index = 0 // active is the truehd track (rel 0)
	job.Context.HLS.SegDur = 6
	pl := buildMasterPlaylist(job, "http://x/transcoding/abc")
	// Synthesized muxed member from the probe's active track.
	if !strings.Contains(pl, `NAME="TrueHD Atmos",LANGUAGE="eng",DEFAULT=YES,AUTOSELECT=YES`+"\n") {
		t.Errorf("synthesized muxed member wrong:\n%s", pl)
	}
	// Both shelvable tracks are lazy renditions.
	if !strings.Contains(pl, `URI="aud_2/index.m3u8"`) || !strings.Contains(pl, `URI="aud_3/index.m3u8"`) {
		t.Errorf("lazy rendition URIs missing:\n%s", pl)
	}
	// Exactly one DEFAULT=YES audio member (no ambiguity).
	if n := strings.Count(pl, "TYPE=AUDIO") - strings.Count(pl, "DEFAULT=NO"); n != 1 {
		t.Errorf("expected exactly one DEFAULT=YES audio member, got %d:\n%s", n, pl)
	}
}

func TestBuildAudioRenditionPlaylist(t *testing.T) {
	pl := buildAudioRenditionPlaylist(60, 6)
	if !strings.Contains(pl, "#EXT-X-MAP:URI=\"init.mp4\"") {
		t.Error("missing EXT-X-MAP")
	}
	if !strings.Contains(pl, "seg_00009.m4s") || strings.Contains(pl, "seg_00010.m4s") {
		t.Errorf("expected exactly 10 segments:\n%s", pl)
	}
	if !strings.Contains(pl, "#EXT-X-ENDLIST") {
		t.Error("missing ENDLIST")
	}
}

func TestMasterPlaylistAudioGroup(t *testing.T) {
	job := &TranscodingJob{Context: transcodingContext{
		Mode: transcode.ModeRemux,
		ShelfTracks: []shelfAudioTrack{
			{AbsIndex: 1, RelIndex: 0, Codec: "eac3", Lang: "rus", Title: "Дубляж"},
			{AbsIndex: 2, RelIndex: 1, Codec: "aac", Lang: "eng"},
		},
		ActiveAudioAbs: 1,
	}}
	job.Context.HLS.SegDur = 6
	job.Context.HLS.FMP4 = true
	pl := buildMasterPlaylist(job, "http://x/transcoding/abc")
	// Active track: DEFAULT=YES, NO URI (audio muxed in the variant).
	if !strings.Contains(pl, `TYPE=AUDIO,GROUP-ID="aud",NAME="Дубляж",LANGUAGE="rus",DEFAULT=YES,AUTOSELECT=YES`+"\n") {
		t.Errorf("active muxed rendition wrong:\n%s", pl)
	}
	if strings.Contains(pl, `NAME="Дубляж"`+`,LANGUAGE="rus",DEFAULT=YES,AUTOSELECT=YES,URI=`) {
		t.Error("active rendition must not carry a URI")
	}
	// Other track: URI to lazy rendition.
	if !strings.Contains(pl, `URI="aud_2/index.m3u8"`) {
		t.Errorf("lazy rendition URI missing:\n%s", pl)
	}
	// Variant references the group.
	if !strings.Contains(pl, `,AUDIO="aud"`) {
		t.Errorf("STREAM-INF missing AUDIO group:\n%s", pl)
	}
}

func TestMasterPlaylistNoAudioGroupSingleTrack(t *testing.T) {
	job := &TranscodingJob{Context: transcodingContext{Mode: transcode.ModeRemux}}
	job.Context.HLS.SegDur = 6
	pl := buildMasterPlaylist(job, "http://x/transcoding/abc")
	if strings.Contains(pl, "TYPE=AUDIO") {
		t.Error("no shelf tracks → no audio group expected")
	}
}

func TestNextSegName(t *testing.T) {
	if got := nextSegName("seg_00007.m4s"); got != "seg_00008.m4s" {
		t.Errorf("nextSegName: %q", got)
	}
	if got := nextSegName("init.mp4"); got != "" {
		t.Errorf("init should yield empty, got %q", got)
	}
}
