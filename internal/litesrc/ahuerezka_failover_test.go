package litesrc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newAhueTestChecker(hosts []string) *ahueRezkaChecker {
	ahueRezkaHostIdx.Store(0)
	ahueRezkaKPHostIdx.Store(0)
	return &ahueRezkaChecker{
		client:       &http.Client{Timeout: 5 * time.Second},
		clientDirect: &http.Client{Timeout: 5 * time.Second},
		host:         hosts[0],
		hosts:        hosts,
	}
}

// TestAhueRezkaHostListOrder: primary first, then configured spares, then the
// built-in default; blanks, trailing slashes and duplicates collapse away.
func TestAhueRezkaHostListOrder(t *testing.T) {
	got := ahueRezkaHostList(
		"https://primary.example/",
		[]string{"", "https://spare.example", "https://primary.example", "  "},
		"https://default.example",
	)
	want := []string{"https://primary.example", "https://spare.example", "https://default.example"}
	if len(got) != len(want) {
		t.Fatalf("host list = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("host list = %v, want %v", got, want)
		}
	}
}

// TestAhueRezkaFailsOverOnDeadHost: a host we cannot reach at all must be
// skipped, and the next one must answer. Without this the whole balancer dies
// with the single Cloudflare worker it hangs off.
func TestAhueRezkaFailsOverOnDeadHost(t *testing.T) {
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"translators":[{"id":"56","name":"Дубляж"}]}`))
	}))
	defer live.Close()

	// A server that is closed immediately gives us a guaranteed transport error.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	a := newAhueTestChecker([]string{deadURL, live.URL})

	body, ok := a.getJSONPath(context.Background(), "/info?id=301")
	if !ok {
		t.Fatal("expected failover to the live mirror")
	}
	if !strings.Contains(string(body), "Дубляж") {
		t.Fatalf("unexpected body: %s", body)
	}
	if got := a.activeHost(); got != live.URL {
		t.Fatalf("active host = %q, want the live mirror %q", got, live.URL)
	}
}

// TestAhueRezkaDoesNotRotateOnHTTPError: a mirror answering 404 is alive and is
// telling us about the CONTENT, not about itself. Rotating on that would walk
// off a healthy worker whenever a title simply isn't on HDRezka.
func TestAhueRezkaDoesNotRotateOnHTTPError(t *testing.T) {
	var firstHits, secondHits int
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		http.NotFound(w, r)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer second.Close()

	a := newAhueTestChecker([]string{first.URL, second.URL})

	if _, ok := a.getJSONPath(context.Background(), "/info?id=999999"); ok {
		t.Fatal("404 must not be reported as success")
	}
	if secondHits != 0 {
		t.Fatalf("rotated to the spare on an HTTP error (spare hits=%d)", secondHits)
	}
	if firstHits != 1 {
		t.Fatalf("first mirror hits = %d, want 1", firstHits)
	}
	if got := a.activeHost(); got != first.URL {
		t.Fatalf("active host = %q, want to stay on %q", got, first.URL)
	}
}

// TestAhueRezkaAdvanceHostIsIdempotent: when several concurrent requests all see
// the same mirror fail, only the first advances — otherwise a burst of failures
// would skip over healthy mirrors.
func TestAhueRezkaAdvanceHostIsIdempotent(t *testing.T) {
	hosts := []string{"https://a.example", "https://b.example", "https://c.example"}
	ahueRezkaHostIdx.Store(0)

	for i := 0; i < 5; i++ {
		ahueRezkaAdvanceHost(hosts, &ahueRezkaHostIdx, hosts[0], "worker")
	}
	if got := ahueRezkaPickHost(hosts, &ahueRezkaHostIdx, ""); got != hosts[1] {
		t.Fatalf("after repeated failures of host[0], active = %q, want %q", got, hosts[1])
	}
}
