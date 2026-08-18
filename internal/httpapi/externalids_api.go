package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var externalIDsCache = struct {
	mu            sync.RWMutex
	items         map[string]string
	lastCheck     time.Time
	lastWriteTime time.Time
}{
	items: map[string]string{},
}

func externalIDsHandler(cfgRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		imdbID, kinopoiskID, ok := resolveExternalIDsLocal(cfgRoot, r)
		if ok {
			writeExternalIDs(w, imdbID, kinopoiskID)
			return
		}

		// local-only fallback
		imdbID = strings.TrimSpace(r.URL.Query().Get("imdb_id"))
		kinopoiskID = strings.TrimSpace(r.URL.Query().Get("kinopoisk_id"))
		writeExternalIDs(w, imdbID, kinopoiskID)
	}
}

func resolveExternalIDsLocal(cfgRoot string, r *http.Request) (string, string, bool) {
	loadExternalIDs(cfgRoot)
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	imdbID := strings.TrimSpace(r.URL.Query().Get("imdb_id"))
	kpid := strings.TrimSpace(r.URL.Query().Get("kinopoisk_id"))

	externalIDsCache.mu.RLock()
	defer externalIDsCache.mu.RUnlock()
	items := externalIDsCache.items

	// KP_123 -> imdb lookup
	if after, ok := strings.CutPrefix(strings.ToUpper(id), "KP_"); ok {
		kp := after
		for imdb, kpVal := range items {
			if kpVal == kp && imdb != "" {
				return imdb, kp, true
			}
		}
		return "", kp, false
	}

	if imdbID == "" {
		if kpid == "" && id != "" {
			if _, err := strconv.ParseInt(id, 10, 64); err == nil {
				// numeric tmdb id needs legacy/tmdb path
				return "", "", false
			}
		}

		if kpid != "" {
			for imdb, kpVal := range items {
				if kpVal == kpid && imdb != "" {
					imdbID = imdb
					break
				}
			}
		}
	}

	if imdbID != "" && kpid == "" {
		kpid = items[imdbID]
	}

	if imdbID == "" && kpid == "" {
		return "", "", false
	}
	return imdbID, kpid, true
}

func loadExternalIDs(cfgRoot string) {
	externalIDsCache.mu.Lock()
	defer externalIDsCache.mu.Unlock()

	now := time.Now()
	if !externalIDsCache.lastCheck.IsZero() && now.Sub(externalIDsCache.lastCheck) < 5*time.Minute {
		return
	}
	externalIDsCache.lastCheck = now

	candidates := []string{
		filepath.Join(cfgRoot, "data", "externalids.json"),
		filepath.Join("data", "externalids.json"),
		filepath.Join("/home/data", "externalids.json"),
	}

	var (
		path  string
		info  os.FileInfo
		found bool
	)
	for _, p := range candidates {
		st, err := os.Stat(p)
		if err == nil && !st.IsDir() {
			path = p
			info = st
			found = true
			break
		}
	}

	if !found {
		return
	}
	if !externalIDsCache.lastWriteTime.IsZero() && info.ModTime().Equal(externalIDsCache.lastWriteTime) {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var parsed map[string]string
	if err := stdjson.Unmarshal(data, &parsed); err != nil {
		return
	}

	items := make(map[string]string, len(parsed))
	for imdb, kp := range parsed {
		imdb = strings.TrimSpace(imdb)
		kp = strings.TrimSpace(kp)
		if imdb != "" && kp != "" {
			items[imdb] = kp
		}
	}

	externalIDsCache.items = items
	externalIDsCache.lastWriteTime = info.ModTime()
}

func writeExternalIDs(w http.ResponseWriter, imdbID, kinopoiskID string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"imdb_id":      strings.TrimSpace(imdbID),
		"kinopoisk_id": strings.TrimSpace(kinopoiskID),
	})
}
