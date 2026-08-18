package adminhttp

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/logbuf"
	"lampac-go/internal/tgauth"
)

// tgAdminLogsHandler returns recent log entries (JSON).
// GET /{admin}/api/logs?category=youtube,alice&level=error&search=text&limit=200&offset=0
func tgAdminLogsHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, buf *logbuf.Buffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		f := parseLogFilter(r)
		entries := buf.Query(f)

		writeJSON(w, http.StatusOK, map[string]any{
			"entries":    entries,
			"total":      buf.Count(),
			"categories": buf.Categories(),
		})
	}
}

// tgAdminLogsStreamHandler streams log entries via SSE.
// GET /{admin}/api/logs/stream?category=youtube&level=error
func tgAdminLogsStreamHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, buf *logbuf.Buffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		ch, id := buf.Subscribe()
		defer buf.Unsubscribe(id)

		// Parse category/level filters once.
		catSet := make(map[string]bool)
		if cats := r.URL.Query().Get("category"); cats != "" {
			for c := range strings.SplitSeq(cats, ",") {
				catSet[strings.ToLower(strings.TrimSpace(c))] = true
			}
		}
		levelFilter := strings.ToLower(r.URL.Query().Get("level"))
		searchFilter := strings.ToLower(r.URL.Query().Get("search"))

		// Send initial ping.
		fmt.Fprintf(w, ": connected\n\n")
		flusher.Flush()

		ctx := r.Context()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Keep-alive comment.
				fmt.Fprintf(w, ": ping\n\n")
				flusher.Flush()
			case entry, ok := <-ch:
				if !ok {
					return
				}
				// Apply filters.
				if len(catSet) > 0 && !catSet[entry.Category] {
					continue
				}
				if levelFilter != "" && entry.Level != levelFilter {
					continue
				}
				if searchFilter != "" && !strings.Contains(strings.ToLower(entry.Message), searchFilter) {
					continue
				}

				data, _ := json.Marshal(entry)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

// tgAdminLogsExportHandler exports log entries as a file download.
// GET /{admin}/api/logs/export?format=json|txt&category=...&level=...
func tgAdminLogsExportHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, buf *logbuf.Buffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		f := parseLogFilter(r)
		f.Limit = 10000 // Export all available entries.
		entries := buf.Query(f)

		format := r.URL.Query().Get("format")
		ts := time.Now().Format("2006-01-02_15-04-05")

		switch format {
		case "txt":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="logs_%s.txt"`, ts))
			for i := len(entries) - 1; i >= 0; i-- {
				e := entries[i]
				fmt.Fprintf(w, "%s [%s] [%s] %s\n", e.Time, strings.ToUpper(e.Level), e.Category, e.Message)
			}
		default: // json
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="logs_%s.json"`, ts))
			// Reverse to chronological order.
			for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
				entries[i], entries[j] = entries[j], entries[i]
			}
			data, _ := json.Marshal(entries)
			w.Write(data)
		}
	}
}

func parseLogFilter(r *http.Request) logbuf.Filter {
	f := logbuf.Filter{}
	if cats := r.URL.Query().Get("category"); cats != "" {
		for c := range strings.SplitSeq(cats, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				f.Categories = append(f.Categories, c)
			}
		}
	}
	f.Level = r.URL.Query().Get("level")
	f.Search = r.URL.Query().Get("search")
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		f.Limit = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		f.Offset = v
	}
	return f
}
