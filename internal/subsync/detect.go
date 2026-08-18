package subsync

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"time"
)

// SampleDuration is how much of the stream we listen to. Long enough for the
// correlation to have something to work with, short enough that one ffmpeg run
// stays cheap — and the offset we are looking for is constant, so the opening
// minutes answer it as well as the whole film would.
const SampleDuration = 12 * time.Minute

// MaxShift bounds the search. Beyond a minute the "subtitle" is not a late
// version of the same audio, it is a different release, and a match at that
// distance is more likely to be noise than an answer.
const MaxShift = 60 * time.Second

// noiseFloor / minSilence are silencedetect's thresholds. -30dB with a quarter
// second minimum tracks dialogue without chopping on breaths, and without
// calling quiet music "silence".
const (
	noiseFloor = "-30dB"
	minSilence = "0.25"
)

// DetectSpeech runs ffmpeg over the first SampleDuration of src and returns the
// speech track. Video is discarded (-vn): decoding pictures we never look at is
// the expensive half of the job.
func DetectSpeech(ctx context.Context, ffmpegPath, src string, sample, step time.Duration) (Track, error) {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if src == "" {
		return nil, errors.New("subsync: empty source")
	}
	if sample <= 0 {
		sample = SampleDuration
	}
	if step <= 0 {
		step = DefaultStep
	}

	args := []string{
		"-hide_banner", "-nostats",
		"-t", strconv.FormatFloat(sample.Seconds(), 'f', 0, 64),
		"-i", src,
		"-vn",
		"-af", "silencedetect=noise=" + noiseFloor + ":d=" + minSilence,
		"-f", "null", "-",
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// A non-zero exit is not automatically fatal: ffmpeg returns an error when
	// the input ends early or a stray stream fails, while still having reported
	// every silence up to that point. Judge by what it printed, not by the code.
	runErr := cmd.Run()
	log := stderr.String()
	silences := ParseSilenceLog(log, sample)
	if len(silences) == 0 {
		if runErr != nil {
			return nil, errors.New("subsync: ffmpeg failed: " + runErr.Error())
		}
		// No silence at all in twelve minutes means either a broken decode or an
		// unbroken wall of sound; either way there is no signal to align on.
		return nil, errors.New("subsync: no silence detected — nothing to align on")
	}
	return TrackFromSilence(silences, sample, step), nil
}
