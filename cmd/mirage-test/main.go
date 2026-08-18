// mirage-test: standalone CDN segment fetcher for debugging token lifetime.
// Usage: go run ./cmd/mirage-test -url "https://cdn.../TOKEN/index-f1-v1.m3u8" -hash "edge_hash"
//
// Fetches segments sequentially from the given HLS playlist URL,
// reporting success/failure for each. Runs until stopped or all segments fail.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

func main() {
	playlistURL := flag.String("url", "", "variant playlist URL (index-f1-v1.m3u8)")
	edgeHash := flag.String("hash", "", "current edge_hash for Accepts-Controls")
	origin := flag.String("origin", "https://quadrillion-as.stloadi.live", "Origin header")
	proxyAddr := flag.String("proxy", "", "SOCKS5 proxy (e.g. 127.0.0.1:40006)")
	startSeg := flag.Int("start", 1, "starting segment number")
	interval := flag.Duration("interval", 4*time.Second, "interval between segments")
	flag.Parse()

	if *playlistURL == "" {
		fmt.Println("Usage: mirage-test -url <playlist_url> -hash <edge_hash>")
		os.Exit(1)
	}

	// Extract base URL from playlist
	baseURL := *playlistURL
	if idx := strings.LastIndex(baseURL, "/"); idx > 0 {
		baseURL = baseURL[:idx+1]
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	if *proxyAddr != "" {
		// Try SOCKS5
		dialer, err := proxy.SOCKS5("tcp", *proxyAddr, nil, proxy.Direct)
		if err != nil {
			fmt.Printf("SOCKS5 proxy error: %v\n", err)
			os.Exit(1)
		}
		transport.Dial = dialer.Dial
		// Also set proxy URL for HTTPS
		proxyURL, _ := url.Parse("socks5://" + *proxyAddr)
		transport.Proxy = http.ProxyURL(proxyURL)
		fmt.Printf("Using SOCKS5 proxy: %s\n", *proxyAddr)
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	// If no hash provided, try without it
	hash := *edgeHash

	fmt.Printf("Base URL: %s\n", baseURL)
	fmt.Printf("Edge hash: %s\n", hash)
	fmt.Printf("Origin: %s\n", *origin)
	fmt.Printf("Interval: %s\n", *interval)
	fmt.Printf("Starting from seg-%d\n\n", *startSeg)

	startTime := time.Now()
	consecFails := 0

	for seg := *startSeg; ; seg++ {
		elapsed := time.Since(startTime).Round(time.Second)

		// Try both video and audio
		for _, track := range []string{"v1", "a1"} {
			segURL := fmt.Sprintf("%sseg-%d-f1-%s.ts", baseURL, seg, track)

			req, _ := http.NewRequest("GET", segURL, nil)
			req.Header.Set("Accept", "*/*")
			req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
			req.Header.Set("Origin", *origin)
			req.Header.Set("Referer", *origin+"/")
			req.Header.Set("Sec-Ch-Ua", `"Chromium";v="131", "Not_A Brand";v="24"`)
			req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
			req.Header.Set("Sec-Ch-Ua-Platform", `"Linux"`)
			req.Header.Set("Sec-Fetch-Dest", "empty")
			req.Header.Set("Sec-Fetch-Mode", "cors")
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

			if hash != "" {
				req.Header.Set("Accepts-Controls", hash)
				req.Header["pc_hash"] = []string{hash}
			}

			resp, err := client.Do(req)
			if err != nil {
				fmt.Printf("[%s] seg-%d-%s: ERROR %v\n", elapsed, seg, track, err)
				consecFails++
				continue
			}

			size, _ := io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			status := resp.StatusCode
			if status == 200 || status == 206 {
				fmt.Printf("[%s] seg-%d-%s: %d (%d KB) ✓\n", elapsed, seg, track, status, size/1024)
				consecFails = 0
			} else {
				fmt.Printf("[%s] seg-%d-%s: %d (%d bytes) ✗\n", elapsed, seg, track, status, size)
				consecFails++
			}
		}

		if consecFails >= 10 {
			fmt.Printf("\n10 consecutive failures — CDN token expired after %s\n", time.Since(startTime).Round(time.Second))
			break
		}

		time.Sleep(*interval)
	}

	// Also test: fetch the playlist itself
	fmt.Printf("\n--- Testing playlist fetch ---\n")
	req, _ := http.NewRequest("GET", *playlistURL, nil)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", *origin)
	req.Header.Set("Referer", *origin+"/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	if hash != "" {
		req.Header.Set("Accepts-Controls", hash)
		req.Header["pc_hash"] = []string{hash}
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Playlist: ERROR %v\n", err)
	} else {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Printf("Playlist: %d (%d bytes)\n", resp.StatusCode, len(body))
		if resp.StatusCode == 200 {
			// Count segments
			re := regexp.MustCompile(`seg-\d+-f1-v1\.ts`)
			segs := re.FindAllString(string(body), -1)
			fmt.Printf("Segments in playlist: %d\n", len(segs))
		}
	}
}
