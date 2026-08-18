package envpresets

import (
	"archive/zip"
	"bytes"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Manager owns the env_presets/ directory and tracks installed presets.
//
// Concurrency:
//   - mu protects the in-memory map of loaded presets.
//   - Install/Uninstall serialize through mu (one preset at a time on disk).
//   - Read-only methods (List, Get) take RLock.
type Manager struct {
	dir        string // {repoRoot}/env_presets
	lampacRoot string // путь к корню lampac-go (для {{lampac_root}})
	lampacUser string // OS-юзер, под которым работает lampac (для {{lampac_user}})
	logger     *zerolog.Logger

	mu      sync.RWMutex
	presets map[string]*Preset
}

// Preset — runtime view одного установленного пресета.
type Preset struct {
	ID       string
	Manifest *Manifest
	State    *InstallState // .installed.json — список dst-файлов
	Config   map[string]any // .config.json — пользовательские значения параметров
	Dir      string         // абсолютный путь {repoRoot}/env_presets/{id}
	LoadedAt time.Time
	LastErr  string // ошибка последней операции, если была
}

// NewManager creates a manager and scans existing installed presets.
func NewManager(dir, lampacRoot, lampacUser string, logger *zerolog.Logger) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create env_presets dir: %w", err)
	}
	m := &Manager{
		dir:        dir,
		lampacRoot: lampacRoot,
		lampacUser: lampacUser,
		logger:     logger,
		presets:    map[string]*Preset{},
	}
	m.loadAll()
	return m, nil
}

// loadAll сканирует dir и загружает manifest+state для каждого подкаталога.
func (m *Manager) loadAll() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if !idRe.MatchString(id) {
			continue
		}
		if p := m.loadOne(id); p != nil {
			m.presets[id] = p
		}
	}
}

func (m *Manager) loadOne(id string) *Preset {
	dir := filepath.Join(m.dir, id)
	man, err := LoadManifest(dir)
	if err != nil {
		if m.logger != nil {
			m.logger.Warn().Err(err).Str("id", id).Msg("envpresets: bad manifest")
		}
		return &Preset{ID: id, Dir: dir, LastErr: err.Error()}
	}
	p := &Preset{ID: id, Manifest: man, Dir: dir, LoadedAt: time.Now()}
	if data, err := os.ReadFile(filepath.Join(dir, ".config.json")); err == nil {
		_ = json.Unmarshal(data, &p.Config)
	}
	if data, err := os.ReadFile(filepath.Join(dir, ".installed.json")); err == nil {
		var st InstallState
		if err := json.Unmarshal(data, &st); err == nil {
			p.State = &st
		}
	}
	return p
}

