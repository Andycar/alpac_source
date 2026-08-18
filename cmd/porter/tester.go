package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Tester verifies balancer functionality via HTTP.
type Tester struct {
	client *http.Client
}

// NewTester creates a new tester.
func NewTester() *Tester {
	return &Tester{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// CheckSearch tests the checksearch endpoint.
// Returns (responseBody, success).
func (t *Tester) CheckSearch(ctx context.Context, addr, name, kpID string) (string, bool) {
	params := url.Values{
		"checksearch":  {"true"},
		"kinopoisk_id": {kpID},
		"title":        {"test"},
	}

	target := addr + "/lite/" + name + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Sprintf("request error: %v", err), false
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Sprintf("HTTP error: %v", err), false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	bodyStr := strings.TrimSpace(string(body))

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("status %d: %s", resp.StatusCode, bodyStr), false
	}

	// Parse JSON response.
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Sprintf("invalid JSON: %s", bodyStr), false
	}

	rch, ok := result["rch"]
	if !ok {
		return fmt.Sprintf("no 'rch' field: %s", bodyStr), false
	}

	if rchBool, ok := rch.(bool); ok && rchBool {
		return bodyStr, true
	}

	return bodyStr, false
}

// TestIndex tests the main index endpoint.
// Returns (responseBody, success).
func (t *Tester) TestIndex(ctx context.Context, addr, name, kpID, title string) (string, bool) {
	params := url.Values{
		"rjson":        {"true"},
		"kinopoisk_id": {kpID},
		"title":        {title},
	}

	target := addr + "/lite/" + name + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Sprintf("request error: %v", err), false
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Sprintf("HTTP error: %v", err), false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	bodyStr := strings.TrimSpace(string(body))

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("status %d: %s", resp.StatusCode, bodyStr), false
	}

	// Check for non-empty data.
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		// Might be HTML response.
		if len(bodyStr) > 10 && strings.Contains(bodyStr, "videos__") {
			return bodyStr[:min(200, len(bodyStr))], true
		}
		return bodyStr, false
	}

	dataType, _ := result["type"].(string)
	data, _ := result["data"].([]any)

	if dataType == "empty" || len(data) == 0 {
		return bodyStr, false
	}

	return fmt.Sprintf("type=%s, items=%d", dataType, len(data)), true
}
