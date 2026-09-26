package litesrc

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/browsertmp"
)

// kinobaseBrowser extracts player data from kinobase.org using a headless Chrome
// browser (via Puppeteer). The obfuscated JavaScript on kinobase generates
// video URLs client-side, so server-side HTTP scraping cannot extract them.
//
// It mirrors the approach used by the C# Playwright implementation:
//  1. Replace playerjs.js with a stub that captures the file parameter
//  2. Wait for #playerjsfile element to appear
//  3. Return the file data as JSON
type kinobaseBrowser struct {
	mu         sync.Mutex
	scriptPath string // path to the extraction JS script
	nodePath   string // path to node binary
	chromePath string // path to Chrome/Chromium binary
	npmReady   bool   // whether puppeteer-core is installed
}

type kinobaseBrowserResult struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

var defaultKinobaseBrowser = &kinobaseBrowser{}

func (kb *kinobaseBrowser) init() error {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	if kb.scriptPath != "" && kb.nodePath != "" && kb.chromePath != "" {
		return nil
	}

	// Find Node.js
	node := kb.findNode()
	if node == "" {
		return fmt.Errorf("node.js not found (install Node.js 18+)")
	}
	kb.nodePath = node

	// Find Chrome
	chrome := kb.findChrome()
	if chrome == "" {
		return fmt.Errorf("chrome/chromium not found")
	}
	kb.chromePath = chrome

	// Write the extraction script to temp
	dir := os.TempDir()
	scriptPath := filepath.Join(dir, "kinobase-extract.js")
	if err := os.WriteFile(scriptPath, []byte(kinobaseExtractScript), 0644); err != nil {
		return fmt.Errorf("write script: %w", err)
	}
	kb.scriptPath = scriptPath

	// Ensure puppeteer-core is installed
	if !kb.npmReady {
		if err := kb.ensurePuppeteer(dir); err != nil {
			return fmt.Errorf("install puppeteer-core: %w", err)
		}
		kb.npmReady = true
	}

	log.Info().
		Str("node", kb.nodePath).
		Str("chrome", kb.chromePath).
		Str("script", kb.scriptPath).
		Msg("kinobase browser: initialized")
	return nil
}

func (kb *kinobaseBrowser) findNode() string {
	// Check common paths
	paths := []string{
		// macOS nvm
		os.ExpandEnv("$HOME/.nvm/versions/node"),
	}

	// Check nvm installations (prefer latest)
	nvmBase := os.ExpandEnv("$HOME/.nvm/versions/node")
	if entries, err := os.ReadDir(nvmBase); err == nil {
		// Pick the latest version
		var best string
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "v") {
				p := filepath.Join(nvmBase, e.Name(), "bin", "node")
				if _, err := os.Stat(p); err == nil {
					best = p
				}
			}
		}
		if best != "" {
			return best
		}
	}

	// Standard paths
	for _, p := range []string{
		"/usr/local/bin/node",
		"/usr/bin/node",
		"/opt/homebrew/bin/node",
		"/snap/bin/node",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// Try PATH
	for _, name := range paths {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}

	if p, err := exec.LookPath("node"); err == nil {
		return p
	}

	return ""
}

func (kb *kinobaseBrowser) findChrome() string {
	paths := []string{
		// macOS
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		// Linux
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/snap/bin/chromium",
		// Windows (WSL)
		"/mnt/c/Program Files/Google/Chrome/Application/chrome.exe",
		"/mnt/c/Program Files (x86)/Google/Chrome/Application/chrome.exe",
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// Try PATH
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}

	return ""
}

func (kb *kinobaseBrowser) ensurePuppeteer(dir string) error {
	// Check if puppeteer-core is already installed
	modPath := filepath.Join(dir, "node_modules", "puppeteer-core")
	if _, err := os.Stat(modPath); err == nil {
		return nil // already installed
	}

	log.Info().Str("dir", dir).Msg("kinobase browser: installing puppeteer-core...")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, kb.nodePath, "-e",
		fmt.Sprintf("process.chdir(%q); require('child_process').execSync('npm install --no-save puppeteer-core', {stdio:'pipe'})",
			dir))
	cmd.Dir = dir

	// Also write a minimal package.json if not present
	pkgPath := filepath.Join(dir, "package.json")
	if _, err := os.Stat(pkgPath); os.IsNotExist(err) {
		_ = os.WriteFile(pkgPath, []byte(`{"private":true}`), 0644)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("npm install failed: %w: %s", err, string(out))
	}
	return nil
}

