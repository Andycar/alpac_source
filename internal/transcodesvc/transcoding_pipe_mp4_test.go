package transcodesvc

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// mp4box builds a minimal ISOBMFF box: [uint32 size][4-byte type][payload].
func mp4box(typ string, payload []byte) []byte {
	if len(typ) != 4 {
		panic("box type must be 4 bytes")
	}
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// mp4box64 builds a box using the 64-bit largesize form (size==1).
func mp4box64(typ string, payload []byte) []byte {
	out := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(out[0:4], 1)
	copy(out[4:8], typ)
	binary.BigEndian.PutUint64(out[8:16], uint64(16+len(payload)))
	copy(out[16:], payload)
	return out
}

// feedInChunks writes data to the segmenter in fixed-size chunks to exercise
// box reassembly across Write boundaries.
func feedInChunks(t *testing.T, seg *mp4Segmenter, data []byte, chunk int) {
	t.Helper()
	for off := 0; off < len(data); off += chunk {
		end := off + chunk
		if end > len(data) {
			end = len(data)
		}
		if _, err := seg.Write(data[off:end]); err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
	}
}

func TestMP4SegmenterBasic(t *testing.T) {
	ftyp := mp4box("ftyp", []byte("isom_brand_data"))
	moov := mp4box("moov", bytes.Repeat([]byte{0xAB}, 200))
	wantInit := append(append([]byte{}, ftyp...), moov...)

	frag := func(n byte, mdatLen int) []byte {
		moof := mp4box("moof", []byte{n, n, n, n})
		mdat := mp4box("mdat", bytes.Repeat([]byte{n}, mdatLen))
		return append(append([]byte{}, moof...), mdat...)
	}
	f0 := frag(0x10, 1000)
	f1 := frag(0x20, 1500)
	f2 := frag(0x30, 800)

	stream := bytes.Join([][]byte{ftyp, moov, f0, f1, f2}, nil)

	// Run with several chunk sizes to stress the reassembly buffer, including
	// 1-byte and a size that splits box headers.
	for _, chunk := range []int{1, 3, 7, 8, 13, 64, 4096, len(stream)} {
		var gotInit []byte
		var segs [][]byte
		seg := &mp4Segmenter{
			onInit: func(init []byte) error {
				gotInit = append([]byte{}, init...)
				return nil
			},
			onSegment: func(data []byte) error {
				segs = append(segs, append([]byte{}, data...))
				return nil
			},
		}
		feedInChunks(t, seg, stream, chunk)

		if !bytes.Equal(gotInit, wantInit) {
			t.Fatalf("chunk=%d: init mismatch: got %d bytes, want %d", chunk, len(gotInit), len(wantInit))
		}
		if len(segs) != 3 {
			t.Fatalf("chunk=%d: got %d segments, want 3", chunk, len(segs))
		}
		for i, want := range [][]byte{f0, f1, f2} {
			if !bytes.Equal(segs[i], want) {
				t.Fatalf("chunk=%d: segment %d mismatch (got %d bytes, want %d)", chunk, i, len(segs[i]), len(want))
			}
		}
	}
}

func TestMP4Segmenter64BitMdat(t *testing.T) {
	ftyp := mp4box("ftyp", []byte("isom"))
	moov := mp4box("moov", []byte("conf"))
	moof := mp4box("moof", []byte{1, 2, 3, 4})
	mdat := mp4box64("mdat", bytes.Repeat([]byte{0x77}, 5000)) // 64-bit size form
	stream := bytes.Join([][]byte{ftyp, moov, moof, mdat}, nil)

	var segs [][]byte
	var inits int
	seg := &mp4Segmenter{
		onInit:    func(init []byte) error { inits++; return nil },
		onSegment: func(data []byte) error { segs = append(segs, append([]byte{}, data...)); return nil },
	}
	feedInChunks(t, seg, stream, 5)

	if inits != 1 {
		t.Fatalf("expected 1 init, got %d", inits)
	}
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segs))
	}
	wantSeg := append(append([]byte{}, moof...), mdat...)
	if !bytes.Equal(segs[0], wantSeg) {
		t.Fatalf("segment with 64-bit mdat mismatch: got %d bytes, want %d", len(segs[0]), len(wantSeg))
	}
}

func TestMP4SegmenterStypHandled(t *testing.T) {
	// Defensive: a muxer that DOES emit styp before moof should still produce
	// one segment per styp+moof+mdat group.
	ftyp := mp4box("ftyp", []byte("isom"))
	moov := mp4box("moov", []byte("conf"))
	styp := mp4box("styp", []byte("msdh"))
	moof := mp4box("moof", []byte{1, 2, 3, 4})
	mdat := mp4box("mdat", bytes.Repeat([]byte{0x55}, 300))
	stream := bytes.Join([][]byte{ftyp, moov, styp, moof, mdat}, nil)

	var segs int
	seg := &mp4Segmenter{
		onInit:    func(init []byte) error { return nil },
		onSegment: func(data []byte) error { segs++; return nil },
	}
	feedInChunks(t, seg, stream, 9)
	if segs != 1 {
		t.Fatalf("expected 1 segment for styp+moof+mdat, got %d", segs)
	}
}

func TestMP4SegmenterCorruptSize(t *testing.T) {
	// A box claiming size < 8 must surface an error rather than loop forever.
	bad := []byte{0, 0, 0, 4, 'm', 'o', 'o', 'f', 0, 0, 0, 0}
	seg := &mp4Segmenter{
		onInit:    func(init []byte) error { return nil },
		onSegment: func(data []byte) error { return nil },
	}
	// Prepend a valid ftyp so init accumulation starts, then the corrupt box.
	ftyp := mp4box("ftyp", []byte("isom"))
	_, err := seg.Write(append(append([]byte{}, ftyp...), bad...))
	if err == nil {
		t.Fatalf("expected error on corrupt box size, got nil")
	}
}
