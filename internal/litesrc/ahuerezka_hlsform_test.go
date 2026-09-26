package litesrc

import (
	"strings"
	"testing"
)

// ahueWorkerPair is the shape the hdbase worker actually returns for one
// quality: the HLS spelling first, then " or ", then the same file as bare .mp4.
const ahueWorkerPair = "[360p]https://stream.voidboost.one/da43315f:2026072809:aUw1WTk=/1/4/2/0/7/2/5xchf.mp4:hls:manifest.m3u8 or " +
	"https://stream.voidboost.one/da43315f:2026072809:aUw1WTk=/1/4/2/0/7/2/5xchf.mp4"

// TestAhueRezkaPickerPrefersHLSSpelling documents WHY the flag has to be able to
// strip: the shared picker scores .m3u8 above .mp4, so the 404-ing spelling is
// what comes out of extraction. If this ever flips, the strip below is harmless.
func TestAhueRezkaPickerPrefersHLSSpelling(t *testing.T) {
	got := pidorezkaExtractQualityURL(ahueWorkerPair, "360p")
	if !strings.HasSuffix(got, ":hls:manifest.m3u8") {
		t.Skipf("picker no longer prefers the HLS spelling (got %q) — strip is now a no-op", got)
	}
}

// TestAhueRezkaHLSFormOffStripsSuffix: hls=false must remove an UPSTREAM suffix,
// not just skip appending one. The old code only ever appended, so `hls = false`
// silently did nothing and every stream 404'd on voidboost.
func TestAhueRezkaHLSFormOffStripsSuffix(t *testing.T) {
	in := "https://stream.voidboost.one/x:2026072809:y=/1/4/2/0/7/2/5xchf.mp4:hls:manifest.m3u8"
	got := ahueRezkaApplyHLSForm(in, false)
	want := "https://stream.voidboost.one/x:2026072809:y=/1/4/2/0/7/2/5xchf.mp4"
	if got != want {
		t.Fatalf("hls=false left the 404-ing suffix:\n got %q\nwant %q", got, want)
	}
}

// TestAhueRezkaHLSFormOffLeavesBareMP4: nothing to strip, nothing to change.
func TestAhueRezkaHLSFormOffLeavesBareMP4(t *testing.T) {
	in := "https://stream.voidboost.one/x:2026072809:y=/1/4/2/0/7/2/5xchf.mp4"
	if got := ahueRezkaApplyHLSForm(in, false); got != in {
		t.Fatalf("bare .mp4 mutated: got %q", got)
	}
}

// TestAhueRezkaHLSFormOnAppendsOnce: on, the suffix is added to a bare URL and an
// already-HLS URL is left alone (no double suffix).
func TestAhueRezkaHLSFormOnAppendsOnce(t *testing.T) {
	bare := "https://gateway.example/x/5xchf.mp4"
	if got, want := ahueRezkaApplyHLSForm(bare, true), bare+":hls:manifest.m3u8"; got != want {
		t.Fatalf("hls=true append:\n got %q\nwant %q", got, want)
	}
	already := bare + ":hls:manifest.m3u8"
	if got := ahueRezkaApplyHLSForm(already, true); got != already {
		t.Fatalf("hls=true double-appended: got %q", got)
	}
}

// TestAhueRezkaExtractionYieldsPlayableForm ties it together: worker pair in,
// bare .mp4 out (the form verified to answer 302 → 206 video/mp4).
func TestAhueRezkaExtractionYieldsPlayableForm(t *testing.T) {
	raw := pidorezkaExtractQualityURL(ahueWorkerPair, "360p")
	if raw == "" {
		t.Fatal("extraction returned nothing for 360p")
	}
	got := ahueRezkaApplyHLSForm(raw, false)
	if strings.Contains(got, ":hls:") {
		t.Fatalf("still carries the HLS spelling: %q", got)
	}
	if !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("expected a bare .mp4, got %q", got)
	}
}

// ahueWorkerPremiumOnly mimics a title whose only tagged tiers are premium: all
// three point at the same shared "buy premium" stub, which answers 404.
const ahueWorkerPremiumOnly = `[<span class="pjs-prem-quality">1080p Ultra<img src="https://static.hdrezka.ac/x.svg" alt=""></span>]https://stream.voidboost.one/h:2026072809:b=/1/4/4/4/3/4/3/rhtie.mp4:hls:manifest.m3u8 or https://stream.voidboost.one/h:2026072809:b=/1/4/4/4/3/4/3/rhtie.mp4,` +
	`[<span class="pjs-prem-quality">2K<img src="https://static.hdrezka.ac/x.svg" alt=""></span>]https://stream.voidboost.one/h:2026072809:b=/1/4/4/4/3/4/3/rhtie.mp4:hls:manifest.m3u8 or https://stream.voidboost.one/h:2026072809:b=/1/4/4/4/3/4/3/rhtie.mp4`

