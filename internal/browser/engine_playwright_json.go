//go:build playwright
// +build playwright

package browser

import "encoding/json"

// playwrightJSONMarshal / Unmarshal isolate the JSON dependency in
// playwright-only files so binaries built without -tags playwright
// don't pull encoding/json transitively from the engine layer.
//
// (encoding/json is in the stdlib so the saving is zero today, but
// keeping it isolated lets us swap to a faster encoder later without
// touching the chromedp / rod files.)
func playwrightJSONMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func playwrightJSONUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
