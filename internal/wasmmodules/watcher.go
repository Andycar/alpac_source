package wasmmodules

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher hot-reloads modules whenever their on-disk files change. Triggers
// a debounced Manager.Scan() — no need for a developer to restart the server
// after `tinygo build`.
//
// Debounce matters because compilers commonly do CREATE → WRITE → RENAME
// sequences; raw fsnotify events would fire 5–10 reloads per build. We coalesce
// a 300 ms quiet window into a single Scan().
type Watcher struct {
	mgr   *Manager
	w     *fsnotify.Watcher
	stop  chan struct{}
	once  sync.Once
	debMu sync.Mutex
	deb   *time.Timer
}

// NewWatcher starts watching mgr.BaseDir and every module subdir under it.
// Call Close to stop.
func NewWatcher(mgr *Manager) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{mgr: mgr, w: fsw, stop: make(chan struct{})}
	if err := w.addBase(); err != nil {
		_ = fsw.Close()
		return nil, err
	}
	go w.run()
	return w, nil
}

// addBase watches the base directory and every immediate subdirectory.
// Compilers may create new module dirs we haven't seen yet — base-watch
// handles the CREATE event for those and then adds the new subdir to the
// watch list inside `run`.
func (w *Watcher) addBase() error {
	if err := w.w.Add(w.mgr.BaseDir); err != nil {
		return err
	}
	for _, mod := range w.mgr.List() {
		_ = w.w.Add(mod.Dir)
	}
	return nil
}

func (w *Watcher) run() {
	for {
		select {
		case <-w.stop:
			return
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			if w.mgr.Logger != nil {
				w.mgr.Logger.Warn().Err(err).Msg("wasmmodules: fsnotify error")
			}
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	// Heuristic: ignore noise from editor temp files.
	base := filepath.Base(ev.Name)
	if strings.HasPrefix(base, ".") || strings.HasSuffix(base, "~") {
		return
	}

	// New subdir created — start watching it AND schedule a scan. fsnotify is
	// not recursive, so files written inside the new dir before our watch.Add
	// completes wouldn't fire. The post-debounce Scan() walks the dir tree
	// directly, which covers that race.
	if ev.Op&fsnotify.Create != 0 {
		if fi, err := osLstat(ev.Name); err == nil && fi.IsDir() {
			_ = w.w.Add(ev.Name)
			w.scheduleScan()
			return
		}
	}

	// Only trigger Scan() for things we care about: manifest.json or any
	// .wasm file. Otherwise dot-files / IDE state bursts every keystroke.
	if base != "manifest.json" && !strings.HasSuffix(base, ".wasm") {
		return
	}

	w.scheduleScan()
}

// scheduleScan debounces fsnotify bursts into a single Manager.Scan() call.
func (w *Watcher) scheduleScan() {
	w.debMu.Lock()
	defer w.debMu.Unlock()
	if w.deb != nil {
		w.deb.Stop()
	}
	w.deb = time.AfterFunc(300*time.Millisecond, func() {
		if w.mgr.Logger != nil {
			w.mgr.Logger.Info().Msg("wasmmodules: hot-reload triggered")
		}
		if err := w.mgr.Scan(); err != nil && w.mgr.Logger != nil {
			w.mgr.Logger.Warn().Err(err).Msg("wasmmodules: hot-reload scan failed")
		}
		// New modules may have appeared — add their dirs to the watch.
		for _, mod := range w.mgr.List() {
			_ = w.w.Add(mod.Dir)
		}
	})
}

// Close stops the watcher.
func (w *Watcher) Close() error {
	w.once.Do(func() { close(w.stop) })
	w.debMu.Lock()
	if w.deb != nil {
		w.deb.Stop()
	}
	w.debMu.Unlock()
	return w.w.Close()
}

// Reload triggers an unconditional Scan() — bypassing the debounce. Useful
// when admin presses a "Reload" button.
func (m *Manager) Reload(ctx context.Context) error { return m.Scan() }
