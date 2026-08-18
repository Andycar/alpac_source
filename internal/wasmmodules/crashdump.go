package wasmmodules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// CrashDump captures everything the admin needs to diagnose a plugin crash:
// what the runtime saw, what was on the way in, and the last few log lines
// the plugin produced. Stored on disk as JSON so it survives server restart.
type CrashDump struct {
	Module   string    `json:"module"`
	Time     time.Time `json:"time"`
	Error    string    `json:"error"`
	Path     string    `json:"path,omitempty"`
	Query    string    `json:"query,omitempty"`
	Headers  string    `json:"headers,omitempty"`
	MemBytes uint32    `json:"memory_bytes,omitempty"`
	LogTail  []LogEntry `json:"log_tail,omitempty"`
}

// crashStore manages on-disk crash dumps. Dumps live in
// {repoRoot}/wasm_dumps/{moduleID}/{ts}.json so admin can browse them.
type crashStore struct {
	mu      sync.Mutex
	root    string
	maxKeep int // per-module retention; older dumps are pruned
}

func newCrashStore(root string) *crashStore {
	return &crashStore{root: root, maxKeep: 20}
}

// Save writes one dump to disk and prunes old ones for the same module.
func (s *crashStore) Save(d CrashDump) (string, error) {
	if s == nil || s.root == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.root, sanitizeModuleID(d.Module))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%d.json", d.Time.UnixNano())
	full := filepath.Join(dir, name)
	body, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(full, body, 0644); err != nil {
		return "", err
	}
	s.pruneLocked(dir)
	return full, nil
}

// pruneLocked drops the oldest dumps when there are more than maxKeep.
func (s *crashStore) pruneLocked(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= s.maxKeep {
		return
	}
	type entry struct {
		path string
		mod  time.Time
	}
	var items []entry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, entry{path: filepath.Join(dir, e.Name()), mod: fi.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for i := 0; i < len(items)-s.maxKeep; i++ {
		_ = os.Remove(items[i].path)
	}
}

// List returns all dumps for a module, newest first. Empty moduleID returns
// dumps for every module (flattened).
func (s *crashStore) List(moduleID string, limit int) []CrashDump {
	if s == nil || s.root == "" {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []CrashDump
	walk := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			var d CrashDump
			if json.Unmarshal(data, &d) != nil {
				continue
			}
			out = append(out, d)
		}
	}
	if moduleID != "" {
		walk(filepath.Join(s.root, sanitizeModuleID(moduleID)))
	} else {
		entries, err := os.ReadDir(s.root)
		if err != nil {
			return nil
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			walk(filepath.Join(s.root, e.Name()))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// sanitizeModuleID strips path separators so a malicious manifest can't write
// outside the dumps dir.
func sanitizeModuleID(id string) string {
	id = strings.ReplaceAll(id, "/", "_")
	id = strings.ReplaceAll(id, "\\", "_")
	id = strings.ReplaceAll(id, "..", "_")
	return id
}
