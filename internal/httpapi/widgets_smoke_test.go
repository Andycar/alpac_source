package httpapi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"strings"
	"testing"
)

func TestBuildSamsungWGTSmoke(t *testing.T) {
	src := "../../data/widgets/samsung"
	data, err := buildSamsungWGT(src, "http://test.local")
	if err != nil {
		t.Fatalf("buildSamsungWGT: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty wgt")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	found := map[string]bool{}
	for _, f := range zr.File {
		found[f.Name] = true
		if f.Name == "index.html" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			if !strings.Contains(string(b), "http://test.local") {
				t.Errorf("index.html missing substituted host: %s", b)
			}
			if strings.Contains(string(b), "{localhost}") {
				t.Errorf("index.html still has {localhost} placeholder")
			}
		}
	}
	// Шаблон — наш лаунчер + Tizen-конфиг ALPAC (пересобран 2026-09-10 из web/packaging).
	// Раньше здесь ждали logo_appname_fg.png от лампаковского плейсхолдера, и тест
	// молчаливо охранял устаревший виджет.
	for _, want := range []string{"config.xml", "index.html", "icon.png"} {
		if !found[want] {
			t.Errorf("missing from wgt: %s", want)
		}
	}
	for _, f := range zr.File {
		if f.Name != "config.xml" {
			continue
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		if !strings.Contains(string(b), `application id="ALPACtv001.ALPAC"`) {
			t.Errorf("config.xml не наш (нет ALPACtv001.ALPAC) — шаблон откатился к плейсхолдеру?")
		}
	}
}

func TestBuildWebOSIPKSmoke(t *testing.T) {
	src := "../../data/widgets/webos"
	data, err := buildWebOSIPK(src, "http://test.local")
	if err != nil {
		t.Fatalf("buildWebOSIPK: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("!<arch>\n")) {
		t.Fatal("ipk missing ar magic")
	}
	// Walk ar members looking for data.tar.gz and decompress it
	r := bytes.NewReader(data)
	if _, err := r.Seek(8, 0); err != nil {
		t.Fatal(err)
	}
	var dataMember []byte
	hdr := make([]byte, 60)
	for {
		n, _ := io.ReadFull(r, hdr)
		if n < 60 {
			break
		}
		name := strings.TrimSpace(string(hdr[0:16]))
		size := 0
		for _, b := range bytes.TrimSpace(hdr[48:58]) {
			size = size*10 + int(b-'0')
		}
		body := make([]byte, size)
		io.ReadFull(r, body)
		if size%2 == 1 {
			r.Seek(1, 1)
		}
		if name == "data.tar.gz" {
			dataMember = body
			break
		}
	}
	if dataMember == nil {
		t.Fatal("data.tar.gz not found in ar")
	}
	gz, err := gzip.NewReader(bytes.NewReader(dataMember))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	// id приложения берём из самого шаблона: он менялся (com.lampac.app → cc.alcopa.tv),
	// и зашитый путь ломал тест при каждом обновлении оболочки.
	appinfo, err := os.ReadFile(src + "/appinfo.json")
	if err != nil {
		t.Fatalf("appinfo.json: %v", err)
	}
	var ai struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(appinfo, &ai); err != nil || ai.ID == "" {
		t.Fatalf("appinfo.json без id: %v", err)
	}
	wantPath := "./usr/palm/applications/" + ai.ID + "/index.html"
	var sawIndex bool
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if h.Name == wantPath {
			sawIndex = true
			body, _ := io.ReadAll(tr)
			if !strings.Contains(string(body), "http://test.local") {
				t.Errorf("index.html missing substituted host")
			}
		}
	}
	if !sawIndex {
		t.Errorf("index.html not found in data.tar.gz (expected %s)", wantPath)
	}
}
