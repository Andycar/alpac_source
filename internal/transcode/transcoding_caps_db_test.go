package transcode

import (
	"strings"
	"testing"
)

// TestDetectClientCapsFromUA exercises the UA → profile mapping for the
// device families flagged by the research pass (4PDA, webos-forums,
// jellyfin-web #2262, plex 212143, yumata/lampa #85/#212).
//
// Each case asserts the user-visible behaviour, not the profile internals:
// "would smart-mode let this client play HEVC10 / EAC3 / DTS as-is?"  We
// don't pin the profile label here — that's deliberately tested separately
// so renaming labels doesn't cascade into capability assertions.
func TestDetectClientCapsFromUA(t *testing.T) {
	tests := []struct {
		name              string
		ua                string
		wantLabelContains string

		wantH264   bool
		wantHEVC   bool
		wantHEVC10 bool
		wantEAC3   bool
		wantDTS    bool
		wantMKV    bool
	}{
		// --- Empty / unknown ---------------------------------------------
		{
			name:              "empty UA → conservative unknown profile",
			ua:                "",
			wantLabelContains: "unknown",
			wantH264:          true,
		},
		{
			name:              "garbage UA → unknown profile",
			ua:                "asdf-zxcv-no-keywords",
			wantLabelContains: "unknown",
			wantH264:          true,
		},

		// --- Tizen by version --------------------------------------------
		{
			name:              "Tizen 3 (legacy 2017) → no HEVC, no EAC3, no DTS",
			ua:                "Mozilla/5.0 (SMART-TV; LINUX; Tizen 3.0) AppleWebKit/538.1",
			wantLabelContains: "tizen-legacy",
			wantH264:          true,
			wantMKV:           true,
		},
		{
			name:              "Tizen 4 (2018) → HEVC8 OK, no EAC3",
			ua:                "Mozilla/5.0 (SMART-TV; Tizen 4.0; Samsung) AppleWebKit/538.1",
			wantLabelContains: "tizen-4",
			wantH264:          true,
			wantHEVC:          true,
			wantMKV:           true,
		},
		{
			name:              "Tizen 5 (2019) → HEVC8 OK, no HEVC10, no EAC3",
			ua:                "Mozilla/5.0 (SMART-TV; LINUX; Tizen 5.5) AppleWebKit/538.1",
			wantLabelContains: "tizen-5",
			wantHEVC:          true,
			wantH264:          true,
			wantMKV:           true,
		},
		{
			name:              "Tizen 6+ (2021+) → HEVC10 + EAC3 OK",
			ua:                "Mozilla/5.0 (SMART-TV; LINUX; Tizen 6.5) AppleWebKit/538.1",
			wantLabelContains: "tizen-6",
			wantHEVC:          true,
			wantHEVC10:        true,
			wantEAC3:          true,
			wantH264:          true,
			wantMKV:           true,
		},

		// --- WebOS by version --------------------------------------------
		{
			name:              "WebOS 3 (legacy) → no HEVC",
			ua:                "Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/538 webOS/3.4.0",
			wantLabelContains: "webos-legacy",
			wantH264:          true,
			wantMKV:           true,
		},
		{
			name:              "WebOS 5 (2019) → HEVC OK, no EAC3",
			ua:                "Mozilla/5.0 (Linux; webOS 5.0; LG NetCast) AppleWebKit/538",
			wantLabelContains: "webos-5",
			wantHEVC:          true,
			wantH264:          true,
			wantMKV:           true,
		},
		{
			name:              "WebOS 6 (2021+) → HEVC10 + EAC3 OK",
			ua:                "Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/538 WebOS/6.0",
			wantLabelContains: "webos-6",
			wantH264:          true,
			wantHEVC:          true,
			wantHEVC10:        true,
			wantEAC3:          true,
			wantMKV:           true,
		},

		// --- Apple TV — flagship case ------------------------------------
		{
			name:              "Apple TV tvOS 15 → no EAC3 (jellyfin #2262 known bug)",
			ua:                "AppleTV6,2/15.5.1",
			wantLabelContains: "apple-tv-15",
			wantH264:          true,
			wantHEVC:          true,
		},
		{
			name:              "Apple TV tvOS 17+ → EAC3 fixed",
			ua:                "AppleTV11,1/17.4",
			wantLabelContains: "apple-tv-17",
			wantH264:          true,
			wantHEVC:          true,
			wantHEVC10:        true,
			wantEAC3:          true,
		},

		// --- iOS / iPadOS ------------------------------------------------
		{
			name:              "iOS 16 Safari → no EAC3",
			ua:                "Mozilla/5.0 (iPhone; CPU iPhone OS 16_4 like Mac OS X) AppleWebKit/605.1.15 Safari/604.1",
			wantLabelContains: "ios-16",
			wantH264:          true,
			wantHEVC:          true,
		},
		{
			name:              "iOS 17 Safari → EAC3 OK",
			ua:                "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 Safari/604.1",
			wantLabelContains: "ios-17",
			wantH264:          true,
			wantHEVC:          true,
			wantHEVC10:        true,
			wantEAC3:          true,
		},

		// --- Browsers ----------------------------------------------------
		{
			name:              "Chrome desktop → H264+VP9, no HEVC, no EAC3",
			ua:                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0",
			wantLabelContains: "browser-chrome",
			wantH264:          true,
		},
		{
			name:              "Firefox desktop → H264 only",
			ua:                "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0",
			wantLabelContains: "browser-firefox",
			wantH264:          true,
		},
		{
			name:              "macOS Safari desktop → HEVC + EAC3 OK",
			ua:                "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/17.0 Safari/605.1.15",
			wantLabelContains: "safari-mac",
			wantH264:          true,
			wantHEVC:          true,
			wantHEVC10:        true,
			wantEAC3:          true,
		},

		// --- Android TV --------------------------------------------------
		{
			name:              "Android TV box (X96) → wide codec support",
			ua:                "Mozilla/5.0 (Linux; Android 9; X96 mini) AppleWebKit/537.36",
			wantLabelContains: "android-tv",
			wantH264:          true,
			wantHEVC:          true,
			wantEAC3:          true,
			wantMKV:           true,
		},
		{
			name:              "Mi Box → android-tv",
			ua:                "Mozilla/5.0 (Linux; Android 11; MiBox4) AppleWebKit/537.36",
			wantLabelContains: "android-tv",
			wantH264:          true,
			wantHEVC:          true,
			wantEAC3:          true,
			wantMKV:           true,
		},
		{
			name:              "Amazon Fire TV stick → android-tv",
			ua:                "Mozilla/5.0 (Linux; Android 9; AFTN Build/PS7233.4136N)",
			wantLabelContains: "android-tv",
			wantH264:          true,
			wantHEVC:          true,
			wantEAC3:          true,
			wantMKV:           true,
		},

		// --- Chromecast / Lampa --------------------------------------------
		{
			name:              "Chromecast → wide support",
			ua:                "Mozilla/5.0 CrKey/1.56.500000",
			wantLabelContains: "chromecast",
			wantH264:          true,
			wantHEVC:          true,
			wantEAC3:          true,
			wantMKV:           true,
		},
		{
			name:              "Lampa native app → trusted exoplayer caps",
			ua:                "Lampa/1.0 (Android 11)",
			wantLabelContains: "lampa-native",
			wantH264:          true,
			wantHEVC:          true,
			wantHEVC10:        true,
			wantEAC3:          true,
			wantMKV:           true,
		},

		// --- CLI / probe -------------------------------------------------
		{
			name:              "ffmpeg UA → cli-probe profile",
			ua:                "Lavf/58.76.100",
			wantLabelContains: "unknown",
			wantH264:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectClientCapsFromUA(tt.ua)

			if !strings.Contains(got.Label, tt.wantLabelContains) {
				t.Errorf("label = %q, want substring %q", got.Label, tt.wantLabelContains)
			}
			if got.Caps.CanPlayH264 != tt.wantH264 {
				t.Errorf("CanPlayH264 = %v, want %v", got.Caps.CanPlayH264, tt.wantH264)
			}
			if got.Caps.CanPlayHEVC != tt.wantHEVC {
				t.Errorf("CanPlayHEVC = %v, want %v", got.Caps.CanPlayHEVC, tt.wantHEVC)
			}
			if got.Caps.CanPlayHEVC10 != tt.wantHEVC10 {
				t.Errorf("CanPlayHEVC10 = %v, want %v", got.Caps.CanPlayHEVC10, tt.wantHEVC10)
			}
			if got.Caps.CanPlayEAC3 != tt.wantEAC3 {
				t.Errorf("CanPlayEAC3 = %v, want %v", got.Caps.CanPlayEAC3, tt.wantEAC3)
			}
			if got.Caps.CanPlayDTS != tt.wantDTS {
				t.Errorf("CanPlayDTS = %v, want %v", got.Caps.CanPlayDTS, tt.wantDTS)
			}
			if got.Caps.CanPlayMKV != tt.wantMKV {
				t.Errorf("CanPlayMKV = %v, want %v", got.Caps.CanPlayMKV, tt.wantMKV)
			}
		})
	}
}