// kinobaseProfilePrefix names the Chrome profile directories this source owns.
// Registered in browsertmp.Prefixes so a hard kill still gets them swept.
const kinobaseProfilePrefix = "kinobase-chrome-"

// kinobaseBrowserTimeout bounds one extraction. It must stay BELOW the caller's
// budget — a capi drill gives each source 25s — so our own cleanup runs before
// the parent context cancels us. When the two deadlines coincide node dies
// mid-close and the browser is orphaned, which is exactly how this source used
// to leak Chrome processes.
const kinobaseBrowserTimeout = 20 * time.Second

// Extract runs the Puppeteer script and returns the player file data.
// proxyAddr is optional SOCKS5 address like "socks5://127.0.0.1:40001"
func (kb *kinobaseBrowser) Extract(ctx context.Context, filmURL, proxyAddr string) (string, error) {
	if err := kb.init(); err != nil {
		return "", err
	}

	// Own the profile directory rather than letting Puppeteer mint an unmanaged
	// /tmp/puppeteer_dev_chrome_profile-*. Those belonged to nobody, were skipped
	// by both janitors, and cost ~130MB each.
	profileDir, err := browsertmp.New(kinobaseProfilePrefix)
	if err != nil {
		return "", fmt.Errorf("kinobase profile dir: %w", err)
	}
	defer func() { go browsertmp.Remove(profileDir) }()

	// Positional argv: the script reads proxy at [4] and profile dir at [5], so
	// the proxy slot is always passed even when empty.
	args := []string{kb.scriptPath, filmURL, kb.chromePath, proxyAddr, profileDir}

	execCtx, cancel := context.WithTimeout(ctx, kinobaseBrowserTimeout)
	defer cancel()

	cmd := exec.Command(kb.nodePath, args...)
	cmd.Dir = os.TempDir()
	// Set NODE_PATH so require('puppeteer-core') works
	cmd.Env = append(os.Environ(),
		"NODE_PATH="+filepath.Join(os.TempDir(), "node_modules"),
	)
	setProcGroup(cmd)

	out, err := runKinobaseNode(execCtx, cmd)
	if err != nil {
		return "", fmt.Errorf("puppeteer exec: %w: %s", err, string(out))
	}

	// Parse JSON output (last line)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return "", fmt.Errorf("no output from puppeteer")
	}
	lastLine := lines[len(lines)-1]

	var result kinobaseBrowserResult
	if err := stdjson.Unmarshal([]byte(lastLine), &result); err != nil {
		return "", fmt.Errorf("parse result: %w: %s", err, lastLine)
	}

	if result.Error != "" {
		return "", fmt.Errorf("browser error: %s", result.Error)
	}
	return result.File, nil
}

// runKinobaseNode is exec.Cmd.CombinedOutput with a deadline that kills the
// whole process group instead of just the child.
//
// exec.CommandContext cannot be used here: its cancel path calls
// Process.Kill(), which reaches node and leaves the Chrome node spawned running
// under init. Both streams share one buffer, the same trick CombinedOutput uses
// so os/exec serialises them through a single pipe.
func runKinobaseNode(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return buf.Bytes(), err
	case <-ctx.Done():
		killProcGroup(cmd)
		<-done // reap the child so buf is no longer written to
		return buf.Bytes(), ctx.Err()
	}
}

