//go:build no_rod
// +build no_rod

package browser

// stealthJS is a no-op when the binary is built with -tags no_rod —
// the rod and stealth packages are excluded from the build. Callers
// requesting UseStealth on the chromedp engine in that build get no
// stealth patches; they should provide their own InitScripts.
func stealthJS() string { return "" }
