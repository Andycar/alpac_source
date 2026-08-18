package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/rs/zerolog/log"
)

// DownloadXray downloads the xray binary for the current platform into binDir.
func DownloadXray(binDir string) error {
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return err
	}

	var asset string
	switch runtime.GOOS {
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			asset = "Xray-linux-64.zip"
		case "arm64":
			asset = "Xray-linux-arm64-v8a.zip"
		default:
			return fmt.Errorf("unsupported linux arch: %s", runtime.GOARCH)
		}
	case "darwin":
		switch runtime.GOARCH {
		case "amd64":
			asset = "Xray-macos-64.zip"
		case "arm64":
			asset = "Xray-macos-arm64-v8a.zip"
		default:
			return fmt.Errorf("unsupported darwin arch: %s", runtime.GOARCH)
		}
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	dlURL := "https://github.com/XTLS/Xray-core/releases/latest/download/" + asset
	tmpZip := filepath.Join(binDir, "xray-download.zip")
	destPath := filepath.Join(binDir, "xray")

	if err := DownloadFile(dlURL, tmpZip, 120*time.Second); err != nil {
		return err
	}
	defer os.Remove(tmpZip)

	if err := ExtractFromZip(tmpZip, "xray", destPath); err != nil {
		return err
	}

	log.Info().Str("path", destPath).Msg("sidecar: xray binary installed")
	return nil
}
