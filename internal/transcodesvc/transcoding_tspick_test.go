package transcodesvc

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/torrbalancer"
)

// The ts-pick oracle hands a remote box the credentials of a pool backend, so
// its gate (secret + signature + expiry) and the box↔main round-trip are
// covered explicitly.
func TestTSPickOracle(t *testing.T) {
	const secret = "shared-box-secret"
	const hash = "a793e116ad5ca20b4f6c911f81281c481dab9759"

	prevCfg, prevPool, prevInProc := liveConfigProvider, tsBalancerPoolProvider, torrsIsInProcessProvider
	defer func() {
		liveConfigProvider, tsBalancerPoolProvider, torrsIsInProcessProvider = prevCfg, prevPool, prevInProc
		tsPickCache.Lock()
		tsPickCache.m = map[string]tsPickCacheEntry{}
		tsPickCache.Unlock()
	}()
	torrsIsInProcessProvider = func() bool { return false }

	// --- MAIN: pool of two backends + secret ---
	st, err := torrbalancer.NewStore(t.TempDir(), config.TorrServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add("ts-a", "http://10.0.0.1:8090", "KirillZ", "pw@!a", 1, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add("ts-b", "http://10.0.0.2:8090", "KirillZ", "pwb", 1, true, ""); err != nil {
		t.Fatal(err)
	}
	pool := torrbalancer.NewPool(st)
	tsBalancerPoolProvider = func() *torrbalancer.Pool { return pool }

	var mainCfg config.Config
	mainCfg.Transcoding.RemoteSecret = secret
	mainCfg.Transcoding.RemoteHost = "https://tc.example" // main mints box URLs, is NOT a box
	liveConfigProvider = func() config.Config { return mainCfg }

	// Main and box share one process here, so the main's handler pins the
	// main-side providers for the duration of each request and restores
	// whatever the "box" had set afterwards (requests are sequential).
	mainHandler := transcodingTSPickHandler(mainCfg)
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		curCfg, curPool := liveConfigProvider, tsBalancerPoolProvider
		liveConfigProvider = func() config.Config { return mainCfg }
		tsBalancerPoolProvider = func() *torrbalancer.Pool { return pool }
		mainHandler(w, r)
		liveConfigProvider, tsBalancerPoolProvider = curCfg, curPool
	}))
	defer main.Close()

	exp := time.Now().Add(time.Minute).Unix()
	get := func(q string) (int, string) {
		resp, err := http.Get(main.URL + "/transcoding/ts-pick?" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		return resp.StatusCode, string(buf[:n])
	}

	t.Run("unsigned → 403", func(t *testing.T) {
		if code, _ := get("hash=" + hash); code != http.StatusForbidden {
			t.Fatalf("unsigned request got %d, want 403", code)
		}
	})
	t.Run("wrong secret → 403", func(t *testing.T) {
		sig := tsPickSign("other", hash, exp)
		if code, _ := get("hash=" + hash + "&exp=" + strconv.FormatInt(exp, 10) + "&sig=" + sig); code != http.StatusForbidden {
			t.Fatalf("forged request got %d, want 403", code)
		}
	})
	t.Run("expired → 403", func(t *testing.T) {
		past := time.Now().Add(-time.Minute).Unix()
		sig := tsPickSign(secret, hash, past)
		if code, _ := get("hash=" + hash + "&exp=" + strconv.FormatInt(past, 10) + "&sig=" + sig); code != http.StatusForbidden {
			t.Fatalf("expired request got %d, want 403", code)
		}
	})
	t.Run("a start-URL signature must not open the oracle", func(t *testing.T) {
		// Domain separation: sig over the bare hash (start-URL scheme) ≠ ts-pick scheme.
		sig := transcodeSign(secret, hash, exp)
		if code, _ := get("hash=" + hash + "&exp=" + strconv.FormatInt(exp, 10) + "&sig=" + sig); code != http.StatusForbidden {
			t.Fatalf("start-URL-scheme signature accepted: %d", code)
		}
	})
	t.Run("signed → 200 with the HRW backend", func(t *testing.T) {
		sig := tsPickSign(secret, hash, exp)
		code, body := get("hash=" + hash + "&exp=" + strconv.FormatInt(exp, 10) + "&sig=" + sig)
		if code != http.StatusOK {
			t.Fatalf("signed request got %d: %s", code, body)
		}
		want := pool.PickForHash(hash, nil)
		if want == nil || !strings.Contains(body, `"host":"`+want.Host+`"`) || !strings.Contains(body, `"password":"`+want.Password+`"`) {
			t.Fatalf("reply %s does not carry the pool's HRW pick %+v", body, want)
		}
	})

	// --- BOX: same secret, no remote_host → asks the main, gets a detached backend ---
	var boxCfg config.Config
	boxCfg.Transcoding.RemoteSecret = secret
	boxCfg.TorrServer.URL = "http://127.0.0.1:9080" // the box's static fallback, must NOT be used
	liveConfigProvider = func() config.Config { return boxCfg }
	tsBalancerPoolProvider = func() *torrbalancer.Pool { return nil } // box has no pool of its own

	src := main.URL + "/lite/pidtor/s" + hash + "?tr=x&tsid=1"
	b := tsPickBackend(srcOrigin(src), hash)
	if b == nil {
		t.Fatal("box got no backend from the oracle")
	}
	want := pool.PickForHash(hash, nil)
	if b.Host != want.Host || b.Password != want.Password || b.Login != want.Login {
		t.Fatalf("box pick %+v ≠ main pick %+v", b, want)
	}
	if _, authHdr := b.Target(); !strings.HasPrefix(authHdr, "Basic ") {
		t.Fatalf("detached backend lacks Authorization for the torrents API: %q", authHdr)
	}
	if base := tsBackendBaseURL(b); !strings.HasPrefix(base, "http://KirillZ:") || !strings.HasSuffix(base, "@"+strings.TrimPrefix(want.Host, "http://")) ||
		(want.Password == "pw@!a" && !strings.Contains(base, "pw%40%21a@")) {
		t.Fatalf("ffmpeg base URL not built with escaped userinfo: %q", base)
	}
	// /ts/ path resolves through the same oracle.
	if base := tsDirectStreamBaseFor(boxCfg, srcOrigin(src), "/stream/f.mkv?link="+hash+"&index=1&play", true); !strings.Contains(base, want.Host[len("http://"):]) {
		t.Fatalf("/ts/ rewrite base %q not on the main's backend %q", base, want.Host)
	}

	// Cached: the main can go away and the box still answers for this hash.
	main.Close()
	if b2 := tsPickBackend(srcOrigin(src), hash); b2 == nil || b2.Host != b.Host {
		t.Fatal("pick not served from cache after the main went down")
	}
	// Unknown hash now → oracle unreachable → nil (caller falls back to local).
	if b3 := remoteTSPick(srcOrigin(src), strings.Repeat("7", 40)); b3 != nil {
		t.Fatalf("unreachable main must yield nil, got %+v", b3)
	}

	// Not a box (remote_host set) → never asks, even with a secret.
	boxCfg.Transcoding.RemoteHost = "https://tc.example"
	tsPickCache.Lock()
	tsPickCache.m = map[string]tsPickCacheEntry{}
	tsPickCache.Unlock()
	if b4 := remoteTSPick(srcOrigin(src), hash); b4 != nil {
		t.Fatalf("a non-box process must not consult the oracle, got %+v", b4)
	}
}

// A box with neither a local TorrServer nor an oracle answer must leave the
// pidtor/ts src alone (ffmpeg fetches it over HTTP) — never rewrite onto 127.0.0.1.
func TestTSPickNoLocalNoOracleLeavesSrc(t *testing.T) {
	prevCfg, prevPool, prevInProc := liveConfigProvider, tsBalancerPoolProvider, torrsIsInProcessProvider
	defer func() {
		liveConfigProvider, tsBalancerPoolProvider, torrsIsInProcessProvider = prevCfg, prevPool, prevInProc
	}()
	torrsIsInProcessProvider = func() bool { return false }
	tsBalancerPoolProvider = func() *torrbalancer.Pool { return nil }
	var cfg config.Config
	liveConfigProvider = func() config.Config { return cfg }

	if base := tsDirectStreamBaseFor(cfg, "https://main.example", "/stream/f.mkv?link=abc&play", false); base != "" {
		t.Fatalf("expected no rewrite base, got %q", base)
	}
	if _, ok := ffprobeRewritePidtor(cfg, "https://main.example/lite/pidtor/sabcdef?tsid=1"); ok {
		t.Fatal("pidtor src rewritten with no TorrServer anywhere")
	}
}
