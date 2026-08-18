package sidecar

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/curve25519"
)

const warpAPIBase = "https://api.cloudflareclient.com/v0a2169"

// WARPConfig holds the result of WARP registration.
type WARPConfig struct {
	PrivateKey    string `json:"private_key"`
	PeerPublicKey string `json:"peer_public_key"`
	Addresses     struct {
		V4 string `json:"v4"`
		V6 string `json:"v6"`
	} `json:"addresses"`
	Endpoint string `json:"endpoint"`
	Reserved []int  `json:"reserved"` // client_id bytes
}

// defaultWARPEndpoint is used whenever Cloudflare returns ":0" / empty /
// otherwise unparseable endpoints. The real WARP UDP port is 2408.
const defaultWARPEndpoint = "162.159.193.10:2408"

// RegisterOrLoadWARP registers with Cloudflare WARP API or loads cached config.
// The config is cached in {binDir}/warp-config.json. If a cached config is
// missing fields or has an empty/invalid endpoint, the cache is discarded and
// a fresh registration is attempted.
func RegisterOrLoadWARP(binDir string) (*WARPConfig, error) {
	cachePath := filepath.Join(binDir, "warp-config.json")

	// Try to load cached config.
	if data, err := os.ReadFile(cachePath); err == nil {
		var cfg WARPConfig
		if json.Unmarshal(data, &cfg) == nil && cfg.PrivateKey != "" && cfg.PeerPublicKey != "" {
			// Heal old caches that captured the ":0" placeholder port or
			// missed the port altogether. If the cached endpoint can't be
			// normalized into a usable host:port, drop the cache.
			if fixed := normalizeWARPEndpoint(cfg.Endpoint); fixed != "" {
				if fixed != cfg.Endpoint {
					cfg.Endpoint = fixed
					if data2, err := json.MarshalIndent(&cfg, "", "  "); err == nil {
						_ = os.WriteFile(cachePath, data2, 0600)
					}
				}
				log.Info().Str("endpoint", cfg.Endpoint).Msg("warp: loaded cached config")
				return &cfg, nil
			}
			log.Warn().Msg("warp: cached config has invalid endpoint — re-registering")
		} else {
			log.Warn().Msg("warp: cached config malformed — re-registering")
		}
	}

	// Register new WARP account.
	cfg, err := registerWARP()
	if err != nil {
		return nil, fmt.Errorf("warp: registration failed: %w", err)
	}

	// Belt-and-braces: ensure the freshly registered config has a usable
	// endpoint before we cache it.
	if normalizeWARPEndpoint(cfg.Endpoint) == "" {
		cfg.Endpoint = defaultWARPEndpoint
	}

	// Cache for future use.
	if err := os.MkdirAll(binDir, 0755); err == nil {
		data, _ := json.MarshalIndent(cfg, "", "  ")
		_ = os.WriteFile(cachePath, data, 0600) // restricted perms, contains private key
	}

	log.Info().Str("endpoint", cfg.Endpoint).Int("reserved_len", len(cfg.Reserved)).Msg("warp: registered successfully")
	return cfg, nil
}

