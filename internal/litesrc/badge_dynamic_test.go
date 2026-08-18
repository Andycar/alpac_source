package litesrc

import (
	"testing"
)

// Lift streams top out at 720p (sampled 2026-08-08 across six titles), while the
// static badge claimed FHD — the badge must follow the real master playlist.
func TestLiftHeightBadge(t *testing.T) {
	cases := map[int]string{
		2160: "4K",
		1440: "2K",
		1600: "2K",
		1080: "FHD",
		1000: "FHD",
		720:  "HD",
		530:  "SD", // lift really serves this one
		360:  "SD",
		0:    "",
	}
	for height, want := range cases {
		if got := liftHeightBadge(height); got != want {
			t.Errorf("liftHeightBadge(%d) = %q, want %q", height, got, want)
		}
	}
}

// Vibix publishes the catalogue quality as a free-form string on the same API
// call checksearch already makes.
func TestVibixQualityBadge(t *testing.T) {
	cases := map[string]string{
		"2160p":  "4K",
		"4K":     "4K",
		"UHD":    "4K",
		"1080p":  "FHD",
		"FullHD": "FHD",
		"1440p":  "2K",
		"QHD":    "2K",
		"2K":     "2K",
		"HD":     "HD",
		"720p":   "HD",
		"480p":   "SD",
		"":       "",
	}
	for label, want := range cases {
		if got := vibixQualityBadge(label); got != want {
			t.Errorf("vibixQualityBadge(%q) = %q, want %q", label, got, want)
		}
	}
}

// 2K is a first-class badge: it must survive normalization/sanitization and
// rank between 4K and FHD, otherwise a 1440p source would either be dropped to
// no badge at all or sorted as if it were 1080p.
func TestTwoKBadgeIsCanonical(t *testing.T) {
	for _, raw := range []string{"2K", "1440p", "1440", "QHD", "WQHD", "2560x1440 qhd"} {
		if got := normalizeQualityBadge(raw); got != "2K" {
			t.Errorf("normalizeQualityBadge(%q) = %q, want 2K", raw, got)
		}
		if got := sanitizeQualityBadge(raw); got != "2K" {
			t.Errorf("sanitizeQualityBadge(%q) = %q, want 2K", raw, got)
		}
	}
	if r4, r2, rf := qualityBadgeRank("4K"), qualityBadgeRank("2K"), qualityBadgeRank("FHD"); !(r4 > r2 && r2 > rf) {
		t.Fatalf("rank order broken: 4K=%d 2K=%d FHD=%d", r4, r2, rf)
	}
}

// megaoblako bakes the release into the card title, often listing several
// sources at once. The static badge for this source was "SD" — wrong for a site
// that mostly serves remuxes and 4K.
func TestRemuxQualityBadge(t *testing.T) {
	cases := map[string]string{
		"Веном: Последний танец / Venom (2024/4K/WEB-DL/WEB-DLRip)": "4K",
		"Веном 2 (2021) 4K HDR BD-Remux + Dolby Vision":             "4K",
		"Веном / Venom (2018) | UltraHD 4K 2160p":                   "4K",
		"Веном / Venom (2018/BD-Remux/BDRip/HDRip/3D)":              "FHD",
		"Фильм (2024/BDRip/HDRip)":                                  "FHD",
		"Фильм (2020/HDRip)":                                        "HD",
		"Фильм (2019/DVDRip)":                                       "SD",
		"Фильм (2019/1440p)":                                        "2K",
		"Фильм без меток (2019)":                                    "",
	}
	for title, want := range cases {
		if got := remuxQualityBadge(title); got != want {
			t.Errorf("remuxQualityBadge(%q) = %q, want %q", title, got, want)
		}
	}
}

// The stored linkid is "<public/id>?<filename>": the slash belongs to the id,
// the tail is the file name. Escaping the whole string (the old behaviour) made
// cloud.mail.ru 404 and playback silently returned an empty JSON object.
func TestRemuxPublicID(t *testing.T) {
	cases := map[string]string{
		"AqWs/7vzft5tmo?Venom.2018.D.BDRip.1080p_MegaOblako.com.mkv": "AqWs/7vzft5tmo",
		"AqWs/7vzft5tmo":           "AqWs/7vzft5tmo",
		"  H4sA/32UVEGvKj?x.mkv  ": "H4sA/32UVEGvKj",
		"/AqWs/7vzft5tmo/":         "AqWs/7vzft5tmo",
		"single":                   "single",
		"with space/id?file name":  "with%20space/id",
		"":                         "",
		"?onlyfile.mkv":            "",
	}
	for in, want := range cases {
		if got := remuxPublicID(in); got != want {
			t.Errorf("remuxPublicID(%q) = %q, want %q", in, got, want)
		}
	}
}
