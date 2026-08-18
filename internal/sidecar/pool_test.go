package sidecar

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"testing"
)

// --- Test harness: a fake engine whose "binary" is this test binary re-invoked
// as a helper process. The helper either listens on the SOCKS port (a healthy
// proxy) or writes to stderr and exits (a broken proxy), letting us exercise the
// pool's start/readiness/diagnostic paths without a real xray/mihomo binary.

type fakeEngine struct{}

func (fakeEngine) Type() EngineType { return EngineType("fake") }

func (fakeEngine) EnsureBinary(string) (string, error) { return os.Args[0], nil }

func (fakeEngine) GenerateConfig(out ProxyOutbound, port int) ([]byte, string, error) {
	// Encode "<mode> <port>" — the helper reads this back from the config file.
	return []byte(fmt.Sprintf("%s %d", out.Protocol, port)), "txt", nil
}

func (fakeEngine) StartArgs(binPath, cfgPath string) []string {
	return []string{binPath, "-test.run=TestSidecarHelperProcess", "--", cfgPath}
}

// fakeParse maps a URI scheme to a mode carried in ProxyOutbound.Protocol:
//   good://  -> a process that opens the SOCKS port (healthy)
//   bad://   -> a process that errors out before opening the port (broken)
func fakeParse(uri string) (ProxyOutbound, EngineType, error) {
	mode := "good"
	if strings.HasPrefix(uri, "bad://") {
		mode = "bad"
	}
	return ProxyOutbound{Protocol: mode, Server: "127.0.0.1"}, EngineType("fake"), nil
}

// TestSidecarHelperProcess is not a real test — it's the child process spawned by
// fakeEngine.StartArgs. It only runs when GO_WANT_HELPER_PROCESS=1.
func TestSidecarHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	cfgPath := ""
	for i, a := range args {
		if a == "--" && i+1 < len(args) {
			cfgPath = args[i+1]
			break
		}
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: read config:", err)
		os.Exit(10)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		fmt.Fprintln(os.Stderr, "helper: bad config:", string(data))
		os.Exit(11)
	}
	mode, port := fields[0], fields[1]

	if mode == "bad" {
		fmt.Fprintln(os.Stderr, "fake engine: simulated fatal config error")
		os.Exit(2)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: listen:", err)
		os.Exit(3)
	}
	defer ln.Close()

	// Exit promptly when Stop() sends SIGINT, so pool teardown stays fast.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() { <-sig; os.Exit(0) }()

	for {
		c, err := ln.Accept()
		if err != nil {
			os.Exit(0)
		}
		c.Close()
	}
}

func withHelperEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
}

func TestNewPoolSurvivesOneBadEntry(t *testing.T) {
	withHelperEnv(t)
	engines := map[EngineType]Engine{EngineType("fake"): fakeEngine{}}
	configs := []PoolConfig{
		{URI: "good://a", Label: "good-1", Balancers: []string{"b1"}},
		{URI: "bad://b", Label: "🇷🇺 Russia", Balancers: []string{"b2"}},
		{URI: "good://c", Label: "good-2", Balancers: []string{"b3"}},
	}

	pool, err := NewPool(configs, t.TempDir(), 41700, engines, fakeParse)
	if err != nil {
		t.Fatalf("NewPool returned hard error on partial failure: %v", err)
	}
	t.Cleanup(pool.StopAll)

	if got := len(pool.Entries()); got != 2 {
		t.Fatalf("started entries = %d, want 2", got)
	}
	failures := pool.Failures()
	if len(failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(failures))
	}
	if failures[0].Label != "🇷🇺 Russia" {
		t.Errorf("failure label = %q, want %q", failures[0].Label, "🇷🇺 Russia")
	}
	if failures[0].Index != 1 {
		t.Errorf("failure index = %d, want 1", failures[0].Index)
	}
	if !strings.Contains(failures[0].Error, "simulated fatal config error") {
		t.Errorf("failure error = %q, want it to surface the stderr reason", failures[0].Error)
	}
}

func TestNewPoolHardErrorWhenAllFail(t *testing.T) {
	withHelperEnv(t)
	engines := map[EngineType]Engine{EngineType("fake"): fakeEngine{}}
	configs := []PoolConfig{
		{URI: "bad://a", Label: "x"},
		{URI: "bad://b", Label: "y"},
	}

	pool, err := NewPool(configs, t.TempDir(), 41710, engines, fakeParse)
	if err == nil {
		t.Fatal("expected hard error when every entry fails")
	}
	if pool != nil && len(pool.Entries()) != 0 {
		t.Fatalf("expected no started entries, got %d", len(pool.Entries()))
	}
	if !strings.Contains(err.Error(), "sidecar pool entry 0 (x)") {
		t.Errorf("error = %q, want it to name the first failed entry", err.Error())
	}
}
