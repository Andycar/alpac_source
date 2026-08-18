package transcodesvc

import (
	"strings"
	"testing"
)

func TestIs10BitPixFmt(t *testing.T) {
	cases := map[string]bool{
		"":              false,
		"yuv420p":       false,
		"yuvj420p":      false,
		"nv12":          false,
		"yuv420p10le":   true,
		"yuv422p10le":   true,
		"yuv444p10le":   true,
		"p010le":        true,
		"p016le":        true,
		"yuv420p12le":   true,
		"YUV420P10LE":   true, // case-insensitive
		"unknown_codec": false,
	}
	for in, want := range cases {
		if got := is10BitPixFmt(in); got != want {
			t.Errorf("is10BitPixFmt(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBuildTonemapPrefilter_VAAPI(t *testing.T) {
	args := buildTonemapPrefilter(HWVAAPI)
	if len(args) < 2 || args[0] != "-vf" {
		t.Fatalf("expected -vf args, got %v", args)
	}
	chain := args[1]
	// VAAPI must terminate with hwupload so the HW encoder receives
	// GPU-mapped frames.
	if !strings.Contains(chain, "hwupload") {
		t.Errorf("VAAPI tonemap chain missing hwupload, got %q", chain)
	}
	if !strings.Contains(chain, "format=nv12") {
		t.Errorf("VAAPI tonemap chain should target nv12, got %q", chain)
	}
	if !strings.Contains(chain, "bt2020nc") || !strings.Contains(chain, "bt709") {
		t.Errorf("tonemap chain missing colour-space conversion, got %q", chain)
	}
}

func TestBuildTonemapPrefilter_NVENC_NoHwupload(t *testing.T) {
	// NVENC accepts CPU frames directly — no hwupload terminator (would
	// otherwise force VAAPI semantics).
	args := buildTonemapPrefilter(HWNVENC)
	chain := args[1]
	if strings.Contains(chain, "hwupload") {
		t.Errorf("NVENC tonemap chain should NOT include hwupload, got %q", chain)
	}
	if !strings.Contains(chain, "format=nv12") {
		t.Errorf("NVENC tonemap chain should target nv12, got %q", chain)
	}
}

func TestBuildTonemapPrefilter_VideoToolbox(t *testing.T) {
	args := buildTonemapPrefilter(HWVideoToolbox)
	chain := args[1]
	if strings.Contains(chain, "hwupload") {
		t.Errorf("VideoToolbox tonemap chain should NOT include hwupload, got %q", chain)
	}
}

func TestBuildTonemapPrefilter_SoftwareFallback(t *testing.T) {
	// Empty kind → SW path, terminates with yuv420p (libx264 input format).
	args := buildTonemapPrefilter("")
	chain := args[1]
	if strings.Contains(chain, "hwupload") {
		t.Errorf("SW tonemap chain must not include hwupload")
	}
	if !strings.Contains(chain, "format=yuv420p") {
		t.Errorf("SW tonemap chain should target yuv420p, got %q", chain)
	}
}

// With zscale+tonemap detected, the chain must be a REAL tonemap: linearize the
// PQ/HLG transfer, apply the operator, and land on BT.709 tv-range — not just a
// matrix swap (that produced the washed-out «блеклый HDR» after HDR→SDR encodes).
func TestBuildTonemapPrefilter_RealTonemapWhenSupported(t *testing.T) {
	old := tonemapFiltersHave
	defer func() { tonemapFiltersHave = old }()
	tonemapFiltersHave = tonemapFilters{ZScale: true, Tonemap: true}

	chain := buildTonemapPrefilter("")[1]
	for _, want := range []string{"zscale=t=linear", "tonemap=", "t=bt709", "r=tv", "format=yuv420p"} {
		if !strings.Contains(chain, want) {
			t.Errorf("real tonemap chain missing %q, got %q", want, chain)
		}
	}
	// VAAPI still terminates with hwupload after the real chain.
	if v := buildTonemapPrefilter(HWVAAPI)[1]; !strings.HasSuffix(v, ",hwupload") || !strings.Contains(v, "format=nv12") {
		t.Errorf("VAAPI real tonemap chain must end with nv12,hwupload, got %q", v)
	}

	// Legacy fallback (no zimg): matrix-only, but WITHOUT the old in_range=pc bug
	// (video is limited-range; pc crushed levels on top of the missing transfer fix).
	tonemapFiltersHave = tonemapFilters{}
	legacy := buildTonemapPrefilter("")[1]
	if strings.Contains(legacy, "in_range=pc") {
		t.Errorf("legacy chain must not force in_range=pc, got %q", legacy)
	}
	if !strings.Contains(legacy, "bt2020nc") {
		t.Errorf("legacy chain should keep the matrix conversion, got %q", legacy)
	}
}

func TestHWAccelInfo_TonemapEncoderArgs_NotActive(t *testing.T) {
	h := &HWAccelInfo{Kind: HWNone, Detected: false}
	if got := h.buildHWEncoderArgsForTonemappedInput(6000); got != nil {
		t.Errorf("expected nil when HW not active, got %v", got)
	}
}

func TestHWAccelInfo_TonemapEncoderArgs_PerBackend(t *testing.T) {
	cases := []struct {
		kind          HWKind
		wantEncoder   string
		wantSubstring []string
	}{
		{HWNVENC, "h264_nvenc", []string{"-color_primaries", "bt709", "yuv420p"}},
		{HWQSV, "h264_qsv", []string{"bt709"}},
		{HWVAAPI, "h264_vaapi", []string{"bt709"}},
		{HWVideoToolbox, "h264_videotoolbox", []string{"yuv420p", "bt709"}},
		{HWRKMPP, "h264_rkmpp", []string{"yuv420p", "bt709"}},
		{HWV4L2M2M, "h264_v4l2m2m", []string{"yuv420p", "bt709"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			h := &HWAccelInfo{Kind: tc.kind, Detected: true}
			got := h.buildHWEncoderArgsForTonemappedInput(6000)
			if len(got) == 0 {
				t.Fatalf("got empty args")
			}
			joined := strings.Join(got, " ")
			if !strings.Contains(joined, tc.wantEncoder) {
				t.Errorf("missing encoder %q in %q", tc.wantEncoder, joined)
			}
			for _, sub := range tc.wantSubstring {
				if !strings.Contains(joined, sub) {
					t.Errorf("missing %q in %q", sub, joined)
				}
			}
			// VAAPI must NOT re-emit the format/hwupload chain that the
			// prefilter already produced — that would double-upload.
			if tc.kind == HWVAAPI && strings.Contains(joined, "format=nv12|vaapi") {
				t.Errorf("VAAPI encoder block should NOT re-emit format chain, got %q", joined)
			}
		})
	}
}
