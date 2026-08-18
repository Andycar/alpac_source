package wasmmodules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Studio compiles plugin sources written in the admin UI's Monaco editor
// into a .wasm next to the live wasm_modules dir. It's a deliberately small
// surface — exactly two operations:
//
//   - Build(req)   compile a single source string + manifest snippet.
//   - Install(id)  copy the build artefact into wasm_modules/<id>/ so the
//                  Manager picks it up via the existing fsnotify Watcher.
//
// We invoke the user's tinygo / cargo binaries; the studio doesn't try to
// sandbox them. That's deferred to (a) docker on hub.alcopa.cc, (b) seccomp
// on Linux. For self-hosted lampac this is the same trust level as editing
// jsmodules — admin already has shell access.
type Studio struct {
	root      string // {repoRoot}/wasm_studio — staging area for builds
	mgrTarget string // BaseDir of Manager (where Install drops the result)
	mgr       *Manager
}

// NewStudio sets up the staging dir. mgr.BaseDir is used as the install target.
func NewStudio(mgr *Manager) (*Studio, error) {
	if mgr == nil {
		return nil, errors.New("studio: manager required")
	}
	root := filepath.Join(filepath.Dir(mgr.BaseDir), "wasm_studio")
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	return &Studio{root: root, mgrTarget: mgr.BaseDir, mgr: mgr}, nil
}

// BuildRequest is the JSON body posted by the studio UI.
type BuildRequest struct {
	ID       string `json:"id"`        // module ID (a-z, _, 1-31 chars)
	Language string `json:"language"`  // "tinygo" | "rust" | "as"
	Manifest json.RawMessage `json:"manifest"` // user-edited manifest.json
	Source   string `json:"source"`    // single-file source — main.go / lib.rs / index.ts
}

