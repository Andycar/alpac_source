package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lampac-go/internal/porter"
)

// Generator orchestrates the CLI porting pipeline using internal/porter.
type Generator struct {
	porter   *porter.Porter
	deployer *Deployer
	tester   *Tester
}

// NewGenerator creates a new generator.
func NewGenerator(llm *porter.LLMClient, deployer *Deployer, tester *Tester, maxRetries int) *Generator {
	p := &porter.Porter{
		LLM:        llm,
		MaxRetries: maxRetries,
		Verbose:    flagVerbose,
		OnProgress: func(step int, message string) {
			logStep(step, "%s", message)
		},
	}
	return &Generator{
		porter:   p,
		deployer: deployer,
		tester:   tester,
	}
}

// Port reads a C# file, generates Go code via LLM, compiles, and tests.
func (g *Generator) Port(ctx context.Context, csPath string) error {
	// Step 1: Read C# source.
	logStep(1, "Reading C# source: %s", csPath)
	csCode, err := readCSSource(csPath)
	if err != nil {
		return fmt.Errorf("read C# source: %w", err)
	}

	name := porter.ExtractBalancerName(csCode)
	if name == "" {
		name = strings.TrimSuffix(strings.ToLower(filepath.Base(csPath)), ".cs")
	}

	req := porter.PortRequest{
		CSCode:     csCode,
		CSFileName: filepath.Base(csPath),
		Name:       name,
	}

	// Build function for the porter.
	buildFn := func(code string) (string, error) {
		return g.deployer.WriteAndBuild(name, code)
	}

	result, err := g.porter.GenerateAndFix(ctx, req, buildFn)
	if err != nil {
		return fmt.Errorf("porter: %w", err)
	}

	if !result.BuildOK {
		return fmt.Errorf("build failed: %s", result.Error)
	}

	logOK("Compilation successful (%d attempts)", result.Attempts)

	// Deploy.
	port := g.deployer.AllocPort(name)
	cfg := BalancerConfig{
		Name:         name,
		Port:         port,
		QualityBadge: "FHD",
		ContentType:  detectContentType(csCode),
		AutoStart:    true,
	}
	if err := g.deployer.WriteConfig(name, cfg); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	logStep(5, "Starting process on port %d...", port)
	if err := g.deployer.StartProcess(name, port); err != nil {
		logWarn("Process start failed: %v (may need manual start)", err)
	} else {
		logOK("Process started successfully")
	}

	// Test checksearch.
	logStep(6, "Testing checksearch...")
	addr := fmt.Sprintf("http://127.0.0.1:%d", port)
	testKPIDs := []string{"326", "839380", "535341"}
	tested := false
	for _, kpID := range testKPIDs {
		resp, ok := g.tester.CheckSearch(ctx, addr, name, kpID)
		if ok {
			logOK("checksearch kp=%s: OK — %s", kpID, resp)
			tested = true
			break
		}
		if flagVerbose {
			logInfo("checksearch kp=%s: %s", kpID, resp)
		}
	}

	if !tested {
		logWarn("checksearch did not return rch:true for test IDs")
		logInfo("This may be expected if the upstream is not accessible from this machine")
	}

	g.deployer.StopProcess(name)
	logOK("Porter complete for %s → %s/%s/", name, g.deployer.baseDir, name)
	return nil
}

// Fix re-generates and fixes an existing custom balancer.
func (g *Generator) Fix(ctx context.Context, name string) error {
	goCode, err := g.deployer.ReadMainGo(name)
	if err != nil {
		return fmt.Errorf("read existing code: %w", err)
	}

	// Try to compile first.
	buildOutput, buildErr := g.deployer.WriteAndBuild(name, goCode)
	if buildErr != nil {
		logInfo("Current code has build errors, fixing via LLM...")

		req := porter.PortRequest{Name: name, CSCode: goCode}
		buildFn := func(code string) (string, error) {
			return g.deployer.WriteAndBuild(name, code)
		}

		result, fixErr := g.porter.GenerateAndFix(ctx, req, buildFn)
		if fixErr != nil {
			return fmt.Errorf("could not fix: %w", fixErr)
		}
		if !result.BuildOK {
			return fmt.Errorf("fix did not compile: %s", result.Error)
		}
		logOK("Build errors fixed")
	} else {
		logOK("Code compiles: %s", buildOutput)
	}

	// Start and test.
	port := g.deployer.PortForName(name)
	if port == 0 {
		port = g.deployer.AllocPort(name)
	}

	if err := g.deployer.StartProcess(name, port); err != nil {
		return fmt.Errorf("start process: %w", err)
	}
	defer g.deployer.StopProcess(name)

	addr := fmt.Sprintf("http://127.0.0.1:%d", port)
	resp, ok := g.tester.CheckSearch(ctx, addr, name, "839380")
	if ok {
		logOK("checksearch OK: %s", resp)
		return nil
	}

	logWarn("checksearch failed: %s", resp)
	return nil
}

// readCSSource reads a C# source file or merges files from a directory.
func readCSSource(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}

	if !info.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	// Directory: merge all .cs files.
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".cs") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, e.Name()))
		if err != nil {
			continue
		}
		sb.WriteString("// === FILE: " + e.Name() + " ===\n")
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	if sb.Len() == 0 {
		return "", fmt.Errorf("no .cs files found in %s", path)
	}
	return sb.String(), nil
}

// detectContentType detects "movie", "serial", or "both" from C# code.
func detectContentType(csCode string) string {
	hasMovie := strings.Contains(csCode, "MovieTpl") || strings.Contains(csCode, "StreamTpl")
	hasSerial := strings.Contains(csCode, "SeasonTpl") || strings.Contains(csCode, "EpisodeTpl") ||
		strings.Contains(csCode, "int s =") || strings.Contains(csCode, "season")

	if hasMovie && hasSerial {
		return "both"
	}
	if hasSerial {
		return "serial"
	}
	return "movie"
}
