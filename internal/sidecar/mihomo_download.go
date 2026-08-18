package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/rs/zerolog/log"
)

// DownloadMihomo downloads the mihomo binary for the current platform into binDir.
func DownloadMihomo(binDir string) error {
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return err
	}

	goos := runtime.GOOS
	goarch := runtime.GOARCH

	// mihomo release naming: mihomo-{os}-{arch}-v{version}.gz
	// latest redirect: https://github.com/MetaCubeX/mihomo/releases/latest/download/mihomo-{os}-{arch}.gz
	var asset string
	switch goos {
	case "linux":
		switch goarch {
		case "amd64":
			asset = "mihomo-linux-amd64.gz"
		case "arm64":
			asset = "mihomo-linux-arm64.gz"
		default:
			return fmt.Errorf("unsupported linux arch: %s", goarch)
		}
	case "darwin":
		switch goarch {
		case "amd64":
			asset = "mihomo-darwin-amd64.gz"
		case "arm64":
			asset = "mihomo-darwin-arm64.gz"
		default:
			return fmt.Errorf("unsupported darwin arch: %s", goarch)
		}
	default:
		return fmt.Errorf("unsupported OS: %s", goos)
	}

	dlURL := "https://github.com/MetaCubeX/mihomo/releases/latest/download/" + asset
	tmpGz := filepath.Join(binDir, "mihomo-download.gz")
	destPath := filepath.Join(binDir, "mihomo")

	if err := DownloadFile(dlURL, tmpGz, 120*time.Second); err != nil {
		return err
	}
	defer os.Remove(tmpGz)

	if err := ExtractGzip(tmpGz, destPath); err != nil {
		return err
	}

	log.Info().Str("path", destPath).Msg("sidecar: mihomo binary installed")
	return nil
}
