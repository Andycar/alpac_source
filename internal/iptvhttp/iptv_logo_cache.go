package iptvhttp

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// iptv_logo_cache.go — дисковый кэш логотипов каналов РЕЕСТРА.
//
// Логотипы доноров живут на чужих хостах (imgur и т.п.) — хост умер или
// заретлимитил, и у «своих» каналов пропали лица. Для каналов own_* логотип
// после первой отдачи оседает на диске ({repoRoot}/database/iptv/logos) и
// дальше отдаётся локально; upstream опрашивается не чаще logoFreshFor, а при
// его отказе отдаётся последняя удачная копия (stale). Донорские (не own_*)
// каналы кэш не трогает: их тысячи, и клиент с registry_only их не видит.

// logoFreshFor — сколько кэшу верим без похода на upstream. Логотипы каналов
// меняются раз в никогда; неделя — чтобы ребрендинг всё же долетал.
const logoFreshFor = 7 * 24 * time.Hour

// logoMaxBytes повторяет кап отдачи logo-хендлера.
const logoMaxBytes = 4 << 20

type logoCache struct{ dir string }

func newLogoCache(repoRoot string) *logoCache {
	dir := filepath.Join(repoRoot, "database", "iptv", "logos")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil
	}
	return &logoCache{dir: dir}
}

func (c *logoCache) paths(url string) (data, ct string) {
	h := md5.Sum([]byte(url))
	base := filepath.Join(c.dir, hex.EncodeToString(h[:8]))
	return base + ".img", base + ".ct"
}

// get returns the cached logo (nil when absent) and whether it is still fresh
// (fresh=false → caller should try upstream but may fall back to this copy).
func (c *logoCache) get(url string) (body []byte, contentType string, fresh bool) {
	dataPath, ctPath := c.paths(url)
	fi, err := os.Stat(dataPath)
	if err != nil {
		return nil, "", false
	}
	body, err = os.ReadFile(dataPath)
	if err != nil || len(body) == 0 {
		return nil, "", false
	}
	ctRaw, _ := os.ReadFile(ctPath)
	contentType = strings.TrimSpace(string(ctRaw))
	if contentType == "" {
		contentType = "image/png"
	}
	return body, contentType, time.Since(fi.ModTime()) < logoFreshFor
}

func (c *logoCache) put(url string, body []byte, contentType string) {
	if len(body) == 0 || len(body) > logoMaxBytes {
		return
	}
	dataPath, ctPath := c.paths(url)
	tmp := dataPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0644); err != nil {
		return
	}
	if err := os.Rename(tmp, dataPath); err != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = os.WriteFile(ctPath, []byte(contentType), 0644)
}
