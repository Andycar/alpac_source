package httpapi

import (
	"strings"
	"testing"
)

func TestMergeJSONArrays(t *testing.T) {
	a := []byte(`[{"hash":"a"},{"hash":"b"}]`)
	b := []byte(`[{"hash":"c"}]`)
	out := mergeJSONArrays([][]byte{a, nil, b})
	var arr []map[string]string
	if err := json.Unmarshal(out, &arr); err != nil {
		t.Fatalf("invalid merged json: %v (%s)", err, out)
	}
	if len(arr) != 3 {
		t.Fatalf("want 3 merged entries, got %d: %s", len(arr), out)
	}
	if string(mergeJSONArrays([][]byte{nil, []byte("not json")})) != "[]" {
		t.Fatal("garbage parts should merge to []")
	}
	if string(mergeJSONArrays(nil)) != "[]" {
		t.Fatal("nil should merge to []")
	}
}

func TestMergeM3U(t *testing.T) {
	p1 := []byte("#EXTM3U\n#EXTINF:-1,Movie A\nhttp://x/stream/a?link=h1&index=1\n")
	p2 := []byte("#EXTM3U\n#EXTINF:-1,Movie B\nhttp://y/stream/b?link=h2&index=1\n")
	out := mergeM3U([][]byte{p1, nil, p2})

	if strings.Count(out, "#EXTM3U") != 1 {
		t.Fatalf("want exactly one #EXTM3U header:\n%s", out)
	}
	if !strings.Contains(out, "Movie A") || !strings.Contains(out, "Movie B") {
		t.Fatalf("missing merged entries:\n%s", out)
	}
	if strings.Count(out, "/ts/stream/") != 2 {
		t.Fatalf("want 2 rewritten /ts/stream/ URLs:\n%s", out)
	}
}
