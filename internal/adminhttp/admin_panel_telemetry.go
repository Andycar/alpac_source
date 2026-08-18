package adminhttp

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"lampac-go/internal/balancerstats"
	"lampac-go/internal/tgauth"
)

// telemetryDashboardEntry combines a stats Snapshot with derived flags
// (status_tag, group, etc.) so the UI can render cards without re-correlating.
type telemetryDashboardEntry struct {
	Name             string                          `json:"name"`
	StatusTag        string                          `json:"status_tag"`
	Group            string                          `json:"group"`
	Quality          string                          `json:"quality,omitempty"`
	Status           string                          `json:"status"` // "healthy", "degraded", "down", "unknown"
	Snapshot         balancerstats.Snapshot          `json:"snapshot"`
	Fallbacks        []string                        `json:"fallbacks,omitempty"`
	HasFallback      bool                            `json:"has_fallback"`
	HostStats        map[string]balancerstats.Window `json:"host_stats,omitempty"`
	ChecksearchSnap  *balancerstats.Snapshot         `json:"checksearch,omitempty"` // separate ring for /lite/<bal>?checksearch=true probes
	BreakerOpen      bool                            `json:"breaker_open"`          // checksearch circuit breaker open
	BreakerOpenUntil string                          `json:"breaker_open_until,omitempty"`
	BreakerFailCount int                             `json:"breaker_fail_count,omitempty"`
}

// tgAdminTelemetryHandler returns the JSON snapshot of all balancer stats,
// merged with knownBalancers() metadata. Supports POST actions: "reset",
// "rotate_host", "set_fallbacks".
func tgAdminTelemetryHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		mgr := GetGlobalBalancerStats()
		if mgr == nil {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "entries": []any{}})
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleTelemetrySnapshotGET(w, r, mgr)
		case http.MethodPost:
			handleTelemetryAction(w, r, mgr)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleTelemetrySnapshotGET(w http.ResponseWriter, r *http.Request, mgr *balancerstats.Manager) {
	balancer := strings.TrimSpace(r.URL.Query().Get("balancer"))
	snaps := mgr.Snapshot(balancer)

	// Build a unified list including knownBalancers() entries with no recorded
	// attempts yet (so the UI shows "no data" cards for everything).
	entries := make([]telemetryDashboardEntry, 0, len(knownBalancers())+8)
	seen := make(map[string]struct{}, len(snaps))

	includeAll := balancer == ""
	if includeAll {
		for _, name := range knownBalancers() {
			lower := strings.ToLower(name)
			snap, has := snaps[lower]
			entry := buildTelemetryEntry(name, snap, mgr)
			entries = append(entries, entry)
			if has {
				seen[lower] = struct{}{}
			}
		}
		// Append any recorded balancer not in knownBalancers() (custom ones, etc).
		for k, snap := range snaps {
			if _, ok := seen[k]; ok {
				continue
			}
			entries = append(entries, buildTelemetryEntry(k, snap, mgr))
		}
	} else {
		snap := snaps[strings.ToLower(balancer)]
		entries = append(entries, buildTelemetryEntry(balancer, snap, mgr))
	}

	sort.Slice(entries, func(i, j int) bool {
		// Healthy at bottom, degraded/down at top (most attention-worthy first).
		if entries[i].Status != entries[j].Status {
			return statusRank(entries[i].Status) < statusRank(entries[j].Status)
		}
		return entries[i].Name < entries[j].Name
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   true,
		"alerts_on": liveAlertEngine() != nil,
		"entries":   entries,
		"now":       time.Now(),
	})
}

func handleTelemetryAction(w http.ResponseWriter, r *http.Request, mgr *balancerstats.Manager) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req struct {
		Action   string   `json:"action"`
		Balancer string   `json:"balancer"`
		Hosts    []string `json:"hosts,omitempty"`
	}
	if err := stdjson.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	switch req.Action {
	case "reset":
		mgr.Reset(req.Balancer)
		// Also clear the checksearch circuit breaker so the next /lite/events
		// re-probes immediately instead of waiting for cooldown.
		CSBreakerReset(strings.ToLower(req.Balancer))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "reset_breaker":
		CSBreakerReset(strings.ToLower(req.Balancer))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "rotate_host":
		newHost := mgr.RotateHost(req.Balancer)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "host": newHost})
	case "set_fallbacks":
		mgr.SetFallbacks(req.Balancer, req.Hosts)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hosts": mgr.Fallbacks(req.Balancer)})
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

