package transcodesvc

import (
	"encoding/binary"
	"errors"
)

// ---------------------------------------------------------------------------
// fMP4 box segmenter — the heart of the in-memory pipe transcoding path.
//
// ffmpeg, when invoked with
//
//	-f mp4 -movflags +frag_keyframe+empty_moov+default_base_moof -min_frag_duration N pipe:1
//
// emits a *fragmented* MP4 byte stream on stdout:
//
//	ftyp + moov            ← the init section (codec config, no media)
//	moof + mdat            ← fragment 0  (one keyframe-aligned segment)
//	moof + mdat            ← fragment 1
//	...
//
// mp4Segmenter consumes that stream (it implements io.Writer, so it can be
// driven directly by io.Copy from the stdout pipe) and hands the init blob
// and each media fragment to callbacks — without ever touching the disk.
// Boxes that straddle Write boundaries are reassembled via an internal tail
// buffer.  ffmpeg does NOT write `styp` boxes (verified against movenc.c), so
// a new segment is detected on `moof`; the segment closes on the following
// `mdat`.  `styp` is still handled defensively in case a future ffmpeg or a
// different muxer emits it.
//
// This mirrors the reference C# `Mp4BoxSplitter` from the original Lampac
// prototype, ported to Go and made an io.Writer so backpressure flows
// naturally: when onSegment blocks (segment ring full because the client
// hasn't fetched yet), io.Copy blocks, ffmpeg's stdout write blocks, and the
// whole pipeline freezes — exactly the GStreamer appsink-backpressure model,
// achieved with one OS pipe.
// ---------------------------------------------------------------------------

// mp4Segmenter splits a fragmented-MP4 stream into init + media segments.
// The zero value is NOT ready — construct with onInit/onSegment set.
type mp4Segmenter struct {
	// onInit is invoked once, with the ftyp+moov init section, the moment
	// the first fragment begins.  Returning an error aborts the stream.
	onInit func(init []byte) error
	// onSegment is invoked for every complete moof+mdat fragment, in order.
	// It MAY block (that is how backpressure is applied).  Returning an
	// error aborts the stream (used to unwind on session close / seek).
	onSegment func(data []byte) error

	buf      []byte // unparsed tail (at most one partial box)
	init     []byte // accumulates ftyp/moov until the first fragment
	initDone bool
	seg      []byte // accumulates the current moof(+...)+mdat fragment
	inSeg    bool
}

var (
	errMP4Corrupt = errors.New("mp4 segmenter: invalid box size")
)

// Write implements io.Writer.  It appends p to the internal buffer and emits
// as many complete top-level boxes as are available.
func (s *mp4Segmenter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		box, typ, ok, err := takeMP4Box(s.buf)
		if err != nil {
			return len(p), err
		}
		if !ok {
			break
		}
		n := len(box)
		if herr := s.handle(typ, box); herr != nil {
			return len(p), herr
		}
		s.buf = s.buf[n:]
	}
	// Reclaim the consumed prefix once the buffer drains (it empties between
	// fragments), so we don't pin the whole movie's worth of backing array.
	if len(s.buf) == 0 {
		s.buf = nil
	}
	return len(p), nil
}

// handle dispatches a single complete box.  box aliases the internal buffer
// and is copied before the buffer is mutated, so it is safe to retain.
func (s *mp4Segmenter) handle(typ string, box []byte) error {
	isStart := typ == "moof" || typ == "styp"

	if !s.initDone {
		if !isStart {
			// Pre-fragment box (ftyp, moov, free, …) → part of init.
			s.init = append(s.init, box...)
			return nil
		}
		// First fragment begins → flush the init section.
		s.initDone = true
		if s.onInit != nil {
			if err := s.onInit(s.init); err != nil {
				return err
			}
		}
		s.init = nil
		// fall through: this box opens the first segment
	}

	if isStart {
		if s.inSeg && len(s.seg) > 0 {
			// Previous fragment never saw its mdat (truncated). Drop the
			// partial rather than emit a corrupt segment.
			s.seg = nil
		}
		s.inSeg = true
		s.seg = append([]byte(nil), box...)
		return nil
	}

	if s.inSeg {
		s.seg = append(s.seg, box...)
		if typ == "mdat" {
			data := s.seg
			s.seg = nil
			s.inSeg = false
			if s.onSegment != nil {
				return s.onSegment(data)
			}
		}
	}
	// Boxes outside a segment (trailing free/mfra at EOS, etc.) are ignored.
	return nil
}

// takeMP4Box returns the first complete top-level ISOBMFF box in buf.  ok is
// false when more bytes are needed; err is non-nil on a malformed size.  The
// returned slice aliases buf — copy before mutating buf.
func takeMP4Box(buf []byte) (box []byte, typ string, ok bool, err error) {
	if len(buf) < 8 {
		return nil, "", false, nil
	}
	size := binary.BigEndian.Uint32(buf[0:4])
	typ = string(buf[4:8])

	var boxLen uint64
	switch size {
	case 1:
		// 64-bit largesize in the 8 bytes following the type.
		if len(buf) < 16 {
			return nil, "", false, nil
		}
		boxLen = binary.BigEndian.Uint64(buf[8:16])
		if boxLen < 16 {
			return nil, "", false, errMP4Corrupt
		}
	case 0:
		// Box runs to EOF — undeterminable mid-stream. ffmpeg's fragmented
		// output always writes explicit sizes, so wait for more bytes.
		return nil, "", false, nil
	default:
		boxLen = uint64(size)
		if boxLen < 8 {
			return nil, "", false, errMP4Corrupt
		}
	}

	if uint64(len(buf)) < boxLen {
		return nil, "", false, nil // need more
	}
	return buf[:boxLen], typ, true, nil
}
