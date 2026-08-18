// Package mediaprobe names the codecs actually present in the first bytes of a
// stream — from the bytes themselves, not from what the playlist claims.
//
// The claim and the reality disagree often enough to matter. Playlists reach us
// with no CODECS attribute at all, with a copied-and-pasted one, or with the
// video codec listed and the audio omitted; meanwhile the failure our users
// report is always the same shape — "картинка есть, звука нет" on a TV whose
// decoder set does not include what the source actually sent. Reading the first
// segment answers that before anyone watches it: EC-3 on a box with no
// Dolby licence, HEVC on hardware that only does AVC.
//
// Deliberately small: no dependency, no full demuxer. It parses just enough of
// MPEG-TS (PAT → PMT → stream types) and ISOBMFF (moov → trak → stsd, plus the
// config boxes needed for an RFC 6381 string) to name tracks. Anything it
// cannot recognise it reports as unknown rather than guessing.
package mediaprobe

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Kind is what a track carries.
type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
	KindText  Kind = "text"
)

// Track is one elementary stream.
type Track struct {
	Kind Kind   `json:"kind"`
	Name string `json:"name"`           // human name: h264, hevc, aac, ac3, eac3…
	RFC  string `json:"rfc,omitempty"`  // RFC 6381 codec string when derivable
	PID  int    `json:"pid,omitempty"`  // MPEG-TS only
	Lang string `json:"lang,omitempty"` // ISO-639 when the container states it
}

// Tracks is the probe result.
type Tracks struct {
	Container string  `json:"container"` // "ts" | "mp4" | ""
	Tracks    []Track `json:"tracks,omitempty"`
}

// CodecString renders the tracks the way a playlist would: "avc1.640028,mp4a.40.2".
// Falls back to the human name when no RFC string could be derived — a partial
// answer is still an answer for a human reading a diagnostic.
func (t Tracks) CodecString() string {
	if len(t.Tracks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(t.Tracks))
	for _, tr := range t.Tracks {
		if tr.RFC != "" {
			parts = append(parts, tr.RFC)
			continue
		}
		parts = append(parts, tr.Name)
	}
	return strings.Join(parts, ",")
}

// Has reports whether a codec (by human name) is present.
func (t Tracks) Has(name string) bool {
	for _, tr := range t.Tracks {
		if tr.Name == name {
			return true
		}
	}
	return false
}

// Detect picks the parser by container signature. data is expected to be the
// first tens of kilobytes of a segment or init segment.
func Detect(data []byte) Tracks {
	if len(data) < 8 {
		return Tracks{}
	}
	if isMP4(data) {
		return DetectMP4(data)
	}
	if isTS(data) {
		return DetectTS(data)
	}
	return Tracks{}
}

func isMP4(data []byte) bool {
	// A box header we recognise in the first few boxes: ftyp/styp for a segment,
	// moov for a plain mp4.
	for _, sig := range [][]byte{[]byte("ftyp"), []byte("styp"), []byte("moov"), []byte("moof")} {
		if len(data) >= 12 && string(data[4:8]) == string(sig) {
			return true
		}
	}
	return false
}

func isTS(data []byte) bool {
	// Sync byte every 188 bytes; check a few in a row so a stray 0x47 in an
	// unrelated body doesn't look like transport stream.
	if data[0] != 0x47 {
		return false
	}
	hits := 0
	for off := 0; off+188 < len(data) && hits < 3; off += 188 {
		if data[off] != 0x47 {
			return false
		}
		hits++
	}
	return hits >= 2
}

// ---------------------------------------------------------------------------
// MPEG-TS
// ---------------------------------------------------------------------------

