package transcode

import "testing"

func TestPlanABRLadder_StreamCopyModes_SingleRung(t *testing.T) {
	for _, mode := range []TranscodingMode{ModeNative, ModeDirect, ModeRemux, ModeAudioOnly} {
		ladder := PlanABRLadder(1920, 1080, mode)
		if len(ladder) != 1 {
			t.Errorf("mode %q: expected single rung (no extra encode), got %d rungs", mode, len(ladder))
		}
		if !ladder[0].Primary {
			t.Errorf("mode %q: lone rung must be Primary", mode)
		}
	}
}

func TestPlanABRLadder_4K_CapsAt1080p(t *testing.T) {
	// 4K HEVC input — must NOT include a 4K rung (no client plays 4K
	// reliably from HLS), primary must be 1080p downscale.
	ladder := PlanABRLadder(3840, 2160, ModeSWTranscode)
	if len(ladder) < 2 {
		t.Fatalf("4K source should have at least 1080p+720p ladder, got %d rungs", len(ladder))
	}
	if ladder[0].Height != 1080 {
		t.Errorf("4K primary should be 1080p, got %dp", ladder[0].Height)
	}
	for _, r := range ladder {
		if r.Height >= 2160 {
			t.Errorf("4K rung not allowed in HLS ladder (player compatibility), got %+v", r)
		}
	}
}

func TestPlanABRLadder_1440p_Downscales(t *testing.T) {
	ladder := PlanABRLadder(2560, 1440, ModeSWTranscode)
	if ladder[0].Height != 1080 {
		t.Errorf("1440p source primary should downscale to 1080p, got %dp", ladder[0].Height)
	}
}

func TestPlanABRLadder_1080p_FullLadder(t *testing.T) {
	ladder := PlanABRLadder(1920, 1080, ModeSWTranscode)
	if len(ladder) != 3 {
		t.Fatalf("1080p source should have 3 rungs, got %d", len(ladder))
	}
	expected := []int{1080, 720, 480}
	for i, r := range ladder {
		if r.Height != expected[i] {
			t.Errorf("rung %d: height = %d, want %d", i, r.Height, expected[i])
		}
	}
	if !ladder[0].Primary {
		t.Errorf("first rung must be Primary")
	}
	if ladder[1].Primary || ladder[2].Primary {
		t.Errorf("only first rung is Primary; got Primary on lower rungs")
	}
	// Bitrates must descend.
	if ladder[0].BitrateKbps <= ladder[1].BitrateKbps {
		t.Errorf("bitrates must descend: %d <= %d", ladder[0].BitrateKbps, ladder[1].BitrateKbps)
	}
	if ladder[1].BitrateKbps <= ladder[2].BitrateKbps {
		t.Errorf("bitrates must descend: %d <= %d", ladder[1].BitrateKbps, ladder[2].BitrateKbps)
	}
}

func TestPlanABRLadder_720p_TwoRungs(t *testing.T) {
	ladder := PlanABRLadder(1280, 720, ModeSWTranscode)
	if len(ladder) != 2 {
		t.Fatalf("720p source should have 2 rungs, got %d", len(ladder))
	}
	if ladder[0].Height != 720 {
		t.Errorf("primary should be 720p")
	}
	if ladder[1].Height != 480 {
		t.Errorf("second rung should be 480p")
	}
}

func TestPlanABRLadder_480p_NoLadder(t *testing.T) {
	ladder := PlanABRLadder(854, 480, ModeSWTranscode)
	if len(ladder) != 1 {
		t.Errorf("480p source: ladder not worth the encode cost — single rung, got %d", len(ladder))
	}
}

func TestPlanABRLadder_HWMode_SameLadderAsSW(t *testing.T) {
	// HW transcode picks the same ladder; only the encoder choice differs.
	swLadder := PlanABRLadder(1920, 1080, ModeSWTranscode)
	hwLadder := PlanABRLadder(1920, 1080, ModeHWTranscode)
	if len(swLadder) != len(hwLadder) {
		t.Errorf("HW vs SW ladder length differs: %d vs %d", len(hwLadder), len(swLadder))
	}
}

func TestPlanABRLadder_UnknownDimensions_PrimaryFallback(t *testing.T) {
	// Probe returned 0,0 — fall back to 1080p assumption.
	ladder := PlanABRLadder(0, 0, ModeRemux)
	if len(ladder) != 1 {
		t.Fatalf("unknown dims + copy mode → single rung, got %d", len(ladder))
	}
	if ladder[0].Height != 1080 {
		t.Errorf("fallback should assume 1080p, got %dp", ladder[0].Height)
	}
}