// TestAhueRezkaPremiumTiersAreRecognised: the tier keys the worker actually uses
// must all be treated as premium. 2K/4K arrive as altKey spellings of
// 1440p/2160p, so a gap here would let the dead stub become the default stream.
func TestAhueRezkaPremiumTiersAreRecognised(t *testing.T) {
	for _, key := range []string{"1080p Ultra", "1440p", "2160p"} {
		if !ahueRezkaIsPremiumTier(key) {
			t.Errorf("tier %q not recognised as premium", key)
		}
	}
	for _, key := range []string{"1080p", "720p", "480p", "360p"} {
		if ahueRezkaIsPremiumTier(key) {
			t.Errorf("real tier %q wrongly treated as premium", key)
		}
	}
}

// TestAhueRezkaPremiumOnlyTitleYieldsNothing: with premium off, a premium-only
// title must produce NO streams. The bare-URL fallback takes any URL in the
// payload, so without the premiumSkipped guard it re-served the dead stub —
// better to return nothing and let the client try another source.
func TestAhueRezkaPremiumOnlyTitleYieldsNothing(t *testing.T) {
	a := &ahueRezkaChecker{premium: false, hls: false}
	streams, qual, _ := a.extractStreams(nil, ahueWorkerPremiumOnly, "ahuerezka")
	if len(streams) != 0 || len(qual) != 0 {
		t.Fatalf("premium-only title leaked the stub: streams=%v qual=%v", streams, qual)
	}
}

// ahueWorkerFreeLadder is the free tier set as the worker actually spells it.
const ahueWorkerFreeLadder = `[360p]https://stream.voidboost.one/h:2026072810:b=/1/4/6/1/6/8/4/crej5.mp4 or https://stream.voidboost.one/h:2026072810:b=/1/4/6/1/6/8/4/crej5.mp4,` +
	`[480p]https://stream.voidboost.one/h:2026072810:b=/1/4/6/1/6/8/4/7585h.mp4 or https://stream.voidboost.one/h:2026072810:b=/1/4/6/1/6/8/4/7585h.mp4,` +
	`[720p]https://stream.voidboost.one/h:2026072810:b=/1/4/6/1/6/8/4/fk2fo.mp4 or https://stream.voidboost.one/h:2026072810:b=/1/4/6/1/6/8/4/fk2fo.mp4,` +
	`[1080p]https://stream.voidboost.one/h:2026072810:b=/1/4/6/1/6/8/4/n3es4.mp4 or https://stream.voidboost.one/h:2026072810:b=/1/4/6/1/6/8/4/n3es4.mp4`

// TestAhueRezkaLabelsMatchRealResolution: the worker inflates every label by one
// step (its "1080p" is a 1280x720 file — ffprobe'd on two unrelated titles), so
// the picker offered 1080p and played 720p. Labels must report what plays.
func TestAhueRezkaLabelsMatchRealResolution(t *testing.T) {
	a := &ahueRezkaChecker{premium: false, hls: false}
	streams, qual, _ := a.extractStreams(nil, ahueWorkerFreeLadder, "ahuerezka")
	if len(streams) != 4 {
		t.Fatalf("want 4 free tiers, got %d: %v", len(streams), streams)
	}

	// worker label -> file -> the label we must surface
	want := map[string]string{"240p": "crej5.mp4", "360p": "7585h.mp4", "480p": "fk2fo.mp4", "720p": "n3es4.mp4"}
	for label, file := range want {
		u, ok := qual[label]
		if !ok {
			t.Errorf("missing corrected label %q (have %v)", label, keysOf(qual))
			continue
		}
		if !strings.HasSuffix(u, file) {
			t.Errorf("label %q maps to %q, want the file %q", label, u, file)
		}
	}
	for _, inflated := range []string{"1080p", "2K", "4K", "2160p", "1440p"} {
		if _, ok := qual[inflated]; ok {
			t.Errorf("inflated label %q still offered", inflated)
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