// tsStreamTypes maps PMT stream_type to a human name. Values from ISO 13818-1
// plus the registered private ones broadcasters actually use.
var tsStreamTypes = map[byte]struct {
	kind Kind
	name string
}{
	0x01: {KindVideo, "mpeg1video"},
	0x02: {KindVideo, "mpeg2video"},
	0x03: {KindAudio, "mp2"},
	0x04: {KindAudio, "mp3"},
	0x0F: {KindAudio, "aac"},
	0x10: {KindVideo, "mpeg4video"},
	0x11: {KindAudio, "aac-latm"},
	0x1B: {KindVideo, "h264"},
	0x20: {KindVideo, "h264"}, // MVC sub-bitstream
	0x21: {KindVideo, "jpeg2000"},
	0x24: {KindVideo, "hevc"},
	0x33: {KindVideo, "vvc"},
	0x51: {KindVideo, "av1"},
	0x81: {KindAudio, "ac3"},
	0x82: {KindAudio, "dts"},
	0x84: {KindAudio, "eac3"},
	0x85: {KindAudio, "dts-hd"},
	0x86: {KindAudio, "dts-hd-ma"},
	0x87: {KindAudio, "eac3"},
	0x8A: {KindAudio, "dts"},
	0x06: {KindText, "private"}, // AC-3/E-AC-3/subtitles via descriptor; see below
}

// DetectTS parses PAT and PMT out of a transport stream prefix.
func DetectTS(data []byte) Tracks {
	out := Tracks{Container: "ts"}

	pmtPID := -1
	seen := map[int]bool{}
	for off := 0; off+188 <= len(data); off += 188 {
		pkt := data[off : off+188]
		if pkt[0] != 0x47 {
			continue
		}
		pid := int(pkt[1]&0x1F)<<8 | int(pkt[2])
		payloadStart := pkt[1]&0x40 != 0
		adaptation := (pkt[3] >> 4) & 0x03

		idx := 4
		if adaptation == 2 { // adaptation field only, no payload
			continue
		}
		if adaptation == 3 {
			if idx >= len(pkt) {
				continue
			}
			idx += 1 + int(pkt[idx])
		}
		if idx >= len(pkt) || !payloadStart {
			continue
		}
		// pointer_field: PSI sections start after it.
		idx += 1 + int(pkt[idx])
		if idx >= len(pkt) {
			continue
		}
		section := pkt[idx:]

		switch {
		case pid == 0 && pmtPID < 0:
			pmtPID = parsePAT(section)
		case pmtPID >= 0 && pid == pmtPID:
			for _, tr := range parsePMT(section) {
				if seen[tr.PID] {
					continue
				}
				seen[tr.PID] = true
				out.Tracks = append(out.Tracks, tr)
			}
			if len(out.Tracks) > 0 {
				return out
			}
		}
	}
	return out
}

func parsePAT(s []byte) int {
	if len(s) < 12 || s[0] != 0x00 {
		return -1
	}
	sectionLen := int(s[1]&0x0F)<<8 | int(s[2])
	end := 3 + sectionLen - 4 // minus CRC32
	if end > len(s) {
		end = len(s)
	}
	// Program entries start after the 8-byte section header.
	for i := 8; i+4 <= end; i += 4 {
		programNumber := int(s[i])<<8 | int(s[i+1])
		pid := int(s[i+2]&0x1F)<<8 | int(s[i+3])
		if programNumber != 0 { // 0 = network PID, not a program
			return pid
		}
	}
	return -1
}

func parsePMT(s []byte) []Track {
	if len(s) < 12 || s[0] != 0x02 {
		return nil
	}
	sectionLen := int(s[1]&0x0F)<<8 | int(s[2])
	end := 3 + sectionLen - 4
	if end > len(s) {
		end = len(s)
	}
	programInfoLen := int(s[10]&0x0F)<<8 | int(s[11])
	i := 12 + programInfoLen

	var out []Track
	for i+5 <= end {
		streamType := s[i]
		pid := int(s[i+1]&0x1F)<<8 | int(s[i+2])
		esInfoLen := int(s[i+3]&0x0F)<<8 | int(s[i+4])
		descStart, descEnd := i+5, i+5+esInfoLen
		if descEnd > end {
			descEnd = end
		}

		tr := Track{PID: pid}
		if info, ok := tsStreamTypes[streamType]; ok {
			tr.Kind, tr.Name = info.kind, info.name
		} else {
			tr.Kind, tr.Name = KindText, fmt.Sprintf("unknown-0x%02X", streamType)
		}
		// Private streams (0x06) are whatever their descriptor says — this is how
		// AC-3 and E-AC-3 travel in DVB, and how subtitle tracks appear.
		if descStart < descEnd {
			refineFromDescriptors(&tr, s[descStart:descEnd])
		}
		out = append(out, tr)
		i = i + 5 + esInfoLen
	}
	return out
}

