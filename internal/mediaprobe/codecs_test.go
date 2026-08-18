package mediaprobe

import (
	"encoding/binary"
	"testing"
)

// ---------------------------------------------------------------------------
// builders — small synthetic containers, assembled the way the specs lay them
// out so the parsers are tested against structure rather than a captured blob
// ---------------------------------------------------------------------------

func tsPacket(pid int, payload []byte) []byte {
	pkt := make([]byte, 188)
	pkt[0] = 0x47
	pkt[1] = 0x40 | byte(pid>>8&0x1F) // payload_unit_start_indicator
	pkt[2] = byte(pid & 0xFF)
	pkt[3] = 0x10 // payload only, continuity 0
	pkt[4] = 0x00 // pointer_field
	copy(pkt[5:], payload)
	for i := 5 + len(payload); i < 188; i++ {
		pkt[i] = 0xFF
	}
	return pkt
}

// psiSection wraps a PSI body with the syntax header and a (never checked) CRC.
func psiSection(tableID byte, body []byte) []byte {
	sectionLen := len(body) + 4 // body + CRC32
	out := []byte{tableID, 0xB0 | byte(sectionLen>>8&0x0F), byte(sectionLen & 0xFF)}
	out = append(out, body...)
	return append(out, 0xDE, 0xAD, 0xBE, 0xEF)
}

func patSection(pmtPID int) []byte {
	body := []byte{
		0x00, 0x01, // transport_stream_id
		0xC1,       // version 0, current
		0x00, 0x00, // section numbers
		0x00, 0x01, // program_number 1
		0xE0 | byte(pmtPID>>8&0x1F), byte(pmtPID & 0xFF),
	}
	return psiSection(0x00, body)
}

type esEntry struct {
	streamType byte
	pid        int
	descs      []byte
}

func pmtSection(entries []esEntry) []byte {
	body := []byte{
		0x00, 0x01, // program_number
		0xC1,       // version, current
		0x00, 0x00, // section numbers
		0xE1, 0x00, // PCR_PID
		0xF0, 0x00, // program_info_length = 0
	}
	for _, e := range entries {
		body = append(body,
			e.streamType,
			0xE0|byte(e.pid>>8&0x1F), byte(e.pid&0xFF),
			0xF0|byte(len(e.descs)>>8&0x0F), byte(len(e.descs)&0xFF))
		body = append(body, e.descs...)
	}
	return psiSection(0x02, body)
}

func box(typ string, payload ...[]byte) []byte {
	var body []byte
	for _, p := range payload {
		body = append(body, p...)
	}
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(body)))
	copy(out[4:8], typ)
	return append(out, body...)
}

// stsd carries a version/flags word and an entry count before the sample entry.
func stsd(entry []byte) []byte {
	head := []byte{0, 0, 0, 0, 0, 0, 0, 1}
	return box("stsd", head, entry)
}

func hdlr(kind string) []byte {
	body := make([]byte, 8)
	body = append(body, []byte(kind)...) // handler_type at offset 8
	body = append(body, make([]byte, 12)...)
	return box("hdlr", body)
}

// mdhd v0 with a packed ISO-639 language at the documented offset.
func mdhd(lang string) []byte {
	body := make([]byte, 24)
	packed := uint16(lang[0]-0x60)<<10 | uint16(lang[1]-0x60)<<5 | uint16(lang[2]-0x60)
	binary.BigEndian.PutUint16(body[20:22], packed)
	return box("mdhd", body)
}

func trak(handler, lang string, sampleEntry []byte) []byte {
	m := []byte{}
	if lang != "" {
		m = append(m, mdhd(lang)...)
	}
	m = append(m, hdlr(handler)...)
	m = append(m, box("minf", box("stbl", stsd(sampleEntry)))...)
	return box("trak", box("mdia", m))
}

// videoSampleEntry mimics a VisualSampleEntry: 78 bytes of fixed fields, then
// the codec config box.
func videoSampleEntry(format string, cfg []byte) []byte {
	return box(format, make([]byte, 78), cfg)
}

