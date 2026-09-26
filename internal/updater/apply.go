package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// diskSafetyMargin is extra headroom required on top of the download size, so
// an update never fills the filesystem to the brim — the running process still
// needs scratch space for logs, caches and history.json.
const diskSafetyMargin uint64 = 32 << 20 // 32 MiB

// diskFree probes free bytes for the filesystem backing dir. It is a package
// var so tests can stub it; the real implementation lives in the
// platform-specific diskspace_*.go files.
var diskFree = freeDiskBytes

// ErrNoRollback is returned by Rollback when no previous binary is kept.
var ErrNoRollback = errors.New("updater: nothing to rollback to")

// execPath — путь, по которому бинарник ДОЛЖЕН лежать, независимо от того, из
// какого файла его сейчас запустили.
//
// Activate переименовывает работающий файл в "<exe>.old" и кладёт на его место
// новый. Но на Linux os.Executable() читает /proc/self/exe, а он ходит за
// инодой: сразу после этого переименования он отдаёт уже "<exe>.old". Restart
// брал ИМЕННО его и перезапускал процесс из бэкапа — то есть из той самой
// сборки, которую только что заменили. Новый бинарник по "<exe>" не запускался
// никогда, а следующее обновление повторяло фокус на уровень глубже.
//
// Что это давало на проде (найдено 2026-09-11): в логе каждый раз бодрое
// "cluster update: installed, restarting", сервис active, а работающая сборка
// на обеих нодах не менялась с 9 сентября. Рядом накопилось по 24–25 файлов
// вида lampac-go.old.old.old… — около 2.5 ГБ мусора на ноду.
//
// Разматываем цепочку ".old" до первого пути, который реально существует:
// это чинит уже испорченные машины (после установки фикса они сами вернутся на
// канонический путь) и не ломает бинарник, который НАРОЧНО назван "foo.old".
func execPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("executable: %w", err)
	}
	// Резолвим симлинки, чтобы менять настоящий файл, а не ярлык-лаунчер.
	if rp, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = rp
	}
	return stripBackupSuffix(exe, func(p string) bool {
		_, serr := os.Stat(p)
		return serr == nil
	}), nil
}

// stripBackupSuffix снимает хвост из ".old". Кандидаты перебираются от САМОГО
// КОРОТКОГО и возвращается первый существующий — шагать по одному ".old" нельзя:
// в цепочке бывают дыры (промежуточный бэкап подчистили руками или он не влез
// на диск), и пошаговая размотка застревала на первой же дырке, так и не дойдя
// до канонического пути. Если не существует ни один — отдаём путь как есть,
// чтобы не переименовать бинарник, который НАРОЧНО называется "foo.old".
//
// Вынесено отдельно от execPath, чтобы проверялось тестом без подмены
// /proc/self/exe.
func stripBackupSuffix(path string, exists func(string) bool) string {
	base := path
	for strings.HasSuffix(base, ".old") {
		base = strings.TrimSuffix(base, ".old")
	}
	for cand := base; len(cand) <= len(path); cand += ".old" {
		if cand != path && exists(cand) {
			return cand
		}
	}
	return path
}

// runningPath — файл, из которого реально исполняется процесс (с разрешёнными
// симлинками). Отличается от execPath на машине, испорченной старой
// цепочкой ".old": там процесс живёт в бэкапе, и удалять его нельзя.
func runningPath() string {
	running, _ := os.Executable()
	if rp, rerr := filepath.EvalSymlinks(running); rerr == nil {
		running = rp
	}
	return running
}