// TestModelOverride covers the per-model patches (P3.H) that ride on top
// of the OS-version DB and force-clear capabilities for known-buggy TVs.
func TestModelOverride_SamsungT24N390(t *testing.T) {
	ua := "Mozilla/5.0 (SMART-TV; Tizen 4.0; T24N390SI) AppleWebKit/538"
	p := detectClientCapsFromUA(ua)
	if !strings.Contains(p.Label, "t24n390") {
		t.Errorf("expected T24N390 override label, got %q", p.Label)
	}
	if p.Caps.CanPlayHEVC || p.Caps.CanPlayHEVC10 {
		t.Errorf("T24N390: HEVC must be disabled (black screen workaround), got caps %+v", p.Caps)
	}
}

func TestModelOverride_TCLLongPlayback(t *testing.T) {
	ua := "Mozilla/5.0 (Linux; Android 11; TCL 50C655) AppleWebKit/537.36"
	p := detectClientCapsFromUA(ua)
	if !strings.Contains(p.Label, "c655") {
		t.Errorf("expected TCL C655 override label, got %q", p.Label)
	}
	if len(p.KnownIssues) == 0 {
		t.Errorf("TCL override should carry known-issue note")
	}
}

func TestModelOverride_X96HEVC10Capped(t *testing.T) {
	ua := "Mozilla/5.0 (Linux; Android 9; X96 mini) AppleWebKit/537.36"
	p := detectClientCapsFromUA(ua)
	if !strings.Contains(p.Label, "x96") {
		t.Errorf("expected X96 label, got %q", p.Label)
	}
	if p.Caps.CanPlayHEVC10 {
		t.Errorf("X96 override should disable HEVC10, got %+v", p.Caps)
	}
	if !p.Caps.CanPlayHEVC {
		t.Errorf("X96 override should keep HEVC8, got %+v", p.Caps)
	}
}

