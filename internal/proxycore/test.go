package proxycore

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	sidecaruri "lampac-go/internal/sidecar/uri"
)

// TestResult holds the result of a proxy URI connectivity test.
type TestResult struct {
	Success     bool   `json:"success"`
	LatencyMs   int64  `json:"latency_ms"`
	Protocol    string `json:"protocol"`
	Server      string `json:"server"`
	IP          string `json:"ip,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
	Flag        string `json:"flag,omitempty"`
	Error       string `json:"error,omitempty"`
}

// TestURI tests a proxy URI by establishing a connection and measuring latency.
func TestURI(rawURI string) TestResult {
	out, _, err := sidecaruri.Parse(rawURI)
	if err != nil {
		return TestResult{Error: fmt.Sprintf("parse: %s", err)}
	}

	result := TestResult{
		Protocol: out.Protocol,
		Server:   fmt.Sprintf("%s:%d", out.Server, out.Port),
	}

	// Resolve server IP.
	ips, err := net.LookupHost(out.Server)
	if err == nil && len(ips) > 0 {
		result.IP = ips[0]
	}

	// Try to create a dialer.
	dialer, err := NewDialer(out)
	if err != nil {
		result.Error = fmt.Sprintf("dialer: %s", err)
		return result
	}
	defer dialer.Close()

	// Test by connecting through the proxy.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	conn, err := dialer.DialProxy(ctx, "connectivitycheck.gstatic.com:80")
	elapsed := time.Since(start)

	if err != nil {
		result.Error = fmt.Sprintf("connect: %s", err)
		result.LatencyMs = elapsed.Milliseconds()
		return result
	}
	defer conn.Close()

	// Try an HTTP request through the tunnel.
	httpStart := time.Now()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return conn, nil
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}
	resp, err := client.Get("http://connectivitycheck.gstatic.com/generate_204")
	httpElapsed := time.Since(httpStart)

	if err != nil {
		// Connection was established but HTTP failed — still partially successful.
		result.Success = true
		result.LatencyMs = elapsed.Milliseconds()
		result.Error = fmt.Sprintf("http: %s (connection OK, latency %dms)", err, elapsed.Milliseconds())
		return result
	}
	resp.Body.Close()

	result.Success = true
	result.LatencyMs = httpElapsed.Milliseconds()
	return result
}
