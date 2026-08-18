package httpapi

import (
	"os"
	"testing"
)

// TestDumpGateJS writes the generated auth-gate JS to a file for external
// syntax checking (node --check). Only runs when GATE_JS_DUMP is set.
func TestDumpGateJS(t *testing.T) {
	out := os.Getenv("GATE_JS_DUMP")
	if out == "" {
		t.Skip("GATE_JS_DUMP not set")
	}
	js := buildAuthGateJS("http://srv.example")
	if err := os.WriteFile(out, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
}
