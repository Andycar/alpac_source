package subvtt

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/asticode/go-astisub"
)

// Subtitle handling backed by go-astisub (github.com/asticode/go-astisub) —
// robust multi-format parsing (SRT, SSA/ASS, WebVTT, TTML) + WebVTT output +
// HLS-style fragmentation. Replaces the hand-rolled SRT regex converter, which
// only understood well-formed SubRip and mangled anything else (styled ASS,
// odd timecodes, positioning).

// parseSubtitles reads subtitle bytes in whatever format they arrive in and
// returns go-astisub's model. Format is sniffed from the content, defaulting to
// SRT (the overwhelmingly common OpenSubtitles case).
// utf8BOM is the UTF-8 byte-order mark that OpenSubtitles files often carry.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func ParseSubtitles(data []byte) (*astisub.Subtitles, bool) {
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(data, utf8BOM), " \t\r\n")
	if len(trimmed) == 0 {
		return nil, false
	}
	lower := bytes.ToLower(trimmed)

	var (
		subs *astisub.Subtitles
		err  error
	)
	switch {
	case bytes.HasPrefix(lower, []byte("webvtt")):
		subs, err = astisub.ReadFromWebVTT(bytes.NewReader(data))
	case bytes.HasPrefix(lower, []byte("[script info]")),
		bytes.Contains(lower, []byte("[v4+ styles]")),
		bytes.Contains(lower, []byte("[v4 styles]")),
		bytes.Contains(lower, []byte("\ndialogue:")):
		subs, err = astisub.ReadFromSSA(bytes.NewReader(data))
	case bytes.HasPrefix(lower, []byte("<?xml")),
		bytes.Contains(lower, []byte("<tt ")),
		bytes.Contains(lower, []byte("<tt>")):
		subs, err = astisub.ReadFromTTML(bytes.NewReader(data))
	default:
		subs, err = astisub.ReadFromSRT(bytes.NewReader(data))
	}
	if err != nil || subs == nil || subs.IsEmpty() {
		return nil, false
	}
	return subs, true
}

// convertSubtitlesToVTT converts any supported subtitle format to WebVTT.
// Falls back to the legacy SRT regex converter if go-astisub can't parse the
// input, so we never regress on inputs the old path happened to handle.
func ConvertSubtitlesToVTT(data []byte) string {
	if subs, ok := ParseSubtitles(data); ok {
		if out, ok := writeVTT(subs); ok {
			return out
		}
	}
	return srtToVTTLegacy(string(data))
}

// writeVTT renders subtitles as a WebVTT document. Returns false if the result
// is empty/just the header (nothing usable was produced).
// RenderVTT serialises an already-parsed subtitle set to WebVTT. Callers that
// modify the model first — shifting every cue to match the stream's audio, say —
// need this; ConvertSubtitlesToVTT starts from bytes and would undo the change.
// Empty string means the set held nothing worth serving.
func RenderVTT(subs *astisub.Subtitles) string {
	out, ok := writeVTT(subs)
	if !ok {
		return ""
	}
	return out
}

func writeVTT(subs *astisub.Subtitles) (string, bool) {
	var buf bytes.Buffer
	if err := subs.WriteToWebVTT(&buf); err != nil {
		return "", false
	}
	out := buf.String()
	// A bare "WEBVTT\n\n" with no cues is not useful.
	if len(bytes.TrimSpace(bytes.TrimPrefix(buf.Bytes(), []byte("WEBVTT")))) == 0 {
		return "", false
	}
	return out, true
}

// buildFragmentedVTTSegments splits a subtitle track into per-HLS-segment WebVTT
// documents of segDur each, so a subtitle rendition can be delivered as real
// segments (aligned to the video timeline) instead of one giant blob. Cues that
// straddle a boundary are split by go-astisub's Fragment so each segment is
// self-contained. Returns one VTT string per segment index [0, count).
func BuildFragmentedVTTSegments(data []byte, segDur time.Duration, count int) ([]string, bool) {
	if segDur <= 0 || count <= 0 {
		return nil, false
	}
	subs, ok := ParseSubtitles(data)
	if !ok {
		return nil, false
	}
	subs.Fragment(segDur)

	segments := make([]string, count)
	for i := 0; i < count; i++ {
		start := time.Duration(i) * segDur
		end := start + segDur
		seg := subs.Items // filter into [start, end)
		var b bytes.Buffer
		b.WriteString("WEBVTT\n\n")
		wrote := false
		for _, item := range seg {
			if item == nil || item.EndAt <= start || item.StartAt >= end {
				continue
			}
			b.WriteString(fmt.Sprintf("%s --> %s\n", vttTimestamp(item.StartAt), vttTimestamp(item.EndAt)))
			for _, line := range item.Lines {
				b.WriteString(line.String() + "\n")
			}
			b.WriteString("\n")
			wrote = true
		}
		_ = wrote // empty segments are valid (a stretch with no dialogue)
		segments[i] = b.String()
	}
	return segments, true
}

// vttTimestamp formats a duration as a WebVTT timestamp (HH:MM:SS.mmm).
func vttTimestamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	ms := d / time.Millisecond
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}

// srtToVTTLegacy is the original hand-rolled SRT→WebVTT converter, kept as a
// fallback for inputs go-astisub can't parse (see convertSubtitlesToVTT).
// Replaces comma with dot in timecodes, adds WEBVTT header, removes sequence numbers.
func srtToVTTLegacy(srt string) string {
	// Already VTT
	if strings.HasPrefix(strings.TrimSpace(srt), "WEBVTT") {
		return srt
	}

	var b strings.Builder
	b.WriteString("WEBVTT\n\n")

	// Regex: match SRT timecodes like "00:01:23,456 --> 00:01:25,789"
	tcRe := regexp.MustCompile(`(\d{2}:\d{2}:\d{2}),(\d{3})\s*-->\s*(\d{2}:\d{2}:\d{2}),(\d{3})`)

	// Split by double newline (SRT blocks)
	blocks := regexp.MustCompile(`\r?\n\r?\n`).Split(strings.TrimSpace(srt), -1)

	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}

		lines := strings.Split(block, "\n")
		if len(lines) < 2 {
			continue
		}

		// Find the timecode line
		tcIdx := -1
		for i, line := range lines {
			if tcRe.MatchString(strings.TrimSpace(line)) {
				tcIdx = i
				break
			}
		}
		if tcIdx < 0 {
			continue
		}

		// Replace comma with dot in timecodes
		tc := tcRe.ReplaceAllString(strings.TrimSpace(lines[tcIdx]), "$1.$2 --> $3.$4")
		b.WriteString(tc + "\n")

		// Write text lines (everything after the timecode)
		for i := tcIdx + 1; i < len(lines); i++ {
			line := strings.TrimRight(lines[i], "\r")
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}

	return b.String()
}
