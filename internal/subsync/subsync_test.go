package subsync

import (
	"math"
	"testing"
	"time"
)

const step = DefaultStep

// speech builds a track from second-ranges, the way a film's dialogue would sit.
func speech(total time.Duration, ranges ...[2]float64) Track {
	t := newTrack(total, step)
	for _, r := range ranges {
		fill(t, time.Duration(r[0]*float64(time.Second)), time.Duration(r[1]*float64(time.Second)), step, true)
	}
	return t
}

func cues(ranges ...[2]float64) []Cue {
	out := make([]Cue, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, Cue{
			Start: time.Duration(r[0] * float64(time.Second)),
			End:   time.Duration(r[1] * float64(time.Second)),
		})
	}
	return out
}

func TestParseSilenceLog(t *testing.T) {
	log := `
[silencedetect @ 0x7f] silence_start: 0
[silencedetect @ 0x7f] silence_end: 4.512 | silence_duration: 4.512
[silencedetect @ 0x7f] silence_start: 12.25
[silencedetect @ 0x7f] silence_end: 15.75 | silence_duration: 3.5
[silencedetect @ 0x7f] silence_start: 40
`
	got := ParseSilenceLog(log, 60*time.Second)
	if len(got) != 3 {
		t.Fatalf("silences = %+v", got)
	}
	if got[0].End != 4512*time.Millisecond {
		t.Errorf("first end = %v", got[0].End)
	}
	// The sample ended mid-silence: the dangling start must be closed at total,
	// otherwise the tail of the film reads as speech and skews the correlation.
	if got[2].Start != 40*time.Second || got[2].End != 60*time.Second {
		t.Errorf("dangling silence = %+v", got[2])
	}
}

func TestTrackFromSilenceInvertsIntoSpeech(t *testing.T) {
	tr := TrackFromSilence([]Silence{{0, 2 * time.Second}}, 5*time.Second, step)
	if len(tr) != 50 {
		t.Fatalf("bins = %d", len(tr))
	}
	if tr[0] || tr[19] {
		t.Error("silent bins marked as speech")
	}
	if !tr[20] || !tr[49] {
		t.Error("speech bins marked as silence")
	}
}

// The whole point: a subtitle that runs 2.5s late must be measured as -2.5s.
func TestBestOffsetFindsLateSubtitle(t *testing.T) {
	const total = 120 * time.Second
	dialogue := [][2]float64{{10, 13}, {20, 24}, {31, 33}, {48, 52}, {70, 73}, {95, 99}}
	audio := speech(total, dialogue...)

	late := make([][2]float64, len(dialogue))
	for i, d := range dialogue {
		late[i] = [2]float64{d[0] + 2.5, d[1] + 2.5}
	}
	subs := TrackFromCues(cues(late...), total, step)

	res := BestOffset(audio, subs, MaxShift, step)
	if math.Abs(res.Offset.Seconds()+2.5) > 0.2 {
		t.Errorf("offset = %v, want ≈ -2.5s", res.Offset)
	}
	if res.Confidence < MinConfidence {
		t.Errorf("confidence = %.2f, too low to act on a clean match", res.Confidence)
	}
}

func TestBestOffsetFindsEarlySubtitle(t *testing.T) {
	const total = 120 * time.Second
	dialogue := [][2]float64{{15, 18}, {30, 34}, {51, 53}, {68, 72}, {90, 93}}
	audio := speech(total, dialogue...)

	early := make([][2]float64, len(dialogue))
	for i, d := range dialogue {
		early[i] = [2]float64{d[0] - 4, d[1] - 4}
	}
	subs := TrackFromCues(cues(early...), total, step)

	res := BestOffset(audio, subs, MaxShift, step)
	if math.Abs(res.Offset.Seconds()-4) > 0.2 {
		t.Errorf("offset = %v, want ≈ +4s", res.Offset)
	}
}

func TestBestOffsetAlreadyAligned(t *testing.T) {
	const total = 120 * time.Second
	dialogue := [][2]float64{{10, 13}, {25, 29}, {44, 47}, {66, 70}, {88, 92}}
	audio := speech(total, dialogue...)
	subs := TrackFromCues(cues(dialogue...), total, step)

	res := BestOffset(audio, subs, MaxShift, step)
	if math.Abs(res.Offset.Seconds()) > 0.2 {
		t.Errorf("offset = %v, want ≈ 0", res.Offset)
	}
}

// Wall-to-wall dialogue correlates with everything, so every shift scores well.
// That is precisely when we must report low confidence instead of a number:
// shifting subtitles on a coin flip is worse than leaving them where they are.
func TestBestOffsetRefusesFeaturelessInput(t *testing.T) {
	const total = 120 * time.Second
	audio := speech(total, [2]float64{0, 120})
	subs := TrackFromCues(cues([2]float64{0, 120}), total, step)

	res := BestOffset(audio, subs, MaxShift, step)
	if res.Confidence >= MinConfidence {
		t.Errorf("confidence = %.2f on a featureless match — would shift on noise", res.Confidence)
	}
}

func TestBestOffsetEmptyInputs(t *testing.T) {
	if got := BestOffset(nil, nil, MaxShift, step); got.Offset != 0 || got.Confidence != 0 {
		t.Errorf("res = %+v", got)
	}
	total := 60 * time.Second
	if got := BestOffset(speech(total, [2]float64{1, 2}), TrackFromCues(nil, total, step), MaxShift, step); got.Confidence != 0 {
		t.Errorf("a subtitle with no cues cannot align: %+v", got)
	}
}

// A subtitle for a different film should not produce a confident answer.
func TestBestOffsetUnrelatedSubtitle(t *testing.T) {
	const total = 180 * time.Second
	audio := speech(total, [2]float64{5, 8}, [2]float64{40, 44}, [2]float64{95, 99}, [2]float64{150, 154})
	subs := TrackFromCues(cues([2]float64{12, 14}, [2]float64{63, 66}, [2]float64{120, 123}, [2]float64{170, 172}), total, step)

	res := BestOffset(audio, subs, MaxShift, step)
	if res.Confidence >= 0.8 {
		t.Errorf("unrelated subtitle matched with confidence %.2f (offset %v)", res.Confidence, res.Offset)
	}
}
