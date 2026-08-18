package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"lampac-go/internal/config"
)

// webosIPKHandler builds (and caches) a LG WebOS .ipk package that launches the
// Lampac frontend at the requested host. An .ipk is a Unix `ar` archive with
// three members: debian-binary, control.tar.gz, data.tar.gz. Templates live in
// `data/widgets/webos/` — text files may contain `{localhost}` which is
// substituted with the current server URL at build time.
func webosIPKHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := resolveWidgetHost(r)
		if host == "" {
			writePlain(w, http.StatusBadRequest, "host missing")
			return
		}

		cacheName := md5Hex([]byte(host+"webosv1")) + ".ipk"
		cachePath := widgetsCacheFile(cfg, cacheName)

		if data, err := os.ReadFile(cachePath); err == nil {
			writeWidgetDownload(w, "lampac.ipk", data)
			return
		}

		srcDir := widgetsSourceDir(cfg, "webos")
		if srcDir == "" {
			writePlain(w, http.StatusServiceUnavailable, "webos widget templates not installed (data/widgets/webos)")
			return
		}

		widgetBuildMu.Lock()
		defer widgetBuildMu.Unlock()

		if data, err := os.ReadFile(cachePath); err == nil {
			writeWidgetDownload(w, "lampac.ipk", data)
			return
		}

		data, err := buildWebOSIPK(srcDir, host)
		if err != nil {
			writePlain(w, http.StatusInternalServerError, "ipk build failed: "+err.Error())
			return
		}
		_ = os.WriteFile(cachePath, data, 0o644)

		writeWidgetDownload(w, "lampac.ipk", data)
	}
}

type webosAppMeta struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

func buildWebOSIPK(srcDir, host string) ([]byte, error) {
	meta := webosAppMeta{ID: "com.lampac.app", Title: "Lampac", Version: "1.0.0"}
	if raw, err := os.ReadFile(filepath.Join(srcDir, "appinfo.json")); err == nil {
		var m webosAppMeta
		if json.Unmarshal(raw, &m) == nil {
			if m.ID != "" {
				meta.ID = m.ID
			}
			if m.Title != "" {
				meta.Title = m.Title
			}
			if m.Version != "" {
				meta.Version = m.Version
			}
		}
	}

	// --- data.tar.gz: ./usr/palm/applications/<id>/<file>...
	dataTarGz, err := buildWebOSDataTar(srcDir, meta.ID, host)
	if err != nil {
		return nil, fmt.Errorf("data tar: %w", err)
	}

	// --- control.tar.gz: ./control
	controlTxt := fmt.Sprintf(
		"Package: %s\nVersion: %s\nSection: misc\nPriority: optional\nArchitecture: all\nInstalled-Size: %d\nMaintainer: lampac <noreply@example.com>\nDescription: %s\n  Lampac frontend launcher for LG WebOS.\n",
		meta.ID, meta.Version, len(dataTarGz), meta.Title,
	)
	controlTarGz, err := buildSingleFileTarGz("./control", []byte(controlTxt))
	if err != nil {
		return nil, fmt.Errorf("control tar: %w", err)
	}

	// --- ar archive
	var ar bytes.Buffer
	ar.WriteString("!<arch>\n")
	writeArMember(&ar, "debian-binary", []byte("2.0\n"))
	writeArMember(&ar, "control.tar.gz", controlTarGz)
	writeArMember(&ar, "data.tar.gz", dataTarGz)
	return ar.Bytes(), nil
}

func buildWebOSDataTar(srcDir, appID, host string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	prefix := "./usr/palm/applications/" + appID + "/"

	// Emit parent directories explicitly so unpackers (dpkg, opkg) are happy.
	for _, dir := range []string{"./usr/", "./usr/palm/", "./usr/palm/applications/", prefix} {
		if err := tw.WriteHeader(&tar.Header{
			Name:     dir,
			Mode:     0o755,
			Typeflag: tar.TypeDir,
		}); err != nil {
			return nil, err
		}
	}

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		rel = filepath.ToSlash(rel)
		if rel == "." || strings.HasPrefix(rel, ".") {
			return nil
		}
		name := prefix + rel
		if info.IsDir() {
			return tw.WriteHeader(&tar.Header{
				Name:     name + "/",
				Mode:     0o755,
				Typeflag: tar.TypeDir,
			})
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isTextWidgetFile(rel) {
			b = []byte(strings.ReplaceAll(string(b), "{localhost}", host))
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(b)),
			Typeflag: tar.TypeReg,
			ModTime:  info.ModTime(),
		}); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildSingleFileTarGz(name string, data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(data); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeArMember writes a single file into a POSIX ar archive. Members must be
// padded to an even byte boundary.
func writeArMember(w *bytes.Buffer, name string, data []byte) {
	// Header: name(16) mtime(12) uid(6) gid(6) mode(8) size(10) magic(2=`\n)
	hdr := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8s%-10d`\n",
		name, 0, 0, 0, "100644", len(data))
	w.WriteString(hdr)
	w.Write(data)
	if len(data)%2 == 1 {
		w.WriteByte('\n')
	}
}
