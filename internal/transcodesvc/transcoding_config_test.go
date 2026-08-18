package transcodesvc

import (
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/transcode"
)

// TestDisableABRLadder_DisablesMultiRung verifies the operator config
// knob takes precedence over the planner — even on a 1080p source where
// the ladder would normally fire.
func TestDisableABRLadder_DisablesMultiRung(t *testing.T) {
	svc := &TranscodingService{
		cfg: config.Config{
			Transcoding: config.TranscodingConf{
				DisableABRLadder: true,
			},
		},
	}
	ctx := transcodingContext{
		Mode: transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	if got := svc.shouldMultiRung(ctx); got != nil {
		t.Errorf("DisableABRLadder=true must opt out of multi-rung, got %d rungs", len(got))
	}
}

// TestMaxLadderRungs_CapsLadder verifies operator-set cap is honoured
// when the source resolution would otherwise warrant more rungs.
func TestMaxLadderRungs_CapsLadder(t *testing.T) {
	svc := &TranscodingService{
		ffmpegMajorVersion: 5, // P3.R: multi-rung gate requires ffmpeg ≥ 5
		cfg: config.Config{
			Transcoding: config.TranscodingConf{
				MaxLadderRungs: 2,
			},
		},
	}
	ctx := transcodingContext{
		Mode: transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	got := svc.shouldMultiRung(ctx)
	if got == nil {
		t.Fatalf("1080p source should still be eligible (cap doesn't disqualify)")
	}
	if len(got) != 2 {
		t.Errorf("MaxLadderRungs=2 should cap 1080p ladder (3 rungs) to 2, got %d", len(got))
	}
	// Primary rung must survive the cap.
	if !got[0].Primary {
		t.Errorf("primary rung must be preserved when capping")
	}
}

// TestMaxLadderRungs_ZeroIsNoCap confirms 0 (the default) means "no cap"
// — back-compat for deployments that don't know about the new knob.
func TestMaxLadderRungs_ZeroIsNoCap(t *testing.T) {
	svc := &TranscodingService{
		ffmpegMajorVersion: 5,
		cfg: config.Config{
			Transcoding: config.TranscodingConf{
				MaxLadderRungs: 0,
			},
		},
	}
	ctx := transcodingContext{
		Mode: transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	got := svc.shouldMultiRung(ctx)
	if len(got) != 3 {
		t.Errorf("MaxLadderRungs=0 means no cap; expected full 3-rung ladder, got %d", len(got))
	}
}
