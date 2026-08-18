package sidecar

import (
	"strings"
	"testing"
)

func TestRingBufferKeepsTail(t *testing.T) {
	r := newRingBuffer(8)
	if _, err := r.Write([]byte("0123456789ABCDEF")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := r.String(); got != "89ABCDEF" {
		t.Fatalf("tail = %q, want %q", got, "89ABCDEF")
	}
}

func TestRingBufferTrimsWhitespace(t *testing.T) {
	r := newRingBuffer(64)
	r.Write([]byte("  hello\n\n"))
	if got := r.String(); got != "hello" {
		t.Fatalf("String() = %q, want %q", got, "hello")
	}
}

func TestLastLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"single line", "single line"},
		{"banner\nstartup\nfatal: address already in use", "fatal: address already in use"},
		{"trailing\n\n", "trailing"},
		{"", ""},
	}
	for _, c := range cases {
		if got := lastLine(c.in); got != c.want {
			t.Errorf("lastLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLastLineCaps(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := lastLine(long)
	if len([]rune(got)) != 301 { // 300 chars + ellipsis
		t.Fatalf("len = %d, want 301 (300 + ellipsis)", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
}
