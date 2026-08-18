package httpapi

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"lampac-go/internal/config"
)

// samsungWGTHandler builds (and caches) a Tizen .wgt widget that redirects
// to the Lampac frontend at the requested host. Mirrors the C# LampaWebController.SamsWgt
// pipeline: {localhost} substitution → SHA-512 hashing → signature substitution → zip.
func samsungWGTHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := resolveWidgetHost(r)
		if host == "" {
			writePlain(w, http.StatusBadRequest, "host missing")
			return
		}

		cacheName := md5Hex([]byte(host+"v3")) + ".wgt"
		cachePath := widgetsCacheFile(cfg, cacheName)

		if data, err := os.ReadFile(cachePath); err == nil {
			writeWidgetDownload(w, "lampac.wgt", data)
			return
		}

		srcDir := widgetsSourceDir(cfg, "samsung")
		if srcDir == "" {
			writePlain(w, http.StatusServiceUnavailable, "samsung widget templates not installed (data/widgets/samsung)")
			return
		}

		widgetBuildMu.Lock()
		defer widgetBuildMu.Unlock()

		if data, err := os.ReadFile(cachePath); err == nil {
			writeWidgetDownload(w, "lampac.wgt", data)
			return
		}

		data, err := buildSamsungWGT(srcDir, host)
		if err != nil {
			writePlain(w, http.StatusInternalServerError, "wgt build failed: "+err.Error())
			return
		}
		_ = os.WriteFile(cachePath, data, 0o644)

		writeWidgetDownload(w, "lampac.wgt", data)
	}
}

type widgetEntry struct {
	rel  string
	data []byte
}

// buildSamsungWGT collects files from srcDir, performs {localhost} substitution
// on text files, fills signature hash placeholders, and returns the zipped .wgt
// bytes.
func buildSamsungWGT(srcDir, host string) ([]byte, error) {
	var entries []widgetEntry

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(srcDir, path)
		rel = filepath.ToSlash(rel)
		// Skip cache files and OS junk.
		if strings.HasPrefix(rel, ".") || strings.HasSuffix(rel, "~") {
			return nil
		}
		// Signature XMLs are handled after regular content so their hashes can
		// reference the substituted files.
		if rel == "author-signature.xml" || rel == "signature1.xml" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isTextWidgetFile(rel) {
			b = []byte(strings.ReplaceAll(string(b), "{localhost}", host))
		}
		entries = append(entries, widgetEntry{rel: rel, data: b})
		return nil
	})
	if err != nil {
		return nil, err
	}

	hashByName := make(map[string]string, len(entries))
	for _, e := range entries {
		hashByName[e.rel] = sha512Base64(e.data)
	}

	sigPlaceholders := map[string]string{
		"indexhashsha512":  hashByName["index.html"],
		"loaderhashsha512": hashByName["loader.js"],
		"apphashsha512":    hashByName["app.js"],
		"confighashsha512": hashByName["config.xml"],
		"iconhashsha512":   hashByName["icon.png"],
		"logohashsha512":   hashByName["logo_appname_fg.png"],
	}

	// author-signature.xml (optional)
	if raw, err := os.ReadFile(filepath.Join(srcDir, "author-signature.xml")); err == nil {
		s := string(raw)
		for k, v := range sigPlaceholders {
			s = strings.ReplaceAll(s, k, v)
		}
		entries = append(entries, widgetEntry{rel: "author-signature.xml", data: []byte(s)})
	}

	// signature1.xml (optional) — depends on author-signature hash
	if raw, err := os.ReadFile(filepath.Join(srcDir, "signature1.xml")); err == nil {
		authorHash := ""
		for _, e := range entries {
			if e.rel == "author-signature.xml" {
				authorHash = sha512Base64(e.data)
				break
			}
		}
		s := string(raw)
		for k, v := range sigPlaceholders {
			s = strings.ReplaceAll(s, k, v)
		}
		s = strings.ReplaceAll(s, "authorsignaturehashsha512", authorHash)
		entries = append(entries, widgetEntry{rel: "signature1.xml", data: []byte(s)})
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		fw, err := zw.Create(e.rel)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeWidgetDownload(w http.ResponseWriter, filename string, data []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, bytes.NewReader(data))
}