// pruneBackupChain убирает залежи "<exe>.old.old…", оставшиеся от той самой
// поломки. Трогаем только цепочку глубже одного шага: "<exe>.old" — это
// актуальный откат, а running — файл, из которого сейчас исполняется процесс
// (после починки это уже не встретится, но на испорченной машине встретится
// ровно один раз, и убивать его на всякий случай не будем).
func pruneBackupChain(exe, running string) {
	for depth, path := 2, exe+".old.old"; depth < 64; depth, path = depth+1, path+".old" {
		if path == running {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		if os.Remove(path) == nil {
			log.Warn().Str("path", path).Int64("freed_bytes", fi.Size()).
				Msg("updater: removed leftover backup from the old rename-chain bug")
		}
	}
}

// DownloadResult describes a freshly staged binary ready to be activated.
type DownloadResult struct {
	StagedPath string // path to "<exe>.new" with the downloaded bytes
	Size       int64
	SHA256     string // hex digest of the staged file
}

// DownloadAsset fetches the asset URL into "<currentExe>.new", verifies its
// SHA256 (if wantSHA is non-empty), chmods it executable and returns info
// about the staged file. wantSize is the expected byte size from the release
// metadata; when > 0 it drives a pre-flight free-space check so a full disk
// fails fast with a clear message instead of a raw ENOSPC mid-download.
// token authorizes downloads from password-protected channels. The caller
// is expected to call Activate next.
func DownloadAsset(url, wantSHA string, wantSize int64, token string) (*DownloadResult, error) {
	exe, err := execPath()
	if err != nil {
		return nil, err
	}

	// Уборка залежей ".old.old…" ДО скачивания, а не только в Activate.
	// На испорченной машине это разрывает замкнутый круг: обновление падало
	// раньше активации («create staged: … File name too long» на цепочке в 61
	// звено — упор в NAME_MAX), уборщик жил только в Activate, и мусор
	// оставался навсегда. Заодно освобождает место до проверки диска: на ноде
	// такая цепочка занимала 6 ГБ, а ensureDiskSpace умеет пожертвовать лишь
	// одним "<exe>.old".
	pruneBackupChain(exe, runningPath())

	staged := exe + ".new"
	_ = os.Remove(staged) // leftover from a previous failed attempt

	if wantSize > 0 {
		if err := ensureDiskSpace(exe, wantSize); err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lampac-go-updater")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return nil, fmt.Errorf("create staged: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(staged)
		return nil, fmt.Errorf("write staged: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if wantSHA != "" && got != wantSHA {
		_ = os.Remove(staged)
		return nil, fmt.Errorf("sha256 mismatch: got %s, want %s", got, wantSHA)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(staged, 0o755); err != nil {
			_ = os.Remove(staged)
			return nil, fmt.Errorf("chmod staged: %w", err)
		}
	}

	return &DownloadResult{StagedPath: staged, Size: n, SHA256: got}, nil
}

// ensureDiskSpace verifies the filesystem holding the staged binary has room
// for wantSize bytes plus a safety margin. If it falls short and a stale
// rollback backup ("<exe>.old") is present, that backup is removed to reclaim
// space: Activate recreates a fresh, correct ".old" from the running binary
// right after the download, so the only rollback target sacrificed is the
// stale pre-update one. Returns a clear error (path + figures) when even that
// is not enough.
func ensureDiskSpace(exe string, wantSize int64) error {
	dir := filepath.Dir(exe)
	free, ok := diskFree(dir)
	if !ok {
		return nil // platform can't report free space — skip the check
	}
	needed := uint64(wantSize) + diskSafetyMargin
	if free >= needed {
		return nil
	}

	backup := exe + ".old"
	if fi, statErr := os.Stat(backup); statErr == nil {
		if rmErr := os.Remove(backup); rmErr == nil {
			free += uint64(fi.Size())
			log.Warn().Str("backup", backup).Int64("freed_bytes", fi.Size()).
				Msg("updater: removed stale rollback backup to make room for the new binary")
		}
	}
	if free >= needed {
		return nil
	}
	return fmt.Errorf("not enough disk space in %s: need %s for the new binary, only %s free",
		dir, humanBytes(needed), humanBytes(free))
}

// humanBytes renders a byte count as an approximate MiB/KiB string for messages.
func humanBytes(n uint64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// Activate atomically replaces the running binary with the staged one and
// keeps the old binary as "<exe>.old" for rollback. Call this BEFORE Restart.
func Activate(staged string) (backup string, err error) {
	exe, err := execPath()
	if err != nil {
		return "", err
	}
	pruneBackupChain(exe, runningPath())

	backup = exe + ".old"
	_ = os.Remove(backup)

	// Rename current → backup. If the renamed file is the one currently
	// being executed, that's fine on Linux/macOS — the kernel holds the
	// inode open until the process exits.
	if err := os.Rename(exe, backup); err != nil {
		return "", fmt.Errorf("rename current→backup: %w", err)
	}
	// Move staged → current.
	if err := os.Rename(staged, exe); err != nil {
		// Try to restore backup before giving up.
		_ = os.Rename(backup, exe)
		return "", fmt.Errorf("rename staged→current: %w", err)
	}
	return backup, nil
}

// Rollback restores "<exe>.old" as the running binary. Useful if the new
// build failed to start on the next boot; the supervisor can flip an
// environment variable and invoke this helper.
func Rollback() error {
	exe, err := execPath()
	if err != nil {
		return err
	}
	backup := exe + ".old"
	if _, err := os.Stat(backup); err != nil {
		return ErrNoRollback
	}
	tmp := exe + ".rollback"
	if err := os.Rename(exe, tmp); err != nil {
		return err
	}
	if err := os.Rename(backup, exe); err != nil {
		_ = os.Rename(tmp, exe)
		return err
	}
	_ = os.Remove(tmp)
	return nil
}

// Restart re-executes the current binary. On Unix it uses execve (the
// implementation lives in restart_unix.go), which preserves the PID and
// keeps systemd happy. On Windows it spawns a detached child and calls
// os.Exit(0) — see restart_windows.go.
//
// This function does NOT return on success — the process either replaces
// itself or exits.
func Restart() error {
	// Именно execPath, а не os.Executable: после Activate последний показывает
	// бэкап, и процесс перезапускался из ЗАМЕНЁННОЙ сборки. Подробности — в
	// комментарии к execPath.
	exe, err := execPath()
	if err != nil {
		return err
	}
	args := append([]string{exe}, os.Args[1:]...)
	return platformRestart(exe, args)
}
