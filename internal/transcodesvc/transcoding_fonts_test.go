package transcodesvc

import "testing"

func TestFontAttachmentsFromProbe(t *testing.T) {
	probe := map[string]any{"streams": []any{
		map[string]any{"index": float64(3), "codec_type": "attachment",
			"tags": map[string]any{"filename": "NotoSansJP.ttf", "mimetype": "application/x-truetype-font"}},
		map[string]any{"index": float64(4), "codec_type": "attachment",
			"tags": map[string]any{"filename": "cover.jpg", "mimetype": "image/jpeg"}}, // cover art — skip
		map[string]any{"index": float64(5), "codec_type": "attachment",
			"tags": map[string]any{"filename": "Style.OTF"}}, // no mime, ext-based
		map[string]any{"index": float64(0), "codec_type": "video"},
	}}
	fonts := fontAttachmentsFromProbe(probe)
	if len(fonts) != 2 {
		t.Fatalf("expected 2 fonts, got %d: %+v", len(fonts), fonts)
	}
	if fonts[0].AbsIndex != 3 || fonts[0].Name != "NotoSansJP.ttf" {
		t.Errorf("font 0 mismatch: %+v", fonts[0])
	}
	if fonts[1].AbsIndex != 5 || fonts[1].Name != "Style.OTF" {
		t.Errorf("font 1 mismatch: %+v", fonts[1])
	}
}

func TestSanitizeFontFilename(t *testing.T) {
	cases := map[string]string{
		"NotoSans.ttf":       "NotoSans.ttf",
		"../../etc/passwd":   "passwd",
		"a/b\\c:d*e.woff2":   "c_d_e.woff2", // path + reserved chars stripped
		"..":                 "_font",
		"":                   "_font",
		"weird\x00null.woff": "weird_null.woff",
	}
	for in, want := range cases {
		if got := sanitizeFontFilename(in); got != want {
			t.Errorf("sanitizeFontFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFontMimeAllowed(t *testing.T) {
	if !fontMimeAllowed("font/woff2", "x") || !fontMimeAllowed("application/vnd.ms-opentype", "x") {
		t.Error("mime-based font detection failed")
	}
	if !fontMimeAllowed("", "arial.TTC") {
		t.Error("extension-based fallback failed")
	}
	if fontMimeAllowed("image/jpeg", "cover.jpg") {
		t.Error("cover art must not classify as font")
	}
}

func TestTrickplayPlan(t *testing.T) {
	// 2h movie → 100 thumbs at 72s.
	if c, i := trickplayPlan(7200); c != 100 || i != 72 {
		t.Errorf("7200s: got count=%d interval=%d, want 100/72", c, i)
	}
	// 10min episode → interval floors at 10s, 60 thumbs.
	if c, i := trickplayPlan(600); c != 60 || i != 10 {
		t.Errorf("600s: got count=%d interval=%d, want 60/10", c, i)
	}
	// Too short → nothing.
	if c, _ := trickplayPlan(8); c != 0 {
		t.Errorf("8s: got count=%d, want 0", c)
	}
	// 45min episode → 45min/100=27s < 10s? no: 2700/100=27s interval, 100 thumbs.
	if c, i := trickplayPlan(2700); c != 100 || i != 27 {
		t.Errorf("2700s: got count=%d interval=%d, want 100/27", c, i)
	}
}