func buildTelemetryEntry(name string, snap balancerstats.Snapshot, mgr *balancerstats.Manager) telemetryDashboardEntry {
	displayName := name
	// If name came from the snapshot map (lowercased), look up the canonical
	// PascalCase form from knownBalancers().
	for _, kb := range knownBalancers() {
		if strings.EqualFold(kb, name) {
			displayName = kb
			break
		}
	}
	lower := strings.ToLower(displayName)
	grp := balancerGroupMap()[displayName]
	if grp == "" {
		grp = "other"
	}
	tag := balancerStatusTags()[displayName]
	q := pluginQualityBadgeGet(lower)
	fb := mgr.Fallbacks(lower)

	entry := telemetryDashboardEntry{
		Name:        displayName,
		StatusTag:   tag,
		Group:       grp,
		Quality:     q,
		Status:      computeStatus(snap),
		Snapshot:    snap,
		Fallbacks:   fb,
		HasFallback: len(fb) > 1,
	}

	// Attach checksearch ring snapshot if any probes have been recorded.
	if csMap := mgr.ChecksearchSnapshot(lower); len(csMap) > 0 {
		if cs, ok := csMap[lower]; ok {
			csCopy := cs
			entry.ChecksearchSnap = &csCopy
		}
	}

	// Attach the per-balancer checksearch circuit breaker state.
	open, openUntil, fc := CSBreakerInspect(lower)
	entry.BreakerOpen = open
	entry.BreakerFailCount = fc
	if !openUntil.IsZero() {
		entry.BreakerOpenUntil = openUntil.UTC().Format(time.RFC3339)
	}

	// If the regular stream ring has no data but checksearch does, derive
	// status from checksearch — at least the balancer is being polled.
	if entry.Status == "unknown" && entry.ChecksearchSnap != nil {
		entry.Status = computeStatus(*entry.ChecksearchSnap)
	}

	return entry
}

// computeStatus distills a Snapshot into a single-word status used for color/sort.
func computeStatus(s balancerstats.Snapshot) string {
	if s.LastAttempt.IsZero() {
		return "unknown"
	}
	w5, ok := s.Windows["5m"]
	if !ok || w5.Total == 0 {
		// Try 1h window for low-traffic balancers.
		w1h := s.Windows["1h"]
		if w1h.Total == 0 {
			return "unknown"
		}
		w5 = w1h
	}
	if w5.SuccessRate < 0.20 {
		return "down"
	}
	if w5.SuccessRate < 0.70 {
		return "degraded"
	}
	return "healthy"
}

func statusRank(s string) int {
	switch s {
	case "down":
		return 0
	case "degraded":
		return 1
	case "unknown":
		return 2
	case "healthy":
		return 3
	default:
		return 4
	}
}

// tgAdminTelemetryStreamHandler is a Server-Sent Events endpoint that pushes
// updated snapshots whenever a new attempt is recorded (coalesced, max 1/sec).
func tgAdminTelemetryStreamHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		mgr := GetGlobalBalancerStats()
		if mgr == nil {
			http.Error(w, "telemetry disabled", http.StatusServiceUnavailable)
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

		id, ch := mgr.Subscribe()
		defer mgr.Unsubscribe(id)

		// Send an initial payload immediately so the client renders without delay.
		writeTelemetryEvent(w, mgr)
		flusher.Flush()

		// Coalesce updates with a 1s ticker.
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		// Heartbeat so middleboxes don't drop the connection.
		hb := time.NewTicker(20 * time.Second)
		defer hb.Stop()
		dirty := false
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ch:
				dirty = true
			case <-ticker.C:
				if dirty {
					writeTelemetryEvent(w, mgr)
					flusher.Flush()
					dirty = false
				}
			case <-hb.C:
				_, _ = io.WriteString(w, ": heartbeat\n\n")
				flusher.Flush()
			}
		}
	}
}

func writeTelemetryEvent(w io.Writer, mgr *balancerstats.Manager) {
	snaps := mgr.Snapshot("")
	entries := make([]telemetryDashboardEntry, 0, len(knownBalancers())+8)
	seen := map[string]struct{}{}
	for _, name := range knownBalancers() {
		lower := strings.ToLower(name)
		snap := snaps[lower]
		entries = append(entries, buildTelemetryEntry(name, snap, mgr))
		seen[lower] = struct{}{}
	}
	for k, snap := range snaps {
		if _, ok := seen[k]; ok {
			continue
		}
		entries = append(entries, buildTelemetryEntry(k, snap, mgr))
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Status != entries[j].Status {
			return statusRank(entries[i].Status) < statusRank(entries[j].Status)
		}
		return entries[i].Name < entries[j].Name
	})
	payload := map[string]any{
		"entries": entries,
		"now":     time.Now(),
	}
	data, _ := stdjson.Marshal(payload)
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", data)
}
