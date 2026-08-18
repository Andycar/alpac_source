package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// BalancerConfig mirrors custbal.BalancerConfig for standalone use.
type BalancerConfig struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name,omitempty"`
	Port         int    `json:"port"`
	QualityBadge string `json:"quality_badge,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	Host         string `json:"host,omitempty"`
	AutoStart    bool   `json:"auto_start"`
}

// Deployer handles compilation and subprocess management for custom balancers.
type Deployer struct {
	baseDir  string
	mainHost string
	basePort int
	procs    map[string]*exec.Cmd
}

// NewDeployer creates a new deployer.
func NewDeployer(baseDir, mainHost string, basePort int) *Deployer {
	return &Deployer{
		baseDir:  baseDir,
		mainHost: mainHost,
		basePort: basePort,
		procs:    make(map[string]*exec.Cmd),
	}
}

// WriteAndBuild writes Go source and compiles it.
func (d *Deployer) WriteAndBuild(name, goCode string) (string, error) {
	dir := filepath.Join(d.baseDir, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	// Write main.go.
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(goCode), 0644); err != nil {
		return "", fmt.Errorf("write main.go: %w", err)
	}

	// Write go.mod.
	goMod := fmt.Sprintf("module custbal-%s\n\ngo 1.22\n", name)
	goModPath := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(goModPath, []byte(goMod), 0644); err != nil {
		return "", fmt.Errorf("write go.mod: %w", err)
	}

	// Compile.
	goPath := findGo()
	cmd := exec.Command(goPath, "build", "-o", name, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		return output, fmt.Errorf("go build failed: %s", output)
	}

	// Make executable.
	_ = os.Chmod(filepath.Join(dir, name), 0755)

	return output, nil
}

// WriteConfig writes config.json for the balancer.
func (d *Deployer) WriteConfig(name string, cfg BalancerConfig) error {
	dir := filepath.Join(d.baseDir, name)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0644)
}

// StartProcess starts a balancer subprocess.
func (d *Deployer) StartProcess(name string, port int) error {
	dir := filepath.Join(d.baseDir, name)
	binPath := filepath.Join(dir, name)

	// Stop existing process.
	d.StopProcess(name)

	args := []string{"-port", fmt.Sprintf("%d", port)}
	if d.mainHost != "" {
		args = append(args, "-main-host", d.mainHost)
	}

	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}

	d.procs[name] = cmd

	// Wait for readiness (TCP dial).
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		// Check if process exited.
		if cmd.ProcessState != nil {
			return fmt.Errorf("process %s exited prematurely", name)
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("timeout waiting for %s on %s", name, addr)
}

// StopProcess stops a running subprocess.
func (d *Deployer) StopProcess(name string) {
	cmd, ok := d.procs[name]
	if !ok || cmd.Process == nil {
		return
	}

	// Graceful: SIGINT.
	_ = cmd.Process.Signal(syscall.SIGINT)

	// Wait up to 5 seconds.
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}

	delete(d.procs, name)
}

// AllocPort finds a free port for a balancer.
func (d *Deployer) AllocPort(name string) int {
	// Scan existing configs for used ports.
	used := make(map[int]bool)
	entries, _ := os.ReadDir(d.baseDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cfgPath := filepath.Join(d.baseDir, e.Name(), "config.json")
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		var cfg BalancerConfig
		if json.Unmarshal(data, &cfg) == nil && cfg.Port > 0 {
			used[cfg.Port] = true
		}
	}

	port := d.basePort
	for used[port] {
		port++
	}
	return port
}

// PortForName returns the configured port for a balancer, or 0 if not found.
func (d *Deployer) PortForName(name string) int {
	cfgPath := filepath.Join(d.baseDir, name, "config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return 0
	}
	var cfg BalancerConfig
	if json.Unmarshal(data, &cfg) != nil {
		return 0
	}
	return cfg.Port
}

// ReadMainGo reads the main.go of an existing balancer.
func (d *Deployer) ReadMainGo(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(d.baseDir, name, "main.go"))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Alive checks if a subprocess is listening on its port.
func (d *Deployer) Alive(_ context.Context, port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// findGo locates the Go compiler.
func findGo() string {
	for _, p := range []string{
		"/usr/local/go/bin/go",
		"/usr/bin/go",
		"/opt/homebrew/bin/go",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return "go"
}
