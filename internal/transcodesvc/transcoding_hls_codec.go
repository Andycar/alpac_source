package transcodesvc

import (
	"fmt"
	"lampac-go/internal/transcode"
	"strconv"
	"strings"
)

// RFC 6381 §3.3 HLS codec descriptors, ported from Jellyfin's
// HlsCodecStringHelpers (Jellyfin.Api/Helpers/HlsCodecStringHelpers.cs).
//
// The CODECS attribute of #EXT-X-STREAM-INF must accurately describe the actual
// stream: strict HLS players (Safari native HLS, many Smart-TVs, hls.js in MSE
// mode) reject a variant whose declared codec string doesn't match the real
// SPS/profile — a hard "cannot play" that happens BEFORE the first segment is
// fetched. Our previous builder hard-coded profile/level (e.g. always
// "avc1.4d401f" for any H.264, mislabelled "High" while 4d40 is Main), which
// could under-declare a High@4.1 source and get refused. These helpers derive
// the exact string from the ffprobe profile + level instead.

// videoCodecRFC6381 maps an ffprobe video codec_name + profile + level to its
// RFC 6381 descriptor. Returns "" for codecs we don't describe.
func videoCodecRFC6381(name, profile string, level int) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "h264", "avc", "avc1":
		return hlsAVCString(profile, level)
	case "hevc", "h265", "hev1", "hvc1":
		return hlsHEVCString(profile, level)
	case "av1", "av01":
		return hlsAV1String(profile, level)
	case "vp9", "vp09":
		// VP9's full descriptor needs width/height/framerate/bitdepth; a bare
		// "vp09" is accepted by browsers that support VP9 at all, so keep it
		// conservative rather than fabricate a wrong level.
		return "vp09.00.10.08"
	}
	return ""
}

// hlsAVCString builds an avc1 descriptor: avc1.<profile-hex><level-hex>.
// level is the ffprobe H.264 level_idc (e.g. 41 for L4.1 → hex "29").
func hlsAVCString(profile string, level int) string {
	var pfx string
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "high":
		pfx = "6400"
	case "main":
		pfx = "4d40"
	case "baseline", "constrained baseline":
		pfx = "42e0"
	default:
		pfx = "4240" // constrained baseline — safe minimum
	}
	if level <= 0 {
		level = 31 // L3.1 fallback
	}
	return fmt.Sprintf("avc1.%s%02x", pfx, level)
}

// hlsHEVCString builds an hvc1 descriptor: hvc1.<profile>.4.L<level>.B0.
// level is the ffprobe HEVC general_level_idc (e.g. 120 for L4.0, 153 for L5.1).
func hlsHEVCString(profile string, level int) string {
	p := "1.4" // Main
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "main 10", "main10", "rext":
		p = "2.4" // Main 10
	}
	if level <= 0 {
		level = 120 // L4.0 fallback
	}
	return fmt.Sprintf("hvc1.%s.L%d.B0", p, level)
}

// hlsAV1String builds an av01 descriptor: av01.<profile>.<level><tier>.<bitdepth>.
func hlsAV1String(profile string, level int) string {
	prof := "0" // Main
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "high":
		prof = "1"
	case "professional":
		prof = "2"
	}
	if level <= 0 || level > 31 {
		level = 19 // max defined (6.3)
	}
	return fmt.Sprintf("av01.%s.%02dM.08", prof, level)
}

// audioCodecRFC6381 maps an ffprobe audio codec_name (+ profile for AAC/DTS) to
// its RFC 6381 descriptor. Returns "" for codecs we don't describe.
func audioCodecRFC6381(name, profile string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "aac", "mp4a":
		if strings.Contains(strings.ToUpper(profile), "HE-AAC") {
			return "mp4a.40.5" // HE-AAC (SBR)
		}
		return "mp4a.40.2" // AAC-LC
	case "ac3":
		return "ac-3"
	case "eac3":
		return "ec-3"
	case "mp3":
		return "mp4a.40.34"
	case "opus":
		return "Opus" // capitalised per the ISO-BMFF sample entry 4CC; lower-case is rejected by some players
	case "flac":
		return "fLaC"
	case "alac":
		return "alac"
	case "truehd":
		return "mlpa"
	case "dts", "dca":
		pu := strings.ToUpper(profile)
		switch {
		case strings.Contains(pu, "HD MA"), strings.Contains(pu, "HD HRA"), strings.Contains(pu, "DTS:X"):
			return "dtsh"
		case strings.Contains(pu, "EXPRESS"):
			return "dtse"
		default:
			return "dtsc"
		}
	}
	return ""
}

