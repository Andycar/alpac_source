package playbackstats

import "testing"

func addN(s *Store, n int, r Report) {
	for range n {
		s.Add(r)
	}
}

// TestAdviceNeedsEnoughFailures: two failures is a bad file or a flaky mirror,
// not a hardware gap. Acting on that would blacklist a codec for everyone with
// the box.
func TestAdviceNeedsEnoughFailures(t *testing.T) {
	s := New(t.TempDir())
	addN(s, 2, Report{Outcome: OutcomeUnsupported, Platform: "androidtv",
		Device: "xiaomi mibox", AudioMime: "audio/eac3"})

	if got := s.AdviceFor("androidtv", "xiaomi mibox"); len(got.AvoidAudio) != 0 {
		t.Fatalf("acted on thin evidence: %v", got.AvoidAudio)
	}

	s.Add(Report{Outcome: OutcomeUnsupported, Platform: "androidtv",
		Device: "xiaomi mibox", AudioMime: "audio/eac3"})
	if got := s.AdviceFor("androidtv", "xiaomi mibox"); len(got.AvoidAudio) != 1 {
		t.Fatalf("want eac3 avoided after 3 failures, got %v", got.AvoidAudio)
	}
}

// TestOneSuccessClearsTheCodec is the guard that keeps this honest: if the codec
// ever played on this device, the failures are the file's fault, not the box's.
func TestOneSuccessClearsTheCodec(t *testing.T) {
	s := New(t.TempDir())
	addN(s, 10, Report{Outcome: OutcomeUnsupported, Platform: "androidtv",
		Device: "box", AudioMime: "audio/eac3"})
	s.Add(Report{Outcome: OutcomeOK, Platform: "androidtv",
		Device: "box", AudioMime: "audio/eac3"})

	if got := s.AdviceFor("androidtv", "box"); len(got.AvoidAudio) != 0 {
		t.Fatalf("a codec that demonstrably plays must not be blacklisted: %v", got.AvoidAudio)
	}
}

// TestAdviceIsPerDevice: a gap on a cheap box must not steer a Shield.
func TestAdviceIsPerDevice(t *testing.T) {
	s := New(t.TempDir())
	addN(s, 5, Report{Outcome: OutcomeUnsupported, Platform: "androidtv",
		Device: "cheap box", VideoMime: "video/av01"})

	if got := s.AdviceFor("androidtv", "nvidia shield"); len(got.AvoidVideo) != 0 {
		t.Fatalf("advice leaked to another device: %v", got.AvoidVideo)
	}
	if got := s.AdviceFor("androidtv", "cheap box"); len(got.AvoidVideo) != 1 {
		t.Fatalf("the failing device got no advice: %v", got)
	}
}

// TestUnknownDeviceGetsNothing: guessing for a device we have no data on would
// push people to the transcoder for no reason.
func TestUnknownDeviceGetsNothing(t *testing.T) {
	s := New(t.TempDir())
	addN(s, 9, Report{Outcome: OutcomeUnsupported, Platform: "androidtv",
		Device: "box", AudioMime: "audio/eac3"})

	got := s.AdviceFor("androidtv", "")
	if len(got.AvoidAudio) != 0 || len(got.AvoidVideo) != 0 || got.PreferTranscode {
		t.Fatalf("guessed for an unknown device: %+v", got)
	}
}

// TestTranscodeOnlyForVideo: an audio gap is fixable on the device (bundled
// software decoders, passthrough off) — re-encoding the picture because of a
// sound track would be a cannon at a fly.
func TestTranscodeOnlyForVideo(t *testing.T) {
	s := New(t.TempDir())
	addN(s, 5, Report{Outcome: OutcomeUnsupported, Platform: "androidtv",
		Device: "box", AudioMime: "audio/eac3"})
	if s.AdviceFor("androidtv", "box").PreferTranscode {
		t.Fatal("an audio-only gap must not force transcoding")
	}

	addN(s, 5, Report{Outcome: OutcomeUnsupported, Platform: "androidtv",
		Device: "box", VideoMime: "video/av01"})
	if !s.AdviceFor("androidtv", "box").PreferTranscode {
		t.Fatal("a video gap is exactly what the transcoder is for")
	}
}

// TestPlatformIsRespected: the same model string on another platform is another
// stack entirely.
func TestPlatformIsRespected(t *testing.T) {
	s := New(t.TempDir())
	addN(s, 5, Report{Outcome: OutcomeUnsupported, Platform: "web",
		Device: "box", AudioMime: "audio/eac3"})

	if got := s.AdviceFor("androidtv", "box"); len(got.AvoidAudio) != 0 {
		t.Fatalf("web failures steered the native client: %v", got.AvoidAudio)
	}
}

func TestDeviceLabelIsStable(t *testing.T) {
	if DeviceLabel("Xiaomi", "MiBox") != DeviceLabel("xiaomi", " mibox ") {
		t.Fatal("casing/whitespace split one device into two — evidence would be halved")
	}
}