func refineFromDescriptors(tr *Track, desc []byte) {
	for i := 0; i+2 <= len(desc); {
		tag, l := desc[i], int(desc[i+1])
		body := desc[i+2 : min(i+2+l, len(desc))]
		switch tag {
		case 0x6A: // AC-3 descriptor (DVB)
			tr.Kind, tr.Name = KindAudio, "ac3"
		case 0x7A: // enhanced AC-3
			tr.Kind, tr.Name = KindAudio, "eac3"
		case 0x7B: // DTS
			tr.Kind, tr.Name = KindAudio, "dts"
		case 0x7C: // AAC
			tr.Kind, tr.Name = KindAudio, "aac"
		case 0x56: // teletext
			tr.Kind, tr.Name = KindText, "teletext"
		case 0x59: // DVB subtitling
			tr.Kind, tr.Name = KindText, "dvbsub"
		case 0x05: // registration descriptor: format_identifier
			if len(body) >= 4 {
				switch string(body[:4]) {
				case "AC-3":
					tr.Kind, tr.Name = KindAudio, "ac3"
				case "EAC3":
					tr.Kind, tr.Name = KindAudio, "eac3"
				case "Opus":
					tr.Kind, tr.Name = KindAudio, "opus"
				}
			}
		case 0x0A: // ISO-639 language
			if len(body) >= 3 {
				tr.Lang = strings.ToLower(string(body[:3]))
			}
		}
		i += 2 + l
	}
}

// ---------------------------------------------------------------------------
// ISOBMFF (mp4 / fMP4 init segments)
// ---------------------------------------------------------------------------

// DetectMP4 walks moov → trak → mdia{hdlr,minf→stbl→stsd} and names each track.
func DetectMP4(data []byte) Tracks {
	out := Tracks{Container: "mp4"}
	moov := findBox(data, "moov")
	if moov == nil {
		return out
	}
	for _, trak := range findBoxes(moov, "trak") {
		mdia := findBox(trak, "mdia")
		if mdia == nil {
			continue
		}
		tr := Track{Kind: kindFromHandler(findBox(mdia, "hdlr"))}
		if lang := languageFromMDHD(findBox(mdia, "mdhd")); lang != "" {
			tr.Lang = lang
		}
		stbl := findBox(findBox(mdia, "minf"), "stbl")
		stsd := findBox(stbl, "stsd")
		if stsd == nil || len(stsd) < 8 {
			continue
		}
		// stsd: 4 version/flags + 4 entry_count, then sample entries.
		entries := stsd[8:]
		if len(entries) < 8 {
			continue
		}
		size := int(binary.BigEndian.Uint32(entries[0:4]))
		if size < 8 || size > len(entries) {
			size = len(entries)
		}
		entry := entries[:size]
		format := string(entry[4:8])
		tr.Name, tr.RFC = codecFromSampleEntry(format, entry)
		if tr.Kind == "" {
			tr.Kind = kindFromFormat(format)
		}
		out.Tracks = append(out.Tracks, tr)
	}
	return out
}

func kindFromHandler(hdlr []byte) Kind {
	if len(hdlr) < 16 {
		return ""
	}
	switch string(hdlr[8:12]) {
	case "vide":
		return KindVideo
	case "soun":
		return KindAudio
	case "text", "sbtl", "subt", "clcp":
		return KindText
	}
	return ""
}

