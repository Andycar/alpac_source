package litesrc

import (
	"os"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
)

// TestSmotrimBuildIndexLive строит полный индекс каталога и сразу меряет, что
// он даёт на реальных карточках прода.
// Запуск: SMOTRIM_BUILD=1 SMOTRIM_ROOT=<dir> SMOTRIM_CARDS=<tsv> go test ./internal/litesrc/ -run TestSmotrimBuildIndexLive -v -timeout 60m
func TestSmotrimBuildIndexLive(t *testing.T) {
	if os.Getenv("SMOTRIM_BUILD") != "1" {
		t.Skip("set SMOTRIM_BUILD=1 to crawl the full catalogue")
	}
	cfg := config.Config{
		Online: config.OnlineConfig{Smotrim: config.SmotrimSource{Host: smotrimDefaultHost, PlayerAPI: smotrimDefaultPlayerAPI}},
		Compat: config.CompatConfig{RepoRoot: os.Getenv("SMOTRIM_ROOT")},
	}
	c := NewSmotrimChecker(cfg)

	start := time.Now()
	c.full.rebuild(t.Context())
	brands, fresh := c.full.Brands()
	t.Logf("индекс: %d брендов за %s (свежий=%v)", len(brands), time.Since(start).Round(time.Second), fresh)
	if len(brands) < 5000 {
		t.Fatalf("индекс вышел подозрительно маленьким: %d", len(brands))
	}
	for _, b := range brands[:5] {
		t.Logf("   %s | %s | %d", b.id, b.title, b.year)
	}

	path := os.Getenv("SMOTRIM_CARDS")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	total, hits := 0, 0
	var examples []string
	for _, line := range strings.Split(string(raw), "\n") {
		cols := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(cols) < 2 {
			continue
		}
		title, orig := strings.TrimSpace(cols[0]), strings.TrimSpace(cols[1])
		year := 0
		if len(cols) > 2 {
			for _, ch := range strings.TrimSpace(cols[2]) {
				if ch >= '0' && ch <= '9' {
					year = year*10 + int(ch-'0')
				}
			}
		}
		if title == "" && orig == "" {
			continue
		}
		total++
		if b, ok := smotrimPickBrand(brands, title, orig, year); ok {
			hits++
			if len(examples) < 12 {
				examples = append(examples, title+" -> "+b.title+" ("+b.id+")")
			}
		}
	}
	t.Logf("охват: %d из %d карточек (%.1f%%)", hits, total, 100*float64(hits)/float64(max(total, 1)))
	for _, e := range examples {
		t.Logf("   %s", e)
	}
}
