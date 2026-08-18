// Quick probe: direct navigate to zetflix iplayer, discover CDN domains
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func main() {
	kinopoiskID := "447301" // Inception

	// Step 1: Resolve go.zet-flix.online to current date-based domain
	fmt.Println("=== Step 1: Resolve go.zet-flix.online ===")
	resolvedHost := resolveGoHost()
	fmt.Printf("  Resolved: %s\n", resolvedHost)

	// Only test the resolved host since .skin returns video_not_found
	target := resolvedHost + "/iplayer/videodb.php?kp=" + kinopoiskID
	fmt.Printf("  Target: %s\n", target)

	// Step 2: chromedp - extract full file data
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()

	fmt.Printf("\n=== Step 2: chromedp probe ===\n")
	probeWithChromedp(allocCtx, target)
}

func resolveGoHost() string {
	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", "https://go.zet-flix.online", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
		return "https://zet-flix.online"
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	bodyStr := string(body)

	re := regexp.MustCompile(`"([^"]+)"\);</script>`)
	m := re.FindStringSubmatch(bodyStr)
	if len(m) > 1 {
		resolved := "https://" + strings.TrimSpace(m[1])
		return resolved
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		return strings.TrimRight(loc, "/")
	}

	return "https://zet-flix.online"
}

func probeWithChromedp(allocCtx context.Context, target string) {
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	tctx, tcancel := context.WithTimeout(ctx, 45*time.Second)
	defer tcancel()

	var (
		mu      sync.Mutex
		xhrData []xhrCapture
		netReqs []string
	)

	err := chromedp.Run(tctx,
		network.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			chromedp.ListenTarget(ctx, func(ev any) {
				switch e := ev.(type) {
				case *network.EventRequestWillBeSent:
					mu.Lock()
					netReqs = append(netReqs, e.Request.URL)
					mu.Unlock()
				case *network.EventResponseReceived:
					if e.Type == network.ResourceTypeXHR || e.Type == network.ResourceTypeFetch {
						go func() {
							body, err := network.GetResponseBody(e.RequestID).Do(ctx)
							if err != nil {
								return
							}
							mu.Lock()
							xhrData = append(xhrData, xhrCapture{url: e.Response.URL, body: string(body)})
							mu.Unlock()
						}()
					}
				}
			})
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var ignore any
			return chromedp.Run(ctx, chromedp.Evaluate(`
				Object.defineProperty(navigator, 'webdriver', { get: () => false });
				Object.defineProperty(navigator, 'plugins', { get: () => [1,2,3,4,5] });
				Object.defineProperty(navigator, 'languages', { get: () => ['ru-RU','ru','en-US','en'] });
				window.chrome = { runtime: {} };
			`, &ignore))
		}),
		chromedp.Navigate(target),
	)
	if err != nil {
		fmt.Printf("  Navigate error: %v\n", err)
		return
	}

	_ = chromedp.Run(tctx, chromedp.WaitReady("body"))
	fmt.Println("  Page loaded, polling...")

	// Enhanced extraction: get full file data + player config
	extractJS := `
		(function() {
			var el = document.getElementById('player');
			if (el && el.playerjs && el.playerjs.option && el.playerjs.option.file) {
				var f = el.playerjs.option.file;
				return JSON.stringify({
					src: 'playerjs',
					file: typeof f === 'string' ? f : JSON.stringify(f),
					poster: el.playerjs.option.poster || '',
					id: el.playerjs.option.id || ''
				});
			}
			if (typeof pljssglobal !== 'undefined' && pljssglobal.file) {
				return JSON.stringify({
					src: 'pljssglobal',
					file: typeof pljssglobal.file === 'string' ? pljssglobal.file : JSON.stringify(pljssglobal.file),
					poster: pljssglobal.poster || '',
					id: pljssglobal.id || ''
				});
			}
			if (typeof plr !== 'undefined' && plr.option && plr.option.file) {
				return JSON.stringify({
					src: 'plr',
					file: typeof plr.option.file === 'string' ? plr.option.file : JSON.stringify(plr.option.file),
					poster: plr.option.poster || '',
					id: plr.option.id || ''
				});
			}
			var divs = document.querySelectorAll('div');
			for (var i = 0; i < divs.length; i++) {
				if (divs[i].playerjs && divs[i].playerjs.option && divs[i].playerjs.option.file) {
					var f3 = divs[i].playerjs.option.file;
					return JSON.stringify({
						src: 'div_playerjs',
						file: typeof f3 === 'string' ? f3 : JSON.stringify(f3),
						poster: divs[i].playerjs.option.poster || '',
						id: divs[i].playerjs.option.id || ''
					});
				}
			}
			return '';
		})()
	`

	var fileData string
	for i := range 20 {
		time.Sleep(time.Second)
		err = chromedp.Run(tctx, chromedp.Evaluate(extractJS, &fileData))
		if err != nil {
			fmt.Printf("  [%ds] Eval error: %v\n", i+1, err)
			break
		}
		if fileData != "" {
			fmt.Printf("  [%ds] ✅ Found player data!\n", i+1)
			fmt.Printf("\n=== FULL PLAYER DATA ===\n%s\n", fileData)
			extractDomains("player data", fileData)

			// Extract quality URLs specifically
			qualRe := regexp.MustCompile(`\[(1080|720|480|360)p?\]([^\[\|,\\"'\s]+)`)
			qualMatches := qualRe.FindAllStringSubmatch(fileData, -1)
			if len(qualMatches) > 0 {
				fmt.Printf("\n=== STREAM URLs ===\n")
				for _, m := range qualMatches {
					if len(m) >= 3 {
						fmt.Printf("  [%sp] %s\n", m[1], m[2])
					}
				}
			}

			// Try to extract unique CDN URLs
			urlRe := regexp.MustCompile(`https?://[^\s"'\\,\]]+\.(?:mp4|m3u8)`)
			urlMatches := urlRe.FindAllString(fileData, 20)
			if len(urlMatches) > 0 {
				fmt.Printf("\n=== MEDIA URLs ===\n")
				for _, u := range urlMatches {
					fmt.Printf("  %s\n", u)
				}
			}
			return
		}
	}

	fmt.Println("  ❌ No player data found.")

	// Fallback: check HTML for file:
	var html string
	_ = chromedp.Run(tctx, chromedp.OuterHTML("html", &html))
	fmt.Printf("  HTML length: %d\n", len(html))

	if strings.Contains(html, "file:") {
		idx := strings.Index(html, "file:")
		start := idx
		end := min(idx+3000, len(html))
		fmt.Printf("\n=== file: context (first 3000 chars) ===\n%s\n", html[start:end])
		extractDomains("HTML", html)

		// Extract stream URLs from HTML
		qualRe := regexp.MustCompile(`\[(1080|720|480|360)p?\]([^\[\|,\\"'\s]+)`)
		qualMatches := qualRe.FindAllStringSubmatch(html, -1)
		if len(qualMatches) > 0 {
			fmt.Printf("\n=== STREAM URLs from HTML ===\n")
			for _, m := range qualMatches {
				if len(m) >= 3 {
					fmt.Printf("  [%sp] %s\n", m[1], m[2])
				}
			}
		}
	}

	// Network requests
	mu.Lock()
	reqs := netReqs
	mu.Unlock()
	fmt.Printf("\n  All network requests (%d):\n", len(reqs))
	for _, u := range reqs {
		fmt.Printf("    %s\n", u)
	}
}

type xhrCapture struct {
	url  string
	body string
}

func extractDomains(source, data string) {
	re := regexp.MustCompile(`https?://[a-zA-Z0-9._\-]+\.[a-zA-Z]{2,}`)
	matches := re.FindAllString(data, -1)
	seen := map[string]int{}
	for _, m := range matches {
		seen[m]++
	}
	if len(seen) > 0 {
		fmt.Printf("\n  Domains from %s:\n", source)
		for domain, count := range seen {
			marker := ""
			if strings.Contains(domain, "cdn") || strings.Contains(domain, "stream") ||
				strings.Contains(domain, "video") || strings.Contains(domain, "hls") ||
				strings.Contains(domain, "hdvideo") || strings.Contains(domain, "prosto") {
				marker = " ← CDN"
			}
			fmt.Printf("    %s (×%d)%s\n", domain, count, marker)
		}
	}
}
