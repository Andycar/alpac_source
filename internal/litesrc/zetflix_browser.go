package litesrc

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// zetflixBrowserSem limits concurrent anti-bot bypass sessions for zetflix.
var zetflixBrowserSem = make(chan struct{}, 4)

// zetflixTokenRe extracts the anti-bot token from the obfuscated JS.
// Pattern: window[String["fromCharCode"](0x74,0x6f,0x6b,0x65,0x6e)]=String["fromCharCode"](0x32,0x61,...)
// The first fromCharCode spells "token", the second is the actual token value.
var zetflixTokenRe = regexp.MustCompile(`String\["fromCharCode"\]\((0x[0-9a-f][0-9a-f, x]*)\)`)

const zetflixUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

// zetflixFetchEmbedBrowser performs the anti-bot bypass via pure HTTP.
// Algorithm:
//  1. GET the embed URL → receive obfuscated JS + PHPSESSID cookie
//  2. Extract token from String["fromCharCode"](...) at end of script
//  3. Compute hash = SHA1(MD5(token + ";" + host + ";" + userAgent))
//  4. POST to same URL with hash=<hash> body, Authorization: Bearer <token> header
//  5. GET same URL again with same session cookie → receive real player HTML
func zetflixFetchEmbedBrowser(ctx context.Context, apiHost string, kinopoiskID int64, season int) (string, bool) {
	return zetflixFetchEmbedBrowserWithHTML(ctx, apiHost, kinopoiskID, season, "")
}

