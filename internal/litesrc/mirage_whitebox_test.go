package litesrc

import (
	"testing"
)

func TestMirageExtractStreamFromJSON(t *testing.T) {
	raw := `{"hlsSource":[{"quality":{"1080":"https://cdn.example/m1080.m3u8","720":"https://cdn.example/m720.m3u8"},"default":true}]}`
	if !mirageAnyM3URe.MatchString(raw) {
		t.Fatalf("regex does not match raw payload")
	}
	got := mirageExtractStream([]byte(raw))
	if got != "https://cdn.example/m1080.m3u8" {
		t.Fatalf("unexpected stream: %s", got)
	}
}
