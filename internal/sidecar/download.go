package sidecar

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
)

// WaitReady polls a TCP address until it accepts connections or timeout.
func WaitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}

// EnsureExecutable makes sure the file at path has the execute permission bit set.
func EnsureExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode()&0100 == 0 {
		if err := os.Chmod(path, info.Mode()|0755); err != nil {
			return err
		}
	}
	return nil
}

// DownloadFile downloads a URL to a local file path.
func DownloadFile(dlURL, destPath string, timeout time.Duration) error {
	log.Info().Str("url", dlURL).Str("dest", destPath).Msg("sidecar: downloading")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, dlURL)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}

	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(destPath)
		return err
	}
	return nil
}

// ExtractFromZip extracts a single file by base name from a zip archive.
func ExtractFromZip(zipPath, targetName, destPath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	for _, zf := range zr.File {
		name := filepath.Base(zf.Name)
		if name != targetName {
			continue
		}

		rc, err := zf.Open()
		if err != nil {
			return err
		}

		out, err := os.Create(destPath)
		if err != nil {
			rc.Close()
			return err
		}

		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()

		if err != nil {
			return err
		}

		return os.Chmod(destPath, 0755)
	}

	return fmt.Errorf("%s not found in zip archive", targetName)
}

// ExtractGzip decompresses a gzip file to destPath.
func ExtractGzip(gzPath, destPath string) error {
	f, err := os.Open(gzPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, gz)
	out.Close()
	if err != nil {
		return err
	}

	return os.Chmod(destPath, 0755)
}
