package sidecar

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// XrayEngine implements Engine for xray-core.
type XrayEngine struct{}

func (e *XrayEngine) Type() EngineType { return EngineXray }

func (e *XrayEngine) EnsureBinary(binDir string) (string, error) {
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", err
	}
	binPath := filepath.Join(binDir, "xray")
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		if err := DownloadXray(binDir); err != nil {
			return "", err
		}
	}
	if err := EnsureExecutable(binPath); err != nil {
		return "", err
	}
	return binPath, nil
}

func (e *XrayEngine) GenerateConfig(out ProxyOutbound, socksPort int) ([]byte, string, error) {
	cfg, err := GenerateXrayConfig(out, socksPort)
	if err != nil {
		return nil, "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("marshal xray config: %w", err)
	}
	return data, "json", nil
}

func (e *XrayEngine) StartArgs(binPath, configPath string) []string {
	return []string{binPath, "run", "-config", configPath}
}