// kinobaseExtractScript is the Node.js Puppeteer script embedded as a constant.
// It receives: <filmUrl> <chromePath> <socks5ProxyAddr|""> <profileDir>
// It outputs: JSON { "file": "...", "error": "" }
//
// Every timeout below is sized to finish inside kinobaseBrowserTimeout so the
// script closes its own browser; the Go-side process-group kill is the backstop,
// not the normal path.
const kinobaseExtractScript = `
const puppeteer = require('puppeteer-core');
const filmUrl = process.argv[2];
const chromePath = process.argv[3];
const proxyAddr = process.argv[4] || '';
const profileDir = process.argv[5] || '';

if (!filmUrl || !chromePath) {
    console.log(JSON.stringify({ error: 'usage: <url> <chromePath> <proxy|""> <profileDir>' }));
    process.exit(1);
}

(async () => {
    const args = ['--no-sandbox', '--disable-setuid-sandbox', '--disable-gpu',
                  '--disable-dev-shm-usage', '--disable-extensions',
                  '--disable-background-networking', '--disable-sync',
                  '--disable-translate', '--metrics-recording-only',
                  '--no-first-run', '--mute-audio'];
    if (proxyAddr) args.push('--proxy-server=' + proxyAddr);

    const launchOpts = {
        executablePath: chromePath,
        headless: 'new',
        args: args
    };
    // Given a userDataDir Puppeteer reuses it instead of creating its own
    // throwaway profile — that is what keeps the directory owned by Go, which
    // deletes it through browsertmp.
    if (profileDir) launchOpts.userDataDir = profileDir;

    const browser = await puppeteer.launch(launchOpts);

    // browser.close() can hang when a page is still busy, and a hung close was
    // how Chrome survived node's death. Race it, then SIGKILL what is left.
    const hardClose = async () => {
        try {
            await Promise.race([
                browser.close(),
                new Promise((r) => setTimeout(r, 3000))
            ]);
        } catch(e) {}
        try {
            const proc = browser.process();
            if (proc && !proc.killed) proc.kill('SIGKILL');
        } catch(e) {}
    };

    const timeout = setTimeout(async () => {
        console.log(JSON.stringify({ error: 'timeout' }));
        await hardClose();
        process.exit(1);
    }, 15000);

    try {
        const page = await browser.newPage();
        const host = new URL(filmUrl).hostname;
        await page.setCookie({
            name: 'player_settings', value: 'new|hls|0',
            domain: host, path: '/'
        });
        await page.setUserAgent('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36');

        await page.setRequestInterception(true);
        // Match any playerjs*.js — kinobase ships the file as
        // playerjs.uncompress.js since 2024 and the literal
        // "/playerjs.js" pattern missed it. That mismatch is why
        // kinobase appeared broken (the obfuscated PlayerJS bundle
        // ran instead of our stub, so #playerjsfile was never set).
        const isPlayerJS = (u) => {
            const lower = u.toLowerCase();
            if (!lower.includes('playerjs')) return false;
            const clean = u.split(/[?#]/)[0];
            const base = clean.substring(clean.lastIndexOf('/') + 1).toLowerCase();
            if (base.startsWith('playerjs') && base.endsWith('.js')) return true;
            if (clean.includes('/playerjs/') && clean.toLowerCase().endsWith('.js')) return true;
            return false;
        };
        page.on('request', (req) => {
            const url = req.url();
            if (isPlayerJS(url)) {
                req.respond({ status: 200, contentType: 'application/javascript',
                    body: 'function Playerjs(o){var e=document.getElementById("playerjsfile");if(!e){e=document.createElement("div");e.id="playerjsfile";e.style.display="none";document.body.appendChild(e);}e.textContent=o.file||"";}var pljssglobal=null,pljssglobalid=null;'
                });
                return;
            }
            if (url.includes('/comments') || ['image','font','media','stylesheet'].includes(req.resourceType())) {
                req.abort(); return;
            }
            req.continue();
        });

        await page.goto(filmUrl, { waitUntil: 'domcontentloaded', timeout: 9000 });
        try {
            await page.waitForSelector('#playerjsfile', { timeout: 5000 });
            const file = await page.$eval('#playerjsfile', el => el.textContent);
            clearTimeout(timeout);
            console.log(JSON.stringify({ file: file }));
        } catch(e) {
            let msg = '';
            try { msg = await page.$eval('.alert h3', el => el.textContent.trim()); } catch(e2) {}
            clearTimeout(timeout);
            console.log(JSON.stringify({ error: msg || 'playerjsfile not found' }));
        }
    } catch(e) {
        clearTimeout(timeout);
        console.log(JSON.stringify({ error: e.message }));
    } finally {
        await hardClose();
    }
})();
`
