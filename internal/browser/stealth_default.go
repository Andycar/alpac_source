//go:build !no_rod
// +build !no_rod

package browser

import "github.com/go-rod/stealth"

// stealthJS returns the puppeteer-extra-plugin-stealth port shipped by
// github.com/go-rod/stealth. Same JS payload used by the rod engine,
// reused here so chromedp sessions can opt into fingerprint patches
// without depending on rod at runtime.
func stealthJS() string { return stealth.JS }
