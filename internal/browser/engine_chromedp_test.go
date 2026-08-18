package browser

import (
	"errors"
	"net/http"
	"testing"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
)

// TestChromedpEngine_Registered verifies the engine self-registered
// at init() so the registry exposes it to admin UI.
func TestChromedpEngine_Registered(t *testing.T) {
	e, err := Get("chromedp")
	if err != nil {
		t.Fatalf("chromedp engine not registered: %v", err)
	}
	if e.Name() != "chromedp" {
		t.Fatalf("Name = %q, want %q", e.Name(), "chromedp")
	}
}

// TestChromedpEngine_AvailableHonoursPATH exercises the binary lookup
// without launching Chrome. The result depends on the dev machine, so
// we accept either nil or ErrEngineUnavailable.
func TestChromedpEngine_AvailableHonoursPATH(t *testing.T) {
	e, _ := Get("chromedp")
	err := e.Available()
	if err != nil && !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("unexpected error type: %v", err)
	}
}

// TestChromedpHijackReq_HeadersAndPostData parses a synthesised CDP
// Fetch.requestPaused event through chromedpHijackReq and asserts the
// public API surface returns what we expect — guards against future
// cdproto field renames.
func TestChromedpHijackReq_HeadersAndPostData(t *testing.T) {
	ev := &fetch.EventRequestPaused{
		RequestID:    "rid-1",
		ResourceType: "XHR",
		Request: &network.Request{
			URL:    "https://example.com/api?x=1",
			Method: "POST",
			Headers: network.Headers{
				"X-Custom": "hello",
				"Cookie":   "a=b",
			},
			HasPostData: true,
			PostDataEntries: []*network.PostDataEntry{
				// b64.EncodeToString("hello world")
				{Bytes: "aGVsbG8gd29ybGQ="},
			},
		},
	}

	r := &chromedpHijackReq{event: ev}

	if got := r.URL(); got != "https://example.com/api?x=1" {
		t.Errorf("URL = %q", got)
	}
	if got := r.Method(); got != "POST" {
		t.Errorf("Method = %q", got)
	}
	if got := r.ResourceType(); got != "XHR" {
		t.Errorf("ResourceType = %q", got)
	}
	h := r.Headers()
	if h.Get("X-Custom") != "hello" {
		t.Errorf("X-Custom header lost: %v", h)
	}
	if h.Get("Cookie") != "a=b" {
		t.Errorf("Cookie header lost: %v", h)
	}
	body := r.PostData()
	if string(body) != "hello world" {
		t.Errorf("PostData = %q, want %q", body, "hello world")
	}
}

func TestHeadersToFetch(t *testing.T) {
	h := http.Header{
		"X-A": []string{"1"},
		"X-B": []string{"2", "3"},
	}
	out := headersToFetch(h)
	if len(out) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(out))
	}
	// Map iteration order is unspecified, so just check that all
	// expected (name, value) pairs are present.
	want := map[string]bool{"X-A=1": true, "X-B=2": true, "X-B=3": true}
	for _, e := range out {
		k := e.Name + "=" + e.Value
		if !want[k] {
			t.Errorf("unexpected entry %q", k)
		}
		delete(want, k)
	}
	if len(want) > 0 {
		t.Errorf("missing entries: %v", want)
	}
}

func TestMapAbortReason(t *testing.T) {
	cases := map[string]network.ErrorReason{
		"":               network.ErrorReasonAborted,
		"aborted":        network.ErrorReasonAborted,
		"failed":         network.ErrorReasonFailed,
		"timeout":        network.ErrorReasonTimedOut,
		"timedout":       network.ErrorReasonTimedOut,
		"BlockedByClient": network.ErrorReasonBlockedByClient,
		"connectionrefused": network.ErrorReasonConnectionRefused,
		"namenotresolved":   network.ErrorReasonNameNotResolved,
		"unknown-garbage":   network.ErrorReasonFailed,
	}
	for in, want := range cases {
		if got := mapAbortReason(in); got != want {
			t.Errorf("mapAbortReason(%q) = %v, want %v", in, got, want)
		}
	}
}
