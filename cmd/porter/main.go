package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"lampac-go/internal/porter"
)

// Global flags accessible by all files.
var (
	flagLLMURL   string
	flagModel    string
	flagBaseDir  string
	flagCSDir    string
	flagMainHost string
	flagBasePort int
	flagRetries  int
	flagTemp     float64
	flagVerbose  bool
)

func main() {
	flag.StringVar(&flagLLMURL, "llm-url", "http://localhost:8080", "llama.cpp API URL")
	flag.StringVar(&flagModel, "model", "qwen2.5-coder-14b", "LLM model name")
	flag.StringVar(&flagBaseDir, "base-dir", "", "custom_balancers directory (default: {cwd}/custom_balancers)")
	flag.StringVar(&flagCSDir, "cs-dir", "", "C# controllers directory (default: auto-detect)")
	flag.StringVar(&flagMainHost, "main-host", "http://127.0.0.1:888", "main lampac-go server address")
	flag.IntVar(&flagBasePort, "port", 50100, "starting port for subprocesses")
	flag.IntVar(&flagRetries, "retries", 5, "max LLM fix attempts")
	flag.Float64Var(&flagTemp, "temp", 0.1, "LLM temperature")
	flag.BoolVar(&flagVerbose, "verbose", false, "verbose output")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	// Resolve base-dir.
	if flagBaseDir == "" {
		wd, _ := os.Getwd()
		flagBaseDir = filepath.Join(wd, "custom_balancers")
	}

	// Auto-detect cs-dir.
	if flagCSDir == "" {
		flagCSDir = autoDetectCSDir()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	cmd := args[0]
	switch cmd {
	case "port":
		if len(args) < 2 {
			logError("usage: porter port <file.cs|dir>")
			os.Exit(1)
		}
		if err := cmdPort(ctx, args[1]); err != nil {
			logError("port failed: %v", err)
			os.Exit(1)
		}
	case "fix":
		if len(args) < 2 {
			logError("usage: porter fix <name>")
			os.Exit(1)
		}
		if err := cmdFix(ctx, args[1]); err != nil {
			logError("fix failed: %v", err)
			os.Exit(1)
		}
	case "test":
		if len(args) < 2 {
			logError("usage: porter test <name>")
			os.Exit(1)
		}
		if err := cmdTest(ctx, args[1]); err != nil {
			logError("test failed: %v", err)
			os.Exit(1)
		}
	case "list":
		cmdList()
	default:
		logError("unknown command: %s", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `porter — LLM-powered C# to Go balancer auto-porter

Usage:
  porter [flags] <command> [args]

Commands:
  port <file.cs|dir>   Port a C# balancer to Go standalone binary
  fix  <name>          Re-generate/fix an existing custom balancer
  test <name>          Test checksearch for a custom balancer
  list                 List C# balancers and porting status

Flags:
`)
	flag.PrintDefaults()
}

// autoDetectCSDir tries to find the C# controllers directory.
func autoDetectCSDir() string {
	candidates := []string{
		"../Online/Controllers",
		"../../Online/Controllers",
		"../../../Online/Controllers",
	}
	wd, _ := os.Getwd()
	for _, c := range candidates {
		p := filepath.Join(wd, c)
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	// Fallback: check lampac-main.
	home, _ := os.UserHomeDir()
	for _, base := range []string{
		filepath.Join(home, "Downloads/lampac-main/Online/Controllers"),
		filepath.Join(home, "lampac/Online/Controllers"),
	} {
		if info, err := os.Stat(base); err == nil && info.IsDir() {
			return base
		}
	}
	return ""
}

// cmdPort ports a C# file or directory.
func cmdPort(ctx context.Context, path string) error {
	llm := porter.NewLLMClient(flagLLMURL, flagModel, flagTemp)
	llm.Verbose = flagVerbose
	if err := llm.Ping(ctx); err != nil {
		return fmt.Errorf("LLM server not ready: %w", err)
	}
	logOK("LLM server connected at %s", flagLLMURL)

	deployer := NewDeployer(flagBaseDir, flagMainHost, flagBasePort)
	tester := NewTester()
	gen := NewGenerator(llm, deployer, tester, flagRetries)

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if info.IsDir() {
		// Port all .cs files in directory.
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		var errors []string
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".cs") {
				continue
			}
			csPath := filepath.Join(path, e.Name())
			logInfo("=== Porting %s ===", e.Name())
			if err := gen.Port(ctx, csPath); err != nil {
				logError("%s: %v", e.Name(), err)
				errors = append(errors, e.Name())
			}
		}
		if len(errors) > 0 {
			return fmt.Errorf("failed to port: %s", strings.Join(errors, ", "))
		}
		return nil
	}

	return gen.Port(ctx, path)
}

// cmdFix re-generates a broken custom balancer.
func cmdFix(ctx context.Context, name string) error {
	llm := porter.NewLLMClient(flagLLMURL, flagModel, flagTemp)
	llm.Verbose = flagVerbose
	if err := llm.Ping(ctx); err != nil {
		return fmt.Errorf("LLM server not ready: %w", err)
	}

	deployer := NewDeployer(flagBaseDir, flagMainHost, flagBasePort)
	tester := NewTester()
	gen := NewGenerator(llm, deployer, tester, flagRetries)

	return gen.Fix(ctx, name)
}

// cmdTest tests a custom balancer's checksearch.
func cmdTest(ctx context.Context, name string) error {
	deployer := NewDeployer(flagBaseDir, flagMainHost, flagBasePort)
	port := deployer.PortForName(name)
	if port == 0 {
		return fmt.Errorf("balancer %q not found or no config", name)
	}

	tester := NewTester()
	addr := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Use a well-known kinopoisk ID for testing.
	testKPIDs := []string{"326", "839380", "535341"} // Побег, Интерстеллар, Джентльмены
	for _, kpID := range testKPIDs {
		resp, ok := tester.CheckSearch(ctx, addr, name, kpID)
		if ok {
			logOK("checksearch kp=%s: OK — %s", kpID, resp)
			return nil
		}
		logWarn("checksearch kp=%s: %s", kpID, resp)
	}
	return fmt.Errorf("checksearch failed for all test IDs")
}

// cmdList shows C# balancers and their porting status.
func cmdList() {
	// List existing custom balancers.
	existing := map[string]bool{}
	if entries, err := os.ReadDir(flagBaseDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				existing[e.Name()] = true
				logOK("  [ported] %s", e.Name())
			}
		}
	}

	// List known built-in balancers.
	builtIn := []string{
		"alloha", "aniliberty", "anilibria", "animebesst", "animedia",
		"animego", "animelib", "animevost", "ashdi", "cdnmovies",
		"cdnvideohub", "collaps", "eneyida", "fancdn", "filmix",
		"filmixtv", "fxapi", "getstv", "hdvb", "iframevideo",
		"iptvonline", "kinobase", "kinogo", "kinotochka", "kinopub",
		"kinoukr", "kodik", "lumex", "mirage", "moonanime",
		"plvideo", "redheadsound", "remux", "rezka", "rutubemovie",
		"vdbmovies", "vcdn", "veoveo", "vibix", "videocdn",
		"videoseed", "vkmovie", "youtube", "zetflix",
	}
	builtInSet := map[string]bool{}
	for _, b := range builtIn {
		builtInSet[b] = true
	}

	// List C# files not yet ported.
	if flagCSDir == "" {
		logWarn("C# controllers directory not found. Use -cs-dir flag.")
		return
	}

	entries, err := os.ReadDir(flagCSDir)
	if err != nil {
		logError("read cs-dir: %v", err)
		return
	}

	fmt.Println("\nC# balancers not yet ported:")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".cs") {
			continue
		}
		name := strings.TrimSuffix(strings.ToLower(e.Name()), ".cs")
		name = strings.TrimSuffix(name, "controller")
		if builtInSet[name] || existing[name] {
			continue
		}
		fmt.Printf("  [todo] %s  (%s)\n", name, e.Name())
	}

	// Check subdirectories (Anime/, ENG/, UKR/)
	for _, subdir := range []string{"Anime", "ENG", "UKR"} {
		subpath := filepath.Join(flagCSDir, subdir)
		subEntries, err := os.ReadDir(subpath)
		if err != nil {
			continue
		}
		for _, e := range subEntries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".cs") {
				continue
			}
			name := strings.TrimSuffix(strings.ToLower(e.Name()), ".cs")
			name = strings.TrimSuffix(name, "controller")
			if strings.HasPrefix(name, "base") {
				continue
			}
			if builtInSet[name] || existing[name] {
				continue
			}
			fmt.Printf("  [todo] %s  (%s/%s)\n", name, subdir, e.Name())
		}
	}
}

// --- Logging helpers ---

func logInfo(format string, args ...any) {
	fmt.Printf("  ℹ "+format+"\n", args...)
}

func logOK(format string, args ...any) {
	fmt.Printf("  ✓ "+format+"\n", args...)
}

func logWarn(format string, args ...any) {
	fmt.Printf("  ⚠ "+format+"\n", args...)
}

func logError(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "  ✗ "+format+"\n", args...)
}

func logStep(step int, format string, args ...any) {
	fmt.Printf("  [%d] "+format+"\n", append([]any{step}, args...)...)
}
