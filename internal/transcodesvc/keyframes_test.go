package transcodesvc

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Фикстура testdata/kf10.mkv: 10 с, 25 fps, keyint 50 (ключевые кадры на 0,2,4,6,8 с),
// собрана ffmpeg (testsrc2 + sine, x264 ultrafast). kf10.mp4 — то же в MP4 (faststart).

func TestMKVKeyframesFromCues(t *testing.T) {
	f, err := os.Open("testdata/kf10.mkv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	km, err := mkvKeyframes(f, st.Size())
	if err != nil {
		t.Fatalf("mkvKeyframes: %v", err)
	}
	want := []float64{0, 2, 4, 6, 8}
	if len(km.Times) != len(want) {
		t.Fatalf("times = %v, want %v", km.Times, want)
	}
	for i, w := range want {
		if math.Abs(km.Times[i]-w) > 0.02 {
			t.Fatalf("time[%d] = %.3f, want %.3f (all: %v)", i, km.Times[i], w, km.Times)
		}
	}
	if math.Abs(km.Duration-10) > 0.2 {
		t.Fatalf("duration = %.3f, want ≈10", km.Duration)
	}
	if km.HeaderEnd <= 0 || km.HeaderEnd > 64*1024 {
		t.Fatalf("header end = %d (want small positive: EBML+Segment head before first cluster)", km.HeaderEnd)
	}
	prev := int64(-1)
	for i, off := range km.Offsets {
		if off <= prev || off < km.HeaderEnd {
			t.Fatalf("offset[%d] = %d not increasing / before header end %d (all: %v)", i, off, km.HeaderEnd, km.Offsets)
		}
		prev = off
	}
	// Смещение кластера должно указывать ровно на элемент Cluster (ID 1F 43 B7 75).
	buf := make([]byte, 4)
	if _, err := f.ReadAt(buf, km.Offsets[1]); err != nil {
		t.Fatal(err)
	}
	if buf[0] != 0x1F || buf[1] != 0x43 || buf[2] != 0xB6 || buf[3] != 0x75 {
		t.Fatalf("offset[1] does not point at a Cluster: % X", buf)
	}
}

// Границы, которые нарезает hlsenc с нашими флагами (проверено ffmpeg 9 на 60-с файле с
// GOP 2 с): с -hls_init_time 1 — каждый ключевой кадр отдельным сегментом (2 с); без него —
// сетка hls_time (6 с); GOP 10 с при hls_time 6 — по одному GOP (10 с).
func TestHLSGridMatchesFFmpeg(t *testing.T) {
	gop2 := &keyframeMap{Duration: 60}
	for s := 0.0; s < 60; s += 2 {
		gop2.Times = append(gop2.Times, s)
	}
	segs := hlsGrid(gop2, 6, 1, 60)
	if segs.count() != 30 {
		t.Fatalf("init_time=1: %d segments, want 30 (%v)", segs.count(), segs.Durs)
	}
	for i, d := range segs.Durs {
		if math.Abs(d-2) > 1e-9 {
			t.Fatalf("init_time=1: dur[%d] = %v, want 2", i, d)
		}
	}
	segs = hlsGrid(gop2, 6, 0, 60)
	if segs.count() != 10 {
		t.Fatalf("no init_time: %d segments, want 10 (%v)", segs.count(), segs.Durs)
	}
	for i, d := range segs.Durs {
		if math.Abs(d-6) > 1e-9 {
			t.Fatalf("no init_time: dur[%d] = %v, want 6", i, d)
		}
	}
	gop10 := &keyframeMap{Duration: 60}
	for s := 0.0; s < 60; s += 10 {
		gop10.Times = append(gop10.Times, s)
	}
	segs = hlsGrid(gop10, 6, 0, 60)
	if segs.count() != 6 || segs.Durs[0] != 10 {
		t.Fatalf("gop10: %v", segs.Durs)
	}
	// Хвост: последний сегмент — до конца файла; индекс по времени и экстраполяция.
	if segs.start(3) != 30 || segs.indexAt(31) != 3 || segs.indexAt(0) != 0 {
		t.Fatalf("start/indexAt: %v %v", segs.start(3), segs.indexAt(31))
	}
	if segs.start(7) != 70 { // за пределами карты — шаг последней длительности
		t.Fatalf("extrapolated start(7) = %v", segs.start(7))
	}
	// Первый ключевой кадр не в нуле: времена относительно него.
	late := &keyframeMap{Times: []float64{0.5, 2.5, 4.5}, Duration: 6.5}
	segs = hlsGrid(late, 6, 1, 6.5)
	if segs.Starts[0] != 0 || math.Abs(segs.Durs[0]-2) > 1e-9 || math.Abs(segs.Starts[2]-4) > 1e-9 {
		t.Fatalf("relative times: %v %v", segs.Starts, segs.Durs)
	}
}

func TestFFProbeKeyframesFallback(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	km, err := ffprobeKeyframes(context.Background(), "ffprobe", "testdata/kf10.mp4", "", "")
	if err != nil {
		t.Fatalf("ffprobeKeyframes: %v", err)
	}
	if len(km.Times) != 5 || math.Abs(km.Times[2]-4) > 0.05 {
		t.Fatalf("times = %v", km.Times)
	}
	if km.Duration < 9.5 || km.Duration > 10.5 {
		t.Fatalf("duration = %v", km.Duration)
	}
}

func TestExtractKeyframesPicksCuesForMKV(t *testing.T) {
	probe := map[string]any{"format": map[string]any{"format_name": "matroska,webm", "size": "398308"}}
	km, err := extractKeyframes(context.Background(), "testdata/kf10.mkv", "", "", probe, nil, "ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	if km.Source != "mkv-cues" || len(km.Times) != 5 {
		t.Fatalf("source=%s times=%v", km.Source, km.Times)
	}
	if fmtSeek(4.0004) != "4" || fmtSeek(4.0506) != "4.051" {
		t.Fatalf("fmtSeek: %s %s", fmtSeek(4.0004), fmtSeek(4.0506))
	}
}

// Захват превью по карте: голова + один кластер через stdin — из тестового MKV должен выйти JPEG.
func TestCueGrabberProducesThumb(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	f, err := os.Open("testdata/kf10.mkv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	km, err := mkvKeyframes(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, km.HeaderEnd)
	if _, err := f.ReadAt(header, 0); err != nil {
		t.Fatal(err)
	}
	g := &cueGrabber{km: km, ra: f, header: header}
	if i := g.nearest(5.2); i != 3 { // ключевые кадры 0,2,4,6,8: к 5.2 ближе 6 (индекс 3)
		t.Fatalf("nearest(5.2) = %d", i)
	}
	out := filepath.Join(t.TempDir(), "thumb.jpg")
	if err := g.grab(context.Background(), ffmpeg, 5.2, out); err != nil {
		t.Fatalf("grab: %v", err)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Size() < 500 {
		t.Fatalf("thumb missing/small: %v %v", err, fi)
	}
	if g.bytes <= 0 || g.bytes > int64(st.Size()) {
		t.Fatalf("bytes read = %d", g.bytes)
	}
}
