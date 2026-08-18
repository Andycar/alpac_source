package litesrc

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/httpclient"
)

// NOTE: uses package-level `json` variable (jsoniter) defined in server.go.

// mirageGuardClient implements the Alloha "Guard" proof-of-browser system.
//
// The real browser player runs a challenge-response cycle:
//  1. GET /sarn → {challenge, nonce, uap}
//  2. proof = SHA-256(nonce + "||" + uap + "|" + parentOrigin + "|" + parentOrigin)
//  3. POST /vorf {challenge, proof, parentOrigin, referrer} → {ok, token}
//  4. The returned token is used as Accepts-Controls header on every CDN request
//
// Without Guard verification, CDN starts rejecting segments after ~4 minutes.
// Guard tokens are refreshed periodically (every ~2 min) to stay valid.
type mirageGuardClient struct {
	linkHost     string // e.g. "https://quadrillion-as.stloadi.live"
	parentOrigin string // e.g. "https://kinomix.web.app"
	referrer     string // e.g. "https://kinomix.web.app/"
	balancer     string // transport name for SOCKS5 routing (e.g. "alloha", "mirage")

	token atomic.Value // string: latest Guard-verified token
	done  chan struct{}
	mu    sync.Mutex
}

// sarnResponse is the JSON response from GET /sarn.
type sarnResponse struct {
	Challenge string `json:"challenge"`
	Nonce     string `json:"nonce"`
	UAP       string `json:"uap"`
}

// vorfResponse is the JSON response from POST /vorf.
type vorfResponse struct {
	OK    bool   `json:"ok"`
	Token string `json:"token"`
}

// newMirageGuardClient creates a new Guard client and starts background refresh.
// parentOrigin is typically extracted from the linkHost or can be a fixed origin.
func newMirageGuardClient(linkHost, balancer string) *mirageGuardClient {
	g := &mirageGuardClient{
		linkHost:     strings.TrimRight(linkHost, "/"),
		parentOrigin: "", // empty — Guard works without parentOrigin
		referrer:     "",
		balancer:     balancer,
		done:         make(chan struct{}),
	}

	// Initial token fetch.
	if tok, err := g.fetchToken(); err != nil {
		log.Warn().Err(err).Str("linkHost", linkHost).Msg("mirage guard: initial token fetch failed")
	} else {
		g.token.Store(tok)
		log.Info().Str("token", tok[:min(20, len(tok))]+"...").Msg("mirage guard: initial token OK")
	}

	// Background refresh loop.
	go g.refreshLoop()

	return g
}

// Token returns the current Guard-verified token, or "" if not available.
func (g *mirageGuardClient) Token() string {
	v := g.token.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

// Close stops the background refresh loop.
func (g *mirageGuardClient) Close() {
	select {
	case <-g.done:
	default:
		close(g.done)
	}
}

// refreshLoop periodically fetches fresh Guard tokens.
// Guard tokens seem to have a ~5 min TTL. Refresh every 90 seconds.
func (g *mirageGuardClient) refreshLoop() {
	ticker := time.NewTicker(90 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-g.done:
			return
		case <-ticker.C:
			tok, err := g.fetchToken()
			if err != nil {
				log.Warn().Err(err).Msg("mirage guard: refresh failed")
				continue
			}
			g.token.Store(tok)
			log.Debug().Str("token", tok[:min(20, len(tok))]+"...").Msg("mirage guard: refreshed")
		}
	}
}

// fetchToken performs one challenge-response cycle: GET /sarn → compute proof → POST /vorf.
func (g *mirageGuardClient) fetchToken() (string, error) {
	// Direct uTLS connection: linkHost endpoints (/sarn, /vorf) are not
	// geo-restricted, and forcing them through a possibly-flaky SOCKS5
	// sidecar produced ~70% timeout/reset failures in production.
	client := httpclient.NewUTLS(10 * time.Second)

	// Step 1: GET /sarn to get challenge.
	sarnURL := g.linkHost + "/sarn"
	req, err := http.NewRequest(http.MethodGet, sarnURL, nil)
	if err != nil {
		return "", fmt.Errorf("sarn request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	if g.referrer != "" {
		req.Header.Set("Referer", g.referrer)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sarn fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sarn status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return "", fmt.Errorf("sarn read: %w", err)
	}

	var sarn sarnResponse
	if err := json.Unmarshal(body, &sarn); err != nil {
		return "", fmt.Errorf("sarn parse: %w", err)
	}
	if sarn.Challenge == "" || sarn.Nonce == "" {
		return "", fmt.Errorf("sarn missing challenge/nonce")
	}

	// Step 2: Compute proof = SHA-256(nonce + "||" + uap + "|" + parentOrigin + "|" + parentOrigin)
	parentOrigin := g.parentOrigin
	proofStr := sarn.Nonce + "||" + sarn.UAP + "|" + parentOrigin + "|" + parentOrigin
	hash := sha256.Sum256([]byte(proofStr))
	proof := fmt.Sprintf("%x", hash)

	// Step 3: POST /vorf with challenge + proof.
	vorfURL := g.linkHost + "/vorf"
	vorfBody, _ := json.Marshal(map[string]string{
		"challenge":    sarn.Challenge,
		"proof":        proof,
		"parentOrigin": parentOrigin,
		"referrer":     g.referrer,
	})

	vReq, err := http.NewRequest(http.MethodPost, vorfURL, strings.NewReader(string(vorfBody)))
	if err != nil {
		return "", fmt.Errorf("vorf request: %w", err)
	}
	vReq.Header.Set("Content-Type", "application/json")
	vReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	if g.referrer != "" {
		vReq.Header.Set("Referer", g.referrer)
	}

	vResp, err := client.Do(vReq)
	if err != nil {
		return "", fmt.Errorf("vorf fetch: %w", err)
	}
	defer vResp.Body.Close()

	if vResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vorf status %d", vResp.StatusCode)
	}

	vBody, err := io.ReadAll(io.LimitReader(vResp.Body, 4<<10))
	if err != nil {
		return "", fmt.Errorf("vorf read: %w", err)
	}

	var vorf vorfResponse
	if err := json.Unmarshal(vBody, &vorf); err != nil {
		return "", fmt.Errorf("vorf parse: %w", err)
	}
	if !vorf.OK || vorf.Token == "" {
		return "", fmt.Errorf("vorf not ok: %s", string(vBody))
	}

	return vorf.Token, nil
}

// Guard client registry — one per linkHost (Mirage and Aladdin use different hosts).
var (
	guardClients   = make(map[string]*mirageGuardClient)
	guardClientsMu sync.Mutex
)

// getOrCreateGuardClient returns the Guard client for the given linkHost, creating if needed.
// balancer is the transport name for SOCKS5 routing (e.g. "alloha", "mirage", "aladdin").
func getOrCreateGuardClient(linkHost, balancer string) *mirageGuardClient {
	guardClientsMu.Lock()
	defer guardClientsMu.Unlock()

	if g, ok := guardClients[linkHost]; ok {
		return g
	}
	g := newMirageGuardClient(linkHost, balancer)
	guardClients[linkHost] = g
	return g
}

// GetGuardToken returns the current Guard-verified token for the given linkHost, or "".
func GetGuardToken(linkHost string) string {
	if linkHost == "" {
		return ""
	}
	guardClientsMu.Lock()
	g, ok := guardClients[linkHost]
	guardClientsMu.Unlock()
	if !ok {
		return ""
	}
	return g.Token()
}
