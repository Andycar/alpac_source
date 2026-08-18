package httpapi

import (
	"net/http"
	"strings"
	"time"
)

// probeHost makes a quick HEAD request to check if a host is reachable. Used by
// getstv.go + iptvonline.go. (The admin inspector moved to internal/adminhttp and
// keeps its own copy of this tiny stdlib-only helper.)
func probeHost(host string) bool {
	if !strings.HasPrefix(host, "http") {
		host = "https://" + host
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Head(host)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}
