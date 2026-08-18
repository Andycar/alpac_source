package httpapi

import (
	"strings"
	"testing"
)

// TestDeviceLabelPrefersClientName: a native client's UA is a library string, so
// without an explicit label every phone and every box would be listed under the
// same meaningless name.
func TestDeviceLabelPrefersClientName(t *testing.T) {
	if got := deviceLabel("Xiaomi Mi Box S", "okhttp/4.12.0"); got != "Xiaomi Mi Box S" {
		t.Fatalf("client label ignored: %q", got)
	}
}

func TestDeviceLabelFallsBackToUA(t *testing.T) {
	if got := deviceLabel("   ", "Mozilla/5.0 (SMART-TV; Linux; Tizen 6.0)"); got != "Samsung TV" {
		t.Fatalf("want the UA-derived label, got %q", got)
	}
}

// TestDeviceLabelIsSanitised: the label is rendered inside the bot's HTML
// message, so markup and control characters must not survive the trip.
func TestDeviceLabelIsSanitised(t *testing.T) {
	got := deviceLabel("<b>pwn</b>\nTV", "okhttp/4.12.0")
	for _, bad := range []string{"<", ">", "&", "\n", ""} {
		if strings.Contains(got, bad) {
			t.Fatalf("label kept %q: %q", bad, got)
		}
	}
}

func TestDeviceLabelIsBounded(t *testing.T) {
	if got := deviceLabel(strings.Repeat("x", 200), "okhttp/4.12.0"); len(got) > 48 {
		t.Fatalf("label not bounded: %d chars", len(got))
	}
}

// TestDeviceLabelAllMarkupFallsBack: a label that sanitises down to nothing must
// not leave a blank row in the bot's device list.
func TestDeviceLabelAllMarkupFallsBack(t *testing.T) {
	if got := deviceLabel("<<>>", "Mozilla/5.0 (Web0S; Linux/SmartTV)"); got != "LG TV" {
		t.Fatalf("want the UA fallback, got %q", got)
	}
}
