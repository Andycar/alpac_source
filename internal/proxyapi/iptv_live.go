package proxyapi

import (
	"strconv"
	"strings"
)

// iptvLiveKeepSegments bounds a live IPTV media playlist to its newest segments before the
// rewrite. Providers commonly expose hours of DVR (thousands of segments); after every segment
// URL is rewritten to a long /proxy/<aes> form such playlists balloon to megabytes, which the
// TV-native HLS parsers (webOS/Tizen old Chromium pipelines) chew on for up to a minute before
// the first frame — while Shaka on desktop/phone parses the same thing instantly. ~20 segments
// ≈ 2 minutes at a typical 6s target: enough buffer, near-instant start at the live edge.
const iptvLiveKeepSegments = 20

// segment-level tags that open/belong to a segment block (they apply to the NEXT URI line).
var iptvSegTagPrefixes = []string{
	"#EXTINF",
	"#EXT-X-KEY",
	"#EXT-X-MAP",
	"#EXT-X-PROGRAM-DATE-TIME",
	"#EXT-X-BYTERANGE",
	"#EXT-X-DATERANGE",
}

func isIPTVSegTag(line string) bool {
	// exact match: "#EXT-X-DISCONTINUITY-SEQUENCE:" is a HEADER tag sharing this prefix
	if line == "#EXT-X-DISCONTINUITY" {
		return true
	}
	for _, p := range iptvSegTagPrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// trimLiveDVR returns src reduced to its last `keep` media segments. Master playlists, VOD/event
// playlists (EXT-X-ENDLIST / EXT-X-PLAYLIST-TYPE — incl. flussonic catchup archives) and already
// short playlists pass through untouched. EXT-X-MEDIA-SEQUENCE / EXT-X-DISCONTINUITY-SEQUENCE are
// advanced by what was dropped (inserted if absent, so refreshes stay continuous), and the last
// EXT-X-KEY / EXT-X-MAP seen in the dropped span are re-emitted so the kept segments still
// decrypt / initialize correctly.
func trimLiveDVR(src string, keep int) string {
	if keep <= 0 ||
		strings.Contains(src, "#EXT-X-ENDLIST") ||
		strings.Contains(src, "#EXT-X-PLAYLIST-TYPE") ||
		!strings.Contains(src, "#EXTINF") {
		return src
	}

	lines := strings.Split(src, "\n") // keeps a trailing \r on CRLF input; prefix checks still match

	// Locate segment blocks: a run of segment-level tags followed by its URI line.
	type seg struct{ start, uri int } // line span [start..uri], uri = the segment URI line
	segs := make([]seg, 0, 256)
	blockStart := -1
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "#") {
			if blockStart == -1 && isIPTVSegTag(t) {
				blockStart = i
			}
			continue
		}
		s := blockStart
		if s == -1 {
			s = i
		}
		segs = append(segs, seg{s, i})
		blockStart = -1
	}
	if len(segs) <= keep {
		return src
	}

	drop := len(segs) - keep
	headerEnd := segs[0].start // header = everything before the first segment block
	cut := segs[drop].start    // first kept line

	// Scan the dropped span: count discontinuities, remember the last KEY/MAP in effect.
	droppedDisc := 0
	lastKey, lastMap := "", ""
	for i := headerEnd; i < cut; i++ {
		t := strings.TrimSpace(lines[i])
		switch {
		case t == "#EXT-X-DISCONTINUITY":
			droppedDisc++
		case strings.HasPrefix(t, "#EXT-X-KEY:"):
			lastKey = t
		case strings.HasPrefix(t, "#EXT-X-MAP:"):
			lastMap = t
		}
	}

	var b strings.Builder
	b.Grow(len(src)/4 + 256)

	// Header, with sequence counters advanced (or inserted when absent).
	wroteMediaSeq, wroteDiscSeq := false, false
	for i := 0; i < headerEnd; i++ {
		t := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(t, "#EXT-X-MEDIA-SEQUENCE:"):
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(t, "#EXT-X-MEDIA-SEQUENCE:")))
			b.WriteString("#EXT-X-MEDIA-SEQUENCE:")
			b.WriteString(strconv.Itoa(n + drop))
			b.WriteByte('\n')
			wroteMediaSeq = true
		case strings.HasPrefix(t, "#EXT-X-DISCONTINUITY-SEQUENCE:"):
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(t, "#EXT-X-DISCONTINUITY-SEQUENCE:")))
			b.WriteString("#EXT-X-DISCONTINUITY-SEQUENCE:")
			b.WriteString(strconv.Itoa(n + droppedDisc))
			b.WriteByte('\n')
			wroteDiscSeq = true
		default:
			b.WriteString(lines[i])
			b.WriteByte('\n')
		}
	}
	if !wroteMediaSeq {
		// implicit sequence was 0; the kept window starts at `drop`
		b.WriteString("#EXT-X-MEDIA-SEQUENCE:")
		b.WriteString(strconv.Itoa(drop))
		b.WriteByte('\n')
	}
	if !wroteDiscSeq && droppedDisc > 0 {
		b.WriteString("#EXT-X-DISCONTINUITY-SEQUENCE:")
		b.WriteString(strconv.Itoa(droppedDisc))
		b.WriteByte('\n')
	}
	// Re-emit the encryption key / init segment in effect at the cut point.
	if lastKey != "" {
		b.WriteString(lastKey)
		b.WriteByte('\n')
	}
	if lastMap != "" {
		b.WriteString(lastMap)
		b.WriteByte('\n')
	}

	for i := cut; i < len(lines); i++ {
		if i == len(lines)-1 && lines[i] == "" {
			break // don't add a trailing newline the source didn't have
		}
		b.WriteString(lines[i])
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
