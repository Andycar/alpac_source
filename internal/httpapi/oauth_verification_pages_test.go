package httpapi

import (
	"strings"
	"testing"
)

// Google's OAuth verification rejects the app when the privacy policy omits any
// of these disclosures — the Aug 2026 review failed on exactly that ("does not
// specify any data protection mechanisms for sensitive data"). Each entry below
// is a requirement, not decoration: keep the page and the real behaviour in sync
// rather than deleting a case to make this pass.
func TestPrivacyPageKeepsGoogleRequiredDisclosures(t *testing.T) {
	page := renderPrivacyPage("Alpac TV")

	required := map[string]string{
		"scope named":            "youtube.readonly",
		"encryption in transit":  "HTTPS",
		"encryption at rest":     "AES-256-GCM",
		"access restriction":     "0600",
		"retention limit":        "пока подключение к YouTube активно",
		"deletion on unbind":     "/youtube_unbind",
		"deletion on revoke":     "myaccount.google.com/permissions",
		"no third-party sharing": "не продаём",
		"limited use":            "Limited Use requirements",
		"contact":                oauthVerificationContactEmail,
		"english summary":        "encrypted at rest",
	}
	for name, want := range required {
		if !strings.Contains(page, want) {
			t.Errorf("privacy page is missing the %s disclosure (looked for %q)", name, want)
		}
	}
}

// The reviewer visits both pages unauthenticated; the homepage must link to the
// policy, and neither page may sit behind the app's Telegram auth wall.
func TestAboutPageLinksPrivacyPolicy(t *testing.T) {
	page := renderAboutPage("Alpac TV")
	if !strings.Contains(page, `href="/privacy"`) {
		t.Error("about page does not link to /privacy")
	}
	if !strings.Contains(page, "youtube.readonly") {
		t.Error("about page does not state the requested scope")
	}
}
