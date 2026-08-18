package subsync

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// End-to-end against a real ffmpeg: silencedetect's output format is the one
// thing here we do not control, and a unit test on a canned log would keep
// passing after ffmpeg changed its wording.
func TestDetectSpeechAgainstRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	// 20 seconds of tone, muted everywhere except 4–8s and 13–17s: two clearly
	// separated "dialogue" stretches.
	wav := filepath.Join(t.TempDir(), "speech.wav")
	gen := exec.Command(ffmpeg, "-hide_banner", "-nostats", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=20",
		"-af", "volume=enable='between(t,4,8)+between(t,13,17)':volume=1,volume=enable='not(between(t,4,8)+between(t,13,17))':volume=0",
		wav)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("could not synthesise test audio: %v\n%s", err, out)
	}

	track, err := DetectSpeech(context.Background(), ffmpeg, wav, 20*time.Second, DefaultStep)
	if err != nil {
		t.Fatalf("DetectSpeech: %v", err)
	}
	if len(track) != 200 {
		t.Fatalf("bins = %d, want 200", len(track))
	}

	speaks := func(sec float64) bool { return track[int(sec*10)] }
	for _, sec := range []float64{5, 6, 7, 14, 15, 16} {
		if !speaks(sec) {
			t.Errorf("t=%.0fs should be speech", sec)
		}
	}
	for _, sec := range []float64{1, 2, 10, 11, 19} {
		if speaks(sec) {
			t.Errorf("t=%.0fs should be silence", sec)
		}
	}
}

// The measurement that matters: a subtitle timed 2s late against real audio.
func TestEndToEndOffsetAgainstRealAudio(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	wav := filepath.Join(t.TempDir(), "dialogue.wav")
	gen := exec.Command(ffmpeg, "-hide_banner", "-nostats", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=60",
		"-af", "volume=enable='between(t,5,9)+between(t,20,24)+between(t,38,42)+between(t,50,55)':volume=1,"+
			"volume=enable='not(between(t,5,9)+between(t,20,24)+between(t,38,42)+between(t,50,55))':volume=0",
		wav)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("could not synthesise test audio: %v\n%s", err, out)
	}

	audio, err := DetectSpeech(context.Background(), ffmpeg, wav, 60*time.Second, DefaultStep)
	if err != nil {
		t.Fatalf("DetectSpeech: %v", err)
	}

	late := []Cue{}
	for _, r := range [][2]float64{{5, 9}, {20, 24}, {38, 42}, {50, 55}} {
		late = append(late, Cue{
			Start: time.Duration((r[0] + 2) * float64(time.Second)),
			End:   time.Duration((r[1] + 2) * float64(time.Second)),
		})
	}
	subs := TrackFromCues(late, 60*time.Second, DefaultStep)

	res := BestOffset(audio, subs, MaxShift, DefaultStep)
	if res.Offset < -2400*time.Millisecond || res.Offset > -1600*time.Millisecond {
		t.Errorf("offset = %v, want ≈ -2s", res.Offset)
	}
	if res.Confidence < MinConfidence {
		t.Errorf("confidence = %.2f — a clean synthetic match should be actionable", res.Confidence)
	}
}

func TestDetectSpeechRejectsMissingSource(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := DetectSpeech(context.Background(), "ffmpeg", filepath.Join(t.TempDir(), "nope.wav"), time.Minute, DefaultStep); err == nil {
		t.Error("a missing input must be an error, not an empty alignment")
	}
}
