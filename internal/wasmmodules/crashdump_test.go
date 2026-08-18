package wasmmodules

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCrashStoreSaveAndList(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wasm_dumps")
	s := newCrashStore(root)
	s.maxKeep = 3 // exercise pruning

	for i := 0; i < 5; i++ {
		_, err := s.Save(CrashDump{
			Module: "echo",
			Time:   time.Now().Add(time.Duration(i) * time.Millisecond),
			Error:  "boom",
		})
		if err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	got := s.List("echo", 10)
	if len(got) != 3 {
		t.Fatalf("expected pruning to keep 3, got %d", len(got))
	}
	// Newest first.
	if !got[0].Time.After(got[1].Time) {
		t.Fatalf("expected newest first")
	}
}

func TestSanitizeModuleID(t *testing.T) {
	cases := map[string]string{
		"foo":          "foo",
		"a/b":          "a_b",
		"..":           "_",
		"./../etc":     ".___etc",
		"normal_name1": "normal_name1",
	}
	for in, want := range cases {
		if got := sanitizeModuleID(in); got != want {
			t.Errorf("sanitize(%q)=%q, want %q", in, got, want)
		}
	}
}
