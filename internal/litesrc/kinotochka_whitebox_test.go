package litesrc

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lampac-go/internal/config"
)

func TestKinotochkaMovieFileFromBodyPrefersMovieOverTrailer(t *testing.T) {
	checker := NewKinotochkaChecker(config.Config{})
	body := `<script>
	var a={id:"first", file:"https://cdn.example/video_mp4/trailer/sample.mp4"};
	var b={id:"second", file:"https://cdn.example/video_mp4/films/sample_1080.mp4"};
	</script>`

	got := checker.movieFileFromBody(body)
	want := "https://cdn.example/video_mp4/films/sample_1080.mp4"
	if got != want {
		t.Fatalf("unexpected movie file: got=%q want=%q", got, want)
	}
}

func TestKinotochkaPickMovieFileHandlesEscapedURL(t *testing.T) {
	checker := NewKinotochkaChecker(config.Config{})
	raw := `https:\\/\\/cdn.example\\/video_mp4\\/films\\/demo_720.mp4`

	got := checker.pickMovieFile(raw)
	want := "https://cdn.example/video_mp4/films/demo_720.mp4"
	if got != want {
		t.Fatalf("unexpected picked url: got=%q want=%q", got, want)
	}
}

// The fetch-chain budget: two consecutive transport failures (dead upstream) must
// stop the chain instead of queueing a dozen more full-timeout fetches — one slow
// card used to eat the entire 25s capi drill budget.
func TestKinotochkaFetchBudgetStopsAfterConsecutiveFailures(t *testing.T) {
	k := &kinotochkaChecker{client: &http.Client{Timeout: 200 * time.Millisecond}, host: "http://127.0.0.1:1"}
	req := kinotochkaWithBudget(httptest.NewRequest(http.MethodGet, "/lite/kinotochka", nil))

	for i := 0; i < 5; i++ {
		if _, ok := k.fetch(req, http.MethodGet, "http://127.0.0.1:1/dead", "", ""); ok {
			t.Fatal("fetch to a dead port must fail")
		}
	}
	b := kinotochkaBudgetOf(req)
	if b == nil {
		t.Fatal("budget not attached")
	}
	// Only the first kinotochkaMaxFailRun fetches actually hit the network; the rest
	// were short-circuited before counting.
	if b.fetches != kinotochkaMaxFailRun {
		t.Errorf("fetches = %d, want %d (fail-fast after the failure run)", b.fetches, kinotochkaMaxFailRun)
	}
}
