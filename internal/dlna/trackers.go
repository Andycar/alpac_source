package dlna

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// DefaultTrackers returns the built-in tracker list (matches .NET defaults).
var DefaultTrackers = []string{
	"http://retracker.local/announce",
	"http://bt4.t-ru.org/ann?magnet",
	"http://retracker.mgts.by:80/announce",
	"udp://opentor.org:2710",
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://explodie.org:6969",
	"udp://public.popcorn-tracker.org:6969/announce",
}

// TrackerSources lists URLs for fetching best tracker lists.
var TrackerSources = []string{
	"https://raw.githubusercontent.com/ngosang/trackerslist/master/trackers_best_ip.txt",
	"https://raw.githubusercontent.com/XIU2/TrackersListCollection/master/best_aria2.txt",
	"https://newtrackon.com/api/stable",
}

// FetchBestTrackers downloads tracker lists from multiple sources,
// merges and deduplicates them.
func FetchBestTrackers(timeout time.Duration) []string {
	client := &http.Client{Timeout: timeout}
	seen := make(map[string]bool)
	var result []string

	// Start with defaults.
	for _, t := range DefaultTrackers {
		if !seen[t] {
			seen[t] = true
			result = append(result, t)
		}
	}

	for _, src := range TrackerSources {
		trackers := fetchTrackerSource(client, src)
		for _, t := range trackers {
			if !seen[t] {
				seen[t] = true
				result = append(result, t)
			}
		}
	}

	log.Info().Int("count", len(result)).Msg("dlna-trackers: fetched trackers")
	return result
}

func fetchTrackerSource(client *http.Client, url string) []string {
	resp, err := client.Get(url)
	if err != nil {
		log.Debug().Err(err).Str("url", url).Msg("dlna-trackers: fetch failed")
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
	if err != nil {
		return nil
	}

	var trackers []string
	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "http") || strings.HasPrefix(line, "udp") || strings.HasPrefix(line, "wss") {
			trackers = append(trackers, line)
		}
	}
	return trackers
}