// zetflixFetchEmbedBrowserWithHTML performs the anti-bot bypass.
// If prefetchedHTML is non-empty, it uses that HTML to extract the token
// (skipping the first GET), but still performs POST + re-GET with a fresh session.
func zetflixFetchEmbedBrowserWithHTML(ctx context.Context, apiHost string, kinopoiskID int64, season int, prefetchedHTML string) (string, bool) {
	select {
	case zetflixBrowserSem <- struct{}{}:
		defer func() { <-zetflixBrowserSem }()
	case <-ctx.Done():
		return "", false
	}

	target := strings.TrimRight(apiHost, "/") + "/iplayer/videodb.php?kp=" + strconv.FormatInt(kinopoiskID, 10)
	if season > 0 {
		target += "&season=" + strconv.Itoa(season)
	}

	log.Info().Str("url", target).Bool("prefetched", prefetchedHTML != "").Msg("zetflix: anti-bot bypass")

	// Fast path: FlareSolverr (headless Chrome) solves the JS challenge
	// natively. We just hand it the URL and get rendered HTML back. Avoids
	// fragility of token/hash extraction when the obfuscator changes.
	// Requires FlareSolverr URL configured and "zetflix" in flaresolverr_balancers.
	if res, ok := httpclient.FlareSolverrFetch(ctx, "zetflix", target, httpclient.FlareSolverrFetchOptions{
		Headers: map[string]string{
			"Referer":         "https://www.google.com/",
			"Accept-Language": "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7",
		},
	}); ok {
		html := res.Solution.Response
		// FlareSolverr returns a full HTML doc — strip the outer <html> wrapper
		// only if the inner content looks like our expected player HTML.
		if strings.Contains(html, "new Playerjs") || strings.Contains(html, "file:") || strings.Contains(html, "playerConfigs") {
			log.Info().Int("len", len(html)).Msg("zetflix: FlareSolverr returned player HTML")
			return html, true
		}
		log.Debug().Int("len", len(html)).Msg("zetflix: FlareSolverr response not player HTML, falling through")
	}

	// Create HTTP client with cookie jar (for PHPSESSID)
	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Warn().Err(err).Msg("zetflix: cookiejar creation failed")
		return "", false
	}
	// Use proxied transport if available (VLESS/SOCKS5 for non-RU servers).
	transport := httpclient.TransportForBalancer("zetflix")
	if transport == nil {
		transport = httpclient.SharedTransport
	}
	client := &http.Client{
		Transport: transport,
		Jar:       jar,
		Timeout:   14 * time.Second,
	}

	// Parse host from target URL for hash computation.
	host := apiHost
	if idx := strings.Index(host, "://"); idx >= 0 {
		host = host[idx+3:]
	}
	host = strings.TrimRight(host, "/")
	// Remove port if present
	if idx := strings.Index(host, ":"); idx >= 0 {
		host = host[:idx]
	}

	// Referer should be the zetflix site itself — the embed is loaded in an
	// iframe on a zetflix serial/movie page.  Using an external referer
	// (e.g. google.com) can cause videodb.php to return "video_not_found".
	siteReferer := strings.TrimRight(apiHost, "/") + "/"

	// ---- Step 1: GET the page to get obfuscated HTML + PHPSESSID ----
	// Always do our own GET to establish a fresh session+token pair.
	// The prefetchedHTML is ignored because the token is tied to the PHPSESSID.
	_ = prefetchedHTML
	var obfHTML string
	{
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return "", false
		}
		req.Header.Set("User-Agent", zetflixUserAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("DNT", "1")
		req.Header.Set("Referer", siteReferer)

		resp, err := client.Do(req)
		if err != nil {
			log.Warn().Err(err).Msg("zetflix: anti-bot GET failed")
			return "", false
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return "", false
		}
		obfHTML = string(body)
	}

	// ---- Step 2: Extract token ----
	token := zetflixExtractToken(obfHTML)
	if token == "" {
		log.Debug().Int("htmlLen", len(obfHTML)).Msg("zetflix: no anti-bot token found (page may not be obfuscated)")
		// Page is not protected by anti-bot — return raw HTML so the
		// caller can parse file: / folder: data directly.
		if strings.Contains(obfHTML, "file:") || strings.Contains(obfHTML, "file\":") || strings.Contains(obfHTML, "<html") {
			return obfHTML, true
		}
		return "", false
	}

	log.Debug().Str("token", token).Str("host", host).Msg("zetflix: extracted anti-bot token")

	// ---- Step 3: Compute hash = SHA1(MD5(token + ";" + host + ";" + UA)) ----
	raw := token + ";" + host + ";" + zetflixUserAgent
	md5sum := md5.Sum([]byte(raw))
	md5hex := hex.EncodeToString(md5sum[:])
	sha1sum := sha1.Sum([]byte(md5hex))
	hash := hex.EncodeToString(sha1sum[:])

	log.Debug().Str("hash", hash).Msg("zetflix: computed challenge hash")

	// ---- Step 4: POST with hash ----
	postBody := "hash=" + hash
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(postBody))
	if err != nil {
		return "", false
	}
	postReq.Header.Set("User-Agent", zetflixUserAgent)
	postReq.Header.Set("Authorization", "Bearer "+token)
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.Header.Set("Referer", target)
	postReq.Header.Set("Origin", fmt.Sprintf("https://%s", host))

	postResp, err := client.Do(postReq)
	if err != nil {
		log.Warn().Err(err).Msg("zetflix: anti-bot POST failed")
		return "", false
	}
	postRespBody, _ := io.ReadAll(io.LimitReader(postResp.Body, 1024))
	postResp.Body.Close()

	postResult := strings.TrimSpace(string(postRespBody))
	if !strings.Contains(postResult, "success") {
		log.Warn().Str("response", postResult).Msg("zetflix: anti-bot POST did not return success")
		return "", false
	}

	log.Info().Msg("zetflix: anti-bot challenge passed")

	// ---- Step 5: Re-GET with unlocked session ----
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	getReq.Header.Set("User-Agent", zetflixUserAgent)
	getReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	getReq.Header.Set("Referer", siteReferer)

	getResp, err := client.Do(getReq)
	if err != nil {
		log.Warn().Err(err).Msg("zetflix: anti-bot re-GET failed")
		return "", false
	}
	defer getResp.Body.Close()

	finalBody, err := io.ReadAll(io.LimitReader(getResp.Body, 8<<20))
	if err != nil {
		return "", false
	}

	result := string(finalBody)
	resultLen := len(result)

	// Check if the response is "Removed" / empty / error
	trimmed := strings.TrimSpace(result)
	if trimmed == "" || strings.Contains(trimmed, "video_not_found") {
		snippet := trimmed
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		log.Warn().Int("len", resultLen).Int("status", getResp.StatusCode).Str("body", snippet).Msg("zetflix: anti-bot bypass returned empty/not_found")
		return "", false
	}
	if strings.Contains(trimmed, "удалено") || strings.Contains(trimmed, "Removed this video") {
		log.Warn().Int("len", resultLen).Msg("zetflix: video removed on upstream")
		return "", false
	}

	log.Info().Int("len", resultLen).Msg("zetflix: anti-bot bypass returned content")
	return result, true
}

// zetflixExtractToken extracts the challenge token from obfuscated zetflix HTML.
// The token is encoded as String["fromCharCode"](0xHH, ...) at the end of the script.
// There are two fromCharCode calls: the first spells "token" (the key name),
// the second is the actual token value.
func zetflixExtractToken(html string) string {
	matches := zetflixTokenRe.FindAllStringSubmatch(html, -1)
	if len(matches) < 2 {
		return ""
	}

	// The last two matches: key name ("token") and token value.
	// Use the last match as the token value.
	hexStr := matches[len(matches)-1][1]
	return zetflixDecodeFromCharCode(hexStr)
}

// zetflixDecodeFromCharCode decodes a comma-separated hex string like "0x32,0x61,0x64,0x61"
// into a UTF-8 string.
func zetflixDecodeFromCharCode(hexCSV string) string {
	parts := strings.Split(hexCSV, ",")
	var sb strings.Builder
	sb.Grow(len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		val, err := strconv.ParseInt(p, 0, 32)
		if err != nil {
			continue
		}
		sb.WriteRune(rune(val))
	}
	return sb.String()
}

func zetMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
