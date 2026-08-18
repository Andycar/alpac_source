package subvtt

import (
	"strings"
	"testing"
	"time"
)

func TestConvertSRTToVTT(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:03,000\nHello world\n\n2\n00:00:04,500 --> 00:00:06,000\nSecond line\n"
	vtt := ConvertSubtitlesToVTT([]byte(srt))
	if !strings.HasPrefix(strings.TrimSpace(vtt), "WEBVTT") {
		t.Fatalf("missing WEBVTT header:\n%s", vtt)
	}
	if !strings.Contains(vtt, "00:00:01.000 --> 00:00:03.000") {
		t.Errorf("comma timecode not converted to dot:\n%s", vtt)
	}
	if !strings.Contains(vtt, "Hello world") || !strings.Contains(vtt, "Second line") {
		t.Errorf("cue text missing:\n%s", vtt)
	}
}

func TestConvertSSAToVTT(t *testing.T) {
	// The legacy converter mangled this (not SubRip); go-astisub handles it.
	ass := strings.Join([]string{
		"[Script Info]",
		"ScriptType: v4.00+",
		"",
		"[V4+ Styles]",
		"Format: Name, Fontname, Fontsize",
		"Style: Default,Arial,20",
		"",
		"[Events]",
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text",
		"Dialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,Hello from ASS",
		"",
	}, "\n")
	vtt := ConvertSubtitlesToVTT([]byte(ass))
	if !strings.Contains(vtt, "WEBVTT") {
		t.Fatalf("no WEBVTT:\n%s", vtt)
	}
	if !strings.Contains(vtt, "Hello from ASS") {
		t.Errorf("ASS dialogue not converted:\n%s", vtt)
	}
	if !strings.Contains(vtt, "00:00:01.000 --> 00:00:03.000") {
		t.Errorf("ASS timing not converted:\n%s", vtt)
	}
}

func TestConvertWebVTTPassthrough(t *testing.T) {
	vttIn := "WEBVTT\n\n00:00:02.000 --> 00:00:04.000\nAlready VTT\n"
	vtt := ConvertSubtitlesToVTT([]byte(vttIn))
	if !strings.Contains(vtt, "Already VTT") || !strings.Contains(vtt, "00:00:02.000 --> 00:00:04.000") {
		t.Errorf("VTT passthrough wrong:\n%s", vtt)
	}
}

func TestConvertBOMPrefixedSRT(t *testing.T) {
	srt := string(utf8BOM) + "1\n00:00:01,000 --> 00:00:02,000\nBom cue\n"
	vtt := ConvertSubtitlesToVTT([]byte(srt))
	if !strings.Contains(vtt, "Bom cue") {
		t.Errorf("BOM-prefixed SRT failed:\n%s", vtt)
	}
}

func TestConvertGarbageFallsBack(t *testing.T) {
	// Not parseable as any known format → legacy fallback path (returns a
	// WEBVTT header, no cues) rather than panicking.
	vtt := ConvertSubtitlesToVTT([]byte("this is not a subtitle file at all"))
	if !strings.Contains(vtt, "WEBVTT") {
		t.Errorf("fallback should still emit WEBVTT header:\n%s", vtt)
	}
}

func TestFragmentedVTTSegments(t *testing.T) {
	// One cue spanning 00:05 → 00:15, segments of 10s. Fragment splits it at
	// the 10s boundary: segment 0 = [5s,10s], segment 1 = [10s,15s].
	srt := "1\n00:00:05,000 --> 00:00:15,000\nStraddling cue\n"
	segs, ok := BuildFragmentedVTTSegments([]byte(srt), 10*time.Second, 2)
	if !ok || len(segs) != 2 {
		t.Fatalf("expected 2 segments, got ok=%v n=%d", ok, len(segs))
	}
	if !strings.Contains(segs[0], "Straddling cue") || !strings.Contains(segs[0], "00:00:05.000 --> 00:00:10.000") {
		t.Errorf("segment 0 wrong:\n%s", segs[0])
	}
	if !strings.Contains(segs[1], "Straddling cue") || !strings.Contains(segs[1], "00:00:10.000 --> 00:00:15.000") {
		t.Errorf("segment 1 wrong:\n%s", segs[1])
	}
}

func TestVTTTimestamp(t *testing.T) {
	if got := vttTimestamp(3*time.Hour + 25*time.Minute + 45*time.Second + 678*time.Millisecond); got != "03:25:45.678" {
		t.Errorf("vttTimestamp: %q", got)
	}
	if got := vttTimestamp(0); got != "00:00:00.000" {
		t.Errorf("zero: %q", got)
	}
}
