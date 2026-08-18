package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var playerInnerSanitizer = regexp.MustCompile(`(?i)[^a-z0-9_:\-\/\.\=\?\&\%\@]+`)

func playerInnerHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		playerPath := playerInnerPath()
		if playerPath == "" {
			w.WriteHeader(http.StatusOK)
			return
		}

		uri := strings.TrimSpace(chiURLParam(r, "*"))
		if uri == "" {
			w.WriteHeader(http.StatusOK)
			return
		}

		uri = playerInnerSanitizer.ReplaceAllString(uri, "")
		if uri == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.RawQuery != "" {
			uri += "?" + r.URL.RawQuery
		}
		if !isHTTPURL(uri) {
			w.WriteHeader(http.StatusOK)
			return
		}

		if strings.TrimSpace(os.Getenv("LAMPAC_GO_PLAYER_INNER_EXEC")) == "1" {
			cmd := exec.Command(playerPath, uri)
			_ = cmd.Start()
		}

		w.WriteHeader(http.StatusOK)
	}
}

func playerInnerPath() string {
	if v := strings.TrimSpace(os.Getenv("LAMPAC_GO_PLAYER_INNER")); v != "" {
		return v
	}

	data, ok := readFileAny("init.conf")
	if !ok {
		return ""
	}
	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return ""
	}
	return strings.TrimSpace(toString(root["playerInner"]))
}

func isHTTPURL(raw string) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
}
