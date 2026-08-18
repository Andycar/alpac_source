package browser

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// persistedSelection is the on-disk form of the runtime engine choice.
// Lives at <repoRoot>/database/browser_engine.json. Overrides values
// from config.toml [browser_pool] when present.
type persistedSelection struct {
	Engine          string            `json:"engine,omitempty"`
	BalancerEngines map[string]string `json:"balancer_engines,omitempty"`
}

var persistMu sync.Mutex

// LoadPersisted reads the on-disk override file. Returns (zero, nil)
// when the file is missing — the caller should fall back to config.
func LoadPersisted(path string) (engine string, balancerEngines map[string]string, err error) {
	persistMu.Lock()
	defer persistMu.Unlock()

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if len(data) == 0 {
		return "", nil, nil
	}
	var p persistedSelection
	if err := json.Unmarshal(data, &p); err != nil {
		return "", nil, err
	}
	return p.Engine, p.BalancerEngines, nil
}

// SavePersisted writes the override file atomically (write to tmp,
// rename into place). Empty engine and nil balancerEngines are
// permitted — they translate to an empty JSON file which LoadPersisted
// handles as "no override".
func SavePersisted(path, engine string, balancerEngines map[string]string) error {
	persistMu.Lock()
	defer persistMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(persistedSelection{
		Engine:          engine,
		BalancerEngines: balancerEngines,
	}, "", "  ")
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
