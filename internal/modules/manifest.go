package modules

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type RootModule struct {
	Enable     bool     `json:"enable"`
	Version    int      `json:"version,omitempty"`
	Dll        string   `json:"dll"`
	Initspace  string   `json:"initspace,omitempty"`
	References []string `json:"references,omitempty"`
}

func LoadManifest(path string) ([]RootModule, error) {
	if path == "" {
		path = defaultManifestPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var modules []RootModule
	if err := json.Unmarshal(data, &modules); err != nil {
		return nil, err
	}

	return modules, nil
}

func defaultManifestPath() string {
	candidates := []string{
		"module/manifest.json",
		"/home/module/manifest.json",
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Clean("module/manifest.json")
}

func EnabledNames(modules []RootModule) ([]string, error) {
	if len(modules) == 0 {
		return nil, errors.New("module manifest is empty")
	}

	res := make([]string, 0, len(modules))
	for _, mod := range modules {
		if mod.Enable {
			res = append(res, mod.Dll)
		}
	}
	return res, nil
}