func languageFromMDHD(mdhd []byte) string {
	// version(1) flags(3) then times; language is the packed 5-bit triple that
	// sits 4 bytes before the end of the v0 box body.
	if len(mdhd) < 24 {
		return ""
	}
	var packed uint16
	if mdhd[0] == 0 {
		packed = binary.BigEndian.Uint16(mdhd[20:22])
	} else if len(mdhd) >= 36 {
		packed = binary.BigEndian.Uint16(mdhd[32:34])
	} else {
		return ""
	}
	if packed == 0 {
		return ""
	}
	l := []byte{
		byte((packed>>10)&0x1F) + 0x60,
		byte((packed>>5)&0x1F) + 0x60,
		byte(packed&0x1F) + 0x60,
	}
	for _, c := range l {
		if c < 'a' || c > 'z' {
			return ""
		}
	}
	return string(l)
}

func kindFromFormat(format string) Kind {
	switch format {
	case "avc1", "avc3", "hvc1", "hev1", "vp09", "av01", "dvh1", "dvhe":
		return KindVideo
	case "mp4a", "ac-3", "ec-3", "Opus", "fLaC", "dtsc", "dtse":
		return KindAudio
	case "wvtt", "stpp", "tx3g":
		return KindText
	}
	return ""
}

// codecFromSampleEntry names the codec and, where the config box makes it
// possible, builds the RFC 6381 string a playlist would carry.
func codecFromSampleEntry(format string, entry []byte) (name, rfc string) {
	switch format {
	case "avc1", "avc3":
		name = "h264"
		if cfg := findChildBox(entry, "avcC"); len(cfg) >= 4 {
			// avcC: configurationVersion, profile, compat, level
			rfc = fmt.Sprintf("%s.%02X%02X%02X", format, cfg[1], cfg[2], cfg[3])
		}
	case "hvc1", "hev1":
		name = "hevc"
		if cfg := findChildBox(entry, "hvcC"); len(cfg) >= 13 {
			rfc = hevcCodecString(format, cfg)
		}
	case "dvh1", "dvhe":
		// Dolby Vision profile 5/7/8 — the one that plays as a green picture or
		// not at all on hardware without DV support.
		name = "dolbyvision"
		if cfg := findChildBox(entry, "dvcC"); len(cfg) >= 3 {
			profile := (cfg[2] >> 1) & 0x7F
			level := ((cfg[2] & 0x01) << 5) | ((cfg[3] >> 3) & 0x1F)
			rfc = fmt.Sprintf("%s.%02d.%02d", format, profile, level)
		}
	case "vp09":
		name = "vp9"
	case "av01":
		name = "av1"
	case "mp4a":
		name = "aac"
		rfc = "mp4a.40.2"
		if esds := findChildBox(entry, "esds"); len(esds) > 0 {
			if oti, aot, ok := parseESDS(esds); ok {
				if aot > 0 {
					rfc = fmt.Sprintf("mp4a.%02X.%d", oti, aot)
				} else {
					rfc = fmt.Sprintf("mp4a.%02X", oti)
				}
				if oti == 0x69 || oti == 0x6B {
					name = "mp3"
				}
			}
		}
	case "ac-3":
		name, rfc = "ac3", "ac-3"
	case "ec-3":
		name, rfc = "eac3", "ec-3"
	case "Opus":
		name, rfc = "opus", "Opus"
	case "fLaC":
		name, rfc = "flac", "fLaC"
	case "dtsc", "dtse", "dtsh", "dtsl":
		name, rfc = "dts", format
	case "wvtt":
		name, rfc = "webvtt", "wvtt"
	case "stpp":
		name, rfc = "ttml", "stpp"
	default:
		name = format
	}
	return name, rfc
}

func hevcCodecString(format string, cfg []byte) string {
	// hvcC: configurationVersion(1), then profile_space(2)|tier(1)|profile_idc(5),
	// profile_compatibility(4), constraint flags(6), level_idc(1).
	profileSpace := (cfg[1] >> 6) & 0x03
	tierFlag := (cfg[1] >> 5) & 0x01
	profileIDC := cfg[1] & 0x1F
	compat := binary.BigEndian.Uint32(cfg[2:6])
	level := cfg[12]

	space := ""
	switch profileSpace {
	case 1:
		space = "A"
	case 2:
		space = "B"
	case 3:
		space = "C"
	}
	tier := "L"
	if tierFlag == 1 {
		tier = "H"
	}
	// Compatibility flags are carried reversed in the codec string.
	return fmt.Sprintf("%s.%s%d.%X.%s%d", format, space, profileIDC, reverseBits32(compat), tier, level)
}