func registerWARP() (*WARPConfig, error) {
	// 1. Generate X25519 keypair.
	var privateKey [32]byte
	if _, err := rand.Read(privateKey[:]); err != nil {
		return nil, fmt.Errorf("generate private key: %w", err)
	}
	// Clamp private key (standard X25519 clamping).
	privateKey[0] &= 248
	privateKey[31] &= 127
	privateKey[31] |= 64

	publicKey, err := curve25519.X25519(privateKey[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive public key: %w", err)
	}

	privB64 := base64.StdEncoding.EncodeToString(privateKey[:])
	pubB64 := base64.StdEncoding.EncodeToString(publicKey)

	// 2. Register with WARP API.
	regBody := map[string]any{
		"key":    pubB64,
		"tos":    time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"type":   "Android",
		"model":  "PC",
		"locale": "en_US",
	}
	bodyData, _ := json.Marshal(regBody)

	req, err := http.NewRequest("POST", warpAPIBase+"/reg", bytes.NewReader(bodyData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "okhttp/3.12.1")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("WARP API request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("WARP API HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	// 3. Parse response.
	var regResp struct {
		Config struct {
			Peers []struct {
				PublicKey string `json:"public_key"`
				Endpoint struct {
					V4 string `json:"v4"`
					V6 string `json:"v6"`
				} `json:"endpoint"`
			} `json:"peers"`
			Interface struct {
				Addresses struct {
					V4 string `json:"v4"`
					V6 string `json:"v6"`
				} `json:"addresses"`
			} `json:"interface"`
			ClientID string `json:"client_id"`
		} `json:"config"`
	}
	if err := json.Unmarshal(respBody, &regResp); err != nil {
		return nil, fmt.Errorf("parse WARP response: %w", err)
	}

	if len(regResp.Config.Peers) == 0 {
		return nil, fmt.Errorf("WARP response has no peers")
	}

	peer := regResp.Config.Peers[0]

	// Parse client_id to reserved bytes.
	var reserved []int
	if clientID := regResp.Config.ClientID; clientID != "" {
		decoded, err := base64.StdEncoding.DecodeString(clientID)
		if err == nil {
			for _, b := range decoded {
				reserved = append(reserved, int(b))
			}
		}
	}

	// Cloudflare returns peer.Endpoint.V4 as "host:0" — the port is a placeholder.
	// The actual WARP UDP port is 2408 (sometimes 1701/500), so normalize to 2408.
	endpoint := normalizeWARPEndpoint(peer.Endpoint.V4)
	if endpoint == "" {
		endpoint = defaultWARPEndpoint
	}

	return &WARPConfig{
		PrivateKey:    privB64,
		PeerPublicKey: peer.PublicKey,
		Addresses: struct {
			V4 string `json:"v4"`
			V6 string `json:"v6"`
		}{
			V4: regResp.Config.Interface.Addresses.V4,
			V6: regResp.Config.Interface.Addresses.V6,
		},
		Endpoint: endpoint,
		Reserved: reserved,
	}, nil
}

// WARPToOutbound converts WARP config to ProxyOutbound for mihomo.
func WARPToOutbound(cfg *WARPConfig) ProxyOutbound {
	// Parse endpoint to host:port. normalizeWARPEndpoint guarantees a real
	// UDP port (it rewrites :0 / missing-port forms), but we still defend
	// against a fully empty input by falling back to the well-known WARP IP.
	endpoint := normalizeWARPEndpoint(cfg.Endpoint)
	if endpoint == "" {
		endpoint = defaultWARPEndpoint
	}
	server := endpoint
	port := 2408 // default WARP port

	// Try split host:port. For "host:port" we strip the port; if parsing
	// fails the loop bails out without mutating server/port.
	for i := len(server) - 1; i >= 0; i-- {
		if server[i] == ':' {
			if p, err := parsePort(server[i+1:]); err == nil {
				server = server[:i]
				port = p
			}
			break
		}
	}
	// Last-resort guards: an upstream regression that wipes endpoint should
	// never let us hand a half-built outbound to newWireGuardDialer (which
	// otherwise rejects it with "server and port required").
	if server == "" {
		server = "162.159.193.10"
	}
	if port == 0 {
		port = 2408
	}

	var localAddr []string
	if cfg.Addresses.V4 != "" {
		localAddr = append(localAddr, cfg.Addresses.V4)
	}
	if cfg.Addresses.V6 != "" {
		localAddr = append(localAddr, cfg.Addresses.V6)
	}

	return ProxyOutbound{
		Protocol:      "wireguard",
		Server:        server,
		Port:          port,
		PrivateKey:    cfg.PrivateKey,
		PeerPublicKey: cfg.PeerPublicKey,
		LocalAddr:     localAddr,
		MTU:           1280,
		Reserved:      cfg.Reserved,
	}
}

// normalizeWARPEndpoint cleans up the endpoint string returned by Cloudflare's
// /reg API. The v4/v6 fields use ":0" as a port placeholder — the real WARP
// UDP port is 2408 — so any zero port is rewritten. Bracketed IPv6 hosts and
// hosts without an explicit port are also normalized to use 2408.
func normalizeWARPEndpoint(ep string) string {
	ep = strings.TrimSpace(ep)
	if ep == "" {
		return ""
	}
	// IPv6 with brackets: "[::1]:0" or "[::1]"
	if ep[0] == '[' {
		end := strings.IndexByte(ep, ']')
		if end < 0 {
			return ep
		}
		host := ep[:end+1]
		rest := ep[end+1:]
		if rest == "" || rest == ":" || rest == ":0" {
			return host + ":2408"
		}
		return ep
	}
	// IPv4 / hostname: "x:y" — split on last colon.
	idx := strings.LastIndexByte(ep, ':')
	if idx < 0 {
		return ep + ":2408"
	}
	port := ep[idx+1:]
	if port == "" || port == "0" {
		return ep[:idx] + ":2408"
	}
	return ep
}

func parsePort(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid port char: %c", c)
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 || n > 65535 {
		return 0, fmt.Errorf("port out of range: %d", n)
	}
	return n, nil
}
