package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

type cmdConfig struct {
	Path      string   `json:"path"`
	Arguments []string `json:"arguments"`
	Eval      string   `json:"eval"`
}

func cmdHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(chiURLParam(r, "key"))
		commandValue := strings.TrimSpace(chiURLParam(r, "*"))

		if key == "" {
			w.WriteHeader(http.StatusOK)
			return
		}

		cfg, ok := loadCmdConfig(key)
		if !ok {
			w.WriteHeader(http.StatusOK)
			return
		}

		if strings.TrimSpace(cfg.Eval) != "" {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}

		if strings.TrimSpace(cfg.Path) == "" || len(cfg.Arguments) == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}

		fullValue := commandValue
		if r.URL.RawQuery != "" {
			fullValue += "?" + r.URL.RawQuery
		}

		args := make([]string, 0, len(cfg.Arguments))
		for _, arg := range cfg.Arguments {
			arg = strings.ReplaceAll(arg, "{value}", fullValue)
			args = append(args, arg)
		}

		cmd := exec.Command(cfg.Path, args...)
		cmd.Env = os.Environ()
		_ = cmd.Start()

		w.WriteHeader(http.StatusOK)
	}
}

func loadCmdConfig(key string) (cmdConfig, bool) {
	data, ok := readFileAny("init.conf")
	if !ok {
		return cmdConfig{}, false
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return cmdConfig{}, false
	}

	cmdNode, ok := root["cmd"].(map[string]any)
	if !ok {
		return cmdConfig{}, false
	}

	raw, ok := cmdNode[key].(map[string]any)
	if !ok {
		return cmdConfig{}, false
	}

	cfg := cmdConfig{
		Path: strings.TrimSpace(toString(raw["path"])),
		Eval: strings.TrimSpace(toString(raw["eval"])),
	}

	if arr, ok := raw["arguments"].([]any); ok {
		cfg.Arguments = make([]string, 0, len(arr))
		for _, item := range arr {
			cfg.Arguments = append(cfg.Arguments, toString(item))
		}
	}
	return cfg, true
}
