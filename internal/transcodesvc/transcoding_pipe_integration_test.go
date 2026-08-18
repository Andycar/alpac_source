package transcodesvc

import (
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// TestPipeFFmpegIntegration drives a REAL ffmpeg through the exact pipe-mode
// output args and asserts the mp4Segmenter splits the live fragmented-MP4
// stream into a valid init + keyframe-started segments. Skips when ffmpeg is
// not installed. This is the end-to-end proof that the ffmpeg→segmenter chain
// (video copy + AC-3→AAC transcode, fMP4 over stdout) works on real output.
func TestPipeFFmpegIntegration(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed; skipping integration test")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")

	// Generate a 30s H.264 + AC-3 source (the realistic "RU dub in AC-3" case).
	// -g 48 → ~2s GOP at 24fps, so min_frag_duration=6s groups ~3 GOPs/segment.
	gen := exec.Command(ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=30:size=640x360:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=30",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-pix_fmt", "yuv420p",
		"-c:a", "ac3", "-ac", "2",
		src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("failed to generate test source: %v\n%s", err, out)
	}

	// The exact output flags pipeSession.buildArgs emits, reading the local file.
	pipe := exec.Command(ffmpeg, "-hide_banner", "-nostats", "-loglevel", "error",
		"-i", src,
		"-map", "0:v:0", "-map", "0:a:0",
		"-c:v", "copy",
		"-c:a", "aac", "-ac", "2", "-b:a", "192k",
		"-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1",
		"-f", "mp4",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof",
		"-min_frag_duration", "6000000",
		"pipe:1")

	stdout, err := pipe.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := pipe.Start(); err != nil {
		t.Fatalf("start ffmpeg pipe: %v", err)
	}

	var mu sync.Mutex
	var initData []byte
	var segs [][]byte
	seg := &mp4Segmenter{
		onInit: func(in []byte) error {
			mu.Lock()
			initData = append([]byte{}, in...)
			mu.Unlock()
			return nil
		},
		onSegment: func(data []byte) error {
			mu.Lock()
			segs = append(segs, append([]byte{}, data...))
			mu.Unlock()
			return nil
		},
	}
	if _, err := io.Copy(seg, stdout); err != nil {
		t.Fatalf("segmenter copy error: %v", err)
	}
	if err := pipe.Wait(); err != nil {
		t.Fatalf("ffmpeg pipe exited with error: %v", err)
	}

	// init must be present and begin with an ftyp box, and contain moov.
	if len(initData) < 8 {
		t.Fatalf("init too small: %d bytes", len(initData))
	}
	if typ := string(initData[4:8]); typ != "ftyp" {
		t.Fatalf("init first box = %q, want ftyp", typ)
	}
	if !containsBoxType(initData, "moov") {
		t.Fatalf("init does not contain a moov box")
	}

	// ~30s / 6s ≈ 5 segments; allow slack for keyframe snapping.
	if len(segs) < 3 || len(segs) > 9 {
		t.Fatalf("got %d segments, want ~5 (3..9)", len(segs))
	}

	// Every segment must START with a moof box (independent, seekable).
	for i, s := range segs {
		if len(s) < 8 {
			t.Fatalf("segment %d too small: %d bytes", i, len(s))
		}
		if typ := string(s[4:8]); typ != "moof" {
			t.Fatalf("segment %d first box = %q, want moof", i, typ)
		}
		if !containsBoxType(s, "mdat") {
			t.Fatalf("segment %d has no mdat box", i)
		}
	}

	t.Logf("ffmpeg→segmenter: init=%d bytes, %d segments", len(initData), len(segs))
}

// containsBoxType reports whether buf contains a top-level box of the given
// type (walks the box chain).
func containsBoxType(buf []byte, want string) bool {
	for len(buf) >= 8 {
		box, typ, ok, err := takeMP4Box(buf)
		if !ok || err != nil {
			return false
		}
		if typ == want {
			return true
		}
		buf = buf[len(box):]
	}
	return false
}
