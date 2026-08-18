package sidecar

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// MihomoEngine implements Engine for mihomo (Meta) proxy core.
type MihomoEngine struct{}

func (e *MihomoEngine) Type() EngineType { return EngineMihomo }

func (e *MihomoEngine) EnsureBinary(binDir string) (string, error) {
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", err
	}
	binPath := filepath.Join(binDir, "mihomo")
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		if err := DownloadMihomo(binDir); err != nil {
			return "", err
		}
	}
	if err := EnsureExecutable(binPath); err != nil {
		return "", err
	}
	return binPath, nil
}

func (e *MihomoEngine) GenerateConfig(out ProxyOutbound, socksPort int) ([]byte, string, error) {
	cfg := GenerateMihomoConfig(out, socksPort)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("marshal mihomo config: %w", err)
	}
	return data, "yaml", nil
}

func (e *MihomoEngine) StartArgs(binPath, configPath string) []string {
	return []string{binPath, "-f", configPath}
}
