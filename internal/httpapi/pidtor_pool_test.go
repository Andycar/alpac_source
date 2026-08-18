package httpapi

import (
	"strings"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/torrbalancer"
)

// TestPidtorGetTorrServerPool: with the TS-balancer pool active, pidtor must
// resolve the SAME backend the /ts proxy and the transcoder pick for the same
// infohash (HRW stickiness), carry that backend's Authorization, and fall back
// to the static config when the pool is off. Explicit [online.pidtor] torrs
// lists always win over the pool.
func TestPidtorGetTorrServerPool(t *testing.T) {
	prevPool := tsBalancerPoolRef
	defer func() { tsBalancerPoolRef = prevPool }()

	st, err := torrbalancer.NewStore(t.TempDir(), config.TorrServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []struct{ name, host, pw string }{
		{"a", "http://10.0.0.1:8090", "pwA"},
		{"b", "http://10.0.0.2:8090", "pwB"},
		{"c", "http://10.0.0.3:8090", ""},
	} {
		if _, err := st.Add(b.name, b.host, "ts", b.pw, 1, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	tsBalancerPoolRef = torrbalancer.NewPool(st)

	var cfg config.Config
	cfg.TorrServer.URL = "http://static.example:9080"
	cfg.TorrServer.Password = "staticpw"

	const hash = "a793e116ad5ca20b4f6c911f81281c481dab9759"

	// 1) Pool pick is sticky and matches the transcoder's pick for the same hash.
	host1, hdrs1 := pidtorGetTorrServer(cfg, "", hash)
	host2, _ := pidtorGetTorrServer(cfg, "", hash)
	if host1 != host2 {
		t.Errorf("pool pick not sticky: %q vs %q", host1, host2)
	}
	if host1 == "http://static.example:9080" {
		t.Errorf("pool active but static config used: %q", host1)
	}
	// The transcoder's pick (transcodesvc.TSPoolBackendFor) is exactly the pool's
	// HRW for the same lowercased hash, so check pidtor against the pool directly —
	// both paths must agree on the sticky backend.
	wantB := tsBalancerPoolRef.PickForHash(strings.ToLower(strings.TrimSpace(hash)), nil)
	if wantB == nil {
		t.Fatal("pool PickForHash returned nil with an active pool")
	}
	if wantHost, _ := wantB.Target(); strings.TrimRight(wantHost, "/") != host1 {
		t.Errorf("pidtor picked %q, transcoder picks %q — stickiness broken across paths", host1, wantHost)
	}

	// 2) The chosen backend's auth rides in the headers (pwA/pwB → Basic ...).
	if strings.Contains(host1, "10.0.0.1") || strings.Contains(host1, "10.0.0.2") {
		if hdrs1["Authorization"] == "" || !strings.HasPrefix(hdrs1["Authorization"], "Basic ") {
			t.Errorf("backend with password but no Authorization header: %v", hdrs1)
		}
	}

	// 3) Different hashes spread across backends (HRW actually distributes).
	seen := map[string]bool{}
	for _, h := range []string{hash, strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40), strings.Repeat("4", 40), strings.Repeat("5", 40)} {
		hst, _ := pidtorGetTorrServer(cfg, "", h)
		seen[hst] = true
	}
	if len(seen) < 2 {
		t.Errorf("6 hashes all landed on one backend %v — distribution broken", seen)
	}

	// 4) Explicit [online.pidtor] torrs list wins over the pool.
	cfgTorrs := cfg
	cfgTorrs.Online.PidTor.Torrs = []string{"http://explicit.example:8090"}
	if hst, _ := pidtorGetTorrServer(cfgTorrs, "", hash); hst != "http://explicit.example:8090" {
		t.Errorf("explicit torrs list ignored: got %q", hst)
	}

	// 5) Pool off → static config fallback with its auth.
	tsBalancerPoolRef = nil
	hst, hdrs := pidtorGetTorrServer(cfg, "", hash)
	if hst != "http://static.example:9080" {
		t.Errorf("static fallback broken: got %q", hst)
	}
	if hdrs["Authorization"] == "" {
		t.Errorf("static TorrServer password not carried into headers")
	}
}