// BuildResult is what the studio UI displays after a build attempt.
type BuildResult struct {
	OK       bool   `json:"ok"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	WASMPath string `json:"wasm_path,omitempty"`
	WASMSize int64  `json:"wasm_size,omitempty"`
	WASMHash string `json:"wasm_hash,omitempty"`
	BuildID  string `json:"build_id"`
	Took     string `json:"took"`
}

// Build runs the toolchain on the supplied sources in a fresh staging dir.
// Successful builds leave a `plugin.wasm` and `manifest.json` ready for
// Install(). Failed builds keep the work tree around for debugging.
func (s *Studio) Build(ctx context.Context, req BuildRequest) (BuildResult, error) {
	if !moduleIDRE.MatchString(req.ID) {
		return BuildResult{}, fmt.Errorf("invalid id %q (must match %s)", req.ID, moduleIDRE)
	}
	switch req.Language {
	case "tinygo", "rust", "as", "zig", "c":
	default:
		return BuildResult{}, fmt.Errorf("unsupported language %q", req.Language)
	}
	stagingDir := filepath.Join(s.root, req.ID)
	if err := os.RemoveAll(stagingDir); err != nil {
		return BuildResult{}, err
	}
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return BuildResult{}, err
	}

	// Always write a manifest first — Validate so we fail before the slow
	// toolchain step on bad input.
	if err := s.writeManifest(stagingDir, req); err != nil {
		return BuildResult{}, err
	}

	switch req.Language {
	case "tinygo":
		return s.buildTinyGo(ctx, stagingDir, req)
	case "rust":
		return s.buildRust(ctx, stagingDir, req)
	case "as":
		return s.buildAssemblyScript(ctx, stagingDir, req)
	case "zig":
		return s.buildZig(ctx, stagingDir, req)
	case "c":
		return s.buildC(ctx, stagingDir, req)
	}
	return BuildResult{}, errors.New("unreachable")
}

// buildZig writes main.zig + a minimal build.zig pointing at the SDK and
// invokes `zig build`. Output `plugin.wasm` is then copied next to
// manifest.json so Install treats this exactly like the other languages.
func (s *Studio) buildZig(ctx context.Context, dir string, req BuildRequest) (BuildResult, error) {
	if err := os.WriteFile(filepath.Join(dir, "main.zig"), []byte(req.Source), 0644); err != nil {
		return BuildResult{}, err
	}
	sdkPath, err := filepath.Rel(dir, filepath.Join(filepath.Dir(s.mgrTarget), "wasm_sdk", "zig", "lampac.zig"))
	if err != nil {
		return BuildResult{}, err
	}
	build := fmt.Sprintf(`const std = @import("std");

pub fn build(b: *std.Build) void {
    const target = b.resolveTargetQuery(.{ .cpu_arch = .wasm32, .os_tag = .wasi });
    const exe = b.addExecutable(.{
        .name = "plugin",
        .root_source_file = b.path("main.zig"),
        .target = target,
        .optimize = .ReleaseSmall,
    });
    exe.root_module.addAnonymousImport("lampac", .{ .root_source_file = b.path(%q) });
    exe.entry = .disabled;
    exe.rdynamic = true;
    b.installArtifact(exe);
}
`, sdkPath)
	if err := os.WriteFile(filepath.Join(dir, "build.zig"), []byte(build), 0644); err != nil {
		return BuildResult{}, err
	}
	res, err := runBuild(ctx, dir, "zig", []string{"build"})
	if err != nil {
		return res, err
	}
	if res.OK {
		// zig build emits zig-out/bin/plugin.wasm; we copy it next to
		// manifest.json so the staging dir matches the other backends.
		src := filepath.Join(dir, "zig-out", "bin", "plugin.wasm")
		if err := copyFile(src, filepath.Join(dir, "plugin.wasm")); err != nil {
			res.OK = false
			res.Stderr += "\ncopy artefact: " + err.Error()
			return res, nil
		}
		res = withWASMMeta(res, filepath.Join(dir, "plugin.wasm"))
	}
	return res, nil
}

// buildC writes main.c, copies the SDK headers/sources alongside, and
// invokes wasi-sdk's clang in reactor mode. The `WASI_SDK` env var (or
// $HOME/wasi-sdk default) tells us where clang lives.
func (s *Studio) buildC(ctx context.Context, dir string, req BuildRequest) (BuildResult, error) {
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte(req.Source), 0644); err != nil {
		return BuildResult{}, err
	}
	sdkDir := filepath.Join(filepath.Dir(s.mgrTarget), "wasm_sdk", "c")
	wasiSDK := os.Getenv("WASI_SDK")
	if wasiSDK == "" {
		wasiSDK = filepath.Join(os.Getenv("HOME"), "wasi-sdk")
	}
	clang := filepath.Join(wasiSDK, "bin", "clang")
	args := []string{
		"--target=wasm32-wasi", "-mexec-model=reactor",
		"-O2", "-nostartfiles",
		"-I" + sdkDir,
		"-Wl,--no-entry",
		"-Wl,--export=alloc",
		"-Wl,--export=handle",
		"-Wl,--export=abi_version",
		"-o", "plugin.wasm",
		filepath.Join(sdkDir, "lampac.c"), "main.c",
	}
	res, err := runBuild(ctx, dir, clang, args)
	if err != nil {
		return res, err
	}
	if res.OK {
		res = withWASMMeta(res, filepath.Join(dir, "plugin.wasm"))
	}
	return res, nil
}

func (s *Studio) writeManifest(dir string, req BuildRequest) error {
	var mf Manifest
	if len(req.Manifest) > 0 {
		if err := json.Unmarshal(req.Manifest, &mf); err != nil {
			return fmt.Errorf("manifest parse: %w", err)
		}
	}
	mf.ID = req.ID
	if mf.Language == "" {
		mf.Language = req.Language
	}
	if err := mf.Validate(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(&mf, "", "  ")
	return os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0644)
}

func (s *Studio) buildTinyGo(ctx context.Context, dir string, req BuildRequest) (BuildResult, error) {
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(req.Source), 0644); err != nil {
		return BuildResult{}, err
	}
	// Resolve the SDK path relative to the staging dir so `replace` works.
	sdkPath, err := filepath.Rel(dir, filepath.Join(filepath.Dir(s.mgrTarget), "wasm_sdk", "tinygo"))
	if err != nil {
		return BuildResult{}, err
	}
	goMod := fmt.Sprintf("module lampac.cc/wasm-studio/%s\n\ngo 1.22\n\nrequire lampac.cc/sdk/lampac v0.0.0\n\nreplace lampac.cc/sdk/lampac => %s\n",
		req.ID, sdkPath)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		return BuildResult{}, err
	}
	return runBuild(ctx, dir, "tinygo", []string{"build", "-o", "plugin.wasm", "-target", "wasi", "-no-debug", "./"})
}

func (s *Studio) buildRust(ctx context.Context, dir string, req BuildRequest) (BuildResult, error) {
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return BuildResult{}, err
	}
	if err := os.WriteFile(filepath.Join(srcDir, "lib.rs"), []byte(req.Source), 0644); err != nil {
		return BuildResult{}, err
	}
	sdkPath, err := filepath.Rel(dir, filepath.Join(filepath.Dir(s.mgrTarget), "wasm_sdk", "rust"))
	if err != nil {
		return BuildResult{}, err
	}
	cargo := fmt.Sprintf(`[package]
name = %q
version = "0.0.1"
edition = "2021"

[lib]
crate-type = ["cdylib"]

[dependencies]
lampac-sdk = { path = %q }
serde = { version = "1.0", features = ["derive"] }
serde_json = "1.0"

[profile.release]
opt-level = "z"
lto = true
strip = true
`, req.ID, sdkPath)
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(cargo), 0644); err != nil {
		return BuildResult{}, err
	}
	res, err := runBuild(ctx, dir, "cargo", []string{"build", "--release", "--target", "wasm32-wasip1"})
	if err != nil {
		return res, err
	}
	if res.OK {
		// Cargo writes target/<triple>/release/<name>.wasm — copy it next to
		// manifest.json so the Install path stays uniform.
		src := filepath.Join(dir, "target", "wasm32-wasip1", "release", req.ID+".wasm")
		if err := copyFile(src, filepath.Join(dir, "plugin.wasm")); err != nil {
			res.OK = false
			res.Stderr += "\ncopy artefact: " + err.Error()
			return res, nil
		}
		res = withWASMMeta(res, filepath.Join(dir, "plugin.wasm"))
	}
	return res, nil
}

func (s *Studio) buildAssemblyScript(ctx context.Context, dir string, req BuildRequest) (BuildResult, error) {
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte(req.Source), 0644); err != nil {
		return BuildResult{}, err
	}
	pkg := `{"name":"plugin","version":"0.0.1","scripts":{"build":"asc index.ts -o plugin.wasm --runtime stub --optimize"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
		return BuildResult{}, err
	}
	return runBuild(ctx, dir, "npx", []string{"-y", "asc", "index.ts", "-o", "plugin.wasm", "--runtime", "stub", "--optimize"})
}

// Install copies the staged plugin.wasm + manifest.json into the live
// wasm_modules dir. The Manager's fsnotify Watcher then picks it up; admin
// can immediately call /lite/{id} to test.
func (s *Studio) Install(id string) error {
	if !moduleIDRE.MatchString(id) {
		return fmt.Errorf("invalid id %q", id)
	}
	stage := filepath.Join(s.root, id)
	dest := filepath.Join(s.mgrTarget, id)
	if _, err := os.Stat(filepath.Join(stage, "plugin.wasm")); err != nil {
		return fmt.Errorf("no plugin.wasm in staging — build first: %w", err)
	}
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(stage, "plugin.wasm"), filepath.Join(dest, "plugin.wasm")); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(stage, "manifest.json"), filepath.Join(dest, "manifest.json")); err != nil {
		return err
	}
	// Trigger a manual scan so the runtime picks the new module up
	// regardless of fsnotify timing.
	return s.mgr.Scan()
}