// List returns all known presets (sorted by ID for deterministic UI).
func (m *Manager) List() []*Preset {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Preset, 0, len(m.presets))
	for _, p := range m.presets {
		out = append(out, p)
	}
	// stable order by ID
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].ID < out[i].ID {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Get returns one preset or nil.
func (m *Manager) Get(id string) *Preset {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.presets[id]
}

// InstallFromZIP — основной entry-point: распаковать ZIP, прочитать manifest,
// провалидировать config, рендерить шаблоны, скопировать файлы, выполнить
// post_install. Возвращает результат с подробным логом каждого шага (для UI).
//
// Если onlyPlan == true — НЕ выполняет реальные действия, только показывает
// что будет (preview-режим).
func (m *Manager) InstallFromZIP(ctx context.Context, zipBytes []byte, userConfig map[string]any, onlyPlan bool) (*InstallResult, error) {
	res := &InstallResult{}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return res, fmt.Errorf("not a valid zip: %w", err)
	}

	// Найти + распарсить manifest.json.
	var (
		manFile *zip.File
		prefix  string
		entries = map[string]*zip.File{}
	)
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, `/`)
		if strings.HasSuffix(name, "/") || strings.Contains(name, "..") {
			continue
		}
		entries[name] = f
		if name == "manifest.json" {
			manFile = f
		} else if strings.HasSuffix(name, "/manifest.json") && strings.Count(name, "/") == 1 {
			if manFile == nil {
				manFile = f
				prefix = name[:len(name)-len("manifest.json")]
			}
		}
	}
	if manFile == nil {
		return res, errors.New("manifest.json not found")
	}
	rc, err := manFile.Open()
	if err != nil {
		return res, err
	}
	manRaw, _ := io.ReadAll(io.LimitReader(rc, 256*1024))
	rc.Close()
	var man Manifest
	if err := json.Unmarshal(manRaw, &man); err != nil {
		return res, fmt.Errorf("manifest.json: %w", err)
	}
	if err := man.Validate(); err != nil {
		return res, err
	}
	res.Manifest = &man

	// Validate user config против manifest.params.
	cfg, errs := ValidateConfig(&man, userConfig)
	if len(errs) > 0 {
		return res, fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	res.Config = cfg

	// Сборка template-vars.
	cfgStr := map[string]any{}
	for k, v := range cfg {
		cfgStr[k] = v
	}
	vars := BuildVars(&man, cfgStr, m.lampacRoot, m.lampacUser)
	res.Vars = vars

	// Гонка root-проверки.
	isRoot := os.Geteuid() == 0
	if man.RequiresRoot && !isRoot {
		return res, errors.New("этот пресет требует root-прав (manifest.requires_root: true), но lampac-go запущен под обычным пользователем")
	}

	// Подготовить план: какие файлы копировать, какие команды выполнять.
	plan := []FilePlan{}
	for _, fs := range man.Files {
		dst := RenderString(fs.Dst, vars)
		if err := validateDstPath(dst, m.lampacRoot); err != nil {
			return res, fmt.Errorf("file %q: %w", fs.Src, err)
		}
		if fs.NeedsRoot && !isRoot {
			plan = append(plan, FilePlan{Src: fs.Src, Dst: dst, Skipped: true, SkipReason: "needs root"})
			continue
		}
		srcPath := prefix + fs.Src
		zf, ok := entries[srcPath]
		if !ok {
			return res, fmt.Errorf("file %q: src not in archive (%s)", fs.Src, srcPath)
		}
		plan = append(plan, FilePlan{
			Src:      fs.Src,
			Dst:      dst,
			Mode:     fs.Mode,
			Template: fs.Template,
			zipEntry: zf,
		})
	}
	res.Files = plan

	// Рендер post_install (только для preview/plan).
	cmds := make([]string, len(man.PostInstall))
	for i, c := range man.PostInstall {
		cmds[i] = RenderString(c, vars)
	}
	res.PostInstall = cmds

	if onlyPlan {
		return res, nil
	}

	// Реальная установка с lock-ом.
	m.mu.Lock()
	defer m.mu.Unlock()

	dir := filepath.Join(m.dir, man.ID)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		return res, err
	}

	// Сохраняем manifest.json и .config.json для inspection / future re-install.
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manRaw, 0o644); err != nil {
		return res, err
	}
	cfgRaw, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, ".config.json"), cfgRaw, 0o600); err != nil {
		return res, err
	}

	// Распаковать src/ — оставляем оригиналы (без template-рендера) для возможной
	// перенастройки.
	for name, zf := range entries {
		// "prefix/src.pac" → "src/src.pac" внутри dir, оставляем без prefix.
		rel := strings.TrimPrefix(name, prefix)
		if rel == "manifest.json" {
			continue
		}
		dstFile := filepath.Join(dir, "src", rel)
		_ = os.MkdirAll(filepath.Dir(dstFile), 0o755)
		if err := extractZipFile(zf, dstFile); err != nil {
			return res, err
		}
	}

	// Копируем файлы по dst, рендерим template если надо.
	state := &InstallState{
		Version: man.Version,
		Files:   []InstalledFile{},
		Config:  cfg,
	}
	for i := range plan {
		fp := &plan[i]
		if fp.Skipped {
			continue
		}
		body, err := readZipBytes(fp.zipEntry)
		if err != nil {
			return res, fmt.Errorf("read %q: %w", fp.Src, err)
		}
		if fp.Template {
			body = []byte(RenderString(string(body), vars))
		}
		if err := os.MkdirAll(filepath.Dir(fp.Dst), 0o755); err != nil {
			return res, fmt.Errorf("mkdir %q: %w", filepath.Dir(fp.Dst), err)
		}
		mode := fileMode(fp.Mode, 0o644)
		if err := os.WriteFile(fp.Dst, body, mode); err != nil {
			return res, fmt.Errorf("write %q: %w", fp.Dst, err)
		}
		fp.Written = true
		hash := sha256.Sum256(body)
		state.Files = append(state.Files, InstalledFile{
			Dst:    fp.Dst,
			SHA256: hex.EncodeToString(hash[:]),
			Mode:   fp.Mode,
		})
	}

	// Запускаем post_install — но ТОЛЬКО если admin отдельно подтвердил.
	// Это в данном API делается через флаг RunPostInstall в res; вызывающий
	// (handler) отдельно вызывает RunPostInstall(ctx, p) после явного confirm.

	// Сохраняем state.
	stateRaw, _ := json.MarshalIndent(state, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, ".installed.json"), stateRaw, 0o600)

	// Регистрируем preset в map.
	m.presets[man.ID] = &Preset{
		ID:       man.ID,
		Manifest: &man,
		State:    state,
		Config:   cfg,
		Dir:      dir,
		LoadedAt: time.Now(),
	}

	return res, nil
}

