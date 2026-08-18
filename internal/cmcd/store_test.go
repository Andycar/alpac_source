package cmcd

import (
	"testing"
)

const exoUA = "ExoPlayerLib/2.19"

func TestStoreFoldsSessionOnFlush(t *testing.T) {
	s := New(t.TempDir())

	// One session that stalled twice, one that played clean — on the same source.
	for i := range 5 {
		s.Add("filmix", exoUA, Report{
			Any: true, SessionID: "bad", ObjectType: ObjVideo,
			BufferLengthMS: 1200, ThroughputKbps: 900, EncodedBitrateKbps: 800,
			TopBitrateKbps: 6000, Starvation: i == 1 || i == 3,
		})
	}
	for range 5 {
		s.Add("filmix", exoUA, Report{
			Any: true, SessionID: "good", ObjectType: ObjVideo,
			BufferLengthMS: 30000, ThroughputKbps: 20000, EncodedBitrateKbps: 6000,
			TopBitrateKbps: 6000,
		})
	}

	// Nothing is aggregated while sessions are in flight.
	if rows := s.Rows(10); len(rows) != 0 {
		t.Fatalf("expected no folded rows before flush, got %d", len(rows))
	}
	if live := s.Live(10); len(live) != 2 {
		t.Fatalf("expected 2 live sessions, got %d", len(live))
	}

	s.Flush()

	rows := s.Rows(10)
	if len(rows) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(rows))
	}
	r := rows[0]
	if r.Plugin != "filmix" || r.Platform != "android" {
		t.Errorf("key = %q/%q", r.Plugin, r.Platform)
	}
	if r.Sessions != 2 || r.Rebuffered != 1 || r.Rebuffers != 2 {
		t.Errorf("sessions=%d rebuffered=%d rebuffers=%d", r.Sessions, r.Rebuffered, r.Rebuffers)
	}
	if r.RebufferRate != 0.5 {
		t.Errorf("rebuffer rate = %v, want 0.5", r.RebufferRate)
	}
	// The stalling session sat at 800 of 6000 kbps for all five requests.
	if r.LowQualityRate != 0.5 {
		t.Errorf("low quality rate = %v, want 0.5", r.LowQualityRate)
	}
	if r.LowBufferRate != 0.5 {
		t.Errorf("low buffer rate = %v, want 0.5", r.LowBufferRate)
	}
	if r.AvgThroughputKbps != (900*5+20000*5)/10 {
		t.Errorf("avg throughput = %d", r.AvgThroughputKbps)
	}
}

func TestStoreSeparatesPlatforms(t *testing.T) {
	s := New(t.TempDir())
	s.Add("alloha", exoUA, Report{Any: true, SessionID: "a", ObjectType: ObjVideo, Starvation: true})
	s.Add("alloha", "Mozilla/5.0 (SMART-TV; Linux; Tizen 6.0)", Report{Any: true, SessionID: "b", ObjectType: ObjVideo})
	s.Flush()

	rows := s.Rows(10)
	if len(rows) != 2 {
		t.Fatalf("expected android and tizen buckets, got %d", len(rows))
	}
	// Worst first: the stalling platform must sort to the top, that is the
	// whole point of the ordering.
	if rows[0].Platform != "android" || rows[0].RebufferRate != 1 {
		t.Errorf("first row = %+v", rows[0])
	}
}

func TestStorePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Add("videoseed", exoUA, Report{Any: true, SessionID: "x", ObjectType: ObjVideo, Starvation: true})
	s.Flush()

	s2 := New(dir)
	rows := s2.Rows(10)
	if len(rows) != 1 || rows[0].Plugin != "videoseed" || rows[0].Rebuffered != 1 {
		t.Fatalf("aggregate did not survive restart: %+v", rows)
	}

	summary := s2.Summary()
	if summary["sessions"] != 1 || summary["rebuffer_rate"] != 1.0 {
		t.Errorf("summary = %+v", summary)
	}
}

func TestStoreIgnoresEmptyReport(t *testing.T) {
	s := New(t.TempDir())
	s.Add("filmix", exoUA, Report{}) // Any=false — nothing was understood
	s.Flush()
	if rows := s.Rows(10); len(rows) != 0 {
		t.Errorf("empty report was recorded: %+v", rows)
	}
}

func TestStoreFollowsMidSessionFailover(t *testing.T) {
	// A playback that starts on one balancer and fails over to another must
	// attribute the stalls that follow to the balancer that caused them.
	s := New(t.TempDir())
	s.Add("alloha", exoUA, Report{Any: true, SessionID: "s", ObjectType: ObjVideo})
	s.Add("filmix", exoUA, Report{Any: true, SessionID: "s", ObjectType: ObjVideo, Starvation: true})
	s.Flush()

	rows := s.Rows(10)
	if len(rows) != 1 || rows[0].Plugin != "filmix" {
		t.Fatalf("expected the session to land on filmix, got %+v", rows)
	}
}
