package transcodesvc

import (
	"lampac-go/internal/transcode"
	"testing"
)

func TestVideoCodecRFC6381_ProfileLevel(t *testing.T) {
	cases := []struct {
		name    string
		codec   string
		profile string
		level   int
		want    string
	}{
		{"h264 high 4.1", "h264", "High", 41, "avc1.640029"},
		{"h264 high 4.0", "h264", "High", 40, "avc1.640028"},
		{"h264 main 3.1", "h264", "Main", 31, "avc1.4d401f"},
		{"h264 baseline 3.0", "h264", "Baseline", 30, "avc1.42e01e"},
		{"h264 unknown profile", "h264", "", 31, "avc1.42401f"},
		{"hevc main 4.0", "hevc", "Main", 120, "hvc1.1.4.L120.B0"},
		{"hevc main10 5.1", "hevc", "Main 10", 153, "hvc1.2.4.L153.B0"},
		{"av1 main", "av1", "Main", 8, "av01.0.08M.08"},
		{"unknown codec", "theora", "", 0, ""},
	}
	for _, c := range cases {
		if got := videoCodecRFC6381(c.codec, c.profile, c.level); got != c.want {
			t.Errorf("%s: videoCodecRFC6381(%q,%q,%d) = %q, want %q", c.name, c.codec, c.profile, c.level, got, c.want)
		}
	}
}

func TestAudioCodecRFC6381(t *testing.T) {
	cases := []struct {
		codec, profile, want string
	}{
		{"aac", "LC", "mp4a.40.2"},
		{"aac", "HE-AAC", "mp4a.40.5"},
		{"ac3", "", "ac-3"},
		{"eac3", "", "ec-3"},
		{"mp3", "", "mp4a.40.34"},
		{"opus", "", "Opus"},   // capital O — lower-case was rejected by strict players
		{"flac", "", "fLaC"},   // mixed case matters
		{"alac", "", "alac"},   // previously unmapped → CODECS dropped the audio track
		{"truehd", "", "mlpa"}, // previously unmapped
		{"dts", "DTS", "dtsc"},
		{"dts", "DTS-HD MA", "dtsh"},
		{"dts", "DTS Express", "dtse"},
	}
	for _, c := range cases {
		if got := audioCodecRFC6381(c.codec, c.profile); got != c.want {
			t.Errorf("audioCodecRFC6381(%q,%q) = %q, want %q", c.codec, c.profile, got, c.want)
		}
	}
}

func TestProbeIntField(t *testing.T) {
	m := map[string]any{"f": float64(41), "s": "153", "i": 120}
	if probeIntField(m, "f") != 41 || probeIntField(m, "s") != 153 || probeIntField(m, "i") != 120 {
		t.Errorf("probeIntField mismatch: %d %d %d", probeIntField(m, "f"), probeIntField(m, "s"), probeIntField(m, "i"))
	}
	if probeIntField(m, "missing") != 0 {
		t.Errorf("missing field should be 0")
	}
}

func TestVideoRangeFromProbe(t *testing.T) {
	mk := func(transfer string) map[string]any {
		return map[string]any{"streams": []any{map[string]any{"codec_type": "video", "color_transfer": transfer}}}
	}
	if got := videoRangeFromProbe(mk("smpte2084"), transcode.ModeRemux); got != "PQ" {
		t.Errorf("HDR10/PQ copy: got %q want PQ", got)
	}
	if got := videoRangeFromProbe(mk("arib-std-b67"), transcode.ModeNative); got != "HLG" {
		t.Errorf("HLG copy: got %q want HLG", got)
	}
	if got := videoRangeFromProbe(mk("bt709"), transcode.ModeRemux); got != "SDR" {
		t.Errorf("SDR copy: got %q want SDR", got)
	}
	// Re-encode always SDR even from an HDR source (no HDR-metadata passthrough).
	if got := videoRangeFromProbe(mk("smpte2084"), transcode.ModeSWTranscode); got != "SDR" {
		t.Errorf("transcode HDR src: got %q want SDR", got)
	}
}

func TestFrameRateFromProbe(t *testing.T) {
	mk := func(avg, r string) map[string]any {
		return map[string]any{"streams": []any{map[string]any{"codec_type": "video", "avg_frame_rate": avg, "r_frame_rate": r}}}
	}
	if got := frameRateFromProbe(mk("24000/1001", "24000/1001")); got != "23.976" {
		t.Errorf("23.976: got %q", got)
	}
	if got := frameRateFromProbe(mk("25/1", "25/1")); got != "25.000" {
		t.Errorf("25: got %q", got)
	}
	// avg_frame_rate 0/0 → fall back to r_frame_rate.
	if got := frameRateFromProbe(mk("0/0", "30/1")); got != "30.000" {
		t.Errorf("fallback to r_frame_rate: got %q", got)
	}
	if got := frameRateFromProbe(nil); got != "" {
		t.Errorf("nil probe should be empty, got %q", got)
	}
}

func TestHlsSupplementalCodecsDoVi(t *testing.T) {
	mk := func(codec string, profile, level, compat int) map[string]any {
		return map[string]any{"streams": []any{map[string]any{
			"codec_type": "video", "codec_name": codec,
			"side_data_list": []any{map[string]any{
				"side_data_type":                "DOVI configuration record",
				"dv_profile":                    float64(profile),
				"dv_level":                      float64(level),
				"dv_bl_signal_compatibility_id": float64(compat),
			}},
		}}}
	}
	// Profile 8.1 (HDR10-compatible) HEVC, stream-copy → dvh1.08.06/db1p.
	if got := hlsSupplementalCodecs(mk("hevc", 8, 6, 1), transcode.ModeRemux); got != "dvh1.08.06/db1p" {
		t.Errorf("DoVi P8.1 HEVC: got %q want dvh1.08.06/db1p", got)
	}
	// Profile 8.4 (HLG-compatible) → db4h.
	if got := hlsSupplementalCodecs(mk("hevc", 8, 6, 4), transcode.ModeNative); got != "dvh1.08.06/db4h" {
		t.Errorf("DoVi P8.4: got %q want dvh1.08.06/db4h", got)
	}
	// AV1 DoVi → dav1 fourCC.
	if got := hlsSupplementalCodecs(mk("av1", 10, 9, 1), transcode.ModeRemux); got != "dav1.10.09/db1p" {
		t.Errorf("DoVi AV1: got %q want dav1.10.09/db1p", got)
	}
	// Compat 2 (SDR) → unlabelled.
	if got := hlsSupplementalCodecs(mk("hevc", 8, 6, 2), transcode.ModeRemux); got != "" {
		t.Errorf("DoVi SDR-compat should be unlabelled, got %q", got)
	}
	// Re-encode strips DoVi → unlabelled.
	if got := hlsSupplementalCodecs(mk("hevc", 8, 6, 1), transcode.ModeSWTranscode); got != "" {
		t.Errorf("transcode should not label DoVi, got %q", got)
	}
	// Non-DoVi source → empty.
	if got := hlsSupplementalCodecs(map[string]any{"streams": []any{map[string]any{"codec_type": "video", "codec_name": "h264"}}}, transcode.ModeRemux); got != "" {
		t.Errorf("non-DoVi should be empty, got %q", got)
	}
}