// videoRangeFromProbe returns the HLS #EXT-X-STREAM-INF VIDEO-RANGE attribute
// (SDR / PQ / HLG), ported from Jellyfin's AppendPlaylistVideoRangeField.
//
// Signalling the range matters for HDR: when we STREAM-COPY an HDR video
// (remux/native — the common iRemux/BDRip case) the PQ/HLG transfer survives
// into the fMP4 segments, but a TV won't switch into HDR mode unless the master
// declares VIDEO-RANGE=PQ/HLG → the "washed-out / dull HDR" symptom. For our
// re-encode modes we output SDR (the x264/x265 pipeline carries no HDR
// metadata), so we always advertise SDR there, matching Jellyfin.
func videoRangeFromProbe(probe map[string]any, mode transcode.TranscodingMode) string {
	switch mode {
	case transcode.ModeSWTranscode, transcode.ModeHWTranscode:
		return "SDR"
	}
	transfer := ""
	if probe != nil {
		if streams, ok := probe["streams"].([]any); ok {
			for _, s := range streams {
				sm, _ := s.(map[string]any)
				if sm == nil || fmt.Sprint(sm["codec_type"]) != "video" {
					continue
				}
				transfer = strings.ToLower(strings.TrimSpace(fmt.Sprint(sm["color_transfer"])))
				break
			}
		}
	}
	switch transfer {
	case "smpte2084": // PQ — HDR10 / HDR10+ / Dolby Vision base layer
		return "PQ"
	case "arib-std-b67": // HLG
		return "HLG"
	}
	return "SDR"
}

// frameRateFromProbe returns the FRAME-RATE attribute value (e.g. "23.976")
// from the first video stream's avg_frame_rate/r_frame_rate ("24000/1001").
// Empty when unknown — the field is then omitted rather than lied about.
func frameRateFromProbe(probe map[string]any) string {
	if probe == nil {
		return ""
	}
	streams, ok := probe["streams"].([]any)
	if !ok {
		return ""
	}
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil || fmt.Sprint(sm["codec_type"]) != "video" {
			continue
		}
		for _, key := range []string{"avg_frame_rate", "r_frame_rate"} {
			if fps := parseFrameRateFraction(fmt.Sprint(sm[key])); fps > 0 {
				return strconv.FormatFloat(fps, 'f', 3, 64)
			}
		}
		break
	}
	return ""
}

// parseFrameRateFraction parses an ffprobe rate ("24000/1001", "25/1", "30").
func parseFrameRateFraction(fr string) float64 {
	fr = strings.TrimSpace(fr)
	if fr == "" || fr == "0/0" {
		return 0
	}
	if i := strings.IndexByte(fr, '/'); i > 0 {
		num, err1 := strconv.ParseFloat(fr[:i], 64)
		den, err2 := strconv.ParseFloat(fr[i+1:], 64)
		if err1 == nil && err2 == nil && den != 0 {
			return num / den
		}
		return 0
	}
	f, _ := strconv.ParseFloat(fr, 64)
	return f
}

// hlsSupplementalCodecs returns the #EXT-X-STREAM-INF SUPPLEMENTAL-CODECS value
// for a Dolby Vision source that is stream-copied (Apple HLS spec §1.10 — lets a
// DoVi-capable player detect it from the manifest, and a non-DoVi player fall
// back to the base CODECS). Ported from Jellyfin's
// AppendPlaylistSupplementalCodecsField. Only cross-compatible profiles are
// labelled: BL-compatible-HDR10 → db1p, BL-compatible-HLG → db4h. Profile-5 /
// SDR / EL DoVi is left unlabelled (spec players choke on it). Returns "" when
// not DoVi, an incompatible profile, or a re-encode mode (we tone-map / strip
// dynamic metadata there, so the copy-only guarantee no longer holds).
func hlsSupplementalCodecs(probe map[string]any, mode transcode.TranscodingMode) string {
	switch mode {
	case transcode.ModeSWTranscode, transcode.ModeHWTranscode:
		return ""
	}
	prof, level, compat, codec, ok := transcode.DoviFromProbe(probe)
	if !ok {
		return ""
	}
	var brand string
	switch compat {
	case 1: // base layer compatible with HDR10
		brand = "db1p"
	case 4: // base layer compatible with HLG
		brand = "db4h"
	default:
		return "" // 0/None, 2/SDR, invalid — don't label
	}
	fourCc := "dvh1"
	if strings.EqualFold(codec, "av1") {
		fourCc = "dav1"
	}
	return fmt.Sprintf("%s.%02d.%02d/%s", fourCc, prof, level, brand)
}

// probeIntField extracts an integer from an ffprobe field that may be a JSON
// number (float64), a string, or already an int.
func probeIntField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return 0
}