func reverseBits32(v uint32) uint32 {
	var out uint32
	for i := 0; i < 32; i++ {
		out = out<<1 | (v & 1)
		v >>= 1
	}
	return out
}

// parseESDS digs the object type indication and the AAC audio object type out
// of an esds box. Returns ok=false when the descriptors are not laid out the
// way the spec's common case expects — a wrong codec string is worse than none.
func parseESDS(esds []byte) (oti byte, aot int, ok bool) {
	if len(esds) < 5 {
		return 0, 0, false
	}
	b := esds[4:] // skip version/flags
	// ES_Descriptor (tag 0x03) → DecoderConfigDescriptor (0x04) → DecoderSpecificInfo (0x05)
	for i := 0; i+2 < len(b); i++ {
		if b[i] != 0x04 {
			continue
		}
		j := i + 1
		// descriptor length uses 7-bit continuation bytes
		for j < len(b) && b[j]&0x80 != 0 {
			j++
		}
		j++
		if j >= len(b) {
			return 0, 0, false
		}
		oti = b[j]
		// DecoderSpecificInfo follows the 13-byte DecoderConfigDescriptor body.
		for k := j; k+2 < len(b); k++ {
			if b[k] != 0x05 {
				continue
			}
			m := k + 1
			for m < len(b) && b[m]&0x80 != 0 {
				m++
			}
			m++
			if m < len(b) {
				aot = int(b[m] >> 3)
			}
			break
		}
		return oti, aot, true
	}
	return 0, 0, false
}

// ---------------------------------------------------------------------------
// box walking
// ---------------------------------------------------------------------------

// findBox returns the BODY of the first box with this type, searching one level
// down and, for container boxes, recursively. Truncated input (we only ever see
// a prefix of the file) simply yields nothing.
func findBox(data []byte, typ string) []byte {
	boxes := findBoxes(data, typ)
	if len(boxes) == 0 {
		return nil
	}
	return boxes[0]
}

// findChildBox finds a config box INSIDE a sample entry. Sample entries start
// with a fixed-size field block whose length depends on the entry type (78
// bytes for video, 28 or more for audio, and version-dependent for both), so
// walking boxes from the start does not work — we look up the type tag and
// read the size word that precedes it.
func findChildBox(entry []byte, typ string) []byte {
	for i := 12; i+4 <= len(entry); i++ {
		if string(entry[i:i+4]) != typ {
			continue
		}
		start := i - 4
		size := int(binary.BigEndian.Uint32(entry[start : start+4]))
		if size < 8 {
			continue
		}
		end := start + size
		if end > len(entry) {
			end = len(entry) // truncated prefix: return what is there
		}
		if i+4 > end {
			continue
		}
		return entry[i+4 : end]
	}
	return nil
}

func findBoxes(data []byte, typ string) [][]byte {
	var out [][]byte
	if len(data) < 8 {
		return nil
	}
	for off := 0; off+8 <= len(data); {
		size := int(binary.BigEndian.Uint32(data[off : off+4]))
		name := string(data[off+4 : off+8])
		body := off + 8
		switch {
		case size == 0: // extends to end of file
			size = len(data) - off
		case size == 1: // 64-bit size
			if off+16 > len(data) {
				return out
			}
			size = int(binary.BigEndian.Uint64(data[off+8 : off+16]))
			body = off + 16
		}
		if size < 8 {
			return out
		}
		end := off + size
		if end > len(data) {
			end = len(data) // truncated tail: use what we have
		}
		if body > end {
			return out
		}
		if name == typ {
			out = append(out, data[body:end])
		}
		off = end
		if off <= body-8 { // no forward progress: malformed
			return out
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
