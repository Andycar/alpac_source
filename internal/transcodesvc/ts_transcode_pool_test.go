package transcodesvc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/torrbalancer"
)

func TestTSInfohashFromPath(t *testing.T) {
	hash := "eb0324a90e0d44d479cffaf5c795d1e32d0e6bf9"
	cases := []struct {
		path string
		want string
	}{
		{"/stream/file.avi?link=" + hash + "&index=1&play", hash},
		{"/stream/file.mkv?hash=" + strings.ToUpper(hash) + "&play", hash},
		{"/stream/file.mkv", ""},
		{"/stream/file.mkv?index=1", ""},
	}
	for _, c := range cases {
		if got := tsInfohashFromPath(c.path); got != c.want {
			t.Errorf("tsInfohashFromPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestTSBackendBaseURLEscapesCreds(t *testing.T) {
	// Passwords with '@' and '!' must be %-escaped in userinfo — the prod URL
	// "KirillZ:7S9swMJPbr17@%21@144.31..." carried a BARE '@' (PathEscape bug).
	st, err := torrbalancer.NewStore(t.TempDir(), config.TorrServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add("a", "http://144.31.111.97:8090", "KirillZ", "pw@!x", 1, true, ""); err != nil {
		t.Fatal(err)
	}
	pool := torrbalancer.NewPool(st)
	b := pool.Backends()[0]

	got := tsBackendBaseURL(b)
	want := "http://KirillZ:pw%40%21x@144.31.111.97:8090"
	if got != want {
		t.Errorf("tsBackendBaseURL = %q, want %q", got, want)
	}
}

func TestTranscodePrivateSrcError(t *testing.T) {
	prevPool := tsBalancerPoolProvider
	tsBalancerPoolProvider = nil
	defer func() { tsBalancerPoolProvider = prevPool }()

	var cfg config.Config

	// A public deployment: the client reached tv.example over the internet.
	pub := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/transcoding/start", nil)
		r.Host = "tv.example"
		r.RemoteAddr = "77.88.8.8:44321"
		return r
	}

	// The failure mode: a client's HOME-LAN TorrServer.
	if msg := transcodePrivateSrcError(cfg, pub(), "http://192.168.1.78:8090/stream/x.mkv?link=abc&play"); msg == "" {
		t.Error("home-LAN TorrServer src must be rejected")
	}
	if msg := transcodePrivateSrcError(cfg, pub(), "http://10.0.0.5:8090/stream/x"); msg == "" {
		t.Error("10/8 src must be rejected")
	}
	if msg := transcodePrivateSrcError(cfg, pub(), "http://127.0.0.1:8090/stream/x"); msg == "" {
		t.Error("client-supplied loopback src must be rejected")
	}

	// Public hosts pass.
	if msg := transcodePrivateSrcError(cfg, pub(), "https://beta.l-vid.online/ts/stream/x.mkv?link=abc"); msg != "" {
		t.Errorf("public host rejected: %s", msg)
	}
	if msg := transcodePrivateSrcError(cfg, pub(), "http://144.31.111.97:8090/stream/x"); msg != "" {
		t.Errorf("public IP rejected: %s", msg)
	}

	// Self-hosted LAN deployment: src host == configured TorrServer → allowed.
	cfg.TorrServer.URL = "http://192.168.1.78:8090"
	if msg := transcodePrivateSrcError(cfg, pub(), "http://192.168.1.78:8090/stream/x"); msg != "" {
		t.Errorf("configured TorrServer host rejected: %s", msg)
	}
	cfg.TorrServer.URL = ""

	// A LOCAL install: the client reached the box at its LAN address, from the
	// same LAN. Every private src is normal there — the gate must stand down.
	lan := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/transcoding/start", nil)
		r.Host = "192.168.1.50:8888"
		r.RemoteAddr = "192.168.1.31:51234"
		return r
	}
	if msg := transcodePrivateSrcError(cfg, lan(), "http://192.168.1.90:8090/stream/x"); msg != "" {
		t.Errorf("LAN deployment must not gate private srcs: %s", msg)
	}

	// The server's OWN address is never «чужая домашняя сеть», even when the
	// request came in over the internet (public host, LAN-addressed src).
	selfSrc := httptest.NewRequest(http.MethodPost, "/transcoding/start", nil)
	selfSrc.Host = "192.168.1.50:8888"
	selfSrc.RemoteAddr = "77.88.8.8:44321" // client outside → not a LAN deployment
	if msg := transcodePrivateSrcError(cfg, selfSrc, "http://192.168.1.50:8888/ts/stream?link=abc&play"); msg != "" {
		t.Errorf("server's own address rejected: %s", msg)
	}

	// /ts/ and pidtor srcs are rewritten onto the local TorrServer before
	// ffmpeg runs, so the private host is never dialled → allowed.
	cfg.TorrServer.Port = 9080
	if msg := transcodePrivateSrcError(cfg, pub(), "http://192.168.1.90:8888/ts/stream?link=abc&play"); msg != "" {
		t.Errorf("/ts/ src (rewritten to local TorrServer) rejected: %s", msg)
	}
	if msg := transcodePrivateSrcError(cfg, pub(), "http://192.168.1.90:8888/lite/pidtor/s/x"); msg != "" {
		t.Errorf("pidtor src (rewritten to local TorrServer) rejected: %s", msg)
	}
	// …but a plain /stream/ URL on someone's home box still is.
	if msg := transcodePrivateSrcError(cfg, pub(), "http://192.168.1.90:8090/stream/x"); msg == "" {
		t.Error("home-LAN TorrServer src must stay rejected when TS is configured")
	}
	cfg.TorrServer.Port = 0

	// Escape hatch.
	cfg.Transcoding.AllowPrivateSrc = true
	if msg := transcodePrivateSrcError(cfg, pub(), "http://192.168.1.1:8090/stream/x"); msg != "" {
		t.Errorf("allow_private_src=true must disable the check: %s", msg)
	}
}

func TestInputNeverOpened(t *testing.T) {
	dead := []string{
		"[tcp @ 0x1] Connection to tcp://192.168.1.1:8090 failed: Connection timed out",
		"[in#0 @ 0x2] Error opening input: Connection timed out",
		"Error opening input file http://192.168.1.1:8090/stream/x.mkv.",
		"Error opening input files: Connection timed out",
	}
	if !inputNeverOpened(dead) {
		t.Error("input-open failure not detected")
	}
	midstream := []string{
		"[https @ 0x1] Error reading HTTP response: Connection reset by peer",
		"[out#0/hls @ 0x2] Error muxing a packet",
		"Conversion failed!",
	}
	if inputNeverOpened(midstream) {
		t.Error("mid-stream death misclassified as input-open failure (would kill legit auto-restarts)")
	}
}
