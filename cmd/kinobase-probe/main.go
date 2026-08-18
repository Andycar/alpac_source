// kinobase-probe resolves a kinobase film URL through the new
// internal/browser facade engine (rod by default). Mirrors what
// ExtractFacade in internal/httpapi/kinobase_browser_facade.go does
// without dragging the rest of lampac into the build.
//
// Usage:
//
//	go run ./cmd/kinobase-probe https://kinobase.org/films/...
//
// Optional flags:
//
//	-engine rod|chromedp     (default rod)
//	-proxy host:port         SOCKS5
//	-timeout 30s             total deadline
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"lampac-go/internal/browser"
)

const playerJSStub = `function Playerjs(o){var e=document.getElementById("playerjsfile");if(!e){e=document.createElement("div");e.id="playerjsfile";e.style.display="none";document.body.appendChild(e);}e.textContent=o.file||"";}var pljssglobal=null,pljssglobalid=null;`

func main() {
	var (
		engineName = flag.String("engine", "rod", "browser engine: rod | chromedp")
		proxy      = flag.String("proxy", "", "SOCKS5 host:port (no auth)")
		proxyAuth  = flag.String("proxy-auth", "", "SOCKS5 host:port with auth — needs -proxy-user / -proxy-pass; we spin up a local NO_AUTH forwarder for Chrome")
		proxyUser  = flag.String("proxy-user", "", "SOCKS5 username for -proxy-auth")
		proxyPass  = flag.String("proxy-pass", "", "SOCKS5 password for -proxy-auth")
		timeout    = flag.Duration("timeout", 60*time.Second, "overall timeout")
		verbose    = flag.Bool("v", false, "verbose: print intercepted requests")
	)
	flag.Parse()

	// If the caller specified an authenticated upstream, bridge it
	// through a local no-auth SOCKS5 listener — Chrome can't pass
	// credentials via --proxy-server.
	if *proxyAuth != "" {
		local, err := startSocks5Forwarder(*proxyAuth, *proxyUser, *proxyPass)
		if err != nil {
			fmt.Fprintf(os.Stderr, "socks5 forwarder: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("local SOCKS5 forwarder %s → %s (auth: %s)\n", local, *proxyAuth, *proxyUser)
		*proxy = local
	}
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: kinobase-probe [flags] <film_url>")
		flag.Usage()
		os.Exit(2)
	}
	filmURL := flag.Arg(0)

	eng, err := browser.Get(*engineName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "browser.Get(%q): %v\n", *engineName, err)
		os.Exit(1)
	}
	if err := eng.Available(); err != nil {
		fmt.Fprintf(os.Stderr, "engine not available: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("engine=%s ok\n", eng.Name())

	parsed, err := url.Parse(filmURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse URL: %v\n", err)
		os.Exit(1)
	}
	host := parsed.Hostname()

	// Many RU ISPs (and the default macOS router DNS) blackhole
	// kinobase.org with NXDOMAIN. Resolve it via Cloudflare 1.1.1.1
	// out-of-band and pin the result into Chromium via
	// --host-resolver-rules so the in-browser DNS lookup can't fail
	// the same way.
	extraFlags := []string{}
	if ips, derr := resolveVia("1.1.1.1:53", host); derr != nil {
		fmt.Fprintf(os.Stderr, "DNS lookup via 1.1.1.1 failed: %v\n", derr)
	} else if len(ips) > 0 {
		fmt.Printf("resolved %s via 1.1.1.1 → %s\n", host, ips[0])
		extraFlags = append(extraFlags,
			fmt.Sprintf("host-resolver-rules=MAP %s %s", host, ips[0]),
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	t0 := time.Now()
	session, err := eng.NewSession(ctx, browser.SessionOptions{
		Headless:          true,
		UseStealth:        true,
		UserAgent:         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		SocksProxy:        *proxy,
		ExtraFlags:        extraFlags,
		NavigationTimeout: 20 * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewSession: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()
	fmt.Printf("session ready in %v\n", time.Since(t0))

	if err := session.SetCookie(&http.Cookie{
		Name: "player_settings", Value: "new|hls|0", Path: "/",
	}, host); err != nil {
		fmt.Printf("SetCookie (non-fatal): %v\n", err)
	}

	hijackCancel, err := session.Hijack([]string{"*"}, func(req browser.HijackRequest) {
		u := req.URL()
		rt := strings.ToLower(req.ResourceType())
		if *verbose {
			fmt.Printf("  [%s] %s %s\n", rt, req.Method(), u)
		}
		if matchPlayerJS(u) {
			if *verbose {
				fmt.Printf("  ↳ FULFILLED stub for %s\n", u)
			}
			_ = req.Fulfill(200, http.Header{"Content-Type": []string{"application/javascript"}}, []byte(playerJSStub))
			return
		}
		if strings.Contains(u, "/comments") {
			_ = req.Abort("aborted")
			return
		}
		switch rt {
		case "image", "font", "media", "stylesheet":
			_ = req.Abort("aborted")
			return
		}
		_ = req.Continue()
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Hijack setup: %v\n", err)
		os.Exit(1)
	}
	defer hijackCancel()

	t1 := time.Now()
	if err := session.Navigate(filmURL); err != nil {
		fmt.Printf("Navigate returned (continuing): %v\n", err)
	} else {
		fmt.Printf("navigated in %v\n", time.Since(t1))
	}

	deadline := time.Now().Add(15 * time.Second)
	pollStart := time.Now()
	for time.Now().Before(deadline) {
		var file string
		err := session.Eval(`(function(){
			var el = document.getElementById('playerjsfile');
			return el ? (el.textContent || '') : '';
		})()`, &file)
		if err == nil && strings.TrimSpace(file) != "" {
			fmt.Printf("resolved in %v (total %v)\n", time.Since(pollStart), time.Since(t0))
			fmt.Println("---")
			fmt.Println(file)
			return
		}
		// Early bail-out: if kinobase showed a geo-block / not-available
		// alert there's no point polling for 15s.
		var earlyAlert string
		_ = session.Eval(`(function(){
			var el = document.querySelector('.alert h3');
			return el ? (el.textContent || '').trim() : '';
		})()`, &earlyAlert)
		if earlyAlert != "" {
			fmt.Fprintf(os.Stderr, "kinobase alert (early): %s\n", earlyAlert)
			os.Exit(1)
		}
		time.Sleep(250 * time.Millisecond)
	}

	var alert string
	_ = session.Eval(`(function(){
		var el = document.querySelector('.alert h3');
		return el ? (el.textContent || '').trim() : '';
	})()`, &alert)
	if alert != "" {
		fmt.Fprintf(os.Stderr, "kinobase alert: %s\n", alert)
	}
	fmt.Fprintf(os.Stderr, "playerjsfile not populated within 15s; aborting\n")
	os.Exit(1)
}

// matchPlayerJS — see internal/httpapi/kinobase_browser_facade.go
// for the production version. Probe replicates the predicate so we
// don't import internal/httpapi.
func matchPlayerJS(u string) bool {
	if !strings.Contains(strings.ToLower(u), "playerjs") {
		return false
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	base := u
	if i := strings.LastIndex(u, "/"); i >= 0 {
		base = u[i+1:]
	}
	if strings.HasPrefix(strings.ToLower(base), "playerjs") && strings.HasSuffix(base, ".js") {
		return true
	}
	if strings.Contains(u, "/playerjs/") && strings.HasSuffix(strings.ToLower(u), ".js") {
		return true
	}
	return false
}

// resolveVia issues a direct DNS A-query against the given resolver
// (e.g. "1.1.1.1:53") bypassing the system's stub resolver. Used to
// dodge ISP-level NXDOMAIN poisoning.
func resolveVia(resolverAddr, host string) ([]string, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, resolverAddr)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := r.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	out := addrs[:0]
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return addrs, nil
	}
	return out, nil
}
