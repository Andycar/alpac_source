package proxyapi

import (
	"encoding/binary"
	"testing"
)

// makeBox creates a minimal MP4 box with the given type and body.
func makeBox(typ string, body []byte) []byte {
	size := 8 + len(body)
	b := make([]byte, size)
	binary.BigEndian.PutUint32(b[:4], uint32(size))
	copy(b[4:8], typ)
	copy(b[8:], body)
	return b
}

// makeVideoSampleEntry creates a minimal encv or avc1 box with given sub-boxes.
func makeVideoSampleEntry(typ string, subBoxes []byte) []byte {
	// 86 bytes fixed part: 8 header + 78 data
	fixedLen := 86
	totalLen := fixedLen + len(subBoxes)
	b := make([]byte, totalLen)
	binary.BigEndian.PutUint32(b[:4], uint32(totalLen))
	copy(b[4:8], typ)
	// Fill fixed part with zeros (reserved/predefined fields)
	copy(b[fixedLen:], subBoxes)
	return b
}

// makeAudioSampleEntry creates a minimal enca or mp4a box with given sub-boxes.
func makeAudioSampleEntry(typ string, subBoxes []byte) []byte {
	// 36 bytes fixed part: 8 header + 28 data
	fixedLen := 36
	totalLen := fixedLen + len(subBoxes)
	b := make([]byte, totalLen)
	binary.BigEndian.PutUint32(b[:4], uint32(totalLen))
	copy(b[4:8], typ)
	copy(b[fixedLen:], subBoxes)
	return b
}

func TestStripFMP4DRM_NoChange(t *testing.T) {
	// Data without any encv/enca should pass through unchanged.
	data := makeBox("ftyp", []byte("isom"))
	result := stripFMP4DRM(data)
	if len(result) != len(data) {
		t.Fatalf("expected no change, got different length: %d vs %d", len(result), len(data))
	}
}

func TestStripFMP4DRM_VideoEntry(t *testing.T) {
	// Build: moov → trak → mdia → minf → stbl → stsd → encv(with sinf)
	sinf := makeBox("sinf", makeBox("schm", []byte{0, 0, 0, 0, 'c', 'b', 'c', 's'}))
	avcC := makeBox("avcC", []byte{1, 100, 0, 31}) // minimal avcC
	encv := makeVideoSampleEntry("encv", append(avcC, sinf...))

	stsdBody := make([]byte, 8) // version(4) + entry_count(4)
	binary.BigEndian.PutUint32(stsdBody[4:8], 1)
	stsdBody = append(stsdBody, encv...)
	stsd := makeBox("stsd", stsdBody)

	stbl := makeBox("stbl", stsd)
	minf := makeBox("minf", stbl)
	mdia := makeBox("mdia", minf)
	trak := makeBox("trak", mdia)
	moov := makeBox("moov", trak)

	result := stripFMP4DRM(moov)

	// Check that "encv" is now "avc1"
	if !containsBoxType(result, "avc1") {
		t.Fatal("expected avc1 box after stripping, not found")
	}
	if containsBoxType(result, "encv") {
		t.Fatal("encv box should have been renamed to avc1")
	}
	// Check that sinf is removed
	if containsBoxType(result, "sinf") {
		t.Fatal("sinf box should have been removed")
	}
	// avcC should still be present
	if !containsBoxType(result, "avcC") {
		t.Fatal("avcC box should still be present")
	}
	// Init segment length must stay the same (removed bytes are replaced with free box)
	// so BYTERANGE offsets in playlists remain valid.
	if len(result) != len(moov) {
		t.Fatalf("result length mismatch: %d != %d", len(result), len(moov))
	}
	if !containsBoxType(result, "free") {
		t.Fatal("expected compensating free box")
	}
}

func TestStripFMP4DRM_AudioEntry(t *testing.T) {
	sinf := makeBox("sinf", makeBox("tenc", []byte{0, 0, 0, 0, 0, 0}))
	esds := makeBox("esds", []byte{0, 0, 0, 0}) // minimal esds
	enca := makeAudioSampleEntry("enca", append(esds, sinf...))

	stsdBody := make([]byte, 8)
	binary.BigEndian.PutUint32(stsdBody[4:8], 1)
	stsdBody = append(stsdBody, enca...)
	stsd := makeBox("stsd", stsdBody)

	stbl := makeBox("stbl", stsd)
	minf := makeBox("minf", stbl)
	mdia := makeBox("mdia", minf)
	trak := makeBox("trak", mdia)
	moov := makeBox("moov", trak)

	result := stripFMP4DRM(moov)

	if !containsBoxType(result, "mp4a") {
		t.Fatal("expected mp4a box after stripping, not found")
	}
	if containsBoxType(result, "enca") {
		t.Fatal("enca box should have been renamed to mp4a")
	}
	if containsBoxType(result, "sinf") {
		t.Fatal("sinf box should have been removed")
	}
	if !containsBoxType(result, "esds") {
		t.Fatal("esds box should still be present")
	}
}

func TestStripFMP4DRM_MultipleTracks(t *testing.T) {
	// Two tracks: video (encv) and audio (enca)
	sinfV := makeBox("sinf", []byte{1, 2, 3, 4})
	encv := makeVideoSampleEntry("encv", append(makeBox("avcC", []byte{1}), sinfV...))
	stsdV := makeBox("stsd", append(make([]byte, 8), encv...))

	sinfA := makeBox("sinf", []byte{5, 6, 7, 8})
	enca := makeAudioSampleEntry("enca", append(makeBox("esds", []byte{0}), sinfA...))
	stsdA := makeBox("stsd", append(make([]byte, 8), enca...))

	trakV := makeBox("trak", makeBox("mdia", makeBox("minf", makeBox("stbl", stsdV))))
	trakA := makeBox("trak", makeBox("mdia", makeBox("minf", makeBox("stbl", stsdA))))

	moov := makeBox("moov", append(trakV, trakA...))
	result := stripFMP4DRM(moov)

	if containsBoxType(result, "encv") || containsBoxType(result, "enca") {
		t.Fatal("encrypted entries should be renamed")
	}
	if !containsBoxType(result, "avc1") || !containsBoxType(result, "mp4a") {
		t.Fatal("expected avc1 and mp4a")
	}
	if containsBoxType(result, "sinf") {
		t.Fatal("sinf boxes should be removed")
	}
}

func TestHasFMP4DRMBoxes(t *testing.T) {
	encv := makeVideoSampleEntry("encv", nil)
	if !hasFMP4DRMBoxes(encv) {
		t.Fatal("should detect encv")
	}

	avc1 := makeVideoSampleEntry("avc1", nil)
	if hasFMP4DRMBoxes(avc1) {
		t.Fatal("should not detect avc1 as DRM")
	}

	enca := makeAudioSampleEntry("enca", nil)
	if !hasFMP4DRMBoxes(enca) {
		t.Fatal("should detect enca")
	}
}

// containsBoxType checks if any box in the data has the given 4-byte type.
func containsBoxType(data []byte, typ string) bool {
	for i := 4; i+4 <= len(data); i++ {
		if string(data[i:i+4]) == typ {
			return true
		}
	}
	return false
}
