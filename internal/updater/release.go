package updater

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Release is a version published by the alcopa-site update manager.
// The JSON shape mirrors what GitHub used to return so existing callers
// (apply.go, service.go, the admin UI) keep working unchanged.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset is a single downloadable file attached to a release.
// When alcopa-site serves a release it populates SHA256 directly — the
// legacy `.sha256`/`SHA256SUMS` companion-file lookup is only used as a
// fallback for minisign verification (FetchSignedSums).
type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	ContentType        string `json:"content_type"`
	SHA256             string `json:"sha256,omitempty"`
}

// FindAsset returns the asset whose name matches exactly, or nil if missing.
func (r *Release) FindAsset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// FindChecksum prefers the inline SHA256 field (populated by alcopa-site)
// and falls back to the legacy SHA256SUMS download for compatibility with
// older release layouts. token authorizes password-protected channels.
func (r *Release) FindChecksum(target, token string) string {
	if a := r.FindAsset(target); a != nil && a.SHA256 != "" {
		return strings.ToLower(a.SHA256)
	}
	if a := r.FindAsset("SHA256SUMS"); a != nil {
		body, err := httpGet(a.BrowserDownloadURL, token)
		if err == nil {
			for _, line := range strings.Split(string(body), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && strings.TrimPrefix(fields[1], "*") == target {
					return strings.ToLower(fields[0])
				}
			}
		}
	}
	return ""
}

// FetchSignedSums downloads SHA256SUMS + SHA256SUMS.minisig when present.
// Returns (nil, nil, nil) when no SHA256SUMS asset exists.
func (r *Release) FetchSignedSums(token string) (sums, sig []byte, err error) {
	a := r.FindAsset("SHA256SUMS")
	if a == nil {
		return nil, nil, nil
	}
	sums, err = httpGet(a.BrowserDownloadURL, token)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch SHA256SUMS: %w", err)
	}
	if sa := r.FindAsset("SHA256SUMS.minisig"); sa != nil {
		sig, err = httpGet(sa.BrowserDownloadURL, token)
		if err != nil {
			return sums, nil, fmt.Errorf("fetch SHA256SUMS.minisig: %w", err)
		}
	}
	return sums, sig, nil
}

// httpGet is a tiny helper used by checksum/signature fetches. Small
// payloads only (≤1 MiB). token, when non-empty, is sent as a Bearer
// credential — required for releases in password-protected channels.
func httpGet(url, token string) ([]byte, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "lampac-go-updater")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
