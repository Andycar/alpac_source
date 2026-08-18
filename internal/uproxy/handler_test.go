package uproxy

import "testing"

func TestDecodeDirectTargetPlain(t *testing.T) {
	got, ok := decodeDirectTarget("https://example.com/video.m3u8", "a=1")
	if !ok {
		t.Fatalf("expected ok")
	}
	if got != "https://example.com/video.m3u8?a=1" {
		t.Fatalf("unexpected target: %s", got)
	}
}

func TestDecodeDirectTargetEscaped(t *testing.T) {
	got, ok := decodeDirectTarget("https%3A%2F%2Fexample.com%2Fapi%3Fx%3D1", "y=2")
	if !ok {
		t.Fatalf("expected ok")
	}
	if got != "https://example.com/api?x=1&y=2" {
		t.Fatalf("unexpected target: %s", got)
	}
}

func TestDecodeDirectTargetReserve(t *testing.T) {
	got, ok := decodeDirectTarget("https://a.example/vid.m3u8 or https://b.example/vid.m3u8", "")
	if !ok {
		t.Fatalf("expected ok")
	}
	if got != "https://a.example/vid.m3u8" {
		t.Fatalf("unexpected target: %s", got)
	}
}

func TestDecodeDirectTargetRejectEncryptedLike(t *testing.T) {
	_, ok := decodeDirectTarget("U2FsdGVkX1+abc", "")
	if ok {
		t.Fatalf("expected not ok")
	}
}