func audioSampleEntry(format string, cfg []byte) []byte {
	return box(format, make([]byte, 28), cfg)
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestDetectTSNamesTracks(t *testing.T) {
	// h264 video + E-AC-3 audio with a language descriptor: the exact shape that
	// plays silently on a box without a Dolby licence.
	lang := []byte{0x0A, 0x04, 'r', 'u', 's', 0x00}
	data := append(tsPacket(0, patSection(0x1000)),
		tsPacket(0x1000, pmtSection([]esEntry{
			{0x1B, 0x100, nil},
			{0x87, 0x101, lang},
		}))...)
	// A third packet so isTS's sync-byte run has something to walk.
	data = append(data, tsPacket(0x100, []byte{0x00, 0x00, 0x01})...)

	got := Detect(data)
	if got.Container != "ts" {
		t.Fatalf("container = %q", got.Container)
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("tracks = %+v", got.Tracks)
	}
	if got.Tracks[0].Kind != KindVideo || got.Tracks[0].Name != "h264" || got.Tracks[0].PID != 0x100 {
		t.Errorf("video track = %+v", got.Tracks[0])
	}
	if got.Tracks[1].Kind != KindAudio || got.Tracks[1].Name != "eac3" {
		t.Errorf("audio track = %+v", got.Tracks[1])
	}
	if got.Tracks[1].Lang != "rus" {
		t.Errorf("lang = %q", got.Tracks[1].Lang)
	}
	if !got.Has("eac3") {
		t.Error("Has(eac3) should be true")
	}
}

// DVB carries AC-3 as private data (0x06) plus a descriptor — taking the stream
// type at face value would call it "private" and hide the audio codec entirely.
func TestDetectTSPrivateStreamWithAC3Descriptor(t *testing.T) {
	data := append(tsPacket(0, patSection(0x1000)),
		tsPacket(0x1000, pmtSection([]esEntry{
			{0x1B, 0x100, nil},
			{0x06, 0x102, []byte{0x6A, 0x01, 0x00}}, // AC-3 descriptor
		}))...)
	data = append(data, tsPacket(0x100, []byte{0x00})...)

	got := Detect(data)
	if len(got.Tracks) != 2 || got.Tracks[1].Name != "ac3" || got.Tracks[1].Kind != KindAudio {
		t.Fatalf("tracks = %+v", got.Tracks)
	}
}

func TestDetectMP4AVCAndAAC(t *testing.T) {
	avcC := box("avcC", []byte{0x01, 0x64, 0x00, 0x28, 0xFF})
	// esds: version/flags, then DecoderConfig (0x04) with OTI 0x40 and a
	// DecoderSpecificInfo (0x05) whose first byte encodes AAC-LC (AOT 2).
	esds := box("esds", []byte{
		0x00, 0x00, 0x00, 0x00,
		0x03, 0x19, 0x00, 0x01, 0x00,
		0x04, 0x11, 0x40, 0x15, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x05, 0x02, 0x11, 0x90,
	})

	data := append(box("ftyp", []byte("isom")),
		box("moov",
			trak("vide", "", videoSampleEntry("avc1", avcC)),
			trak("soun", "rus", audioSampleEntry("mp4a", esds)),
		)...)

	got := Detect(data)
	if got.Container != "mp4" || len(got.Tracks) != 2 {
		t.Fatalf("got = %+v", got)
	}
	if got.Tracks[0].Name != "h264" || got.Tracks[0].RFC != "avc1.640028" {
		t.Errorf("video = %+v", got.Tracks[0])
	}
	if got.Tracks[1].Name != "aac" || got.Tracks[1].RFC != "mp4a.40.2" {
		t.Errorf("audio = %+v", got.Tracks[1])
	}
	if got.Tracks[1].Lang != "rus" {
		t.Errorf("lang = %q", got.Tracks[1].Lang)
	}
	if got.CodecString() != "avc1.640028,mp4a.40.2" {
		t.Errorf("codec string = %q", got.CodecString())
	}
}

func TestDetectMP4HEVCAndEAC3(t *testing.T) {
	// hvcC for Main profile, tier L, level 120 (4.0).
	hvcC := box("hvcC", []byte{
		0x01,                   // configurationVersion
		0x01,                   // profile_space 0, tier 0, profile_idc 1
		0x60, 0x00, 0x00, 0x00, // profile_compatibility
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // constraint flags
		120, // level_idc
	})
	data := append(box("styp", []byte("msdh")),
		box("moov",
			trak("vide", "", videoSampleEntry("hvc1", hvcC)),
			trak("soun", "", audioSampleEntry("ec-3", box("dec3", []byte{0x00}))),
		)...)

	got := Detect(data)
	if len(got.Tracks) != 2 {
		t.Fatalf("tracks = %+v", got.Tracks)
	}
	if got.Tracks[0].Name != "hevc" || got.Tracks[0].RFC != "hvc1.1.6.L120" {
		t.Errorf("video = %+v", got.Tracks[0])
	}
	if got.Tracks[1].Name != "eac3" || got.Tracks[1].RFC != "ec-3" {
		t.Errorf("audio = %+v", got.Tracks[1])
	}
}

// The esds parse has to be real, not a default that happens to be right for
// AAC-LC: HE-AAC and MP3-in-MP4 must come out as themselves.
func TestDetectMP4ParsesESDSObjectTypes(t *testing.T) {
	esdsFor := func(oti, aotByte byte) []byte {
		return box("esds", []byte{
			0x00, 0x00, 0x00, 0x00,
			0x03, 0x19, 0x00, 0x01, 0x00,
			0x04, 0x11, oti, 0x15, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x05, 0x02, aotByte, 0x90,
		})
	}

	cases := []struct {
		oti, aotByte byte
		wantName     string
		wantRFC      string
	}{
		{0x40, 0x11, "aac", "mp4a.40.2"}, // AAC-LC
		{0x40, 0x2B, "aac", "mp4a.40.5"}, // HE-AAC (SBR)
		{0x40, 0x3D, "aac", "mp4a.40.7"}, // HE-AACv2 (PS)
		// MPEG-2 audio: no audio object type, so the codec string stops at the OTI.
		{0x69, 0x00, "mp3", "mp4a.69"},
	}
	for _, c := range cases {
		data := append(box("ftyp", []byte("isom")),
			box("moov", trak("soun", "", audioSampleEntry("mp4a", esdsFor(c.oti, c.aotByte))))...)
		got := Detect(data)
		if len(got.Tracks) != 1 {
			t.Fatalf("oti=%02X: tracks = %+v", c.oti, got.Tracks)
		}
		if got.Tracks[0].Name != c.wantName || got.Tracks[0].RFC != c.wantRFC {
			t.Errorf("oti=%02X aot=%02X → %+v, want %s/%s",
				c.oti, c.aotByte, got.Tracks[0], c.wantName, c.wantRFC)
		}
	}
}

// Dolby Vision is worth naming separately: it is the one that plays as a green
// or black picture on hardware without DV rather than failing outright.
func TestDetectMP4DolbyVision(t *testing.T) {
	dvcC := box("dvcC", []byte{0x01, 0x00, 0x0A, 0x40})
	data := append(box("ftyp", []byte("isom")),
		box("moov", trak("vide", "", videoSampleEntry("dvh1", dvcC)))...)

	got := Detect(data)
	if len(got.Tracks) != 1 || got.Tracks[0].Name != "dolbyvision" {
		t.Fatalf("tracks = %+v", got.Tracks)
	}
}

func TestDetectRejectsNonMedia(t *testing.T) {
	for name, body := range map[string][]byte{
		"html":  []byte("<!DOCTYPE html><html><body>403</body></html>"),
		"empty": nil,
		"short": []byte{0x47, 0x01},
		// A stray 0x47 must not be read as transport stream.
		"text-with-G": []byte("Gone fishing, come back later. Gone fishing, come back later."),
	} {
		if got := Detect(body); got.Container != "" || len(got.Tracks) != 0 {
			t.Errorf("%s: expected nothing, got %+v", name, got)
		}
	}
}

// We only ever see the first tens of kilobytes of a segment, so a truncated
// box tree must degrade to "what I could read", never to a panic.
func TestDetectMP4TruncatedInput(t *testing.T) {
	avcC := box("avcC", []byte{0x01, 0x64, 0x00, 0x28, 0xFF})
	full := append(box("ftyp", []byte("isom")),
		box("moov", trak("vide", "", videoSampleEntry("avc1", avcC)))...)

	for cut := len(full); cut > 8; cut -= 7 {
		got := Detect(full[:cut])
		for _, tr := range got.Tracks {
			if tr.Name == "" {
				t.Fatalf("cut=%d produced a nameless track: %+v", cut, got.Tracks)
			}
		}
	}
}
