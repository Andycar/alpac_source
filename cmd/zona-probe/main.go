// zona-probe: drive headless Chrome through the zona.im player flow and
// capture the m3u8/CDN requests that the kinoserial.online iframe issues.
//
// Usage:
//
//	go run ./cmd/zona-probe -url "https://w1.zona.im/movies/krik-7"
//
// Prereq: Chrome running with --remote-debugging-port=9333 and
// --disable-features=IsolateOrigins,site-per-process (so cross-origin iframe
// requests show up on the top-level Network domain).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func main() {
	spyURL := flag.String("spy", "", "if set, navigate to this zona page and spy on real getStreams() calls")
	port := flag.Int("port", 9333, "Chrome remote debugging port")
	wait := flag.Duration("wait", 30*time.Second, "total time to observe network")
	kpID := flag.Int("kp", 5364826, "kinopoisk ID")
	season := flag.Int("season", 0, "season number (0 for movies)")
	episode := flag.Int("episode", 0, "episode number (0 for movies)")
	flag.Parse()

	episodeKey := ""
	if *season > 0 && *episode > 0 {
		episodeKey = fmt.Sprintf("S%02dE%02d", *season, *episode)
	}

	// Resolve WS URL
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", *port))
	if err != nil {
		fmt.Printf("no Chrome on :%d — start with --remote-debugging-port=%d --disable-features=IsolateOrigins,site-per-process\n", *port, *port)
		return
	}
	var ver struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	json.NewDecoder(resp.Body).Decode(&ver)
	resp.Body.Close()

	allocCtx, cancel1 := chromedp.NewRemoteAllocator(context.Background(), ver.WS)
	defer cancel1()
	ctx, cancel2 := chromedp.NewContext(allocCtx)
	defer cancel2()
	ctx, cancel3 := context.WithTimeout(ctx, *wait+15*time.Second)
	defer cancel3()

	var mu sync.Mutex
	type req struct {
		method string
		url    string
		rtype  string
	}
	var reqs []req
	seen := make(map[string]bool)
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			mu.Lock()
			if !seen[e.Request.URL] {
				seen[e.Request.URL] = true
				reqs = append(reqs, req{method: e.Request.Method, url: e.Request.URL, rtype: string(e.Type)})
			}
			mu.Unlock()
		case *runtime.EventConsoleAPICalled:
			var parts []string
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				} else {
					parts = append(parts, a.Description)
				}
			}
			fmt.Printf("console[%s]: %s\n", e.Type, strings.Join(parts, " "))
		case *runtime.EventExceptionThrown:
			fmt.Printf("exception: %s\n", e.ExceptionDetails.Error())
		}
	})

	if *spyURL != "" {
		// Spy mode: navigate to real zona page, inject getStreams hook BEFORE loader runs.
		err := chromedp.Run(ctx,
			network.Enable(),
			runtime.Enable(),
			page.Enable(),
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, err := page.AddScriptToEvaluateOnNewDocument(`
				(function(){
					var installed = false;
					function installHook() {
						if (installed) return;
						var api = window['ru.zona.stream.site'];
						if (!api || typeof api.getStreams !== 'function') return;
						installed = true;
						var orig = api.getStreams;
						api.getStreams = function() {
							console.log('ZONA_CALL', JSON.stringify(Array.prototype.slice.call(arguments, 0, 3)));
							return orig.apply(this, arguments);
						};
					}
					setInterval(installHook, 200);
				})();
			`).Do(ctx)
				return err
			}),
			chromedp.Navigate(*spyURL),
			chromedp.Sleep(8*time.Second),
		)
		if err != nil {
			fmt.Println("spy nav err:", err)
			return
		}
		// Try clicking iframe
		var rectJSON string
		chromedp.Run(ctx, chromedp.Evaluate(`(function(){var f=document.querySelector('iframe');if(!f)return '';var r=f.getBoundingClientRect();return JSON.stringify({x:r.x+r.width/2,y:r.y+r.height/2});})()`, &rectJSON))
		var pos struct{ X, Y float64 }
		if rectJSON != "" {
			json.Unmarshal([]byte(rectJSON), &pos)
			for i := 0; i < 3; i++ {
				chromedp.Run(ctx, chromedp.MouseClickXY(pos.X, pos.Y))
				chromedp.Run(ctx, chromedp.Sleep(1500*time.Millisecond))
			}
		}
		chromedp.Run(ctx, chromedp.Sleep(*wait))
		return
	}

	// Navigate directly to the player page — we'll call its internal
	// getStreams API instead of going through the parent postMessage dance.
	target := "https://kinoserial.online/?projectId=1&projectName=zona"
	err = chromedp.Run(ctx,
		network.Enable(),
		runtime.Enable(),
		page.Enable(),
		chromedp.Navigate(target),
		chromedp.Sleep(6*time.Second),
	)
	if err != nil {
		fmt.Println("nav err:", err)
		return
	}

	// Install window.__zona_streams collector, then call getStreams.
	callJS := fmt.Sprintf(`(function(){
		window.__zona_streams = [];
		window.__zona_done = false;
		window.__zona_err = '';
		var api = window['ru.zona.stream.site'];
		if (!api || typeof api.getStreams !== 'function') {
			window.__zona_err = 'no api: ' + (typeof api);
			window.__zona_done = true;
			return 'no api';
		}
		try {
			var arg2 = false; // isTrailer-ish slot
			var arg3 = %q || null;
			api.getStreams(%d, arg2, arg3, {
				onStreamsReceived: function(s){ window.__zona_streams.push(s); },
				onCompletion: function(n){ window.__zona_done = true; window.__zona_count = n; }
			});
			return 'called';
		} catch (e) {
			window.__zona_err = e.message;
			window.__zona_done = true;
			return 'err ' + e.message;
		}
	})()`, episodeKey, *kpID)

	var r1 string
	chromedp.Run(ctx, chromedp.Evaluate(callJS, &r1))
	fmt.Println("getStreams call:", r1)

	// Observe network + poll collector
	deadline := time.Now().Add(*wait)
	for time.Now().Before(deadline) {
		var done bool
		chromedp.Run(ctx, chromedp.Evaluate(`window.__zona_done || false`, &done))
		if done {
			break
		}
		chromedp.Run(ctx, chromedp.Sleep(1500*time.Millisecond))
	}

	var streamsDump string
	chromedp.Run(ctx, chromedp.Evaluate(`JSON.stringify({streams:window.__zona_streams||[], done:window.__zona_done, err:window.__zona_err||'', count:window.__zona_count})`, &streamsDump))
	fmt.Println("\n=== STREAMS DUMP ===")
	fmt.Println(streamsDump)

	// Screenshot for debugging
	var buf []byte
	chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 70))
	if len(buf) > 0 {
		os.WriteFile("/tmp/zona_screen.png", buf, 0644)
	}

	mu.Lock()
	defer mu.Unlock()
	fmt.Printf("\n=== %d unique requests ===\n", len(reqs))
	for _, r := range reqs {
		u := r.url
		if strings.HasPrefix(u, "data:") ||
			strings.Contains(u, "fonts.googleapis") ||
			strings.Contains(u, "google-analytics") ||
			strings.Contains(u, "mc.yandex") ||
			strings.Contains(u, "unpkg.com") ||
			strings.Contains(u, "zonapic.com") ||
			strings.Contains(u, "w1.zona.im/_next") ||
			strings.Contains(u, "w1.zona.im/assets") ||
			strings.Contains(u, "w1.zona.im/icon") ||
			strings.Contains(u, "favicon") ||
			strings.Contains(u, "/api/assist") {
			continue
		}
		fmt.Printf("%s [%s] %s\n", r.method, r.rtype, r.url)
	}

	fmt.Println("\n=== interesting ===")
	for _, r := range reqs {
		lu := strings.ToLower(r.url)
		if strings.Contains(lu, "m3u8") || strings.Contains(lu, "vibio") || strings.Contains(lu, "cdn") || strings.Contains(lu, "manifest") || strings.Contains(lu, ".mp4") || strings.Contains(lu, ".ts") || strings.Contains(lu, "/api/") || strings.Contains(lu, "kinoserial.online/") {
			fmt.Printf("%s [%s] %s\n", r.method, r.rtype, r.url)
		}
	}
}