// runBuild executes `bin args...` in dir, captures stdout/stderr, populates
// BuildResult with hash + size on success.
func runBuild(ctx context.Context, dir, bin string, args []string) (BuildResult, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return BuildResult{
			BuildID: buildID(),
			Stderr:  fmt.Sprintf("toolchain not installed: %s — install it on the lampac host first", bin),
		}, nil
	}
	bctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bctx, bin, args...)
	cmd.Dir = dir
	stdout := &strings.Builder{}
	stderr := &strings.Builder{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	start := time.Now()
	err := cmd.Run()
	res := BuildResult{
		BuildID: buildID(),
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Took:    time.Since(start).Round(time.Millisecond).String(),
	}
	if err != nil {
		return res, nil
	}
	res.OK = true
	wasmPath := filepath.Join(dir, "plugin.wasm")
	res = withWASMMeta(res, wasmPath)
	return res, nil
}

func withWASMMeta(res BuildResult, wasmPath string) BuildResult {
	fi, err := os.Stat(wasmPath)
	if err != nil {
		res.OK = false
		res.Stderr += "\nplugin.wasm missing after build: " + err.Error()
		return res
	}
	res.WASMPath = wasmPath
	res.WASMSize = fi.Size()
	if data, err := os.ReadFile(wasmPath); err == nil {
		sum := sha256.Sum256(data)
		res.WASMHash = hex.EncodeToString(sum[:])
	}
	return res
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// buildID is a short cache-busting tag the UI shows in build history.
func buildID() string {
	return time.Now().UTC().Format("20060102-150405.000")
}
