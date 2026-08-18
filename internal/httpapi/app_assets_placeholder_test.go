package httpapi

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// Прод-инцидент 2026-08-05: `{filmix-direct}` уехал в app.min.js неподставленным, JS прочитал его как
// блок с выражением `filmix - direct`, и плеер падал с «filmix is not defined» — торренты в том числе.
func TestPlayerInnerSnippetLeavesNoPlaceholders(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "PI {useplayer} {notUseTranscoding}\n{filmix-direct}\nPlayer.play(element);\n"
	if err := os.WriteFile(filepath.Join(root, "plugins", "player-inner.js"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// direct_lampa выключен — плейсхолдер обязан исчезнуть, а не остаться в коде.
	got, ok := loadPlayerInnerSnippet(root, config.Config{})
	if !ok {
		t.Fatal("сниппет должен читаться")
	}
	if strings.Contains(got, "{filmix-direct}") {
		t.Fatalf("плейсхолдер уехал бы клиенту:\n%s", got)
	}
	// Ни один плейсхолдер вида {…} не должен пережить подстановку.
	if left := regexp.MustCompile(`\{[a-zA-Z][a-zA-Z0-9_-]*\}`).FindAllString(got, -1); len(left) > 0 {
		t.Fatalf("неподставленные плейсхолдеры: %v", left)
	}
}