func TestModelOverride_HDBoxNoDTS(t *testing.T) {
	ua := "Mozilla/5.0 (Linux; Android 9; HD BOX 2.0) AppleWebKit/537.36"
	p := detectClientCapsFromUA(ua)
	if p.Caps.CanPlayDTS {
		t.Errorf("HD BOX override should disable DTS, got %+v", p.Caps)
	}
}

// TestEnrichCapsFromUA confirms that explicit ClientCaps from the request
// always win over UA detection — we only fill blanks.
func TestEnrichCapsFromUA(t *testing.T) {
	t.Run("nil client → UA caps win", func(t *testing.T) {
		caps, label, _ := EnrichCapsFromUA(nil, "Mozilla/5.0 (SMART-TV; Tizen 6.5)")
		if !caps.CanPlayHEVC10 {
			t.Errorf("expected Tizen 6 caps to allow HEVC10, got %+v", caps)
		}
		if !strings.Contains(label, "tizen") {
			t.Errorf("expected tizen label, got %q", label)
		}
	})

	t.Run("explicit caps win over UA — even when UA disagrees", func(t *testing.T) {
		// User-supplied caps say "no HEVC", UA suggests Tizen 6 (HEVC OK).
		// Explicit must win.
		explicit := &ClientCaps{
			Lang:        "ru",
			CanPlayH264: true,
			CanPlayHEVC: false, // explicitly disabled
		}
		caps, _, _ := EnrichCapsFromUA(explicit, "Mozilla/5.0 (SMART-TV; Tizen 6.5)")
		if caps.CanPlayHEVC {
			t.Errorf("explicit caps should win — expected HEVC false, got %+v", caps)
		}
	})

	t.Run("explicit caps with empty Lang/Platform borrows from UA", func(t *testing.T) {
		explicit := &ClientCaps{CanPlayH264: true} // no Lang, no Platform
		caps, _, _ := EnrichCapsFromUA(explicit, "Mozilla/5.0 (Web0S; webOS 5.0)")
		if caps.Lang == "" {
			t.Errorf("expected Lang to be borrowed from UA profile, got empty")
		}
		if caps.Platform == "" {
			t.Errorf("expected Platform to be borrowed from UA profile, got empty")
		}
	})

	t.Run("Apple TV pre-17 — known issues populated", func(t *testing.T) {
		_, label, issues := EnrichCapsFromUA(nil, "AppleTV6,2/15.5.1")
		if !strings.Contains(label, "apple-tv-15") {
			t.Errorf("label = %q, want apple-tv-15", label)
		}
		if len(issues) == 0 {
			t.Errorf("expected known issues for tvOS<17 EAC3 bug, got none")
		}
		joined := strings.Join(issues, " ")
		if !strings.Contains(strings.ToLower(joined), "eac3") {
			t.Errorf("expected EAC3 mention in known issues, got %q", joined)
		}
	})
}
