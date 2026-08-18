// Package porter provides LLM-powered C# to Go balancer porting.
// It generates Go standalone binaries from C# controller source code
// using a local LLM (e.g., Qwen2.5-Coder via llama.cpp).
package porter

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// PortRequest contains input data for porting.
type PortRequest struct {
	CSCode     string // C# source code
	CSFileName string // source file name (for prompt context)
	Name       string // balancer name (auto-detected if empty)
	Category   string // category: embed/api/dle/playerjs (auto-detected if empty)
}

// PortResult contains the output of a porting operation.
type PortResult struct {
	GoCode   string `json:"go_code"`   // generated Go source code
	BuildOK  bool   `json:"build_ok"`  // whether compilation succeeded
	Attempts int    `json:"attempts"`  // number of LLM attempts (generate + fixes)
	BuildLog string `json:"build_log"` // last build output
	Error    string `json:"error"`     // error message if failed
}

// ProgressFunc is called with progress updates during porting.
// step: 1=generating, 2=building, 3=fixing
// message: human-readable status
type ProgressFunc func(step int, message string)

// Porter orchestrates LLM-powered porting.
type Porter struct {
	LLM        *LLMClient
	MaxRetries int
	Verbose    bool
	OnProgress ProgressFunc // optional progress callback
}

// New creates a new Porter with the given LLM configuration.
func New(llmURL, model string, temp float64, maxRetries int) *Porter {
	return &Porter{
		LLM:        NewLLMClient(llmURL, model, temp),
		MaxRetries: maxRetries,
	}
}

// Generate generates Go code from C# source using the LLM.
// Does NOT compile — returns raw generated code.
func (p *Porter) Generate(ctx context.Context, req PortRequest) (string, error) {
	p.fillDefaults(&req)

	p.progress(1, fmt.Sprintf("Generating Go code for %q (category: %s)...", req.Name, req.Category))

	messages := BuildPortPrompt(req.CSCode, req.CSFileName, req.Category)
	startTime := time.Now()

	rawResponse, err := p.LLM.ChatCompletion(ctx, messages, 8192)
	if err != nil {
		return "", fmt.Errorf("LLM generation: %w", err)
	}

	goCode := ExtractGoCode(rawResponse)
	if p.Verbose {
		log.Printf("porter: LLM generated %d chars in %s", len(goCode), time.Since(startTime).Round(time.Second))
	}

	if goCode == "" || !strings.Contains(goCode, "package main") {
		return "", fmt.Errorf("LLM returned invalid Go code (no 'package main')")
	}

	return goCode, nil
}

// GenerateAndFix generates Go code and iteratively fixes build errors.
// buildFn should compile the code and return (buildOutput, error).
// If error is nil, compilation succeeded.
func (p *Porter) GenerateAndFix(ctx context.Context, req PortRequest, buildFn func(code string) (string, error)) (*PortResult, error) {
	goCode, err := p.Generate(ctx, req)
	if err != nil {
		return &PortResult{Error: err.Error()}, err
	}

	result := &PortResult{
		GoCode:   goCode,
		Attempts: 1,
	}

	// Try building.
	p.progress(2, "Compiling...")
	buildOutput, buildErr := buildFn(goCode)
	if buildErr == nil {
		result.BuildOK = true
		result.BuildLog = buildOutput
		p.progress(2, "Compilation successful")
		return result, nil
	}

	result.BuildLog = buildOutput
	p.progress(3, fmt.Sprintf("Build failed, attempting fix (1/%d)...", p.MaxRetries))

	// Fix loop.
	for attempt := 1; attempt <= p.MaxRetries; attempt++ {
		fixedCode, fixErr := p.fixBuildError(ctx, goCode, buildOutput)
		if fixErr != nil {
			result.Error = fmt.Sprintf("LLM fix attempt %d: %v", attempt, fixErr)
			if p.Verbose {
				log.Printf("porter: fix attempt %d failed: %v", attempt, fixErr)
			}
			continue
		}

		result.Attempts++
		goCode = fixedCode
		result.GoCode = goCode

		p.progress(2, fmt.Sprintf("Build attempt %d/%d...", attempt+1, p.MaxRetries+1))
		buildOutput, buildErr = buildFn(goCode)
		if buildErr == nil {
			result.BuildOK = true
			result.BuildLog = buildOutput
			p.progress(2, "Compilation successful")
			return result, nil
		}

		result.BuildLog = buildOutput
		if attempt < p.MaxRetries {
			p.progress(3, fmt.Sprintf("Build still failing, fix attempt %d/%d...", attempt+1, p.MaxRetries))
		}
	}

	result.Error = fmt.Sprintf("build failed after %d attempts: %s", result.Attempts, truncate(buildOutput, 200))
	return result, fmt.Errorf("build failed after %d attempts", result.Attempts)
}

// fixBuildError asks the LLM to fix compilation errors.
func (p *Porter) fixBuildError(ctx context.Context, goCode, buildErrors string) (string, error) {
	messages := BuildFixPrompt(goCode, buildErrors)
	rawResponse, err := p.LLM.ChatCompletion(ctx, messages, 8192)
	if err != nil {
		return "", fmt.Errorf("LLM fix request: %w", err)
	}

	fixedCode := ExtractGoCode(rawResponse)
	if fixedCode == "" || !strings.Contains(fixedCode, "package main") {
		return "", fmt.Errorf("LLM returned invalid fix")
	}

	return fixedCode, nil
}

// fillDefaults auto-detects name and category if not provided.
func (p *Porter) fillDefaults(req *PortRequest) {
	if req.Name == "" {
		req.Name = ExtractBalancerName(req.CSCode)
	}
	if req.Category == "" {
		req.Category = DetectCategory(req.CSCode)
	}
	if req.CSFileName == "" {
		req.CSFileName = req.Name + ".cs"
	}
}

// progress calls the progress callback if set.
func (p *Porter) progress(step int, message string) {
	if p.OnProgress != nil {
		p.OnProgress(step, message)
	}
}

// DetectContentType detects "movie", "serial", or "both" from C# code.
func DetectContentType(csCode string) string {
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

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