// RunPostInstall запускает manifest.post_install команды для уже установленного
// пресета. Каждая команда выполняется отдельно через `sh -c`. Возвращает
// результат с stdout/stderr и exit code каждой.
//
// Защита: не выполняем если res.Manifest = nil. Команды НЕ объединяются через &&
// чтобы каждая шла независимо (если первая упала — следующие всё равно идут,
// admin видит лог).
func (m *Manager) RunPostInstall(ctx context.Context, id string) ([]CmdResult, error) {
	m.mu.RLock()
	p := m.presets[id]
	m.mu.RUnlock()
	if p == nil || p.Manifest == nil {
		return nil, fmt.Errorf("preset %q not installed", id)
	}
	vars := BuildVars(p.Manifest, p.Config, m.lampacRoot, m.lampacUser)
	out := make([]CmdResult, 0, len(p.Manifest.PostInstall))
	for _, raw := range p.Manifest.PostInstall {
		cmd := RenderString(raw, vars)
		out = append(out, runShell(ctx, cmd))
	}
	return out, nil
}

// Uninstall удаляет файлы по .installed.json + запускает post_uninstall.
func (m *Manager) Uninstall(ctx context.Context, id string) (*UninstallResult, error) {
	m.mu.Lock()
	p := m.presets[id]
	m.mu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("preset %q not installed", id)
	}
	res := &UninstallResult{}

	// Запускаем post_uninstall ДО удаления файлов (systemctl disable пока сервис ещё есть).
	if p.Manifest != nil {
		vars := BuildVars(p.Manifest, p.Config, m.lampacRoot, m.lampacUser)
		for _, raw := range p.Manifest.PostUninstall {
			cmd := RenderString(raw, vars)
			res.PostUninstall = append(res.PostUninstall, runShell(ctx, cmd))
		}
	}

	// Удаляем файлы.
	if p.State != nil {
		for _, f := range p.State.Files {
			err := os.Remove(f.Dst)
			res.Removed = append(res.Removed, RemovedFile{Dst: f.Dst, Err: errStr(err)})
		}
	}

	// Удаляем preset-каталог целиком.
	if err := os.RemoveAll(p.Dir); err != nil {
		return res, fmt.Errorf("remove preset dir: %w", err)
	}

	m.mu.Lock()
	delete(m.presets, id)
	m.mu.Unlock()
	return res, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────

// validateDstPath enforces dst whitelist: пресет может писать только в:
//   - {lampacRoot}/...     (внутри установки lampac-go)
//   - /etc/systemd/system/X.service
//   - /usr/local/bin/X
//   - /etc/lampac-go/X
func validateDstPath(dst, lampacRoot string) error {
	if strings.Contains(dst, "..") {
		return fmt.Errorf("path contains '..'")
	}
	dst = filepath.Clean(dst)
	root := filepath.Clean(lampacRoot)
	prefixes := []string{
		root + string(filepath.Separator),
		"/etc/systemd/system/",
		"/usr/local/bin/",
		"/etc/lampac-go/",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(dst, p) || dst == strings.TrimSuffix(p, "/") {
			return nil
		}
	}
	return fmt.Errorf("dst %q outside whitelist (lampac_root, /etc/systemd/system/, /usr/local/bin/, /etc/lampac-go/)", dst)
}

func extractZipFile(zf *zip.File, dst string) error {
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

func readZipBytes(zf *zip.File) ([]byte, error) {
	rc, err := zf.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func fileMode(modeStr string, dflt os.FileMode) os.FileMode {
	if modeStr == "" {
		return dflt
	}
	s := modeStr
	if !strings.HasPrefix(s, "0") {
		s = "0" + s
	}
	n, err := strconv.ParseInt(s, 8, 32)
	if err != nil {
		return dflt
	}
	return os.FileMode(n)
}

// runShell выполняет одну команду через `sh -c` с 60-секундным timeout.
// Никаких pipe-обработок не делаем — администратор пишет команды как для shell.
func runShell(ctx context.Context, cmd string) CmdResult {
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "sh", "-c", cmd).CombinedOutput()
	res := CmdResult{Cmd: cmd, Output: string(out)}
	if err != nil {
		res.Err = err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
	}
	return res
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ─── result types ────────────────────────────────────────────────────────

// InstallResult — что мы сделали (или собираемся сделать в plan-mode).
type InstallResult struct {
	Manifest    *Manifest      `json:"manifest"`
	Config      map[string]any `json:"config"`
	Vars        map[string]string `json:"vars"`
	Files       []FilePlan     `json:"files"`
	PostInstall []string       `json:"post_install"`
}

type FilePlan struct {
	Src        string `json:"src"`
	Dst        string `json:"dst"`
	Mode       string `json:"mode,omitempty"`
	Template   bool   `json:"template,omitempty"`
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
	Written    bool   `json:"written,omitempty"`
	zipEntry   *zip.File
}

type CmdResult struct {
	Cmd      string `json:"cmd"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
	Err      string `json:"err,omitempty"`
}

type UninstallResult struct {
	Removed       []RemovedFile `json:"removed"`
	PostUninstall []CmdResult   `json:"post_uninstall,omitempty"`
}

type RemovedFile struct {
	Dst string `json:"dst"`
	Err string `json:"err,omitempty"`
}
