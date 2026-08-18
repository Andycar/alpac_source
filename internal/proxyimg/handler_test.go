package proxyimg

import "testing"

func TestParsePathSimple(t *testing.T) {
	w, h, target, ok := parsePath("/proxyimg/https://example.com/a.jpg")
	if !ok {
		t.Fatalf("expected ok")
	}
	if w != 0 || h != 0 {
		t.Fatalf("unexpected resize values %d:%d", w, h)
	}
	if target != "https://example.com/a.jpg" {
		t.Fatalf("unexpected target: %s", target)
	}
}

func TestParsePathResize(t *testing.T) {
	w, h, target, ok := parsePath("/proxyimg:210:0/https://example.com/a.png")
	if !ok {
		t.Fatalf("expected ok")
	}
	if w != 210 || h != 0 {
		t.Fatalf("unexpected resize values %d:%d", w, h)
	}
	if target != "https://example.com/a.png" {
		t.Fatalf("unexpected target: %s", target)
	}
}

func TestDecodeDirectTargetEscaped(t *testing.T) {
	got, ok := decodeDirectTarget("https%3A%2F%2Fexample.com%2Fx.jpg", "q=1")
	if !ok {
		t.Fatalf("expected ok")
	}
	if got != "https://example.com/x.jpg?q=1" {
		t.Fatalf("unexpected target: %s", got)
	}
}

func TestSplitReserveURL(t *testing.T) {
	a, b := splitReserveURL("https://a/x.jpg or https://b/x.jpg")
	if a != "https://a/x.jpg" || b != "https://b/x.jpg" {
		t.Fatalf("unexpected reserve split: %s | %s", a, b)
	}
}
