package jacred

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/rs/zerolog/log"
)

// bootstrapStampFile marks a completed FDB bootstrap so restarts don't
// re-download the multi-GB archive.
const bootstrapStampFile = ".fdb_bootstrapped"

// BootstrapDB downloads the prebuilt FDB archive (tar.zst, possibly wrapped
// in a zip — mirrors jacred.sh which handles both) and unpacks it into
// {homeDir}/Data. Skipped when a previous bootstrap completed.
func BootstrapDB(ctx context.Context, homeDir, archiveURL string) error {
	stamp := filepath.Join(homeDir, bootstrapStampFile)
	if _, err := os.Stat(stamp); err == nil {
		return nil
	}
	dataDir := filepath.Join(homeDir, "Data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}

	log.Info().Str("url", archiveURL).Msg("jacred: bootstrapping FDB database (this can take a while)")
	tmp, err := downloadToTemp(ctx, archiveURL, homeDir, "jacred-fdb-*.archive")
	if err != nil {
		return fmt.Errorf("download fdb archive: %w", err)
	}
	defer os.Remove(tmp)

	if err := unpackFDBArchive(tmp, dataDir); err != nil {
		return fmt.Errorf("unpack fdb archive: %w", err)
	}

	_ = os.WriteFile(stamp, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
	log.Info().Int64("db_size_mb", dirSizeMB(dataDir)).Msg("jacred: FDB database ready")
	return nil
}

// unpackFDBArchive detects the container format (zip-wrapped tar.zst vs raw
// tar.zst) by magic bytes and extracts the tar stream into dataDir.
func unpackFDBArchive(src, dataDir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	switch {
	case bytes.HasPrefix(magic, []byte("PK\x03\x04")):
		return unpackZipWrappedTarZst(src, dataDir)
	case bytes.Equal(magic, []byte{0x28, 0xB5, 0x2F, 0xFD}):
		return extractTarZst(f, dataDir)
	default:
		return fmt.Errorf("unknown archive magic %x", magic)
	}
}

// unpackZipWrappedTarZst opens the single tar.zst entry inside a zip and
// streams it through the tar extractor.
func unpackZipWrappedTarZst(src, dataDir string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		err = extractTarZst(rc, dataDir)
		rc.Close()
		return err // archive holds exactly one payload entry
	}
	return fmt.Errorf("zip archive contains no files")
}

// extractTarZst streams a zstd-compressed tar into dataDir (zip-slip safe).
func extractTarZst(r io.Reader, dataDir string) error {
	zr, err := zstd.NewReader(bufio.NewReaderSize(r, 1<<20))
	if err != nil {
		return err
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	cleanRoot := filepath.Clean(dataDir)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel := filepath.Clean(hdr.Name)
		if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			continue
		}
		target := filepath.Join(cleanRoot, rel)
		if !strings.HasPrefix(target, cleanRoot+string(os.PathSeparator)) && target != cleanRoot {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, cerr := io.Copy(out, tr)
			if err := out.Close(); err != nil && cerr == nil {
				cerr = err
			}
			if cerr != nil {
				return cerr
			}
		default:
			// symlinks etc. are not expected in the FDB dump — skip.
		}
	}
}
