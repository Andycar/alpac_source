package playbackstats

import "testing"

func TestAggregatesByCodecAndDecoderPresence(t *testing.T) {
	s := New(t.TempDir())
	// Same codec, but one box has a DTS decoder and the other doesn't — that
	// split IS the finding, so it must not collapse into one bucket.
	s.Add(Report{Outcome: OutcomeNoAudio, Source: "torrent", AudioMime: "audio/vnd.dts", AudioDecoder: false, Platform: "androidtv", Device: "Xiaomi MiBox"})
	s.Add(Report{Outcome: OutcomeNoAudio, Source: "torrent", AudioMime: "audio/vnd.dts", AudioDecoder: false, Platform: "androidtv", Device: "Xiaomi MiBox"})
	s.Add(Report{Outcome: OutcomeOK, Source: "torrent", AudioMime: "audio/vnd.dts", AudioDecoder: true, Platform: "androidtv", Device: "NVIDIA Shield"})

	rows := s.All(0)
	if len(rows) != 2 {
		t.Fatalf("want 2 buckets (decoder present vs not), got %d: %+v", len(rows), rows)
	}
	if rows[0].Count != 2 || rows[0].Outcome != OutcomeNoAudio {
		t.Fatalf("most frequent bucket should be the 2 failures, got %+v", rows[0])
	}
	if rows[0].Devices["xiaomi mibox"] != 2 {
		t.Fatalf("device breakdown lost: %+v", rows[0].Devices)
	}
}

func TestProblemsExcludesHealthyPlaybacks(t *testing.T) {
	s := New(t.TempDir())
	s.Add(Report{Outcome: OutcomeOK, Source: "balancer", AudioMime: "audio/mp4a-latm", AudioDecoder: true})
	s.Add(Report{Outcome: OutcomeUnsupported, Source: "torrent", AudioMime: "audio/true-hd", AudioDecoder: false})

	probs := s.Problems(0)
	if len(probs) != 1 || probs[0].Outcome != OutcomeUnsupported {
		t.Fatalf("Problems() must return only actionable rows, got %+v", probs)
	}
	sum := s.Summary()
	if sum["total"].(int) != 2 || sum["problems"].(int) != 1 {
		t.Fatalf("summary wrong: %+v", sum)
	}
}

// TestUnknownOutcomeCoerced: a newer client must not be able to invent unbounded
// key space in a store that lives in memory.
func TestUnknownOutcomeCoerced(t *testing.T) {
	s := New(t.TempDir())
	s.Add(Report{Outcome: "totally-new-thing", AudioMime: "audio/aac"})
	rows := s.All(0)
	if len(rows) != 1 || rows[0].Outcome != OutcomeOther {
		t.Fatalf("unknown outcome not coerced: %+v", rows)
	}
}

func TestNormalisationKeepsBucketsStable(t *testing.T) {
	s := New(t.TempDir())
	// Clients differ in casing/whitespace; the same codec must land in one bucket.
	s.Add(Report{Outcome: OutcomeOK, AudioMime: "Audio/AC3", Platform: "AndroidTV"})
	s.Add(Report{Outcome: OutcomeOK, AudioMime: " audio/ac3 ", Platform: "androidtv"})
	if rows := s.All(0); len(rows) != 1 || rows[0].Count != 2 {
		t.Fatalf("casing variants split the bucket: %+v", rows)
	}
}

func TestPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Add(Report{Outcome: OutcomeNoAudio, Source: "torrent", AudioMime: "audio/eac3"})
	s.Flush()

	again := New(dir)
	rows := again.All(0)
	if len(rows) != 1 || rows[0].Count != 1 || rows[0].AudioMime != "audio/eac3" {
		t.Fatalf("aggregate lost across restart: %+v", rows)
	}
}
