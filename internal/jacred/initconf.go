package jacred

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
)

// ensureInitConf makes {homeDir}/init.conf agree with our Config. The file is
// jacred's hot-reloaded JSON config. Strategy: start from the existing file
// (or the Data/init.conf template shipped in the DB archive, or an empty map)
// and enforce only the keys lampac owns — listen address, port, apikey,
// syncapi. Everything else (tracker aliases, proxies, tuning) stays as the
// user left it.
func ensureInitConf(cfg Config) error {
	confPath := filepath.Join(cfg.HomeDir, "init.conf")

	base := map[string]any{}
	for _, candidate := range []string{confPath, filepath.Join(cfg.HomeDir, "Data", "init.conf")} {
		b, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		if json.Unmarshal(b, &base) == nil {
			break
		}
		base = map[string]any{}
	}

	desired := map[string]any{
		// Local only: clients reach it through lampac's authenticated proxy.
		"listenip":   "127.0.0.1",
		"listenport": cfg.Port,
		"apikey":     cfg.APIKey,
		"syncapi":    cfg.SyncAPI,
	}
	changed := false
	for k, v := range desired {
		if !reflect.DeepEqual(normalizeJSON(base[k]), normalizeJSON(v)) {
			base[k] = v
			changed = true
		}
	}
	if _, err := os.Stat(confPath); err == nil && !changed {
		return nil
	}

	out, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(confPath, append(out, '\n'), 0o644)
}

// normalizeJSON makes int/float64 comparable (json.Unmarshal yields float64).
func normalizeJSON(v any) any {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float32:
		return float64(n)
	}
	return v
}
