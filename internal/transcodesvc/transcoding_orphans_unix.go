//go:build !windows

package transcodesvc

// transcoding_orphans_unix.go — сторож «ffmpeg вне учёта».
//
// Разбор 26.09.2026 на боксе транскодинга (нода DE-NUXOA): 15 ffmpeg по 16–34 часа, которых
// сервис уже не вёл (в /transcoding/stats — одно задание), 14 из них — стоящие на SIGSTOP паузе
// окна, все в удалённых папках заданий; вместе 12 ГБ памяти. Плюс два десятка зомби. Какой путь
// теряет задание из учёта, не убив процесс, по журналу не восстановился — поэтому здесь не
// починка одного пути, а сетка под все: всё, что работает в папке задания, которого больше нет, —
// мусор. Каждое срабатывание пишется в журнал с id задания: по нему в журнале видна его история.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
)

// orphanMinAge — моложе не трогаем: процесс стартует (cmd.Start) раньше, чем задание попадает в
// карту, и свежий запуск не должен выглядеть сиротой.
var orphanMinAge = 2 * time.Minute // var — тест на живых процессах ставит 0

// Папка задания — temp_root/<id>, id — 32 hex (newJobID). У процесса с удалённой папкой ссылка
// cwd оканчивается на « (deleted)».
var orphanJobDirRe = regexp.MustCompile(`/([0-9a-f]{32})(?: \(deleted\))?$`)

// orphanProc — то, что нужно сторожу о процессе из /proc.
type orphanProc struct {
	pid, ppid int
	comm      string
	state     byte
	cwd       string
	age       time.Duration
}

// orphanVerdict — чистое решение по одному процессу: убить (сирота), прибрать (зомби, которого
// никто не ждёт) или не трогать. liveIDs — id заданий в учёте, livePIDs — их процессы,
// zombiesBefore — зомби, замеченные прошлым проходом.
func orphanVerdict(p orphanProc, me int, liveIDs map[string]struct{}, livePIDs map[int]struct{}, zombiesBefore map[int]struct{}) (kill, reap bool) {
	if p.ppid != me || p.comm != "ffmpeg" {
		return false, false
	}
	if _, ok := livePIDs[p.pid]; ok {
		return false, false
	}
	if p.state == 'Z' {
		// Зомби значит: процесс кончился, а Wait его не ждёт — ждущая горутина прибрала бы его
		// сразу. Но за миг между выходом и её wait4 зомби бывает и у живого задания, поэтому
		// прибираем только переживших один проход.
		_, seen := zombiesBefore[p.pid]
		return false, seen
	}
	m := orphanJobDirRe.FindStringSubmatch(p.cwd)
	if m == nil {
		return false, false // не процесс задания (потоковый режим, превью, служебные запуски)
	}
	if _, ok := liveIDs[m[1]]; ok {
		return false, false // задание живо (сам процесс или дорожки его звука)
	}
	return p.age >= orphanMinAge, false
}

// reapOrphanProcs — один проход сторожа. Возвращает, сколько сирот убито и зомби прибрано.
func (svc *TranscodingService) reapOrphanProcs() (killed, reaped int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, 0 // не Linux — нечего смотреть
	}
	me := os.Getpid()
	liveIDs := map[string]struct{}{}
	livePIDs := map[int]struct{}{}
	svc.mu.RLock()
	for id, j := range svc.jobs {
		liveIDs[id] = struct{}{}
		if j.Cmd != nil && j.Cmd.Process != nil {
			livePIDs[j.Cmd.Process.Pid] = struct{}{}
		}
	}
	svc.mu.RUnlock()

	zombies := map[int]struct{}{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		p, ok := readOrphanProc(pid)
		if !ok {
			continue
		}
		if p.state == 'Z' && p.ppid == me && p.comm == "ffmpeg" {
			zombies[pid] = struct{}{}
		}
		kill, reap := orphanVerdict(p, me, liveIDs, livePIDs, svc.orphanZombies)
		switch {
		case reap:
			var ws syscall.WaitStatus
			if got, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); got == pid {
				reaped++
				delete(zombies, pid)
			}
		case kill:
			// Стоящий на паузе окна процесс сначала будим: SIGKILL убьёт и так, но ждущий
			// торрент-читатель на том конце трубы разблокируется только от живого процесса.
			_ = syscall.Kill(pid, syscall.SIGCONT)
			if syscall.Kill(pid, syscall.SIGKILL) == nil {
				killed++
				jobID := ""
				if m := orphanJobDirRe.FindStringSubmatch(p.cwd); m != nil {
					jobID = m[1]
				}
				log.Warn().Int("pid", pid).Str("jobId", jobID).Str("age", p.age.Round(time.Minute).String()).
					Bool("dirDeleted", strings.HasSuffix(p.cwd, " (deleted)")).Str("state", string(p.state)).
					Msg("transcoding: убит ffmpeg вне учёта")
			}
		}
	}
	svc.orphanZombies = zombies
	return killed, reaped
}

// readOrphanProc читает comm, состояние, родителя, cwd и возраст процесса из /proc.
func readOrphanProc(pid int) (orphanProc, bool) {
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	raw, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return orphanProc{}, false
	}
	// «pid (comm) state ppid …»: comm может содержать пробелы и скобки — режем по ПОСЛЕДНЕЙ «)».
	s := string(raw)
	open, closing := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || closing < open {
		return orphanProc{}, false
	}
	rest := strings.Fields(s[closing+1:])
	if len(rest) < 2 || len(rest[0]) == 0 {
		return orphanProc{}, false
	}
	ppid, _ := strconv.Atoi(rest[1])
	p := orphanProc{pid: pid, ppid: ppid, comm: s[open+1 : closing], state: rest[0][0]}
	p.cwd, _ = os.Readlink(filepath.Join(dir, "cwd"))
	p.age = procAge(rest, dir)
	return p, true
}

// procAge — возраст процесса: starttime (22-е поле stat, такты с загрузки; USER_HZ = 100 на всех
// Linux, где мы живём) против /proc/uptime. Запасной путь — время каталога /proc/<pid>: оно
// ставится при ПЕРВОМ обращении, то есть возраст выйдет заниженным — сирота подождёт лишний
// проход, но свежий процесс старше не покажется никогда.
func procAge(rest []string, dir string) time.Duration {
	if len(rest) > 19 {
		if start, err := strconv.ParseFloat(rest[19], 64); err == nil {
			if up, err := os.ReadFile("/proc/uptime"); err == nil {
				if f := strings.Fields(string(up)); len(f) > 0 {
					if upSec, err := strconv.ParseFloat(f[0], 64); err == nil && upSec >= start/100 {
						return time.Duration((upSec - start/100) * float64(time.Second))
					}
				}
			}
		}
	}
	if fi, err := os.Stat(dir); err == nil {
		return time.Since(fi.ModTime())
	}
	return 0
}
