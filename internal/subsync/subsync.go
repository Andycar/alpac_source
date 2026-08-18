// Package subsync aligns an external subtitle file to the actual audio of the
// stream being watched.
//
// Downloaded subtitles are timed against SOME release, and the release we play
// is usually a different one: another cut, another framerate conversion, an
// extra distributor logo at the head. The result is the complaint we can never
// answer — "субтитры уехали на пару секунд" — because nothing on our side is
// wrong; the two files simply never agreed.
//
// The fix does not need speech recognition. Both sides carry the same signal in
// coarse form: WHEN somebody is talking. ffmpeg's silencedetect gives that for
// the audio, the cue timings give it for the subtitle, and the offset between
// them is the lag of one binary track against the other — a cross-correlation
// over a few thousand bins, which is milliseconds of CPU.
//
// What it deliberately does NOT do: variable drift (framerate mismatch stretches
// the timeline rather than shifting it). That needs a two-point fit and is a
// different feature; a constant offset covers the overwhelming majority.
package subsync

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultStep is the resolution of the speech tracks. 100ms is well under the
// smallest offset a viewer notices (~150ms) and keeps a 20-minute sample at a
// few thousand bins.
const DefaultStep = 100 * time.Millisecond

// Track marks, per bin, whether somebody is speaking.
type Track []bool

// Cue is one subtitle interval.
type Cue struct {
	Start time.Duration
	End   time.Duration
}

// Silence is one interval ffmpeg reported as silent.
type Silence struct {
	Start time.Duration
	End   time.Duration
}

var (
	reSilenceStart = regexp.MustCompile(`silence_start:\s*(-?[0-9.]+)`)
	reSilenceEnd   = regexp.MustCompile(`silence_end:\s*(-?[0-9.]+)`)
)

// ParseSilenceLog reads ffmpeg's silencedetect output. A trailing silence_start
// with no matching end (the sample ended mid-silence) is closed at total.
func ParseSilenceLog(stderr string, total time.Duration) []Silence {
	var out []Silence
	var open = time.Duration(-1)

	for _, line := range strings.Split(stderr, "\n") {
		if m := reSilenceStart.FindStringSubmatch(line); m != nil {
			if d, ok := parseSeconds(m[1]); ok {
				open = d
			}
			continue
		}
		if m := reSilenceEnd.FindStringSubmatch(line); m != nil {
			d, ok := parseSeconds(m[1])
			if !ok || open < 0 {
				continue
			}
			if d > open {
				out = append(out, Silence{Start: open, End: d})
			}
			open = -1
		}
	}
	if open >= 0 && total > open {
		out = append(out, Silence{Start: open, End: total})
	}
	return out
}

func parseSeconds(s string) (time.Duration, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return time.Duration(f * float64(time.Second)), true
}

// TrackFromSilence inverts silence into speech.
func TrackFromSilence(silences []Silence, total, step time.Duration) Track {
	t := newTrack(total, step)
	for i := range t {
		t[i] = true // speech unless a silence covers this bin
	}
	for _, s := range silences {
		fill(t, s.Start, s.End, step, false)
	}
	return t
}

// TrackFromCues marks the bins covered by subtitle cues.
func TrackFromCues(cues []Cue, total, step time.Duration) Track {
	t := newTrack(total, step)
	for _, c := range cues {
		fill(t, c.Start, c.End, step, true)
	}
	return t
}

func newTrack(total, step time.Duration) Track {
	if step <= 0 {
		step = DefaultStep
	}
	n := int(total / step)
	if n < 1 {
		n = 1
	}
	return make(Track, n)
}

func fill(t Track, from, to, step time.Duration, v bool) {
	if step <= 0 || to <= from {
		return
	}
	lo := int(from / step)
	hi := int(to / step)
	if lo < 0 {
		lo = 0
	}
	if hi > len(t) {
		hi = len(t)
	}
	for i := lo; i < hi; i++ {
		t[i] = v
	}
}

// Result is what an alignment attempt concluded. Not a wire type — the HTTP
// layer renders the offset in milliseconds, which is what clients think in.
type Result struct {
	// Offset to ADD to every cue so the subtitle matches the audio. Negative
	// means the subtitle currently runs late.
	Offset time.Duration
	// Confidence in 0..1: how far the winning alignment stands out from the
	// field. A high score at a flat correlation curve means everything matched
	// equally well, which means nothing matched.
	Confidence float64
	// Score is the raw cosine similarity of the two tracks at Offset.
	Score float64
}

// MinConfidence is the bar below which an offset should be ignored rather than
// applied. Shifting subtitles the wrong way is worse than leaving them alone:
// a viewer can compensate for a known constant lag, but not for our guess.
const MinConfidence = 0.35

// BestOffset finds the shift that best lines the subtitle track up with the
// speech track, searching ±maxShift.
func BestOffset(audio, subs Track, maxShift, step time.Duration) Result {
	if step <= 0 {
		step = DefaultStep
	}
	if len(audio) == 0 || len(subs) == 0 {
		return Result{}
	}
	maxBins := int(maxShift / step)
	if maxBins < 1 {
		maxBins = 1
	}

	scores := make([]float64, 0, 2*maxBins+1)
	best, bestShift := -1.0, 0
	for shift := -maxBins; shift <= maxBins; shift++ {
		s := cosine(audio, subs, shift)
		scores = append(scores, s)
		if s > best {
			best, bestShift = s, shift
		}
	}
	if best <= 0 {
		return Result{}
	}

	// Confidence = how far the peak rises above the typical alignment. A subtitle
	// that correlates with everything (dense dialogue over a talky film) produces
	// a high score at every shift, and that is exactly when we must not act.
	med := median(scores)
	conf := 0.0
	if best > med {
		conf = (best - med) / (1 - med + 1e-9)
	}
	return Result{
		Offset:     time.Duration(bestShift) * step,
		Confidence: clamp01(conf),
		Score:      best,
	}
}

// cosine is the similarity of the two binary tracks with subs shifted by
// `shift` bins. Positive shift = subtitle moved later.
func cosine(audio, subs Track, shift int) float64 {
	var inter, na, nb int
	for i := range audio {
		j := i - shift
		if j < 0 || j >= len(subs) {
			continue
		}
		if audio[i] {
			na++
		}
		if subs[j] {
			nb++
		}
		if audio[i] && subs[j] {
			inter++
		}
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float64(inter) / math.Sqrt(float64(na)*float64(nb))
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	cp := append([]float64(nil), v...)
	sort.Float64s(cp)
	return cp[len(cp)/2]
}

func clamp01(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}
